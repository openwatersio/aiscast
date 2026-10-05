package main

// The record import merges each vessel's history in ClickHouse into the vessel record, so the record holds
// vessels only an archive heard and each vessel's first_seen reaches back to the earliest report ClickHouse
// holds: positions_1m gives the first and last position, vessel_statics an archive's particulars and earliest
// row. Every source in ClickHouse, live or archive, reaches the record through this one path.
//
// The merge has its own rule because history is usually older than what the record holds: a stored name or
// particular is never replaced, only filled when blank; first_seen takes the earlier value; a position is
// taken only when it is newer than the stored one. The live upsert's rule, where any non-blank field wins,
// would let an old name overwrite a current one.

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// importPage is the vessels read per page.
var importPage = 20_000

const importEvery = 23 * time.Hour

// importStats is read by /metrics.
type importStats struct {
	runs, failures, rows atomic.Int64
	lastSuccess          atomic.Int64 // unix seconds
}

// importUpsertSQL merges a vessel from history into its row with the import's rule.
const importUpsertSQL = `
INSERT INTO vessels (mmsi, name, search, kind, class, ship_type, flag, draught, callsign, imo, length, beam, has_pos,
	lat, lon, cell, pos_at, seen, first_seen, source)
VALUES (?, ?, ?, 'vessel', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (mmsi) DO UPDATE SET
	name       = iif(vessels.name = '', excluded.name, vessels.name),
	search     = iif(vessels.name = '', excluded.search, vessels.search),
	class      = iif(vessels.class = '', excluded.class, vessels.class),
	ship_type  = iif(vessels.ship_type = 0, excluded.ship_type, vessels.ship_type),
	draught    = iif(vessels.draught = 0, excluded.draught, vessels.draught),
	callsign   = iif(vessels.callsign = '', excluded.callsign, vessels.callsign),
	imo        = iif(vessels.imo = 0, excluded.imo, vessels.imo),
	length     = iif(vessels.length = 0, excluded.length, vessels.length),
	beam       = iif(vessels.beam = 0, excluded.beam, vessels.beam),
	lat        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.lat, vessels.lat),
	lon        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.lon, vessels.lon),
	cell       = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.cell, vessels.cell),
	cog        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 360, vessels.cog),
	sog        = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 102.3, vessels.sog),
	heading    = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 511, vessels.heading),
	nav_status = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, 15, vessels.nav_status),
	source     = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.source, vessels.source),
	station    = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, '', vessels.station),
	msg_type   = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, '', vessels.msg_type),
	pos_at     = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, excluded.pos_at, vessels.pos_at),
	has_pos    = max(excluded.has_pos, vessels.has_pos),
	seen       = iif(excluded.has_pos AND excluded.pos_at > vessels.pos_at, max(excluded.seen, vessels.seen), vessels.seen),
	first_seen = min(excluded.first_seen, vessels.first_seen)
`

// historyRow is one vessel's history as the import reads it.
type historyRow struct {
	mmsi     uint32
	name     string
	callsign string
	imo      uint32
	length   uint16
	beam     uint16
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
		if _, err := st.Exec(r.mmsi, name, searchKey(name), r.class, r.shipType, flagOf(r.mmsi), r.draught, strings.TrimSpace(r.callsign),
			r.imo, r.length, r.beam, r.hasPos, r.lat, r.lon, cell, unixMs(r.last), unixMs(seen), unixMs(first), r.source); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.refreshMirror(len(rows), func(i int) uint32 { return rows[i].mmsi })
	return nil
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

// importVessels reads every vessel's history a page at a time in MMSI order and merges it into the record.
func (p *Pipeline) importVessels(ctx context.Context) (int, error) {
	total, after := 0, uint32(0)
	for {
		batch, err := p.vesselHistory(ctx, after, importPage)
		if err != nil {
			return total, err
		}
		// Stop on an empty page, not a short one, so a source that returns fewer rows than asked cannot end the
		// import early and still count it done.
		if len(batch) == 0 {
			return total, nil
		}
		if err := p.store.importRows(batch); err != nil {
			return total, err
		}
		total += len(batch)
		p.imports.rows.Add(int64(len(batch)))
		after = batch[len(batch)-1].mmsi
	}
}

// chImportSettings keeps the import's reads to what the live writer can spare, and lets a page of vessels stop
// reading at its last one.
var chImportSettings = clickhouse.Settings{"max_threads": 2, "max_memory_usage": 1_500_000_000, "optimize_aggregation_in_order": 1}

// vesselHistory reads up to limit vessels after the MMSI after, in MMSI order: each one's first and last position
// from positions_1m, and an archive's particulars and earliest row from vessel_statics. A vessel only
// vessel_statics knows, with no position anywhere, comes in the page its MMSI falls in.
func (p *Pipeline) vesselHistoryFromClickHouse(ctx context.Context, after uint32, limit int) ([]historyRow, error) {
	c := p.chConn()
	if c == nil {
		return nil, errors.New("ClickHouse is not attached")
	}
	return c.vesselHistory(ctx, after, limit)
}

func (c *chConn) vesselHistory(ctx context.Context, after uint32, limit int) ([]historyRow, error) {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(chImportSettings))
	rows, err := c.conn.Query(ctx, `SELECT mmsi, min(ts), max(ts), argMax(lat6, ts), argMax(lon6, ts), argMax(source, ts)
		FROM `+c.db+`.positions_1m WHERE mmsi > ? GROUP BY mmsi ORDER BY mmsi LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	byMMSI := map[uint32]*historyRow{}
	for rows.Next() {
		var h historyRow
		var lat6, lon6 int32
		var source string
		if err := rows.Scan(&h.mmsi, &h.first, &h.last, &lat6, &lon6, &source); err != nil {
			rows.Close()
			return nil, err
		}
		h.lat, h.lon, h.hasPos, h.source = float64(lat6)/600000, float64(lon6)/600000, true, sourceKind(source)
		byMMSI[h.mmsi] = &h
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	upTo := uint32(math.MaxUint32) // a short page is the last, so it takes every static vessel after it
	if len(byMMSI) == limit {
		upTo = 0
		for m := range byMMSI {
			upTo = max(upTo, m)
		}
	}
	rows, err = c.conn.Query(ctx, `SELECT mmsi, min(first_ts), max(last_ts), argMaxMerge(name), argMaxMerge(callsign), argMaxMerge(imo),
		argMaxMerge(ship_type), argMaxMerge(length), argMaxMerge(beam), argMaxMerge(draught10)
		FROM `+c.db+`.vessel_statics WHERE mmsi > ? AND mmsi <= ? GROUP BY mmsi`, after, upTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s historyRow
		var draught10 uint16
		if err := rows.Scan(&s.mmsi, &s.first, &s.updated, &s.name, &s.callsign, &s.imo, &s.shipType, &s.length, &s.beam, &draught10); err != nil {
			return nil, err
		}
		h := byMMSI[s.mmsi]
		if h == nil {
			h = &historyRow{mmsi: s.mmsi, first: s.first}
			byMMSI[s.mmsi] = h
		}
		h.name, h.callsign, h.imo, h.shipType, h.length, h.beam, h.updated = s.name, s.callsign, s.imo, s.shipType, s.length, s.beam, s.updated
		h.draught = float64(draught10) / 10
		if s.first.Before(h.first) {
			h.first = s.first
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]historyRow, 0, len(byMMSI))
	for _, h := range byMMSI {
		out = append(out, *h)
	}
	slices.SortFunc(out, func(a, b historyRow) int { return cmp.Compare(a.mmsi, b.mmsi) })
	return out, nil
}

// runImport merges ClickHouse's vessel history into the record once a day, and after a restart when the last
// import is more than a day old.
func (p *Pipeline) runImport() {
	time.Sleep(time.Minute) // let the boot flush seed the record first
	p.importIfDue(time.Now().UTC())
	for range time.Tick(15 * time.Minute) {
		p.importIfDue(time.Now().UTC())
	}
}

// importIfDue runs the import when the last one is a day old, or when there has never been one. It reports
// whether an import ran and merged anything.
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
		return false // no history yet: try again at the next check rather than waiting a day
	}
	if err := p.store.setMeta("vessels_import", now.Format(time.RFC3339)); err != nil {
		log.Printf("import: %v", err)
	}
	p.imports.lastSuccess.Store(now.Unix())
	log.Printf("import: merged %d vessels from ClickHouse", n)
	return true
}
