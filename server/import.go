package main

// The record import merges ais.vessels from the lake into the vessel record, so the record holds vessels heard
// before it existed and each vessel's first_seen reaches back to the earliest report any archive holds. The
// packager records, per MMSI, the earliest report of any kind and the latest position across every day it has
// packaged, and every historical source it packages reaches the record through this one path.
//
// The merge has its own rule because history is usually older than what the record holds: a stored name or
// particular is never replaced, only filled when blank; first_seen takes the earlier value; a position is
// taken only when it is newer than the stored one. The live upsert's rule, where any non-blank field wins,
// would let an old name overwrite a current one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// importPage is the rows read from the lake per query.
var importPage = 20_000

const (
	// importHour is the UTC hour after which the day's import runs: the packager starts at 01:30 and a
	// week of catch-up normally finishes well within the hour after.
	importHour  = 3
	importEvery = 23 * time.Hour
)

// importStats is read by /metrics.
type importStats struct {
	runs, failures, rows atomic.Int64
	lastSuccess          atomic.Int64 // unix seconds
}

// importUpsertSQL merges a vessel from history into its row with the import's rule.
const importUpsertSQL = `
INSERT INTO vessels (mmsi, name, search, kind, class, ship_type, flag, draught, callsign, has_pos, lat, lon, cell,
	pos_at, seen, first_seen, source)
VALUES (?, ?, ?, 'vessel', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (mmsi) DO UPDATE SET
	name       = iif(vessels.name = '', excluded.name, vessels.name),
	search     = iif(vessels.name = '', excluded.search, vessels.search),
	class      = iif(vessels.class = '', excluded.class, vessels.class),
	ship_type  = iif(vessels.ship_type = 0, excluded.ship_type, vessels.ship_type),
	draught    = iif(vessels.draught = 0, excluded.draught, vessels.draught),
	callsign   = iif(vessels.callsign = '', excluded.callsign, vessels.callsign),
	lat        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.lat, vessels.lat),
	lon        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.lon, vessels.lon),
	cell       = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.cell, vessels.cell),
	cog        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 360, vessels.cog),
	sog        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 102.3, vessels.sog),
	heading    = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 511, vessels.heading),
	source     = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.source, vessels.source),
	station    = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, '', vessels.station),
	msg_type   = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, '', vessels.msg_type),
	pos_at     = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.pos_at, vessels.pos_at),
	has_pos    = max(excluded.has_pos, vessels.has_pos),
	seen       = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, max(excluded.seen, vessels.seen), vessels.seen),
	first_seen = min(excluded.first_seen, vessels.first_seen)
`

// historyRow is one ais.vessels row as the import reads it.
type historyRow struct {
	mmsi     uint32
	name     string
	callsign string
	shipType uint8
	draught  float64
	class    string
	first    time.Time
	last     time.Time // of the last position; zero without one
	lat, lon float64
	hasPos   bool
	updated  time.Time
	source   string // source kind of the last position
}

func (s *store) importRows(rows []historyRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(importUpsertSQL)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		// seen is the time of the message that source, station, and msg_type describe. History knows the source
		// of the last position only, so with a position seen is that position's time. A vessel without one is
		// new to the record, with no source, and seen is its latest static update.
		seen := r.last
		if !r.hasPos {
			seen = r.updated
			if seen.IsZero() || r.first.After(seen) {
				seen = r.first
			}
		}
		first := r.first
		if first.IsZero() || first.After(seen) {
			first = seen
		}
		var cell any
		if r.hasPos {
			cell = int64(cellOf(r.lat, r.lon))
		}
		name := strings.TrimSpace(r.name)
		if _, err := st.Exec(r.mmsi, name, strings.ToUpper(name), r.class, r.shipType, flagOf(r.mmsi), r.draught, strings.TrimSpace(r.callsign),
			r.hasPos, r.lat, r.lon, cell, unixMs(r.last), unixMs(seen), unixMs(first), r.source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// importVessels reads ais.vessels a page at a time in MMSI order and merges every row into the record.
func (p *Pipeline) importVessels(ctx context.Context) (int, error) {
	// last_source is the source whose copy the server accepted for the last position, so an imported
	// position carries the credit line its license requires.
	const cols = "mmsi, name, callsign, ship_type, draught10, cls, first_ts, last_ts, last_lat6, last_lon6, updated_ts, last_source"
	total, after := 0, int64(-1)
	for {
		rows, err := p.lake.run(ctx, fmt.Sprintf(`SELECT %s FROM lake.ais.vessels WHERE mmsi > %d ORDER BY mmsi LIMIT %d`, cols, after, importPage))
		if err != nil {
			return total, err
		}
		batch := make([]historyRow, 0, len(rows))
		for _, r := range rows {
			h, err := parseHistoryRow(r)
			if err != nil {
				return total, err
			}
			batch = append(batch, h)
			after = int64(h.mmsi)
		}
		if err := p.store.importRows(batch); err != nil {
			return total, err
		}
		total += len(batch)
		p.imports.rows.Add(int64(len(batch)))
		// Stop on an empty page, not a short one: a query engine that caps its rows below importPage would
		// otherwise end the import early and still count it done.
		if len(rows) == 0 {
			return total, nil
		}
	}
}

func parseHistoryRow(r map[string]json.RawMessage) (historyRow, error) {
	var h historyRow
	var mmsi int64
	if err := json.Unmarshal(r["mmsi"], &mmsi); err != nil {
		return h, fmt.Errorf("ais.vessels mmsi: %w", err)
	}
	h.mmsi = uint32(mmsi)
	str := func(key string) string {
		var s *string
		json.Unmarshal(r[key], &s)
		if s == nil {
			return ""
		}
		return *s
	}
	num := func(key string) (int64, bool) {
		var n *int64
		if json.Unmarshal(r[key], &n) != nil || n == nil {
			return 0, false
		}
		return *n, true
	}
	when := func(key string) time.Time {
		raw := r[key]
		if len(raw) == 0 || string(raw) == "null" {
			return time.Time{}
		}
		t, _ := lakeTime(raw)
		return t
	}
	h.name, h.callsign, h.class = str("name"), str("callsign"), str("cls")
	if n, ok := num("ship_type"); ok && n > 0 && n < 256 {
		h.shipType = uint8(n)
	}
	if n, ok := num("draught10"); ok && n > 0 {
		h.draught = float64(n) / 10
	}
	h.first, h.last, h.updated = when("first_ts"), when("last_ts"), when("updated_ts")
	lat, okLat := num("last_lat6")
	lon, okLon := num("last_lon6")
	if okLat && okLon && !h.last.IsZero() {
		h.lat, h.lon, h.hasPos = float64(lat)/600000, float64(lon)/600000, true
	}
	if s := str("last_source"); s != "" {
		h.source = sourceKind(s)
	}
	return h, nil
}

// runImport merges the lake's vessels into the record once a day after the packager has run, and after a
// restart when the last import is more than a day old.
func (p *Pipeline) runImport() {
	time.Sleep(time.Minute) // let the boot flush seed the record first
	p.importIfDue(time.Now().UTC())
	for range time.Tick(15 * time.Minute) {
		p.importIfDue(time.Now().UTC())
	}
}

// importIfDue runs the import when the last one is a day old and the packager's night is over, or when there
// has never been one. It reports whether an import ran and merged anything.
func (p *Pipeline) importIfDue(now time.Time) bool {
	last, err := p.store.meta("vessels_import")
	if err != nil {
		log.Printf("import: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < importEvery {
		p.imports.lastSuccess.Store(t.Unix())
		return false
	}
	if last != "" && now.Hour() < importHour {
		return false // the packager may still be writing last night's days
	}
	p.imports.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	n, err := p.importVessels(ctx)
	if err != nil {
		p.imports.failures.Add(1)
		log.Printf("import: %d vessels merged before: %v", n, err)
		return false
	}
	if n == 0 {
		return false // an empty lake: try again at the next check rather than waiting a day
	}
	if err := p.store.setMeta("vessels_import", now.Format(time.RFC3339)); err != nil {
		log.Printf("import: %v", err)
	}
	p.imports.lastSuccess.Store(now.Unix())
	log.Printf("import: merged %d vessels from the lake", n)
	return true
}
