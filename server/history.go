package main

// Historical archives: government AIS archives loaded into receptions as late copies, each from a feed the
// network does not run. A source lists its files, one UTC day each, and gives the SELECT that reads a file as
// historyColumns; ClickHouse downloads and parses the file itself. Each file is staged, cleaned, matched to
// the copies the network already holds, and inserted, and its statics fold into vessel_statics. history_loads
// records every file, so a file loads once and a changed one loads again. See specs/historical-sources.md.

import (
	"context"
	"fmt"
	"log"
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
	// near is how far, in wire units of latitude or longitude, a row may sit from a stored copy of the same
	// transmission: an archive that rounds its coordinates cannot match the wire value exactly.
	near int32
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

// historySchema is created with the rest of the schema. vessel_statics keeps, per vessel and source, the
// earliest and latest report, the latest position, and the latest non-empty value of each static field;
// rows fold together whatever order the files load in, and a file loaded twice changes nothing.
// history_loads has one row per file, the latest kept.
var historySchema = []string{
	`CREATE TABLE IF NOT EXISTS {db}.vessel_statics (
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
	ORDER BY (mmsi, source)`,
	`CREATE TABLE IF NOT EXISTS {db}.history_loads (
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
	ORDER BY (source, file)`,
}

// historySettings bound every load query, so a load never takes the memory or the cores the live writer
// needs: ClickHouse refusing the writer for 10 minutes drops live positions. A slow archive host streams a
// file for minutes.
var historySettings = clickhouse.Settings{
	"max_memory_usage":                   1_500_000_000,
	"max_threads":                        2,
	"max_insert_threads":                 1,
	"max_bytes_before_external_sort":     500_000_000,
	"max_bytes_before_external_group_by": 500_000_000,
	"http_receive_timeout":               600,
	"max_execution_time":                 3600,
}

// historyPasses is how many groups of vessels a day's receptions are judged and inserted in.
const historyPasses = 8

// historyCheckEvery is how often the loader lists each source for files it has not loaded.
const historyCheckEvery = 6 * time.Hour

// historyStats is read by /metrics, per source.
type historyStats struct {
	mu      sync.Mutex
	latest  map[string]time.Time // the newest day loaded
	loaded  map[string]*atomic.Int64
	failed  map[string]*atomic.Int64
	rows    map[string]*atomic.Int64
	sources []string
}

func newHistoryStats(sources []historySource) *historyStats {
	h := &historyStats{latest: map[string]time.Time{}, loaded: map[string]*atomic.Int64{}, failed: map[string]*atomic.Int64{}, rows: map[string]*atomic.Int64{}}
	for _, s := range sources {
		h.sources = append(h.sources, s.name)
		h.loaded[s.name], h.failed[s.name], h.rows[s.name] = new(atomic.Int64), new(atomic.Int64), new(atomic.Int64)
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

// runHistory loads each source's new files once ClickHouse is attached, and again every historyCheckEvery.
func (p *Pipeline) runHistory(sources []historySource) {
	for {
		if c := p.chConn(); c != nil {
			for _, s := range sources {
				if err := c.loadHistory(context.Background(), s, p.history); err != nil {
					log.Printf("history: %s: %v", s.name, err)
				}
			}
			time.Sleep(historyCheckEvery)
			continue
		}
		time.Sleep(time.Minute)
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
	files, err := s.list(ctx)
	if err != nil {
		return err
	}
	done, err := c.historyLoaded(ctx, s.name)
	if err != nil {
		return err
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
	stage, match := db+".history_stage_"+s.name, db+".history_match_"+s.name
	exec := func(q string, args ...any) error { return c.conn.Exec(ctx, q, args...) }
	drop := func(table string) { c.conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) } // after a timeout too
	record := func(complete bool, read, unplaced, repeated, kept, matched, implausible uint64) error {
		return exec("INSERT INTO "+db+".history_loads VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now64(3))",
			s.name, f.name, f.day, uint64(f.size), f.etag, complete, read, unplaced, repeated, kept, matched, implausible)
	}
	defer drop(stage)
	defer drop(match)

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
	var read, known, placed, distinct uint64
	if err := c.conn.QueryRow(ctx, "SELECT count(), countIf(known), countIf(placed), uniqExactIf((mmsi, ts, lat6, lon6), placed) FROM "+stage).
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

	// A row matches a copy another source delivered of the same transmission: the same vessel and position
	// within 5 seconds, nearest in time. It takes that transmission, and its implausible flag, since views that
	// see one insert at a time filter copy by copy. Rows are joined to the copies in their 5-second bucket and
	// the ones either side, then filtered, because a window and a tolerance cannot be equality keys.
	if err := exec("DROP TABLE IF EXISTS " + match); err != nil {
		return 0, err
	}
	if err := exec(fmt.Sprintf(`CREATE TABLE %[1]s ENGINE = Memory AS
		SELECT s.mmsi AS mmsi, s.ts AS ts, s.lat6 AS lat6, s.lon6 AS lon6,
		       argMin(l.tx, abs(toUnixTimestamp64Milli(l.ts) - toUnixTimestamp64Milli(s.ts))) AS tx, max(l.implausible) AS implausible
		FROM (SELECT mmsi, ts, lat6, lon6, toInt64(intDiv(toUnixTimestamp(ts), 5)) AS b FROM %[2]s WHERE placed) AS s
		INNER JOIN (
			SELECT mmsi, ts, lat6, lon6, tx, implausible, arrayJoin([b - 1, b, b + 1]) AS b
			FROM (SELECT mmsi, ts, lat6, lon6, tx, implausible, toInt64(intDiv(toUnixTimestamp(ts), 5)) AS b
			      FROM %[3]s.receptions
			      WHERE ts >= toDateTime64('%[4]s', 3, 'UTC') - INTERVAL 10 SECOND
			        AND ts < toDateTime64('%[4]s', 3, 'UTC') + INTERVAL 1 DAY + INTERVAL 10 SECOND
			        AND source != '%[5]s' AND mmsi IN (SELECT mmsi FROM %[2]s WHERE placed))
		) AS l ON s.mmsi = l.mmsi AND s.b = l.b
		WHERE abs(toUnixTimestamp64Milli(l.ts) - toUnixTimestamp64Milli(s.ts)) <= 5000
		  AND abs(l.lat6 - s.lat6) <= %[6]d AND abs(l.lon6 - s.lon6) <= %[6]d
		GROUP BY s.mmsi, s.ts, s.lat6, s.lon6`, match, stage, db, day, s.name, s.near)); err != nil {
		return 0, fmt.Errorf("match: %w", err)
	}

	// The fold tests each report against the vessel's last; a single statement cannot walk rows that way, and
	// testing each against the one before flags a lone spike and the good row after it. So a row is implausible
	// when it jumps from both neighbors, the fold's test either side: more than 10 NM and more than 120 kn.
	// Runs of bad rows pass here, and despike removes them when a track is drawn. A row matched to a flagged
	// transmission is flagged with it. An unmatched row is its own transmission, accepted.
	jump := func(lat, lon, t string) string {
		return fmt.Sprintf(`(%[3]s > toDateTime64('2000-01-01', 3, 'UTC') AND abs(toUnixTimestamp64Milli(ts) - toUnixTimestamp64Milli(%[3]s)) >= 1000
			AND greatCircleDistance(lon6 / 600000, lat6 / 600000, %[2]s / 600000, %[1]s / 600000) / 1852 > %[4]g
			AND greatCircleDistance(lon6 / 600000, lat6 / 600000, %[2]s / 600000, %[1]s / 600000) / 1852
			    / (abs(toUnixTimestamp64Milli(ts) - toUnixTimestamp64Milli(%[3]s)) / 3600000) > %[5]g)`, lat, lon, t, implausibleJumpNM, implausibleKnots)
	}
	// A vessel's rows are judged only against each other, so the day goes in passes of whole vessels, each
	// sorting a fraction of it: one pass would sort a day's 10 M rows at once, past the load's memory.
	for pass := range historyPasses {
		if err := exec(`INSERT INTO `+db+`.receptions
			(mmsi, ts, tx, recv_ts, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad)
			SELECT r.mmsi, r.ts, if(m.tx != 0, m.tx, cityHash64(?, r.mmsi, r.ts, r.lat6, r.lon6)), now64(3), r.lat6, r.lon6,
			       r.sog10, r.cog10, r.heading, r.navstat, ?, ?, m.tx = 0, true, (r.spike OR m.implausible), false
			FROM (
				SELECT mmsi, ts, lat6, lon6, sog10, cog10, heading, navstat,
				       `+jump("plat", "plon", "pts")+` AND `+jump("nlat", "nlon", "nts")+` AS spike
				FROM (
					SELECT *, lagInFrame(lat6) OVER w AS plat, lagInFrame(lon6) OVER w AS plon, lagInFrame(ts) OVER w AS pts,
					       leadInFrame(lat6) OVER w AS nlat, leadInFrame(lon6) OVER w AS nlon, leadInFrame(ts) OVER w AS nts
					FROM (SELECT * FROM `+stage+` WHERE placed AND mmsi % ? = ? ORDER BY mmsi, ts LIMIT 1 BY mmsi, ts, lat6, lon6)
					WINDOW w AS (PARTITION BY mmsi ORDER BY ts ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING)
				)
			) AS r
			LEFT JOIN `+match+` AS m ON r.mmsi = m.mmsi AND r.ts = m.ts AND r.lat6 = m.lat6 AND r.lon6 = m.lon6`,
			s.name, s.name, s.name, historyPasses, pass); err != nil {
			return 0, fmt.Errorf("insert receptions: %w", err)
		}
	}
	if err := exec(`INSERT INTO `+db+`.vessel_statics
		SELECT mmsi, ?, min(ts), max(ts), argMaxStateIf((lat6, lon6), ts, placed),
		       argMaxStateIf(name, ts, name != ''), argMaxStateIf(callsign, ts, callsign != ''), argMaxStateIf(imo, ts, imo != 0),
		       argMaxStateIf(ship_type, ts, ship_type != 0), argMaxStateIf(length, ts, length != 0), argMaxStateIf(beam, ts, beam != 0),
		       argMaxStateIf(draught10, ts, draught10 != 0), argMaxStateIf(destination, ts, destination != '')
		FROM `+stage+` WHERE known GROUP BY mmsi`, s.name); err != nil {
		return 0, fmt.Errorf("insert statics: %w", err)
	}

	var kept, matched, implausible uint64
	if err := c.conn.QueryRow(ctx, "SELECT count(), countIf(NOT accepted), countIf(implausible) FROM "+db+".receptions"+
		" WHERE source = ? AND ts >= toDateTime64(?, 3, 'UTC') AND ts < toDateTime64(?, 3, 'UTC') + INTERVAL 1 DAY", s.name, day, day).
		Scan(&kept, &matched, &implausible); err != nil {
		return 0, err
	}
	return int(kept), record(true, read, read-placed, placed-distinct, kept, matched, implausible)
}

// sqlString quotes s as a ClickHouse string literal.
func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
