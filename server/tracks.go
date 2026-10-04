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
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// trackWindow is how far back a track reaches. Day tables are kept until the window has left them.
const trackWindow = 48 * time.Hour

// maxPending bounds the positions held for the track writer, about eight minutes of traffic at the 600 or so
// positions a second the network carries at peak, around 20 MB. Past it the disk has stalled, and dropping new
// positions keeps memory flat; the drops are counted.
const maxPending = 300_000

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

	// What a copy adds as a reception, written and not read back. The zero values are the table's defaults:
	// accepted, corroborated, moving, and a transmission of its own.
	txAt           time.Time // the transmission this is a copy of: its accepted copy's canonical time
	txDisc         uint8     // and discOf that copy's event id, telling apart transmissions stamped in the same millisecond
	recv           time.Time // when this copy arrived
	station        string
	dup            bool // a later copy of a transmission another copy delivered first
	uncorroborated bool // from an unauthenticated sender, for a vessel no trusted source heard lately
	implausible    bool // the fold judged it an impossible jump from the vessel's last position
	clockBad       bool // stamped clockBadAge or more before it arrived
	still          bool // not moving: reported speed of half a knot or less, or, with none, within movedM of the vessel's last position
}

// discOf is one byte of an event id, the low byte of its first 64 bits, which with the vessel and the time its
// accepted copy was stamped names a transmission: ids repeat for identical payloads minutes apart, so the time
// tells those apart, and the byte tells apart two transmissions of one vessel stamped in the same millisecond.
// On three hours of production receptions the time and the byte together merged 83 of 5 M transmissions, all
// a vessel's reports in one millisecond. ClickHouse computes the same byte from the hex id, so a load from the
// lake names transmissions as the server does.
func discOf(id string) uint8 {
	if len(id) < 16 { // not an event id, as for an event built without one; hash it into one
		id = eventID(id)
	}
	h, _ := strconv.ParseUint(id[:16], 16, 64)
	return uint8(h)
}

// movedM is how far a vessel that reports no speed must be from its last position to count as moving, the
// distance ais.tracks uses: a speed worked out between reports seconds apart is mostly GPS jitter.
const movedM = 50

// isStill reports whether pt is a vessel sitting still, for positions_1m. Reported speed decides when there is
// one, since it does not jitter. Without one, about 0.3% of reports in every feed, from a transmitter whose GPS
// gives it no speed, pt is still if it is within movedM of the vessel's last position, as 91% of those were in
// a sample of production receptions; with no last position it counts as moving, so a voyage is never hidden.
func isStill(pt trackPoint, hadPrev bool, prevLat, prevLon float64) bool {
	if pt.sog10 != 1023 {
		return pt.sog10 <= 5
	}
	return hadPrev && nm(prevLat, prevLon, float64(pt.lat6)/600000, float64(pt.lon6)/600000)*1852 <= movedM
}

// clockBadAge is how far before its arrival a copy's stamp may be before the copy is kept out of history: past
// a satellite pass's hours of delay, and short of a device whose reset clock stamps it years back.
const clockBadAge = 24 * time.Hour

// A rebuilt copy (AISHub, aisstream, BarentsWatch) never byte-matches the raw copy it repeats, and AISHub's
// arrives about a minute behind, after the vessel has sent newer reports, so the fold cannot tell it from a late
// report by time alone. Each vessel keeps its accepted positions of the last few minutes, and a stale rebuilt
// copy at one of their positions is a copy of that transmission. A vessel underway moves between reports, so its
// position names one; a moored one repeats its position, and any of those transmissions is the same point.
const (
	recentKeep  = 5 * time.Minute
	recentMax   = 32 // positions a vessel keeps, enough for one reporting every 10 s
	recentNearA = 3  // wire units of latitude or longitude, about 5 m, for a source that rounds its coordinates
)

type recentPos struct {
	ms         int64 // Unix milliseconds, the transmission's time too: 24 bytes an entry where a time.Time would make it 40
	lat6, lon6 int32
	disc       uint8
}

// remember adds an accepted position to the vessel's recent ones, dropping those too old to be repeated.
func (v *vessel) remember(pt trackPoint) {
	ms := pt.ts.UnixMilli()
	keep := v.recent[:0]
	for _, r := range v.recent {
		if ms-r.ms < recentKeep.Milliseconds() {
			keep = append(keep, r)
		}
	}
	if len(keep) == recentMax {
		keep = append(keep[:0], keep[1:]...)
	}
	v.recent = append(keep, recentPos{ms, pt.lat6, pt.lon6, pt.txDisc})
}

// repeats is the transmission among the vessel's recent positions that pt is a copy of, its time and byte: the
// nearest in time at pt's position.
func (v *vessel) repeats(pt trackPoint) (time.Time, uint8, bool) {
	ms := pt.ts.UnixMilli()
	var best recentPos
	bestDt := int64(-1)
	for _, r := range v.recent {
		dt := max(ms-r.ms, r.ms-ms)
		if dt < recentKeep.Milliseconds() && absInt(r.lat6-pt.lat6) <= recentNearA && absInt(r.lon6-pt.lon6) <= recentNearA && (bestDt < 0 || dt < bestDt) {
			best, bestDt = r, dt
		}
	}
	return time.UnixMilli(best.ms), best.disc, bestDt >= 0
}

// jumps reports whether pt implies an impossible speed from the vessel's position nearest it in time, among its
// recent ones and its latest: the fold's own test, for a stale report the fold does not test.
func (v *vessel) jumps(pt trackPoint) bool {
	ms := pt.ts.UnixMilli()
	lat, lon, at, found := 0.0, 0.0, int64(0), false
	if v.HasPos {
		lat, lon, at, found = v.Lat, v.Lon, v.PosAt.UnixMilli(), true
	}
	for _, r := range v.recent {
		if !found || max(ms-r.ms, r.ms-ms) < max(ms-at, at-ms) {
			lat, lon, at, found = float64(r.lat6)/600000, float64(r.lon6)/600000, r.ms, true
		}
	}
	if !found {
		return false
	}
	dt := float64(max(ms-at, at-ms)) / 1000
	d := nm(lat, lon, float64(pt.lat6)/600000, float64(pt.lon6)/600000)
	return dt >= 1 && d > implausibleJumpNM && d/(dt/3600) > implausibleKnots
}

func absInt(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
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
	db   *sql.DB // the writer: one connection, with a large page cache
	rdb  *sql.DB // readers: a few connections with SQLite's default cache
	path string

	sources map[string]int64 // source kind -> id in the sources table; touched only by the writer
	days    map[string]bool  // day tables known to exist; touched only by the writer

	lakeBytes atomic.Int64 // position bytes cached from the lake; each lakeTrimEvery trims the cache

	// read by /metrics
	pointsWritten, writeFailures, dropped atomic.Int64
	writeNanos                            atomic.Int64
}

// trackReaders bounds the connections serving track requests. Each holds its own page cache, and the pool
// is otherwise unlimited, so a burst of public requests could open as many connections as it liked.
const trackReaders = 4

func openTracks(path string) (*trackStore, error) {
	const dsn = "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	// The writer gets a 64 MB page cache: the day tables are written at random MMSIs, and a large cache
	// keeps their interior pages in memory between flushes. It is one connection, since SQLite runs one
	// writer at a time anyway, so the cache is paid for once.
	db, err := sql.Open("sqlite", "file:"+path+dsn+"&_pragma=cache_size(-65536)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	rdb, err := sql.Open("sqlite", "file:"+path+dsn)
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(trackReaders)
	t := &trackStore{db: db, rdb: rdb, path: path, sources: map[string]int64{}, days: map[string]bool{}}
	if err := t.load(); err != nil {
		t.close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func (t *trackStore) load() error {
	if _, err := t.db.Exec(`CREATE TABLE IF NOT EXISTS sources (id INTEGER PRIMARY KEY, kind TEXT NOT NULL UNIQUE)`); err != nil {
		return err
	}
	if _, err := t.db.Exec(lakeCacheSchema); err != nil {
		return err
	}
	rows, err := t.db.Query(`SELECT id, kind FROM sources`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return err
		}
		t.sources[kind] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
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

func (t *trackStore) close() error { return errors.Join(t.db.Close(), t.rdb.Close()) }

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
					src INTEGER NOT NULL, PRIMARY KEY (mmsi, ts, lat6, lon6)) WITHOUT ROWID`); err != nil {
					return err
				}
				created = append(created, day)
			}
			// Two accepted reports can share a vessel and a millisecond: raw reports with equal stamps survive
			// dedupe as distinct data. The position is in the key so both are kept; a second report at the same
			// time and place is the same point on a track, and the first one written stays.
			if st, err = tx.Prepare(`INSERT OR IGNORE INTO positions_` + day +
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
	for kind, id := range newSources {
		t.sources[kind] = id
	}
	return nil
}

// read runs fn in one read transaction. In WAL mode it sees a single snapshot, so the day tables it finds
// and the rows it reads agree even when the writer drops a day at midnight in between.
func (t *trackStore) read(fn func(tx *sql.Tx) error) error {
	tx, err := t.rdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// dayTables lists the day tables that exist between from and to.
func dayTables(tx *sql.Tx, from, to time.Time) ([]string, error) {
	var tables []string
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to); d = d.Add(24 * time.Hour) {
		name := "positions_" + dayKey(d)
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&exists); err != nil {
			return nil, err
		}
		if exists > 0 {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

// first is the time of the vessel's earliest position between from and to; ok is false when there is none.
func (t *trackStore) first(mmsi uint32, from, to time.Time) (first time.Time, ok bool, err error) {
	err = t.read(func(tx *sql.Tx) error {
		tables, err := dayTables(tx, from, to)
		if err != nil {
			return err
		}
		for _, table := range tables {
			var ts sql.NullInt64
			if err := tx.QueryRow(`SELECT min(ts) FROM `+table+` WHERE mmsi = ? AND ts BETWEEN ? AND ?`, mmsi, from.UnixMilli(), to.UnixMilli()).Scan(&ts); err != nil {
				return err
			}
			if ts.Valid {
				first, ok = time.UnixMilli(ts.Int64).UTC(), true
				return nil
			}
		}
		return nil
	})
	return first, ok, err
}

// track reads one vessel's positions between from and to, oldest first. interval thins the track to the
// first position in each interval. When more than limit match, the newest limit are returned and more is
// true. Positions whose implied speed from their neighbors is impossible are dropped (despike).
func (t *trackStore) track(mmsi uint32, from, to time.Time, interval time.Duration, limit int) (points []trackPoint, more bool, err error) {
	err = t.read(func(tx *sql.Tx) error {
		tables, err := dayTables(tx, from, to)
		if err != nil || len(tables) == 0 {
			return err
		}
		var parts []string
		var args []any
		for _, table := range tables {
			parts = append(parts, `SELECT ts, lat6, lon6, sog10, cog10, heading, nav_status, src FROM `+table+` WHERE mmsi = ? AND ts BETWEEN ? AND ?`)
			args = append(args, mmsi, from.UnixMilli(), to.UnixMilli())
		}
		q := strings.Join(parts, " UNION ALL ")
		if ms := interval.Milliseconds(); ms > 0 {
			// SQLite takes the other columns of an aggregate query with min() from the row holding the
			// minimum, so each group yields its first position whole.
			q = `SELECT min(ts), lat6, lon6, sog10, cog10, heading, nav_status, src FROM (` + q + `) GROUP BY ts / ` + fmt.Sprint(ms)
		}
		// The source names come from the same snapshot as the rows, so a source the writer has just added
		// is always named.
		q = `SELECT p.*, coalesce(s.kind, '') FROM (SELECT * FROM (` + q + `) ORDER BY 1 DESC LIMIT ?) p LEFT JOIN sources s ON s.id = p.src ORDER BY 1 DESC`
		args = append(args, limit+1)
		rows, err := tx.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ts, src int64
			pt := trackPoint{mmsi: mmsi}
			if err := rows.Scan(&ts, &pt.lat6, &pt.lon6, &pt.sog10, &pt.cog10, &pt.heading, &pt.navStatus, &src, &pt.source); err != nil {
				return err
			}
			pt.ts = time.UnixMilli(ts).UTC()
			points = append(points, pt)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	// The row past the limit says older positions match (more). It also anchors despiking: judged
	// against it, the page's oldest row survives or falls the same way it would inside a larger
	// request, instead of always being kept as the first point seen. When the anchor row itself
	// survives the filter it is withheld from the page, as it was before despiking existed; a run
	// can also outvote it inside despike, and then there is nothing to withhold.
	anchored := false
	if len(points) > limit {
		more, anchored = true, true
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].ts.Before(points[j].ts) })
	var anchor trackPoint
	if anchored {
		anchor = points[0]
	}
	points = despike(points)
	if anchored && len(points) > 0 && points[0].ts.Equal(anchor.ts) && points[0].lat6 == anchor.lat6 && points[0].lon6 == anchor.lon6 {
		points = points[1:]
	}
	return points, more, nil
}

// The store keeps every accepted position, but a track drawn straight through them kinks wherever a
// report's stamp disagrees with its fix: AISHub's snapshot stamps run tens of seconds off the
// positions they carry, and a few vessels broadcast broken fixes outright. Those errors are metres
// to a few hundred metres — far under the ingest gate's 10 NM teleport floor — so they are only
// visible here, as segments implying two to forty times the vessel's speed. Serving is the one
// place that can judge them: the fix time never arrives to correct the stamp, and dropping a point
// from a drawn line loses nothing the store does not still hold.
const (
	despikeFloorNM  = 0.03 // under ~55 m a segment cannot draw a visible kink, and jitter over a short dt implies any speed
	despikeMinKnots = 25.0 // fastest implied speed always kept, whatever the vessel reports
	despikeMaxRun   = 3    // consecutive drops before the run outvotes the anchor and the next point re-anchors
)

// despike walks a track oldest first and drops each position implying an impossible speed from the
// last kept one: over despikeMinKnots and more than twice either endpoint's reported speed. A run of
// drops longer than despikeMaxRun outvotes the anchor, and the next position re-anchors rather than
// erasing the rest of the track. An anchor with no plausible kept segment behind it — the oldest
// point, or a prior forced keep — was never corroborated and goes with its run, so the drawn line
// does not connect two impossible positions. A corroborated anchor stays: both sides are then real
// reports (a duplicate MMSI transmitting from two places), a LineString cannot show a break, and one
// straight jump is the honest rendering.
func despike(points []trackPoint) []trackPoint {
	kept := points[:0]
	run := 0
	corroborated := false // the current anchor has a plausible kept segment behind it
	for _, pt := range points {
		if len(kept) == 0 {
			kept = append(kept, pt)
			continue
		}
		a := kept[len(kept)-1]
		dt := pt.ts.Sub(a.ts).Seconds()
		d := nm(float64(a.lat6)/600000, float64(a.lon6)/600000, float64(pt.lat6)/600000, float64(pt.lon6)/600000)
		vmax := despikeMinKnots
		if a.sog10 != 1023 {
			vmax = max(vmax, 2*float64(a.sog10)/10)
		}
		if pt.sog10 != 1023 {
			vmax = max(vmax, 2*float64(pt.sog10)/10)
		}
		// Equal stamps are distinct reports the store keeps (see TestTrackKeepsEqualTimeReports);
		// with no time between them there is no speed to judge.
		if dt > 0 && d > despikeFloorNM && d/(dt/3600) > vmax {
			if run < despikeMaxRun {
				run++
				continue
			}
			if !corroborated {
				kept = kept[:len(kept)-1]
			}
			kept = append(kept, pt)
			corroborated = false
			run = 0
			continue
		}
		run = 0
		kept = append(kept, pt)
		corroborated = true
	}
	return kept
}

// ---- the pipeline side ----

func (p *Pipeline) attachTracks(t *trackStore) {
	p.vmu.Lock()
	p.tracks = t
	p.trackQueue = make([]trackPoint, 0, 1024)
	p.vmu.Unlock()
}

// notePosition queues an accepted position for the track store. The caller holds vmu. ClickHouse takes every
// copy instead, through noteReception.
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

// noteReception queues a copy for ClickHouse.
func (p *Pipeline) noteReception(pt trackPoint) {
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if p.chQueue == nil {
		return
	}
	// A full queue gives up its oldest tenth, so it holds the latest copies; a batch waiting to be sent again is
	// kept apart and still goes. A tenth at a time keeps the copy rare.
	if len(p.chQueue) >= maxPending {
		n := maxPending / 10
		p.ch.dropped.Add(int64(n))
		p.chQueue = append(p.chQueue[:0], p.chQueue[n:]...)
	}
	p.chQueue = append(p.chQueue, pt)
}

// noteCopy queues a copy that dedupe matched by payload to the transmission accepted at tx. It decodes to the
// accepted copy's position, so it needs no fold, and it is implausible when that copy was: otherwise a read that
// skips flagged copies would serve the position through this one.
func (p *Pipeline) noteCopy(ev *Event, key string, tx time.Time, implausible bool) {
	if pt, ok := copyPoint(ev, key, tx); ok {
		pt.implausible = implausible
		p.noteReception(pt)
	}
}

// copyPoint is a dedupe copy as a reception, or false when it carries no position.
func copyPoint(ev *Event, key string, tx time.Time) (trackPoint, bool) {
	u, hasPos, _ := foldOf(ev.Packet)
	if !hasPos {
		return trackPoint{}, false
	}
	pt := newTrackPoint(ev.Packet.GetHeader().UserID, ev.Time, u, ev.Source)
	pt.txAt, pt.txDisc, pt.dup = tx, discOf(eventID(key)), true
	pt.recv, pt.station = ev.RecvTime, ev.Station
	pt.uncorroborated = lowTrust(ev.Source)
	pt.clockBad = ev.RecvTime.Sub(ev.Time) >= clockBadAge
	return pt, true
}
