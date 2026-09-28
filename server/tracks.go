package main

// Recent positions: every position report the pipeline accepts, kept for the last two to three days in a
// SQLite file of its own beside the vessel record, for GET /v1/vessels/{mmsi}/track. It is its own file
// because it is large, around a hundred million rows, and the record is the file an operator copies when
// replacing the box.
//
// Each UTC day is one table keyed by (mmsi, ts), so a track is a range read in each day it spans, and
// expiry drops a whole table instead of deleting tens of millions of rows. The fold appends each accepted
// position to a pending list under the cache lock it already holds, and the record's writer drains it once
// a second. Nothing attaches a track store in replay, so replay never writes here.

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// trackWindow is how far back a track reaches. Day tables are kept until the window has left them.
const trackWindow = 48 * time.Hour

// maxPending bounds the positions held for the track writer, about eight minutes of traffic. Past it the
// disk has stalled, and dropping new positions keeps memory flat; the drops are counted.
const maxPending = 200_000

// trackPoint is one accepted position report. Positions and motion are held in the lake's integer
// encodings: 1/600000 degree, 0.1 knot (1023 not available), 0.1 degree (3600 not available).
type trackPoint struct {
	mmsi      uint32
	ts        time.Time
	lat6      int32
	lon6      int32
	sog10     uint16
	cog10     uint16
	heading   uint16 // 511 not available
	navStatus uint8  // 15 not available
	source    string // source kind, for attribution
}

func newTrackPoint(mmsi uint32, ts time.Time, u *vessel, source string) trackPoint {
	pt := trackPoint{mmsi: mmsi, ts: ts, lat6: int32(math.Round(u.Lat * 600000)), lon6: int32(math.Round(u.Lon * 600000)),
		sog10: 1023, cog10: 3600, heading: u.Heading, navStatus: u.NavStatus, source: sourceKind(source)}
	if u.Sog < 102.3 {
		pt.sog10 = uint16(math.Round(u.Sog * 10))
	}
	if u.Cog < 360 {
		pt.cog10 = uint16(math.Round(u.Cog * 10))
	}
	return pt
}

type trackStore struct {
	db   *sql.DB
	path string

	sources map[string]int64 // source kind -> id in the sources table; touched only by the writer
	names   atomic.Pointer[map[int64]string]
	days    map[string]bool // day tables known to exist; touched only by the writer

	// read by /metrics
	pointsWritten, writeFailures, dropped atomic.Int64
	writeNanos                            atomic.Int64
}

func openTracks(path string) (*trackStore, error) {
	// The cache pragma is per connection: the day tables are written at random MMSIs, and a larger page
	// cache keeps their interior pages in memory between flushes.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-65536)")
	if err != nil {
		return nil, err
	}
	t := &trackStore{db: db, path: path, sources: map[string]int64{}, days: map[string]bool{}}
	if err := t.load(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func (t *trackStore) load() error {
	if _, err := t.db.Exec(`CREATE TABLE IF NOT EXISTS sources (id INTEGER PRIMARY KEY, kind TEXT NOT NULL UNIQUE)`); err != nil {
		return err
	}
	rows, err := t.db.Query(`SELECT id, kind FROM sources`)
	if err != nil {
		return err
	}
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return err
		}
		t.sources[kind], names[id] = id, kind
	}
	rows.Close()
	t.names.Store(&names)
	tables, err := t.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'positions_%'`)
	if err != nil {
		return err
	}
	defer tables.Close()
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			return err
		}
		t.days[strings.TrimPrefix(name, "positions_")] = true
	}
	return tables.Err()
}

func (t *trackStore) close() error { return t.db.Close() }

// bytes is the size of the database and its write-ahead log on disk.
func (t *trackStore) bytes() int64 {
	var n int64
	for _, f := range []string{t.path, t.path + "-wal"} {
		if st, err := os.Stat(f); err == nil {
			n += st.Size()
		}
	}
	return n
}

func dayKey(ts time.Time) string { return ts.UTC().Format("20060102") }

// keepDay reports whether a day's table can still hold positions inside the window.
func keepDay(day string, now time.Time) bool {
	return day >= dayKey(now.Add(-trackWindow))
}

// write stores points in one transaction, creating day tables as they are first needed and dropping those
// the window has left. A point for a day already dropped is skipped: it is older than any track reaches.
func (t *trackStore) write(points []trackPoint, now time.Time) error {
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var created, dropped []string
	for day := range t.days {
		if !keepDay(day, now) {
			if _, err := tx.Exec(`DROP TABLE positions_` + day); err != nil {
				return err
			}
			dropped = append(dropped, day)
		}
	}
	stmts := map[string]*sql.Stmt{}
	defer func() {
		for _, st := range stmts {
			st.Close()
		}
	}()
	newSources := map[string]int64{}
	for _, pt := range points {
		day := dayKey(pt.ts)
		if !keepDay(day, now) {
			continue
		}
		st := stmts[day]
		if st == nil {
			if !t.days[day] {
				if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS positions_` + day + ` (
					mmsi INTEGER NOT NULL, ts INTEGER NOT NULL, lat6 INTEGER NOT NULL, lon6 INTEGER NOT NULL,
					sog10 INTEGER NOT NULL, cog10 INTEGER NOT NULL, heading INTEGER NOT NULL, nav_status INTEGER NOT NULL,
					src INTEGER NOT NULL, PRIMARY KEY (mmsi, ts)) WITHOUT ROWID`); err != nil {
					return err
				}
				created = append(created, day)
			}
			if st, err = tx.Prepare(`INSERT OR REPLACE INTO positions_` + day +
				` (mmsi, ts, lat6, lon6, sog10, cog10, heading, nav_status, src) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
				return err
			}
			stmts[day] = st
		}
		src, ok := t.sources[pt.source]
		if !ok {
			if src, ok = newSources[pt.source]; !ok {
				if err := tx.QueryRow(`INSERT INTO sources (kind) VALUES (?) RETURNING id`, pt.source).Scan(&src); err != nil {
					return err
				}
				newSources[pt.source] = src
			}
		}
		if _, err := st.Exec(pt.mmsi, pt.ts.UnixMilli(), pt.lat6, pt.lon6, pt.sog10, pt.cog10, pt.heading, pt.navStatus, src); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Only a committed transaction changes what exists.
	for _, d := range dropped {
		delete(t.days, d)
	}
	for _, d := range created {
		t.days[d] = true
	}
	if len(newSources) > 0 {
		names := map[int64]string{}
		for id, kind := range *t.names.Load() {
			names[id] = kind
		}
		for kind, id := range newSources {
			t.sources[kind], names[id] = id, kind
		}
		t.names.Store(&names)
	}
	return nil
}

// first is the time of the vessel's earliest position between from and to; ok is false when there is none.
func (t *trackStore) first(mmsi uint32, from, to time.Time) (first time.Time, ok bool, err error) {
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to); d = d.Add(24 * time.Hour) {
		day := dayKey(d)
		var exists int
		if err := t.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, "positions_"+day).Scan(&exists); err != nil {
			return first, false, err
		}
		if exists == 0 {
			continue
		}
		var ts sql.NullInt64
		if err := t.db.QueryRow(`SELECT min(ts) FROM positions_`+day+` WHERE mmsi = ? AND ts BETWEEN ? AND ?`, mmsi, from.UnixMilli(), to.UnixMilli()).Scan(&ts); err != nil {
			return first, false, err
		}
		if ts.Valid {
			return time.UnixMilli(ts.Int64).UTC(), true, nil
		}
	}
	return first, false, nil
}

// track reads one vessel's positions between from and to, oldest first. interval thins the track to the
// first position in each interval. When more than limit match, the newest limit are returned and more is
// true.
func (t *trackStore) track(mmsi uint32, from, to time.Time, interval time.Duration, limit int) (points []trackPoint, more bool, err error) {
	var parts []string
	var args []any
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to); d = d.Add(24 * time.Hour) {
		day := dayKey(d)
		var exists int
		if err := t.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, "positions_"+day).Scan(&exists); err != nil {
			return nil, false, err
		}
		if exists == 0 {
			continue
		}
		parts = append(parts, `SELECT ts, lat6, lon6, sog10, cog10, heading, nav_status, src FROM positions_`+day+` WHERE mmsi = ? AND ts BETWEEN ? AND ?`)
		args = append(args, mmsi, from.UnixMilli(), to.UnixMilli())
	}
	if len(parts) == 0 {
		return nil, false, nil
	}
	q := strings.Join(parts, " UNION ALL ")
	if ms := interval.Milliseconds(); ms > 0 {
		// SQLite takes the other columns of an aggregate query with min() from the row holding the minimum,
		// so each group yields its first position whole.
		q = `SELECT min(ts), lat6, lon6, sog10, cog10, heading, nav_status, src FROM (` + q + `) GROUP BY ts / ` + fmt.Sprint(ms)
	}
	q = `SELECT * FROM (` + q + `) ORDER BY 1 DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	names := *t.names.Load()
	for rows.Next() {
		var ts, src int64
		pt := trackPoint{mmsi: mmsi}
		if err := rows.Scan(&ts, &pt.lat6, &pt.lon6, &pt.sog10, &pt.cog10, &pt.heading, &pt.navStatus, &src); err != nil {
			return nil, false, err
		}
		pt.ts, pt.source = time.UnixMilli(ts).UTC(), names[src]
		points = append(points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(points) > limit {
		points, more = points[:limit], true
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ts.Before(points[j].ts) })
	return points, more, nil
}

// ---- the pipeline side ----

func (p *Pipeline) attachTracks(t *trackStore) {
	p.vmu.Lock()
	p.tracks = t
	p.trackQueue = make([]trackPoint, 0, 1024)
	p.vmu.Unlock()
}

// notePosition queues an accepted position for the track store. The caller holds vmu.
func (p *Pipeline) notePosition(mmsi uint32, ts time.Time, u *vessel, source string) {
	if p.trackQueue == nil {
		return
	}
	if len(p.trackQueue) >= maxPending {
		p.tracks.dropped.Add(1)
		return
	}
	p.trackQueue = append(p.trackQueue, newTrackPoint(mmsi, ts, u, source))
}

func (p *Pipeline) flushTracks() error {
	if p.tracks == nil {
		return nil
	}
	p.vmu.Lock()
	points := p.trackQueue
	if len(points) == 0 {
		p.vmu.Unlock()
		return nil
	}
	p.trackQueue = make([]trackPoint, 0, len(points))
	p.vmu.Unlock()
	start := time.Now()
	err := p.tracks.write(points, time.Now())
	p.tracks.writeNanos.Add(int64(time.Since(start)))
	if err != nil {
		// Put back ahead of what arrived since, so the next flush retries in order, within the same bound.
		p.tracks.writeFailures.Add(1)
		p.vmu.Lock()
		room := maxPending - len(p.trackQueue)
		if room < len(points) {
			p.tracks.dropped.Add(int64(len(points) - max(room, 0)))
			points = points[len(points)-max(room, 0):]
		}
		p.trackQueue = append(points, p.trackQueue...)
		p.vmu.Unlock()
		return err
	}
	p.tracks.pointsWritten.Add(int64(len(points)))
	return nil
}
