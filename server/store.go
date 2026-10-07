package main

// The durable vessel record: one row per vessel ever heard, keyed by MMSI, in one SQLite file. The cache forgets a vessel
// 30 minutes after its last report. The record keeps its last known state, so a lookup by MMSI answers
// for a boat at its berth and a search finds vessels not heard lately. On boot the cache is filled from
// the record's last 30 minutes, so the record is the one state that survives a restart.
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
	search      TEXT    NOT NULL DEFAULT '',   -- the name through searchKey, for prefix search
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
	to_bow      INTEGER NOT NULL DEFAULT 0,    -- metres from the AIS antenna; each pair goes with its total
	to_stern    INTEGER NOT NULL DEFAULT 0,
	to_port     INTEGER NOT NULL DEFAULT 0,
	to_starboard INTEGER NOT NULL DEFAULT 0,
	has_pos     INTEGER NOT NULL DEFAULT 0,
	lat         REAL    NOT NULL DEFAULT 0,
	lon         REAL    NOT NULL DEFAULT 0,
	cell        INTEGER,                       -- one-degree cell of the position, numbered as index.go does
	cog         REAL    NOT NULL DEFAULT 360,
	sog         REAL    NOT NULL DEFAULT 102.3,
	heading     INTEGER NOT NULL DEFAULT 511,
	nav_status  INTEGER NOT NULL DEFAULT 15,
	pos_at      INTEGER NOT NULL DEFAULT 0,    -- unix ms of the position
	trusted_at  INTEGER NOT NULL DEFAULT 0,    -- unix ms of the last position from a source that is not low-trust
	static_at   INTEGER NOT NULL DEFAULT 0,    -- unix ms of the last static report
	seen        INTEGER NOT NULL,              -- unix ms of the last message that updated the vessel
	first_seen  INTEGER NOT NULL,              -- unix ms of the earliest report written to this row
	source      TEXT    NOT NULL DEFAULT '',
	station     TEXT    NOT NULL DEFAULT '',
	msg_type    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS vessels_search ON vessels (search);
CREATE INDEX IF NOT EXISTS vessels_cell ON vessels (cell, seen);
CREATE INDEX IF NOT EXISTS vessels_seen ON vessels (seen);
CREATE INDEX IF NOT EXISTS vessels_imo ON vessels (imo);
CREATE INDEX IF NOT EXISTS vessels_first_seen ON vessels (first_seen);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- particulars from Wikidata by IMO, replaced whole by each weekly sync (wikidata.go)
CREATE TABLE IF NOT EXISTS wikidata (
	imo              INTEGER PRIMARY KEY,
	qid              TEXT    NOT NULL,
	builder          TEXT    NOT NULL DEFAULT '',
	year_built       INTEGER NOT NULL DEFAULT 0,
	gross_tonnage    INTEGER NOT NULL DEFAULT 0,
	deadweight       INTEGER NOT NULL DEFAULT 0,  -- tonnes
	length           REAL    NOT NULL DEFAULT 0,  -- metres
	beam             REAL    NOT NULL DEFAULT 0,
	registry         TEXT    NOT NULL DEFAULT '', -- country of registry, in English
	former_names     TEXT    NOT NULL DEFAULT '', -- JSON array, oldest first
	ship_type        TEXT    NOT NULL DEFAULT '',
	yard_number      TEXT    NOT NULL DEFAULT '',
	draught          REAL    NOT NULL DEFAULT 0,  -- design draught, metres
	home_port        TEXT    NOT NULL DEFAULT '',
	owner            TEXT    NOT NULL DEFAULT '',
	operator         TEXT    NOT NULL DEFAULT '',
	wikipedia        TEXT    NOT NULL DEFAULT '', -- URLs
	commons_category TEXT    NOT NULL DEFAULT '',
	image            TEXT    NOT NULL DEFAULT ''
);
-- US-flag vessels with a call sign or an official number from the Coast Guard's PSIX, listed weekly, with
-- dimensions and tonnage read for the vessels AIS matches to them (uscg.go)
CREATE TABLE IF NOT EXISTS uscg (
	vessel_id       INTEGER PRIMARY KEY,         -- PSIX's id
	callsign        TEXT    NOT NULL,
	name            TEXT    NOT NULL,
	identification  TEXT    NOT NULL DEFAULT '',
	official_key    TEXT    NOT NULL DEFAULT '', -- identification through normOfficial, which the FCC licenses join
	service         TEXT    NOT NULL DEFAULT '',
	status          TEXT    NOT NULL DEFAULT '',
	year_built      INTEGER NOT NULL DEFAULT 0,
	length          REAL    NOT NULL DEFAULT 0,  -- metres
	beam            REAL    NOT NULL DEFAULT 0,
	depth           REAL    NOT NULL DEFAULT 0,
	gross_tonnage   INTEGER NOT NULL DEFAULT 0,
	net_tonnage     INTEGER NOT NULL DEFAULT 0,
	tonnage_measure TEXT    NOT NULL DEFAULT '',
	listed          INTEGER NOT NULL DEFAULT 0,  -- unix ms of the listing that last held the vessel
	details_at      INTEGER NOT NULL DEFAULT 0   -- unix ms of the last dimensions and tonnage read; 0 before
);
CREATE INDEX IF NOT EXISTS uscg_callsign ON uscg (callsign);
-- Norwegian fishing vessels with a call sign from the Directorate of Fisheries' open register, replaced
-- weekly (fiskeridir.go)
CREATE TABLE IF NOT EXISTS fiskeridir (
	vessel_id     TEXT    PRIMARY KEY,         -- the register's id
	callsign      TEXT    NOT NULL,
	name          TEXT    NOT NULL,
	registration  TEXT    NOT NULL DEFAULT '', -- the registration mark, such as VL0148AV
	year_built    INTEGER NOT NULL DEFAULT 0,
	length        REAL    NOT NULL DEFAULT 0,  -- metres
	beam          REAL    NOT NULL DEFAULT 0,
	gross_tonnage INTEGER NOT NULL DEFAULT 0,  -- London Convention tonnage only
	owner         TEXT    NOT NULL DEFAULT ''  -- the owning company; people are dropped at sync
);
CREATE INDEX IF NOT EXISTS fiskeridir_callsign ON fiskeridir (callsign);
-- answers from ISED's Canadian MMSI registry, asked on demand for heard CA-flag vessels; an empty name
-- is a vessel asked about that had no record (ised.go)
CREATE TABLE IF NOT EXISTS ised (
	mmsi       INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL DEFAULT '',
	callsign   TEXT    NOT NULL DEFAULT '',
	checked_at INTEGER NOT NULL DEFAULT 0 -- unix ms of the last ask
);
-- Canadian vessels with an IMO from Transport Canada's Register of Large Vessels, replaced weekly (tc.go)
CREATE TABLE IF NOT EXISTS tc (
	imo           INTEGER PRIMARY KEY,
	official      TEXT    NOT NULL DEFAULT '',
	name          TEXT    NOT NULL DEFAULT '',
	service       TEXT    NOT NULL DEFAULT '', -- the register's vessel descriptor
	year_built    INTEGER NOT NULL DEFAULT 0,
	gross_tonnage INTEGER NOT NULL DEFAULT 0,
	net_tonnage   INTEGER NOT NULL DEFAULT 0,
	length        REAL    NOT NULL DEFAULT 0,  -- metres
	beam          REAL    NOT NULL DEFAULT 0,
	depth         REAL    NOT NULL DEFAULT 0,
	home_port     TEXT    NOT NULL DEFAULT ''
);
-- active FCC ship station licenses with an MMSI, replaced weekly from the ULS bulk files (fcc.go)
CREATE TABLE IF NOT EXISTS fcc (
	mmsi     INTEGER PRIMARY KEY,
	usi      INTEGER NOT NULL,             -- the license's unique system identifier
	callsign TEXT    NOT NULL DEFAULT '',
	name     TEXT    NOT NULL DEFAULT '',
	official TEXT    NOT NULL DEFAULT ''   -- official number or state registration
);
`

// storeAddedCols are columns a file created by an earlier build lacks. SQLite has no ADD COLUMN IF NOT
// EXISTS, so openStore adds each and passes over the duplicate-column error from a file that has it.
var storeAddedCols = []string{
	"trusted_at INTEGER NOT NULL DEFAULT 0",
	"static_at  INTEGER NOT NULL DEFAULT 0",
	// the antenna offsets; a row from before them reads 0, not available, until the vessel's next static
	"to_bow INTEGER NOT NULL DEFAULT 0",
	"to_stern INTEGER NOT NULL DEFAULT 0",
	"to_port INTEGER NOT NULL DEFAULT 0",
	"to_starboard INTEGER NOT NULL DEFAULT 0",
}

// upsertSQL merges a cache state into its row with the fold's own rules, because a vessel the cache swept
// comes back blank: its name, particulars, and position arrive over the next minutes. A blank field keeps
// the stored value, a position replaces the stored one only when it is newer, and seen never moves back.
// Right-hand sides read the row as it was before the update.
const upsertSQL = `
INSERT INTO vessels (mmsi, name, search, kind, class, ship_type, flag, imo, callsign, destination, eta, draught,
	length, beam, to_bow, to_stern, to_port, to_starboard, has_pos, lat, lon, cell, cog, sog, heading, nav_status, pos_at,
	trusted_at, static_at, seen, first_seen, source, station, msg_type)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	to_bow      = iif(excluded.length > 0, excluded.to_bow, vessels.to_bow),
	to_stern    = iif(excluded.length > 0, excluded.to_stern, vessels.to_stern),
	to_port     = iif(excluded.beam > 0, excluded.to_port, vessels.to_port),
	to_starboard = iif(excluded.beam > 0, excluded.to_starboard, vessels.to_starboard),
	lat         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.lat, vessels.lat),
	lon         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.lon, vessels.lon),
	cell        = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.cell, vessels.cell),
	cog         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.cog, vessels.cog),
	sog         = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.sog, vessels.sog),
	heading     = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.heading, vessels.heading),
	pos_at      = iif(excluded.has_pos AND excluded.pos_at >= vessels.pos_at, excluded.pos_at, vessels.pos_at),
	has_pos     = max(excluded.has_pos, vessels.has_pos),
	trusted_at  = max(excluded.trusted_at, vessels.trusted_at),
	static_at   = max(excluded.static_at, vessels.static_at),
	nav_status  = iif(excluded.nav_status != 15 AND excluded.seen >= vessels.seen, excluded.nav_status, vessels.nav_status),
	source      = iif(excluded.seen >= vessels.seen, excluded.source, vessels.source),
	station     = iif(excluded.seen >= vessels.seen, excluded.station, vessels.station),
	msg_type    = iif(excluded.seen >= vessels.seen, excluded.msg_type, vessels.msg_type),
	seen        = max(excluded.seen, vessels.seen),
	first_seen  = min(excluded.first_seen, vessels.first_seen)
`

const recordCols = `mmsi, name, kind, class, ship_type, imo, callsign, destination, eta, draught, length, beam,
	to_bow, to_stern, to_port, to_starboard,
	has_pos, lat, lon, cog, sog, heading, nav_status, pos_at, trusted_at, static_at, seen, first_seen, source, station, msg_type`

// record is one vessel as the store holds it.
type record struct {
	mmsi      uint32
	v         *vessel
	firstSeen time.Time
}

type store struct {
	db     *sql.DB
	path   string
	mirror *recordMirror // every row in memory, refreshed after every write (mirror.go)

	sitemap sitemapMemo

	// read by /metrics
	flushes, flushFailures, rowsWritten atomic.Int64
	flushNanos                          atomic.Int64
	mirrorFailures                      atomic.Int64 // refreshes of the mirror that failed
}

func openStore(path string) (*store, error) {
	// WAL lets the API read while the writer commits; NORMAL sync is durable across a process crash, and a
	// power loss costs at most the last commits, which the next folds rewrite.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// Each connection holds its own page cache, and the pool is otherwise unlimited, so a burst of public
	// lookups and searches could open as many as it liked.
	db.SetMaxOpenConns(storeConns)
	if _, err := db.Exec(storeSchema + stationsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, col := range storeAddedCols {
		if _, err := db.Exec("ALTER TABLE vessels ADD COLUMN " + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// A file from before official_key gains the column, and its sync stamp is cleared so the next hourly
	// check re-lists, filling the keys, rather than serving empty ones for up to a week. The old
	// identification index, which nothing queries now, goes with it.
	if _, err := db.Exec("ALTER TABLE uscg ADD COLUMN official_key TEXT NOT NULL DEFAULT ''"); err == nil {
		if _, err := db.Exec("DELETE FROM meta WHERE key = 'uscg_sync'"); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS uscg_identification",
		"CREATE INDEX IF NOT EXISTS uscg_official_key ON uscg (official_key) WHERE official_key != ''",
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// A file from before searchKey carries punctuation in its search column, so names like RUBY'S
	// STAR cannot be found without it; one boot rewrites the column, a few seconds, marked in meta.
	var reindexed string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'search_key'`).Scan(&reindexed); err != nil {
		rows, err := db.Query(`SELECT mmsi, name FROM vessels WHERE name != ''`)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		type kv struct {
			mmsi uint32
			key  string
		}
		var todo []kv
		for rows.Next() {
			var m uint32
			var name string
			if err := rows.Scan(&m, &name); err != nil {
				rows.Close()
				db.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			todo = append(todo, kv{m, searchKey(name)})
		}
		rows.Close()
		// An iteration error would truncate the list silently, and the flag below would then mark a
		// half-reindexed file as done for good.
		if err := rows.Err(); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		st, err := tx.Prepare(`UPDATE vessels SET search = ? WHERE mmsi = ? AND search != ?`)
		if err == nil {
			for _, x := range todo {
				if _, err = st.Exec(x.key, x.mmsi, x.key); err != nil {
					break
				}
			}
			st.Close()
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO meta (key, value) VALUES ('search_key', '1')`)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// The fold and the import skip MMSIs validMMSI rejects; this clears the rows a file already holds under them,
	// a few range scans of the primary key each boot, and any station's own vessel chosen under one, which
	// decideOwn would otherwise keep until the station sends a valid one.
	for _, stmt := range []string{
		"DELETE FROM vessels WHERE " + invalidMMSIWhere("mmsi"),
		"UPDATE stations SET own = 0 WHERE own != 0 AND (" + invalidMMSIWhere("own") + ")",
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	s := &store{db: db, path: path}
	if s.mirror, err = loadMirror(s); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// storeConns bounds the vessel record's connections: the writer and the requests reading beside it.
const storeConns = 8

func (s *store) close() error { return s.db.Close() }

// invalidMMSIWhere matches the MMSIs in col that validMMSI rejects, spelled from the same tables, in SQL that
// SQLite and ClickHouse both read.
func invalidMMSIWhere(col string) string {
	var or []string
	for _, r := range invalidMMSIRanges {
		or = append(or, fmt.Sprintf("%s BETWEEN %d AND %d", col, r[0], r[1]))
	}
	defaults := make([]string, 0, len(defaultMMSIs))
	for m := range defaultMMSIs {
		defaults = append(defaults, strconv.FormatUint(uint64(m), 10))
	}
	or = append(or, col+" IN ("+strings.Join(defaults, ", ")+")")
	return strings.Join(or, " OR ")
}

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
		if _, err := st.Exec(r.mmsi, v.Name, searchKey(v.Name), v.Kind, v.Class, v.ShipType,
			flagOf(r.mmsi), v.IMO, v.CallSign, v.Destination, packETA(v.ETA), v.Draught, v.Length, v.Beam,
			v.Dim.A, v.Dim.B, v.Dim.C, v.Dim.D,
			v.HasPos, v.Lat, v.Lon, cell, v.Cog, v.Sog, v.Heading, v.NavStatus, unixMs(v.PosAt),
			unixMs(v.TrustedAt), unixMs(v.StaticAt), seen, seen, v.Source, v.Station, v.MsgType); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.refreshMirror(len(rows), func(i int) uint32 { return rows[i].mmsi })
	return nil
}

// refreshMirror never fails the write: the mirror retries with the next one.
func (s *store) refreshMirror(n int, mmsi func(int) uint32) {
	mmsis := make([]uint32, n)
	for i := range mmsis {
		mmsis[i] = mmsi(i)
	}
	if err := s.mirror.refresh(s, mmsis); err != nil {
		s.mirrorFailures.Add(1)
		log.Printf("record mirror: %v", err)
	}
}

// recordQuery selects rows. Every set filter must hold; boxes and mmsis match any of their members.
type recordQuery struct {
	mmsis    []uint32
	imos     []uint32
	boxes    []bbox
	prefix   string // name prefix, or MMSI prefix when all digits
	contains string // name substring
	flag     string // flag state, ISO 3166-1 alpha-2
	since    time.Time
	before   time.Time
	hasPos   bool
	byName   bool // order by name then MMSI, as the MCP tools page, instead of most recently heard first
	filter   *vesselFilter
	now      time.Time // the filter's clock
	limit    int
}

// maxCellRows bounds the per-row cell ranges a box query spells out. Past it the query tests coordinates
// alone, which scans the table: a few hundred thousand rows, tens of milliseconds.
const maxCellRows = 60

// maxCells bounds the cells a query lists one by one, across all its boxes: 1,080 is the most a single box
// within a personal token's 400 square degrees can touch (1.1° by 360° crosses three rows). The budget is
// shared, because bbox repeats and a zero-area box costs no area, and each cell is a bound parameter.
const maxCells = 1080

// where is the WHERE clause for q and its arguments. none reports a filter that matches nothing, an
// empty MMSI or IMO list. byCell reports an area query past the cache whose every box the cell index narrows, so
// find orders on +seen: ordering on seen lets SQLite walk the seen index until the limit fills, which in
// empty water is every row in the range.
func (q recordQuery) where() (clause string, args []any, none, byCell bool) {
	var where []string
	in := func(col string, ids []uint32) {
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i], args = "?", append(args, id)
		}
		where = append(where, col+" IN ("+strings.Join(ph, ",")+")")
	}
	if q.mmsis != nil {
		if len(q.mmsis) == 0 {
			return "", nil, true, false
		}
		in("mmsi", q.mmsis)
	}
	if q.imos != nil {
		if len(q.imos) == 0 {
			return "", nil, true, false
		}
		in("imo", q.imos)
	}
	seen := "seen"
	if len(q.boxes) > 0 {
		// Listing cells serves an area reaching past the cache: before stops at its 30 minutes, and the
		// (cell, seen) index then seeks each cell's range, skipping the vessels the cache answered for. A
		// name or MMSI search, with or without max_age, is better served by its own index.
		bounded := !q.before.IsZero() && q.prefix == "" && q.contains == ""
		byCell = bounded
		budget := maxCells
		var ors []string
		for _, b := range q.boxes {
			c := "(lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?"
			args = append(args, b[0], b[2], b[1], b[3])
			r0, c0 := cellRowCol(b[0], b[1])
			r1, c1 := cellRowCol(b[2], b[3])
			if n := int(r1-r0+1) * int(c1-c0+1); bounded && n <= budget {
				budget -= n
				ph := make([]string, 0, n)
				for r := r0; r <= r1; r++ {
					for col := c0; col <= c1; col++ {
						ph, args = append(ph, "?"), append(args, r*360+col)
					}
				}
				c += " AND cell IN (" + strings.Join(ph, ",") + ")"
			} else if r1-r0 < maxCellRows { // lets the cell index narrow the scan to the rows of cells the box covers
				var cells []string
				for r := r0; r <= r1; r++ {
					cells = append(cells, "cell BETWEEN ? AND ?")
					args = append(args, r*360+c0, r*360+c1)
				}
				c += " AND (" + strings.Join(cells, " OR ") + ")"
				// Without statistics SQLite takes the seen index for a seen range and walks every vessel
				// heard in it, wherever it is. The unary + keeps it on the cells.
				seen = "+seen"
			} else {
				byCell = false
			}
			ors = append(ors, c+")")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if q.prefix != "" {
		if lo, hi, ok := mmsiRange(q.prefix); ok {
			where = append(where, "mmsi BETWEEN ? AND ?")
			args = append(args, lo, hi)
		} else if key := searchKey(q.prefix); key == "" {
			// Punctuation alone normalizes to nothing, and an empty pattern would match every vessel.
			where = append(where, "0")
		} else {
			where = append(where, "search GLOB ?")
			args = append(args, globPrefix(key))
		}
	}
	if q.contains != "" {
		if key := searchKey(q.contains); key == "" {
			where = append(where, "0")
		} else {
			where = append(where, "instr(search, ?) > 0")
			args = append(args, key)
		}
	}
	if q.flag != "" {
		where = append(where, "flag = ?")
		args = append(args, q.flag)
	}
	if !q.since.IsZero() {
		where = append(where, seen+" >= ?")
		args = append(args, unixMs(q.since))
	}
	if !q.before.IsZero() {
		where = append(where, seen+" < ?")
		args = append(args, unixMs(q.before))
	}
	if q.hasPos {
		where = append(where, "has_pos")
	}
	if q.filter != nil {
		w, a := q.filter.where(q.now)
		where, args = append(where, w...), append(args, a...)
	}
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	return clause, args, false, byCell
}

// bounded is where, refused with errTooManyTerms past what one SQLite statement can take: each box is a
// level of OR, and the expression depth stops near a thousand, and each term is a bound parameter.
func (q recordQuery) bounded() (clause string, args []any, none, byCell bool, err error) {
	if len(q.boxes) > maxBoxes {
		return "", nil, false, false, errTooManyTerms
	}
	clause, args, none, byCell = q.where()
	if len(args) > maxParams {
		return "", nil, false, false, errTooManyTerms
	}
	return clause, args, none, byCell, nil
}

// count is the number of rows q matches, ignoring its limit.
func (s *store) count(q recordQuery) (int, error) {
	if s.mirror.answers(q) {
		return s.mirror.count(q)
	}
	clause, args, none, _, err := q.bounded()
	if err != nil || none {
		return 0, err
	}
	var n int
	err = s.db.QueryRow("SELECT count(*) FROM vessels"+clause, args...).Scan(&n)
	return n, err
}

// counts counts every vessel in the record, and those heard and first heard within each window.
func (s *store) counts(now time.Time) (*recordCounts, error) {
	if s.mirror != nil {
		return s.mirror.counts(now), nil
	}
	return s.countsSQL(now)
}

// countsSQL is counts from SQLite: each window is a range on its own index.
func (s *store) countsSQL(now time.Time) (*recordCounts, error) {
	c := &recordCounts{Heard: map[string]int{}, New: map[string]int{}}
	if err := s.db.QueryRow("SELECT count(*) FROM vessels").Scan(&c.Total); err != nil {
		return nil, err
	}
	for _, w := range recordWindows {
		cut := unixMs(now.Add(-w.age))
		var heard, fresh int
		if err := s.db.QueryRow("SELECT count(*) FROM vessels WHERE seen >= ?", cut).Scan(&heard); err != nil {
			return nil, err
		}
		if err := s.db.QueryRow("SELECT count(*) FROM vessels WHERE first_seen >= ?", cut).Scan(&fresh); err != nil {
			return nil, err
		}
		c.Heard[w.key], c.New[w.key] = heard, fresh
	}
	return c, nil
}

// countFirstSeen is the number of vessels first heard at or after since.
func (s *store) countFirstSeen(since time.Time) (int, error) {
	if s.mirror != nil {
		return s.mirror.countFirstSeen(since), nil
	}
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM vessels WHERE first_seen >= ?", unixMs(since)).Scan(&n)
	return n, err
}

// sql is the statement find runs for q.
func (q recordQuery) sql() (text string, args []any, none bool, err error) {
	clause, args, none, byCell, err := q.bounded()
	if err != nil || none {
		return "", nil, none, err
	}
	text = "SELECT " + recordCols + " FROM vessels" + clause
	switch {
	case q.byName:
		text += " ORDER BY search, mmsi"
	case byCell:
		text += " ORDER BY +seen DESC"
	default:
		text += " ORDER BY seen DESC"
	}
	if q.limit > 0 {
		text += " LIMIT " + strconv.Itoa(q.limit)
	}
	return text, args, false, nil
}

// storedPos is where the record last placed a vessel, and when.
type storedPos struct {
	mmsi     uint32
	lat, lon float64
	posAt    time.Time
}

// positions is where each vessel q matches was last placed, ignoring its limit: the few columns a ranking
// by distance needs, read for every match so none is cut before it is ranked.
func (s *store) positions(q recordQuery) ([]storedPos, error) {
	if s.mirror.answers(q) {
		return s.mirror.positions(q)
	}
	clause, args, none, _, err := q.bounded()
	if err != nil || none {
		return nil, err
	}
	rows, err := s.db.Query("SELECT mmsi, lat, lon, pos_at FROM vessels"+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedPos
	for rows.Next() {
		var p storedPos
		var at int64
		if err := rows.Scan(&p.mmsi, &p.lat, &p.lon, &at); err != nil {
			return nil, err
		}
		p.posAt = time.UnixMilli(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// find is the rows q matches, most recently heard first: from the mirror, or from SQLite for a text search.
func (s *store) find(q recordQuery) ([]record, error) {
	if s.mirror.answers(q) {
		return s.mirror.find(q)
	}
	return s.findSQL(q)
}

func (s *store) findSQL(q recordQuery) ([]record, error) {
	sqlText, args, none, err := q.sql()
	if err != nil || none {
		return nil, err
	}
	return s.scan(sqlText, args...)
}

// scan reads the records a statement selecting recordCols answers.
func (s *store) scan(sqlText string, args ...any) ([]record, error) {
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		var r record
		var eta, posAt, trustedAt, staticAt, seen, first int64
		v := newVessel()
		if err := rows.Scan(&r.mmsi, &v.Name, &v.Kind, &v.Class, &v.ShipType, &v.IMO, &v.CallSign, &v.Destination, &eta,
			&v.Draught, &v.Length, &v.Beam, &v.Dim.A, &v.Dim.B, &v.Dim.C, &v.Dim.D, &v.HasPos, &v.Lat, &v.Lon, &v.Cog, &v.Sog, &v.Heading, &v.NavStatus,
			&posAt, &trustedAt, &staticAt, &seen, &first, &v.Source, &v.Station, &v.MsgType); err != nil {
			return nil, err
		}
		v.ETA, v.PosAt, v.Seen = unpackETA(eta), fromMs(posAt), fromMs(seen)
		v.TrustedAt, v.StaticAt = fromMs(trustedAt), fromMs(staticAt)
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

// searchKey is a name as the search column stores and queries it: upper-cased, punctuation dropped,
// spaces collapsed. AIS carries apostrophes and periods and people type them or not, so RUBY'S STAR
// is found by RUBYS STAR and by its own spelling alike.
func searchKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
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

// attachStore connects the record to the cache and fills the cache with every vessel the record heard in
// the last 30 minutes, so a restart resumes the map the last process left. A vessel the cache already
// holds keeps its cached state, which is at least as new, and is marked for its first write.
func (p *Pipeline) attachStore(s *store) error {
	cutoff := time.Now().Add(-vesselTTL)
	recs, err := s.find(recordQuery{since: cutoff})
	if err != nil {
		return err
	}
	p.vmu.Lock()
	p.store = s
	p.dirty = make(map[uint32]struct{}, len(p.vessels))
	for mmsi := range p.vessels {
		p.dirty[mmsi] = struct{}{}
	}
	for _, r := range recs {
		if p.vessels[r.mmsi] != nil {
			continue
		}
		// The row keeps a vessel's last fix however old. A vessel heard lately but whose position is older
		// than the cache keeps (back from the sweep with only statics or no-fix reports) is off the map.
		if r.v.HasPos && r.v.PosAt.Before(cutoff) {
			r.v.HasPos, r.v.Lat, r.v.Lon, r.v.PosAt = false, 0, 0, time.Time{}
			r.v.Cog, r.v.Sog, r.v.Heading = 360, 102.3, 511
		}
		p.putVesselLocked(r.mmsi, r.v)
	}
	p.vmu.Unlock()
	return nil
}

// flushStore writes the vessels folded since the last flush to the record. What it writes is copied under the
// cache lock and written outside it, so a slow disk delays the record and never the fold.
func (p *Pipeline) flushStore() error {
	if p.store == nil {
		return nil
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	if p.storesClosed {
		return nil
	}
	return p.flushRecord()
}

// closeStore writes what is left and closes the record. It waits for a flush already running, and every flush
// after it does nothing, so the database is never closed under a write.
func (p *Pipeline) closeStore() error {
	if p.store == nil {
		return nil
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	if p.storesClosed {
		return nil
	}
	p.storesClosed = true
	return errors.Join(p.flushRecord(), p.store.close())
}

func (p *Pipeline) flushRecord() error {
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
		// Marked again, so the next flush retries: a vessel that never reports again would otherwise keep
		// a stale row. The retry writes whatever the cache holds by then.
		p.store.flushFailures.Add(1)
		p.vmu.Lock()
		for _, r := range rows {
			p.dirty[r.mmsi] = struct{}{}
		}
		p.vmu.Unlock()
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
		Destination: v.Destination, ETA: v.ETA, Draught: v.Draught, Length: v.Length, Beam: v.Beam, Dim: v.Dim, Seen: v.Seen,
		Source: v.Source, Station: v.Station, MsgType: v.MsgType, TrustedAt: v.TrustedAt, PosAt: v.PosAt, StaticAt: v.StaticAt,
	}
}

// merge fills a cache state from the vessel's row by the rules upsertSQL applies, and reports whether the
// row added anything. A vessel that returns after the sweep comes back blank, so until it resends its
// static report the row still holds its name and particulars. The newer position wins, the newer seen
// wins along with its source, and a blank field takes the stored value.
func (v *vessel) merge(o *vessel) (changed bool) {
	fill := func(blank, has bool, set func()) {
		if blank && has {
			set()
			changed = true
		}
	}
	fill(v.Name == "", o.Name != "", func() { v.Name = o.Name })
	fill(v.Kind == "vessel", o.Kind != "vessel", func() { v.Kind = o.Kind })
	fill(v.Class == "", o.Class != "", func() { v.Class = o.Class })
	fill(v.ShipType == 0, o.ShipType != 0, func() { v.ShipType = o.ShipType })
	fill(v.IMO == 0, o.IMO != 0, func() { v.IMO = o.IMO })
	fill(v.CallSign == "", o.CallSign != "", func() { v.CallSign = o.CallSign })
	fill(v.Destination == "", o.Destination != "", func() { v.Destination = o.Destination })
	fill(v.ETA.Month == 0, o.ETA.Month != 0, func() { v.ETA = o.ETA })
	fill(v.Draught == 0, o.Draught != 0, func() { v.Draught = o.Draught })
	fill(v.Length == 0, o.Length != 0, func() { v.Length, v.Dim.A, v.Dim.B = o.Length, o.Dim.A, o.Dim.B })
	fill(v.Beam == 0, o.Beam != 0, func() { v.Beam, v.Dim.C, v.Dim.D = o.Beam, o.Dim.C, o.Dim.D })
	fill(!v.HasPos || o.PosAt.After(v.PosAt), o.HasPos, func() {
		v.Lat, v.Lon, v.HasPos, v.PosAt, v.Cog, v.Sog, v.Heading = o.Lat, o.Lon, true, o.PosAt, o.Cog, o.Sog, o.Heading
	})
	fill(v.NavStatus == 15 || o.Seen.After(v.Seen), o.NavStatus != 15 && o.NavStatus != v.NavStatus, func() { v.NavStatus = o.NavStatus })
	fill(o.Seen.After(v.Seen), true, func() { v.Seen, v.Source, v.Station, v.MsgType = o.Seen, o.Source, o.Station, o.MsgType })
	return changed
}

var errNoStore = errors.New("the vessel record is not available on this server")

// maxBoxes bounds the boxes one record query takes. It is far past what a map or a region needs, and short
// of SQLite's expression depth.
const maxBoxes = 256

// maxParams is SQLite's limit on bound parameters in one statement. bbox repeats and some tokens follow
// any number of MMSIs, so a request can ask for more terms than one query can bind; it is refused as the
// caller's to narrow, not failed as the record's.
const maxParams = 32766

var errTooManyTerms = errors.New("too many bbox or mmsi for one request")
