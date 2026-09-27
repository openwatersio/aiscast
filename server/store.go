package main

// The durable vessel record: one row per MMSI ever heard, in a SQLite file beside the vessel snapshot.
// The cache forgets a vessel 30 minutes after its last report. The record keeps its last known state, so
// a lookup by MMSI answers for a boat at its berth and a search finds vessels not heard lately.
//
// The fold never touches SQLite. It marks the vessel dirty under the cache lock it already holds, and
// flushStore copies the dirty vessels once a second and upserts them in one transaction. The store
// attaches in main, so the pipeline aiscast replay builds has none and a replayed day never writes here.

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BertoldVdb/go-ais"
	_ "modernc.org/sqlite"
)

const storeSchema = `
CREATE TABLE IF NOT EXISTS vessels (
	mmsi        INTEGER PRIMARY KEY,
	name        TEXT    NOT NULL DEFAULT '',
	search      TEXT    NOT NULL DEFAULT '',   -- name trimmed and upper-cased, for prefix search
	kind        TEXT    NOT NULL DEFAULT 'vessel',
	class       TEXT    NOT NULL DEFAULT '',   -- A or B, from the position report types
	ship_type   INTEGER NOT NULL DEFAULT 0,
	flag        TEXT    NOT NULL DEFAULT '',
	imo         INTEGER NOT NULL DEFAULT 0,
	callsign    TEXT    NOT NULL DEFAULT '',
	destination TEXT    NOT NULL DEFAULT '',
	eta         INTEGER NOT NULL DEFAULT 0,    -- month<<24 | day<<16 | hour<<8 | minute; 0 when not sent
	draught     REAL    NOT NULL DEFAULT 0,
	length      INTEGER NOT NULL DEFAULT 0,
	beam        INTEGER NOT NULL DEFAULT 0,
	has_pos     INTEGER NOT NULL DEFAULT 0,
	lat         REAL    NOT NULL DEFAULT 0,
	lon         REAL    NOT NULL DEFAULT 0,
	cell        INTEGER,                       -- one-degree cell of the position, numbered as index.go does
	cog         REAL    NOT NULL DEFAULT 360,
	sog         REAL    NOT NULL DEFAULT 102.3,
	heading     INTEGER NOT NULL DEFAULT 511,
	nav_status  INTEGER NOT NULL DEFAULT 15,
	pos_at      INTEGER NOT NULL DEFAULT 0,    -- unix ms of the position
	seen        INTEGER NOT NULL,              -- unix ms of the last message that updated the vessel
	first_seen  INTEGER NOT NULL,              -- unix ms of the first write to this row
	source      TEXT    NOT NULL DEFAULT '',
	station     TEXT    NOT NULL DEFAULT '',
	msg_type    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS vessels_search ON vessels (search);
CREATE INDEX IF NOT EXISTS vessels_cell ON vessels (cell, seen);
CREATE INDEX IF NOT EXISTS vessels_seen ON vessels (seen);
CREATE INDEX IF NOT EXISTS vessels_imo ON vessels (imo);
`

// upsertSQL merges a cache state into its row with the fold's own rules, because a vessel the cache swept
// comes back blank: its name, particulars, and position arrive over the next minutes. A blank field keeps
// the stored value, a position replaces the stored one only when it is newer, and seen never moves back.
// Right-hand sides read the row as it was before the update.
const upsertSQL = `
INSERT INTO vessels (mmsi, name, search, kind, class, ship_type, flag, imo, callsign, destination, eta, draught,
	length, beam, has_pos, lat, lon, cell, cog, sog, heading, nav_status, pos_at, seen, first_seen, source, station, msg_type)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (mmsi) DO UPDATE SET
	name        = iif(excluded.name != '', excluded.name, vessels.name),
	search      = iif(excluded.name != '', excluded.search, vessels.search),
	kind        = iif(excluded.kind != 'vessel', excluded.kind, vessels.kind),
	class       = iif(excluded.class != '', excluded.class, vessels.class),
	ship_type   = iif(excluded.ship_type != 0, excluded.ship_type, vessels.ship_type),
	flag        = excluded.flag,
	imo         = iif(excluded.imo != 0, excluded.imo, vessels.imo),
	callsign    = iif(excluded.callsign != '', excluded.callsign, vessels.callsign),
	destination = iif(excluded.destination != '', excluded.destination, vessels.destination),
	eta         = iif(excluded.eta != 0, excluded.eta, vessels.eta),
	draught     = iif(excluded.draught > 0, excluded.draught, vessels.draught),
	length      = iif(excluded.length > 0, excluded.length, vessels.length),
	beam        = iif(excluded.beam > 0, excluded.beam, vessels.beam),
	lat         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.lat, vessels.lat),
	lon         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.lon, vessels.lon),
	cell        = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.cell, vessels.cell),
	cog         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.cog, vessels.cog),
	sog         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.sog, vessels.sog),
	heading     = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.heading, vessels.heading),
	pos_at      = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.pos_at, vessels.pos_at),
	has_pos     = max(excluded.has_pos, vessels.has_pos),
	nav_status  = iif(excluded.nav_status != 15 AND excluded.seen >= vessels.seen, excluded.nav_status, vessels.nav_status),
	source      = iif(excluded.seen >= vessels.seen, excluded.source, vessels.source),
	station     = iif(excluded.seen >= vessels.seen, excluded.station, vessels.station),
	msg_type    = iif(excluded.seen >= vessels.seen, excluded.msg_type, vessels.msg_type),
	seen        = max(excluded.seen, vessels.seen)
`

const recordCols = `mmsi, name, kind, class, ship_type, imo, callsign, destination, eta, draught, length, beam,
	has_pos, lat, lon, cog, sog, heading, nav_status, pos_at, seen, first_seen, source, station, msg_type`

// record is one vessel as the store holds it.
type record struct {
	mmsi      uint32
	v         *vessel
	firstSeen time.Time
}

type store struct {
	db   *sql.DB
	path string

	// read by /metrics
	flushes, flushFailures, rowsWritten atomic.Int64
	flushNanos                          atomic.Int64
}

func openStore(path string) (*store, error) {
	// WAL lets the API read while the writer commits; NORMAL sync is durable across a process crash, and a
	// power loss costs at most the last commits, which the next folds rewrite.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(storeSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &store{db: db, path: path}, nil
}

func (s *store) close() error { return s.db.Close() }

// bytes is the size of the database and its write-ahead log on disk.
func (s *store) bytes() int64 {
	var n int64
	for _, f := range []string{s.path, s.path + "-wal"} {
		if st, err := os.Stat(f); err == nil {
			n += st.Size()
		}
	}
	return n
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func packETA(e ais.FieldETA) int64 {
	return int64(e.Month)<<24 | int64(e.Day)<<16 | int64(e.Hour)<<8 | int64(e.Minute)
}

func unpackETA(n int64) ais.FieldETA {
	return ais.FieldETA{Month: uint8(n >> 24), Day: uint8(n >> 16), Hour: uint8(n >> 8), Minute: uint8(n)}
}

// upsert writes cache states in one transaction.
func (s *store) upsert(rows []record) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		v := r.v
		var cell any // null without a position
		if v.HasPos {
			cell = int64(cellOf(v.Lat, v.Lon))
		}
		seen := unixMs(v.Seen)
		if _, err := st.Exec(r.mmsi, v.Name, strings.ToUpper(strings.TrimSpace(v.Name)), v.Kind, v.Class, v.ShipType,
			flagOf(r.mmsi), v.IMO, v.CallSign, v.Destination, packETA(v.ETA), v.Draught, v.Length, v.Beam,
			v.HasPos, v.Lat, v.Lon, cell, v.Cog, v.Sog, v.Heading, v.NavStatus, unixMs(v.PosAt),
			seen, seen, v.Source, v.Station, v.MsgType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// recordQuery selects rows. Every set filter must hold; boxes and mmsis match any of their members.
type recordQuery struct {
	mmsis    []uint32
	imos     []uint32
	boxes    []bbox
	prefix   string // name prefix, or MMSI prefix when all digits
	contains string // name substring
	since    time.Time
	before   time.Time
	hasPos   bool
	limit    int
}

// maxCellRows bounds the per-row cell ranges a box query spells out. Past it the query tests coordinates
// alone, which scans the table: a few hundred thousand rows, tens of milliseconds.
const maxCellRows = 60

func (s *store) find(q recordQuery) ([]record, error) {
	var where []string
	var args []any
	in := func(col string, ids []uint32) {
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i], args = "?", append(args, id)
		}
		where = append(where, col+" IN ("+strings.Join(ph, ",")+")")
	}
	if q.mmsis != nil {
		if len(q.mmsis) == 0 {
			return nil, nil
		}
		in("mmsi", q.mmsis)
	}
	if q.imos != nil {
		if len(q.imos) == 0 {
			return nil, nil
		}
		in("imo", q.imos)
	}
	if len(q.boxes) > 0 {
		var ors []string
		for _, b := range q.boxes {
			c := "(lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?"
			args = append(args, b[0], b[2], b[1], b[3])
			r0, c0 := cellRowCol(b[0], b[1])
			r1, c1 := cellRowCol(b[2], b[3])
			if r1-r0 < maxCellRows { // lets the cell index narrow the scan to the rows of cells the box covers
				var cells []string
				for r := r0; r <= r1; r++ {
					cells = append(cells, "cell BETWEEN ? AND ?")
					args = append(args, r*360+c0, r*360+c1)
				}
				c += " AND (" + strings.Join(cells, " OR ") + ")"
			}
			ors = append(ors, c+")")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if q.prefix != "" {
		c := "search GLOB ?"
		args = append(args, globPrefix(strings.ToUpper(q.prefix)))
		if lo, hi, ok := mmsiRange(q.prefix); ok {
			c += " OR mmsi BETWEEN ? AND ?"
			args = append(args, lo, hi)
		}
		where = append(where, "("+c+")")
	}
	if q.contains != "" {
		where = append(where, "instr(search, ?) > 0")
		args = append(args, strings.ToUpper(q.contains))
	}
	if !q.since.IsZero() {
		where = append(where, "seen >= ?")
		args = append(args, unixMs(q.since))
	}
	if !q.before.IsZero() {
		where = append(where, "seen < ?")
		args = append(args, unixMs(q.before))
	}
	if q.hasPos {
		where = append(where, "has_pos")
	}
	sqlText := "SELECT " + recordCols + " FROM vessels"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY seen DESC"
	if q.limit > 0 {
		sqlText += " LIMIT " + strconv.Itoa(q.limit)
	}
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		var r record
		var eta, posAt, seen, first int64
		v := newVessel()
		if err := rows.Scan(&r.mmsi, &v.Name, &v.Kind, &v.Class, &v.ShipType, &v.IMO, &v.CallSign, &v.Destination, &eta,
			&v.Draught, &v.Length, &v.Beam, &v.HasPos, &v.Lat, &v.Lon, &v.Cog, &v.Sog, &v.Heading, &v.NavStatus,
			&posAt, &seen, &first, &v.Source, &v.Station, &v.MsgType); err != nil {
			return nil, err
		}
		v.ETA, v.PosAt, v.Seen = unpackETA(eta), fromMs(posAt), fromMs(seen)
		r.v, r.firstSeen = v, fromMs(first)
		out = append(out, r)
	}
	return out, rows.Err()
}

// get is one vessel's row.
func (s *store) get(mmsi uint32) (record, bool, error) {
	rs, err := s.find(recordQuery{mmsis: []uint32{mmsi}})
	if err != nil || len(rs) == 0 {
		return record{}, false, err
	}
	return rs[0], true, nil
}

// globPrefix is a GLOB pattern matching strings that start with p. GLOB rather than LIKE because it is
// case-sensitive, which lets SQLite answer a prefix from the index on search. AIS names can hold the
// characters GLOB treats as special, so each is bracketed to match itself.
func globPrefix(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '*', '?', '[':
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteByte(']')
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('*')
	return b.String()
}

// mmsiRange is the range of nine-digit MMSIs that start with the digits p. MMSIs with leading zeros (coast
// stations, 00MIDxxxx) are small numbers, and the same arithmetic covers them.
func mmsiRange(p string) (lo, hi uint32, ok bool) {
	if len(p) == 0 || len(p) > 9 {
		return 0, 0, false
	}
	n, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		return 0, 0, false
	}
	scale := uint64(1)
	for range 9 - len(p) {
		scale *= 10
	}
	return uint32(n * scale), uint32((n+1)*scale - 1), true
}

// ---- the pipeline side ----

// attachStore connects the record to the cache. Every vessel already cached is marked dirty, which seeds
// an empty record from the snapshot and brings an existing one up to date after a restart.
func (p *Pipeline) attachStore(s *store) {
	p.vmu.Lock()
	p.store = s
	p.dirty = make(map[uint32]struct{}, len(p.vessels))
	for mmsi := range p.vessels {
		p.dirty[mmsi] = struct{}{}
	}
	p.vmu.Unlock()
}

// flushStore writes the vessels folded since the last flush. Their states are copied under the cache lock
// and written outside it, so a slow disk delays the record and never the fold.
func (p *Pipeline) flushStore() error {
	if p.store == nil {
		return nil
	}
	p.vmu.Lock()
	if len(p.dirty) == 0 {
		p.vmu.Unlock()
		return nil
	}
	rows := make([]record, 0, len(p.dirty))
	for mmsi := range p.dirty {
		if v := p.vessels[mmsi]; v != nil {
			rows = append(rows, record{mmsi: mmsi, v: v.state()})
		}
	}
	p.dirty = make(map[uint32]struct{}, len(rows))
	p.vmu.Unlock()
	start := time.Now()
	err := p.store.upsert(rows)
	p.store.flushNanos.Add(int64(time.Since(start)))
	p.store.flushes.Add(1)
	if err != nil {
		// The rows are not marked dirty again: each vessel's next fold rewrites it, and a vessel that never
		// folds again keeps its previous row.
		p.store.flushFailures.Add(1)
		return err
	}
	p.store.rowsWritten.Add(int64(len(rows)))
	return nil
}

func (p *Pipeline) runStore() {
	for range time.Tick(time.Second) {
		if err := p.flushStore(); err != nil {
			log.Printf("store: %v", err)
		}
	}
}

// state copies the folded fields of a vessel, leaving out the retained events and the encoded Feature.
func (v *vessel) state() *vessel {
	return &vessel{
		Name: v.Name, Lat: v.Lat, Lon: v.Lon, HasPos: v.HasPos, Cog: v.Cog, Sog: v.Sog, Heading: v.Heading,
		NavStatus: v.NavStatus, ShipType: v.ShipType, Kind: v.Kind, Class: v.Class, IMO: v.IMO, CallSign: v.CallSign,
		Destination: v.Destination, ETA: v.ETA, Draught: v.Draught, Length: v.Length, Beam: v.Beam, Seen: v.Seen,
		Source: v.Source, Station: v.Station, MsgType: v.MsgType, TrustedAt: v.TrustedAt, PosAt: v.PosAt, StaticAt: v.StaticAt,
	}
}

// fillFrom completes a cache state from the record with the rules upsertSQL applies: blank fields take
// the stored value, and a vessel with no position yet takes the stored one.
func (v *vessel) fillFrom(o *vessel) {
	if v.Name == "" {
		v.Name = o.Name
	}
	if v.Kind == "vessel" {
		v.Kind = o.Kind
	}
	if v.Class == "" {
		v.Class = o.Class
	}
	if v.ShipType == 0 {
		v.ShipType = o.ShipType
	}
	if v.IMO == 0 {
		v.IMO = o.IMO
	}
	if v.CallSign == "" {
		v.CallSign = o.CallSign
	}
	if v.Destination == "" {
		v.Destination = o.Destination
	}
	if v.ETA.Month == 0 {
		v.ETA = o.ETA
	}
	if v.Draught == 0 {
		v.Draught = o.Draught
	}
	if v.Length == 0 {
		v.Length = o.Length
	}
	if v.Beam == 0 {
		v.Beam = o.Beam
	}
	if v.NavStatus == 15 {
		v.NavStatus = o.NavStatus
	}
	if !v.HasPos && o.HasPos {
		v.Lat, v.Lon, v.HasPos, v.PosAt, v.Cog, v.Sog, v.Heading = o.Lat, o.Lon, true, o.PosAt, o.Cog, o.Sog, o.Heading
	}
}

var errNoStore = errors.New("the vessel record is not available on this server")
