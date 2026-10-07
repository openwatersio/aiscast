package main

// Historical archives: government AIS archives loaded into receptions as late copies, each from a feed the
// network does not run. A source lists its files, one UTC day each, and gives the SELECT that reads a file as
// historyColumns; ClickHouse downloads and parses the file itself into a staging table. The loader then reads
// the staged day a vessel at a time, beside the transmissions the network already holds, and writes through the
// live writer's own insert, so an archive row is named, judged moving or still, and stamped with its arrival as
// a live copy would be. Its statics fold into vessel_statics. history_loads records every file, so a file loads
// once and a changed one loads again. See specs/historical-sources.md.

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// historySource is one archive.
type historySource struct {
	name string
	from time.Time // the first day to load
	// list returns the archive's files.
	list func(ctx context.Context) ([]historyFile, error)
	// read is a SELECT of one file's rows as historyColumns, in the archive's own order.
	read func(f historyFile) string
}

// historyFile is one day of an archive.
type historyFile struct {
	name string // the archive's own name for it, unique within the source
	url  string
	day  time.Time
	size int64
	etag string // whatever the archive gives that changes when the file does
}

// historyColumns is what a source's read returns, in the server's encodings. ts is the zero time when the row
// had none; the loader drops what it cannot place.
const historyColumns = `mmsi UInt32, ts DateTime64(3, 'UTC'), lat6 Int32, lon6 Int32, sog10 UInt16, cog10 UInt16, heading UInt16,
	navstat UInt8, name String, callsign String, imo UInt32, ship_type UInt8, length UInt16, beam UInt16, draught10 UInt16, destination String`

// chVesselStatics and chHistoryLoads are migration steps. vessel_statics keeps, per vessel and source, the
// earliest and latest report, the latest position, and the latest non-empty value of each static field;
// rows fold together whatever order the files load in, and a file loaded twice changes nothing.
// history_loads has one row per file, the latest kept.
const chVesselStatics = `CREATE TABLE IF NOT EXISTS {db}.vessel_statics (
		mmsi        UInt32,
		source      LowCardinality(String),
		first_ts    SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
		last_ts     SimpleAggregateFunction(max, DateTime64(3, 'UTC')),
		last_pos    AggregateFunction(argMax, Tuple(Int32, Int32), DateTime64(3, 'UTC')),
		name        AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
		callsign    AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
		imo         AggregateFunction(argMax, UInt32, DateTime64(3, 'UTC')),
		ship_type   AggregateFunction(argMax, UInt8, DateTime64(3, 'UTC')),
		length      AggregateFunction(argMax, UInt16, DateTime64(3, 'UTC')),
		beam        AggregateFunction(argMax, UInt16, DateTime64(3, 'UTC')),
		draught10   AggregateFunction(argMax, UInt16, DateTime64(3, 'UTC')),
		destination AggregateFunction(argMax, String, DateTime64(3, 'UTC'))
	) ENGINE = AggregatingMergeTree
	ORDER BY (mmsi, source)`

const chHistoryLoads = `CREATE TABLE IF NOT EXISTS {db}.history_loads (
		source      LowCardinality(String),
		file        String,
		day         Date,
		size        UInt64,
		etag        String,
		complete    Bool,
		read        UInt64,
		unplaced    UInt64,
		repeated    UInt64,
		kept        UInt64,
		matched     UInt64,
		implausible UInt64,
		loaded      DateTime64(3, 'UTC')
	) ENGINE = ReplacingMergeTree(loaded)
	ORDER BY (source, file)`

// historySettings bound every load query, so a load never takes the memory or the cores the live writer
// needs: ClickHouse refusing the writer for 10 minutes drops live positions. A slow archive host streams a
// file for minutes. The loader user's settings profile on the box says the same, so a query that forgets
// them is bounded too.
var historySettings = clickhouse.Settings{
	"max_memory_usage":                   1_500_000_000,
	"max_threads":                        2,
	"max_insert_threads":                 1,
	"max_bytes_before_external_sort":     500_000_000,
	"max_bytes_before_external_group_by": 500_000_000,
	"http_receive_timeout":               600,
	"max_execution_time":                 3600,
	"lightweight_deletes_sync":           2, // a reload's delete is done before its rows go in again
}

// historyBatch is how many receptions go to ClickHouse in one insert.
var historyBatch = 200_000

// historyFrom is the first day an archive loads: the date in env, else 2025-10-01.
func historyFrom(env string) time.Time {
	if t, err := time.Parse("2006-01-02", os.Getenv(env)); err == nil {
		return t
	}
	return time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
}

// historyCheckEvery is how often the loader lists each source for files it has not loaded.
const historyCheckEvery = 6 * time.Hour

// historyListTimeout bounds listing a source's files, so a host that stalls costs one check, not every one after.
var historyListTimeout = 5 * time.Minute

// historyStats is read by /metrics, per source.
type historyStats struct {
	mu      sync.Mutex
	latest  map[string]time.Time // the newest day loaded
	loaded  map[string]*atomic.Int64
	failed  map[string]*atomic.Int64
	checks  map[string]*atomic.Int64 // checks that failed before any file: the listing, history_loads, the connection
	rows    map[string]*atomic.Int64
	sources []string
}

// checkFailed counts err against source's checks when it is one, and returns it.
func (h *historyStats) checkFailed(source string, err error) error {
	if err != nil {
		h.checks[source].Add(1)
	}
	return err
}

func newHistoryStats(sources []historySource) *historyStats {
	h := &historyStats{latest: map[string]time.Time{}, loaded: map[string]*atomic.Int64{}, failed: map[string]*atomic.Int64{},
		checks: map[string]*atomic.Int64{}, rows: map[string]*atomic.Int64{}}
	for _, s := range sources {
		h.sources = append(h.sources, s.name)
		h.loaded[s.name], h.failed[s.name], h.checks[s.name], h.rows[s.name] = new(atomic.Int64), new(atomic.Int64), new(atomic.Int64), new(atomic.Int64)
	}
	return h
}

func (h *historyStats) noteDay(source string, day time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if day.After(h.latest[source]) {
		h.latest[source] = day
	}
}

// historyLoaderURL is the connection archives load over: loader, which names the loader user, whose profile keeps
// a load from starving the live writer, and never the writer's own. It must name the writer's database, which
// the loader user is granted in users.d/loader.xml.
func historyLoaderURL(writer, loader string) (string, error) {
	if loader == "" {
		return "", fmt.Errorf("CLICKHOUSE_LOADER_URL is not set")
	}
	w, err := clickhouse.ParseDSN(writer)
	if err != nil {
		return "", err
	}
	l, err := clickhouse.ParseDSN(loader)
	if err != nil {
		return "", fmt.Errorf("CLICKHOUSE_LOADER_URL: %w", err)
	}
	if cmp.Or(l.Auth.Database, chDatabase) != cmp.Or(w.Auth.Database, chDatabase) {
		return "", fmt.Errorf("CLICKHOUSE_LOADER_URL names database %q, not the writer's %q", cmp.Or(l.Auth.Database, chDatabase), cmp.Or(w.Auth.Database, chDatabase))
	}
	return loader, nil
}

// runHistory loads each source's new files once ClickHouse is attached and its schema current, and again every
// historyCheckEvery, over its own connection to url, as the loader user.
func (p *Pipeline) runHistory(url string, sources []historySource) {
	var c *chConn
	for {
		if c == nil && p.chConn() != nil {
			var err error
			if c, err = dialClickHouse(url); err != nil {
				log.Printf("history: %v", err)
				for _, s := range sources {
					p.history.checkFailed(s.name, err)
				}
			}
		}
		if c == nil {
			time.Sleep(time.Minute)
			continue
		}
		loaded := int64(0)
		for _, s := range sources {
			before := p.history.loaded[s.name].Load()
			if err := c.loadHistory(context.Background(), s, p.history); err != nil {
				log.Printf("history: %s: %v", s.name, err)
			}
			loaded += p.history.loaded[s.name].Load() - before
		}
		if loaded > 0 {
			p.importSoon()
		}
		time.Sleep(historyCheckEvery)
	}
}

// chConn is the attached ClickHouse connection, nil until it connects.
func (p *Pipeline) chConn() *chConn {
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	if p.ch == nil {
		return nil
	}
	c, _ := p.ch.w.(*chConn)
	return c
}

// loadHistory loads every file of s from s.from on that history_loads has not recorded complete with its
// current etag, newest first, so the most recent history arrives first.
func (c *chConn) loadHistory(ctx context.Context, s historySource, stats *historyStats) error {
	// Rows in the first layout name their transmissions by a hash the loader cannot match, so archives wait
	// until aiscast convert-receptions and clickhouse-cleanup have rewritten them.
	if legacy, err := c.legacy(ctx); err != nil || legacy {
		if legacy {
			log.Printf("history: %s waits until receptions are converted and cleaned up", s.name)
		}
		return stats.checkFailed(s.name, err)
	}
	lctx, cancel := context.WithTimeout(ctx, historyListTimeout)
	files, err := s.list(lctx)
	cancel()
	if err != nil {
		return stats.checkFailed(s.name, err)
	}
	done, err := c.historyLoaded(ctx, s.name)
	if err != nil {
		return stats.checkFailed(s.name, err)
	}
	slices.SortFunc(files, func(a, b historyFile) int { return b.day.Compare(a.day) })
	for _, f := range files {
		if f.day.Before(s.from) {
			continue
		}
		prior, seen := done[f.name]
		if seen && prior.complete && prior.etag == f.etag {
			stats.noteDay(s.name, f.day)
			continue
		}
		fctx, cancel := context.WithTimeout(ctx, time.Hour)
		n, err := c.loadHistoryFile(fctx, s, f, seen)
		cancel()
		if err != nil {
			stats.failed[s.name].Add(1)
			log.Printf("history: %s %s: %v", s.name, f.name, err)
			continue
		}
		stats.loaded[s.name].Add(1)
		stats.rows[s.name].Add(int64(n))
		stats.noteDay(s.name, f.day)
		log.Printf("history: %s %s: %d receptions", s.name, f.name, n)
	}
	return nil
}

type historyLoad struct {
	etag     string
	complete bool
}

func (c *chConn) historyLoaded(ctx context.Context, source string) (map[string]historyLoad, error) {
	rows, err := c.conn.Query(ctx, "SELECT file, etag, complete FROM "+c.db+".history_loads FINAL WHERE source = ?", source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]historyLoad{}
	for rows.Next() {
		var file string
		var l historyLoad
		if err := rows.Scan(&file, &l.etag, &l.complete); err != nil {
			return nil, err
		}
		out[file] = l
	}
	return out, rows.Err()
}

// loadHistoryFile loads one file and reports the receptions it kept. again says the file was loaded, or begun,
// before: its day's rows from this source are deleted first, so a load that failed partway or a changed file
// never leaves a row twice.
func (c *chConn) loadHistoryFile(ctx context.Context, s historySource, f historyFile, again bool) (int, error) {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(historySettings))
	db := c.db
	day := f.day.UTC().Format("2006-01-02")
	stage := db + ".history_stage_" + s.name
	exec := func(q string, args ...any) error { return c.conn.Exec(ctx, q, args...) }
	drop := func(table string) { c.conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) } // after a timeout too
	record := func(complete bool, read, unplaced, repeated, kept, matched, implausible uint64) error {
		return exec("INSERT INTO "+db+".history_loads VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now64(3))",
			s.name, f.name, f.day, uint64(f.size), f.etag, complete, read, unplaced, repeated, kept, matched, implausible)
	}
	defer drop(stage)

	// A row is known when its vessel and time are believable, and placed when its position is too. A ship's
	// MMSI begins with a maritime identification digit of 2 to 7; the rest are aids to navigation, base
	// stations, and aircraft. Latitude 91 and longitude 181 are the not-available values, and (0, 0) a GPS
	// default rather than a fix. A row stamped outside its file's day is not the archive's to give.
	if err := exec("DROP TABLE IF EXISTS " + stage); err != nil {
		return 0, err
	}
	if err := exec(`CREATE TABLE ` + stage + ` ENGINE = MergeTree ORDER BY (mmsi, ts) AS
		SELECT *, known AND abs(lat6) <= 54000000 AND abs(lon6) <= 108000000 AND NOT (lat6 = 0 AND lon6 = 0) AS placed
		FROM (SELECT *, mmsi BETWEEN 201000000 AND 775999999 AND toDate(ts) = toDate('` + day + `') AS known
		      FROM (` + s.read(f) + `))`); err != nil {
		return 0, fmt.Errorf("stage: %w", err)
	}
	// Repeats are counted by a hash of the row, which takes half the memory of the row itself: a busy DMA day has
	// 16 million distinct rows, and the next doubling of the set would pass the load's 1.5 GB.
	var read, known, placed, distinct uint64
	if err := c.conn.QueryRow(ctx, "SELECT count(), countIf(known), countIf(placed), uniqExactIf(cityHash64(mmsi, ts, lat6, lon6), placed) FROM "+stage).
		Scan(&read, &known, &placed, &distinct); err != nil {
		return 0, err
	}
	if err := record(false, read, read-placed, placed-distinct, 0, 0, 0); err != nil {
		return 0, err
	}
	if again {
		if err := exec("DELETE FROM "+db+".receptions WHERE source = ? AND ts >= toDateTime64(?, 3, 'UTC') AND ts < toDateTime64(?, 3, 'UTC') + INTERVAL 1 DAY", s.name, day, day); err != nil {
			return 0, fmt.Errorf("delete the earlier load: %w", err)
		}
	}

	n, err := c.loadHistoryDay(ctx, s, f, stage)
	if err != nil {
		return 0, err
	}
	if err := exec(`INSERT INTO `+db+`.vessel_statics
		SELECT mmsi, ?, min(ts), max(ts), argMaxStateIf((lat6, lon6), ts, placed),
		       argMaxStateIf(name, ts, name != ''), argMaxStateIf(callsign, ts, callsign != ''), argMaxStateIf(imo, ts, imo != 0),
		       argMaxStateIf(ship_type, ts, ship_type != 0), argMaxStateIf(length, ts, length != 0), argMaxStateIf(beam, ts, beam != 0),
		       argMaxStateIf(draught10, ts, draught10 != 0), argMaxStateIf(destination, ts, destination != '')
		FROM `+stage+` WHERE known GROUP BY mmsi`, s.name); err != nil {
		return 0, fmt.Errorf("insert statics: %w", err)
	}

	// A day loaded before left rows in positions_1m and coverage that its delete did not reach.
	if again {
		if err := c.rebuildPositions1m(ctx, f.day); err != nil {
			return 0, fmt.Errorf("rebuild positions_1m: %w", err)
		}
		if err := c.rebuildCoverage(ctx, f.day); err != nil {
			return 0, fmt.Errorf("rebuild coverage: %w", err)
		}
	}
	if err := record(true, read, read-placed, placed-distinct, n.kept, n.matched, n.implausible); err != nil {
		return 0, err
	}
	return int(n.kept), nil
}

// historyCounts is what a day's load wrote.
type historyCounts struct{ kept, matched, implausible uint64 }

// archiveRow is a staged archive row, or one of the day's live transmissions beside it.
type archiveRow struct {
	live                  bool
	mmsi                  uint32
	ts                    time.Time // a live transmission's own time
	lat6, lon6            int32
	sog10, cog10, heading uint16
	navstat               uint8
	disc                  uint8 // a live transmission's
	still, bad            bool  // a live transmission's verdicts
}

// loadHistoryDay writes a staged day's rows to receptions, a vessel at a time. Each vessel's rows come in time
// order beside the accepted copies of the transmissions other sources delivered for it within 5 minutes of the
// day, the reach of the live writer's own match for a rebuilt copy. Only the columns a reception keeps go through
// the sort, and exact repeats, adjacent in its order, are dropped here, so a day sorts within the load's memory.
func (c *chConn) loadHistoryDay(ctx context.Context, s historySource, f historyFile, stage string) (historyCounts, error) {
	var n historyCounts
	day := f.day.UTC()
	rows, err := c.conn.Query(ctx, `SELECT * FROM (
		SELECT false AS live, mmsi, ts AS at, lat6, lon6, sog10, cog10, heading, navstat, toUInt8(0) AS disc, false AS still, false AS bad
		FROM `+stage+` WHERE placed
		UNION ALL
		SELECT true, mmsi, fromUnixTimestamp64Milli(toUnixTimestamp64Milli(ts) + tx_off, 'UTC'), lat6, lon6, toUInt16(0), toUInt16(0), toUInt16(0), toUInt8(0),
		       tx_disc, NOT moving, implausible
		FROM `+c.db+`.receptions
		WHERE accepted AND source != ? AND ts >= ? AND ts < ? AND mmsi IN (SELECT mmsi FROM `+stage+` WHERE placed)
	) ORDER BY mmsi, at, live, lat6, lon6`, s.name, day.Add(-recentKeep), day.Add(24*time.Hour+recentKeep))
	if err != nil {
		return n, err
	}
	defer rows.Close()
	run := fmt.Sprintf("history-%s-%s-%d-", s.name, day.Format("2006-01-02"), time.Now().UnixNano())
	loaded := time.Now()
	var batch []trackPoint
	batches := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batches++
		if err := c.insert(ctx, run+fmt.Sprint(batches), batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	var vessel []archiveRow
	emit := func() error {
		pts, m := historyVessel(s.name, vessel, loaded)
		n.kept += uint64(len(pts))
		n.matched += m.matched
		n.implausible += m.implausible
		batch = append(batch, pts...)
		vessel = vessel[:0]
		if len(batch) >= historyBatch {
			return flush()
		}
		return nil
	}
	for rows.Next() {
		var r archiveRow
		if err := rows.Scan(&r.live, &r.mmsi, &r.ts, &r.lat6, &r.lon6, &r.sog10, &r.cog10, &r.heading, &r.navstat, &r.disc, &r.still, &r.bad); err != nil {
			return n, err
		}
		if len(vessel) > 0 && vessel[0].mmsi != r.mmsi {
			if err := emit(); err != nil {
				return n, err
			}
		}
		if l := len(vessel); !r.live && l > 0 && !vessel[l-1].live && vessel[l-1].ts.Equal(r.ts) && vessel[l-1].lat6 == r.lat6 && vessel[l-1].lon6 == r.lon6 {
			continue // the same row twice in the file
		}
		vessel = append(vessel, r)
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if len(vessel) > 0 {
		if err := emit(); err != nil {
			return n, err
		}
	}
	return n, flush()
}

// historyVessel turns one vessel's staged rows, in time order among its live transmissions, into receptions.
// A row at a live transmission's position, within recentNearA, the nearest in time within recentKeep, is a copy
// of it, as a rebuilt copy is live: AISHub's stamps run tens of seconds off. It takes the transmission's name and
// its verdict on moving. An implausible transmission is never matched, as live: a row at its position is judged
// as a report of its own, so a bad live copy cannot hide a real archive report, and the byte stays taken. Any other row is its own transmission, accepted, with a byte
// free in its millisecond and the anchor's verdict on moving; it is implausible when it jumps from both of its
// neighbors, the fold's test either side, since the fold's walk from the last accepted report flags the good
// row after a spike too. Runs of bad rows pass, and despike removes them when a track is drawn. A row arrives
// when it loaded, so it never arrives before the live copies of its transmission and the view keeps serving
// those. The anchor starts afresh each day, since days load newest first.
func historyVessel(source string, rows []archiveRow, loaded time.Time) ([]trackPoint, historyCounts) {
	var live, own []archiveRow
	for _, r := range rows {
		if r.live {
			live = append(live, r)
		} else {
			own = append(own, r)
		}
	}
	used := map[int64][]uint8{}
	for _, l := range live {
		ms := l.ts.UnixMilli()
		used[ms] = append(used[ms], l.disc)
	}
	var n historyCounts
	var a anchor
	out := make([]trackPoint, 0, len(own))
	lo := 0
	for i, r := range own {
		pt := trackPoint{mmsi: r.mmsi, ts: r.ts, lat6: r.lat6, lon6: r.lon6, sog10: r.sog10, cog10: r.cog10, heading: r.heading,
			navStatus: r.navstat, source: source, station: source, recv: loaded}
		for lo < len(live) && r.ts.Sub(live[lo].ts) >= recentKeep {
			lo++
		}
		match, best := -1, recentKeep
		for j := lo; j < len(live) && live[j].ts.Sub(r.ts) < recentKeep; j++ {
			if dt := absDur(live[j].ts.Sub(r.ts)); !live[j].bad && dt < best && absInt(live[j].lat6-r.lat6) <= recentNearA && absInt(live[j].lon6-r.lon6) <= recentNearA {
				match, best = j, dt
			}
		}
		if match >= 0 {
			l := live[match]
			pt.txAt, pt.txDisc, pt.still, pt.implausible, pt.dup = l.ts, l.disc, l.still, l.bad, true
			n.matched++
		} else {
			pt.txAt = r.ts
			pt.txDisc = discOf(eventID(fmt.Sprint(source, r.mmsi, r.ts.UnixMilli(), r.lat6, r.lon6)))
			ms := r.ts.UnixMilli()
			for range 256 {
				if !slices.Contains(used[ms], pt.txDisc) {
					break
				}
				pt.txDisc++
			}
			used[ms] = append(used[ms], pt.txDisc)
			pt.implausible = i > 0 && i < len(own)-1 && historyJumps(own[i-1], r) && historyJumps(r, own[i+1])
			pt.still = a.still(pt, nil, !pt.implausible)
		}
		if pt.implausible {
			n.implausible++
		}
		out = append(out, pt)
	}
	return out, n
}

// historyJumps is the fold's test between two rows: more than implausibleJumpNM at more than implausibleKnots.
func historyJumps(x, y archiveRow) bool {
	dt := absDur(y.ts.Sub(x.ts)).Seconds()
	d := nm(float64(x.lat6)/600000, float64(x.lon6)/600000, float64(y.lat6)/600000, float64(y.lon6)/600000)
	return dt >= 1 && d > implausibleJumpNM && d/(dt/3600) > implausibleKnots
}

// sqlString quotes s as a ClickHouse string literal.
func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
