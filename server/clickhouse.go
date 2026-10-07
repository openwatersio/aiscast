package main

// History in ClickHouse: every copy of every position report the network receives, written once a second over
// the native protocol. receptions holds every copy, sorted by vessel and time, with the transmission it belongs
// to; the positions view keeps the earliest copy of each transmission, and positions_1m, which ClickHouse fills
// as copies arrive, keeps each vessel's track a minute at a time while it moves. ClickHouse being slow or down
// never holds up ingest: its queue is bounded, and what falls out is counted. A batch whose insert failed is
// sent again whole, under the same deduplication token, since a failure can come after ClickHouse committed it.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// chDatabase is the database the schema lives in when CLICKHOUSE_URL names none.
const chDatabase = "aiscast"

// chMigrations brings a database to the current layout, one numbered step at a time, each recorded in
// schema_migrations when it succeeds, so a step runs once. Steps only ever add: a table, a column, a view. A
// server built before a step still writes to a database after it, because every column it adds has a default,
// so rolling back a deploy never stops history. Anything that drops or rewrites data runs only from a command
// (aiscast clickhouse-cleanup), never at start. No step renames a table: a materialized view keeps reading the
// table it was created over, not one that takes its name, so a rename silently stops every view over it.
// {db} is the database. Receptions use the track encodings, 15 in navstat for not available, and a source kind
// rather than a full source; station is the full one. Nothing expires: receptions are the record of history.
//
// A copy names its transmission without a hash that would not compress: the transmission is the vessel, the
// time its accepted copy was stamped, ts plus tx_off, and tx_disc, one byte of that copy's event id, which
// tells apart transmissions stamped in the same millisecond. Most copies are their transmission's accepted one,
// so tx_off is almost always 0. recv_delay is when the copy arrived, as milliseconds after ts. Measured on
// three hours of production receptions, this costs 11.4 bytes a row where a 64-bit hash and a second
// timestamp cost 19.8. A database from before this layout has tx and recv_ts as well; step 2 adds the columns
// beside them, and aiscast convert-receptions rewrites its rows.
var chMigrations = []string{
	1: `CREATE TABLE IF NOT EXISTS {db}.receptions (
		mmsi         UInt32,
		ts           DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
		tx_off       Int32 CODEC(T64, ZSTD),
		tx_disc      UInt8 CODEC(ZSTD),
		recv_delay   Int32 CODEC(T64, ZSTD),
		lat6         Int32 CODEC(Delta, ZSTD),
		lon6         Int32 CODEC(Delta, ZSTD),
		sog10        UInt16 CODEC(ZSTD),
		cog10        UInt16 CODEC(ZSTD),
		heading      UInt16 CODEC(ZSTD),
		navstat      UInt8,
		source       LowCardinality(String),
		station      LowCardinality(String),
		accepted     Bool DEFAULT true,
		corroborated Bool DEFAULT true,
		implausible  Bool DEFAULT false,
		clock_bad    Bool DEFAULT false,
		moving       Bool DEFAULT true
	) ENGINE = MergeTree
	PARTITION BY toYYYYMM(ts)
	ORDER BY (mmsi, ts)
	SETTINGS non_replicated_deduplication_window = 1000`,
	2: `ALTER TABLE {db}.receptions
		ADD COLUMN IF NOT EXISTS tx_off Int32 CODEC(T64, ZSTD) AFTER ts,
		ADD COLUMN IF NOT EXISTS tx_disc UInt8 CODEC(ZSTD) AFTER tx_off,
		ADD COLUMN IF NOT EXISTS recv_delay Int32 CODEC(T64, ZSTD) AFTER tx_disc,
		ADD COLUMN IF NOT EXISTS moving Bool DEFAULT true`,
	3: `CREATE TABLE IF NOT EXISTS {db}.positions_1m (
		mmsi    UInt32,
		slot    DateTime('UTC') CODEC(DoubleDelta, ZSTD),
		cell    UInt32 CODEC(ZSTD),
		ts      DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
		lat6    Int32 CODEC(Delta, ZSTD),
		lon6    Int32 CODEC(Delta, ZSTD),
		sog10   UInt16 CODEC(ZSTD),
		cog10   UInt16 CODEC(ZSTD),
		heading UInt16 CODEC(ZSTD),
		navstat UInt8 CODEC(ZSTD),
		source  LowCardinality(String)
	) ENGINE = ReplacingMergeTree(ts)
	PARTITION BY toYYYYMM(slot)
	ORDER BY (mmsi, slot, cell)`,
	4: `CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.positions_1m_mv TO {db}.positions_1m AS
	SELECT mmsi, toDateTime(if(moving, toStartOfMinute(ts), toStartOfInterval(ts, INTERVAL 30 MINUTE)), 'UTC') AS slot,
	       if(moving, 0, ` + chCell + `) AS cell, ts, lat6, lon6, sog10, cog10, heading, navstat, source
	FROM {db}.receptions WHERE accepted AND ` + chUsable,
	5: chVesselStatics,
	6: chHistoryLoads,
	// coverage, filled from receptions as they are written (coveragemap.go)
	7: chCoverageTable,
	8: `CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.coverage_mv TO {db}.coverage AS ` + chCoverageSelect("{db}.receptions", chUsable),
	9: `CREATE TABLE IF NOT EXISTS {db}.coverage_backfilled (day Date) ENGINE = ReplacingMergeTree ORDER BY day`,
	// the stations that heard each cell; the backfill bins every day again to fill them in
	10: `ALTER TABLE {db}.coverage ADD COLUMN IF NOT EXISTS stations AggregateFunction(uniqExact, String)`,
	11: `ALTER TABLE {db}.coverage_mv MODIFY QUERY ` + chCoverageSelect("{db}.receptions", chUsable),
	12: `TRUNCATE TABLE {db}.coverage_backfilled`,
	// a late copy, which the stream and the station counts leave out; rows from before read as false
	13: `ALTER TABLE {db}.receptions ADD COLUMN IF NOT EXISTS stale Bool DEFAULT false`,
	14: chStationOwn,
	// coverage per station, which the coverage map reads in place of coverage (coveragemap.go)
	15: chStationCoverageTable,
	16: `CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.station_coverage_mv TO {db}.station_coverage AS ` + chStationCoverageSelect("{db}.receptions", chUsable),
	17: `CREATE TABLE IF NOT EXISTS {db}.station_coverage_backfilled (day Date) ENGINE = ReplacingMergeTree ORDER BY day`,
}

// chStationOwn keeps, per station, hour, and vessel, the last time the station sent that vessel as its own ship
// (!AIVDO), from position and static messages alike, so a station's counts can leave out the boat it is on.
// Static messages never reach receptions, so this is the only record of an own ship that only sends those. Max
// merges the same in any order and any number of times, so a resent batch or a replay writes it again safely.
// It is kept as long as the per-vessel station counts that read it.
const chStationOwn = `CREATE TABLE IF NOT EXISTS {db}.station_own (
	hour    DateTime('UTC'),
	station LowCardinality(String),
	mmsi    UInt32,
	last_ts SimpleAggregateFunction(max, DateTime64(3, 'UTC'))
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(hour)
ORDER BY (station, hour, mmsi)
TTL hour + INTERVAL 35 DAY DELETE`

// positions_1m is each vessel's track at one position a minute while it moves, and one every 30 minutes for each
// place it sits still: a heartbeat that says it was heard there then, on the same 30 minutes that make a vessel
// active on the live map. A vessel underway reports every few seconds, so a minute keeps the shape of its track;
// one moored or at anchor reports every few minutes from the same place, which a heartbeat says as well. Each
// window keeps its latest report. A range finds a vessel sitting still in every window it overlaps but the last,
// whose report may fall after the range ends, so a moored vessel is missing from at most the last 30 minutes of
// a range. Its moving rows bound when it arrived and left to the minute. On 2026-10-02 it kept 12.4 M rows of
// 53 M accepted positions, at 12.4 bytes a row in plain columns: 10.2 M minutes underway and 2.2 M heartbeats,
// where one a day would keep 0.15 M; a window reduced to an aggregate state cost twice that. Any step of a minute
// or more reads from it, grouped by the step, so coarser summaries are never needed to answer one. Places are
// cells of a hundredth of a degree, about a kilometer, so a vessel that moves between harbors in 30 minutes has
// a heartbeat at each. Nothing in it expires, as in receptions.
const chCell = `toUInt32((intDiv(lat6, 6000) + 9000) * 36000 + (intDiv(lon6, 6000) + 18000))`

// chCellOf is chCell over other columns.
func chCellOf(lat6, lon6 string) string {
	return strings.NewReplacer("lat6", lat6, "lon6", lon6).Replace(chCell)
}

// chUsable is the condition every history read puts on receptions: copies the fold judged an impossible jump,
// or whose clock put them a day or more before they arrived, are kept but never served.
const chUsable = "NOT implausible AND NOT clock_bad"

// chPositionsView is one row per transmission: the copy that arrived first, among those with a believable
// clock, of each transmission no copy of which the fold judged implausible. A transmission is judged as a
// whole, over every copy, because every copy carries its position: filtering copies before grouping would
// serve a flagged position through an unflagged copy, and a bad clock only rules a copy out as the one served.
// It takes the vessel and range as parameters so the filter reaches receptions' sort key before the grouping;
// a view without them would group the vessel's whole history on every read. A copy is stamped up to 5 minutes
// from its transmission, the reach of the fold's match for a rebuilt copy, so the copies are read 5 minutes
// either side and a transmission is in the range by its own time, not its copies': a page boundary never splits
// one into two. It holds no data, so it is replaced at every start and a database always reads by the current
// definition.
//
// legacy is a database that still has rows from before tx_off, named by tx, a 64-bit hash, and arriving at
// recv_ts. Until aiscast convert-receptions rewrites them they are served too, grouped by tx, except on a day
// receptions_converted says is rewritten, where the rewritten rows serve it. A server rolled back to before the
// current layout writes such rows again; on a day already rewritten they wait, unserved, for the converter to
// do the day again.
func chPositionsView(legacy bool) string {
	return `CREATE OR REPLACE VIEW {db}.positions AS
	SELECT mmsi, f.1 AS ts, f.2 AS lat6, f.3 AS lon6, f.4 AS sog10, f.5 AS cog10, f.6 AS heading, f.7 AS navstat, f.8 AS source
	FROM (` + chFirstCopies(legacy, `mmsi = {mmsi:UInt32}
		  AND ts >= {from:DateTime64(3, 'UTC')} - INTERVAL 5 MINUTE AND ts <= {to:DateTime64(3, 'UTC')} + INTERVAL 5 MINUTE`) + `)
	WHERE sent >= toUnixTimestamp64Milli({from:DateTime64(3, 'UTC')}) AND sent <= toUnixTimestamp64Milli({to:DateTime64(3, 'UTC')})`
}

// chFirstCopies is each transmission among the receptions where holds, by its time in milliseconds, sent, and
// f, the tuple of the copy the positions view serves, with that copy's verdict on moving last. A transmission
// in the first layout has no time of its own stored, so its time is the served copy's, the one main's view
// filtered on, rather than its earliest stamp, which an AISHub copy stamped early would set.
func chFirstCopies(legacy bool, where string) string {
	group, arrival := "toUnixTimestamp64Milli(ts) + tx_off AS tx_ms, tx_disc", "toUnixTimestamp64Milli(ts) + recv_delay"
	sent := "min(toUnixTimestamp64Milli(ts) + tx_off)"
	if legacy {
		group = "tx, if(tx = 0, toUnixTimestamp64Milli(ts) + tx_off, 0) AS tx_ms, if(tx = 0, tx_disc, 0)"
		arrival = "if(tx = 0, toUnixTimestamp64Milli(ts) + recv_delay, toUnixTimestamp64Milli(recv_ts))"
		sent = "if(tx = 0, " + sent + ", toUnixTimestamp64Milli(f.1))"
		where += " AND (tx = 0 OR toDate(ts) NOT IN (SELECT day FROM {db}.receptions_converted))"
	}
	return `SELECT mmsi, ` + sent + ` AS sent,
		       argMinIf((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source), moving), ` + arrival + `, NOT clock_bad) AS f
		FROM {db}.receptions
		WHERE ` + where + `
		GROUP BY mmsi, ` + group + `
		HAVING NOT max(implausible) AND countIf(NOT clock_bad) > 0`
}

// rebuildPositions1m rebuilds positions_1m for one UTC day from receptions, taking each transmission's first
// copy as the positions view does. positions_1m keeps the copy accepted when it was written, and a purge or a
// reload does not move accepted to the copy left, so either is followed by this for each day it touched. Rows
// the live writer adds meanwhile are kept: positions_1m keeps the latest row per window, so the two agree. It
// rebuilds rebuildSlices ranges of vessels in turn, the ranges splitting the day's rows evenly: grouping a large day
// whole, such as an archive's 36 M rows, outgrows the memory a load's query may take, and receptions sorts by vessel
// first, so each range reads only its own rows. A transmission is one vessel's, so its copies never span two.
// ponytail: one vessel's rows always fall in one range, so a day where a single MMSI floods most of the rows still
// groups them in one query; split a vessel's day by time if that ever happens.
func (c *chConn) rebuildPositions1m(ctx context.Context, day time.Time) error {
	end := day.Add(24 * time.Hour)
	legacy, err := c.legacy(ctx)
	if err != nil {
		return err
	}
	deletes := chDeleteSync(ctx)
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_memory_usage": 1_500_000_000, "max_bytes_before_external_group_by": 700_000_000, "optimize_aggregation_in_order": 1,
		"max_threads": 2, "max_execution_time": 3600,
	}))
	var levels []string
	for i := 1; i < rebuildSlices; i++ {
		levels = append(levels, fmt.Sprint(float64(i)/float64(rebuildSlices)))
	}
	var qs []uint32
	if len(levels) > 0 {
		if err := c.conn.QueryRow(ctx, "SELECT arrayMap(x -> if(isNaN(x), 0, toUInt32(x)), quantiles("+strings.Join(levels, ", ")+")(mmsi)) FROM "+c.db+
			".receptions WHERE ts >= ? - INTERVAL 5 MINUTE AND ts < ? + INTERVAL 5 MINUTE", day, end).Scan(&qs); err != nil {
			return err
		}
	}
	q := `INSERT INTO {db}.positions_1m
	SELECT mmsi, toDateTime(if(f.9, toStartOfMinute(f.1), toStartOfInterval(f.1, INTERVAL 30 MINUTE)), 'UTC') AS slot,
	       if(f.9, 0, ` + chCellOf("f.2", "f.3") + `) AS cell,
	       f.1 AS ts, f.2 AS lat6, f.3 AS lon6, f.4 AS sog10, f.5 AS cog10, f.6 AS heading, f.7 AS navstat, f.8 AS source
	FROM (` + chFirstCopies(legacy, "ts >= ? - INTERVAL 5 MINUTE AND ts < ? + INTERVAL 5 MINUTE AND mmsi >= ? AND mmsi <= ?") + `)
	WHERE f.1 >= ? AND f.1 < ?`
	q = strings.ReplaceAll(q, "{db}", c.db)
	// Each range is deleted just before it is rebuilt, so a rebuild that stops partway leaves the ranges it had not
	// reached with the rows they held. Only the range in progress can be left empty, by a stop between its delete
	// and its insert, until the day is rebuilt again.
	for i, rg := range vesselRanges(qs) {
		if err := c.conn.Exec(deletes, "DELETE FROM "+c.db+".positions_1m WHERE slot >= ? AND slot < ? AND mmsi >= ? AND mmsi <= ?",
			day, end, rg[0], rg[1]); err != nil {
			return err
		}
		if err := c.conn.Exec(ctx, q, day, end, rg[0], rg[1], day, end); err != nil {
			return err
		}
		if rebuiltRange != nil {
			if err := rebuiltRange(i); err != nil {
				return err
			}
		}
	}
	return nil
}

// rebuiltRange, when set, is called after each range a rebuild finishes; tests use it to stop a rebuild partway.
var rebuiltRange func(i int) error

// vesselRanges turns ascending MMSI edges into inclusive ranges that cover every MMSI exactly once: the first from
// 0, each next one starting where the last ended, the last to the largest. An edge that repeats or is 0 adds no
// range, so a day with few vessels, or none, has fewer ranges.
func vesselRanges(edges []uint32) [][2]uint32 {
	starts := []uint32{0}
	for _, e := range edges {
		if e > starts[len(starts)-1] {
			starts = append(starts, e)
		}
	}
	out := make([][2]uint32, len(starts))
	for i, lo := range starts {
		hi := uint32(math.MaxUint32)
		if i+1 < len(starts) {
			hi = starts[i+1] - 1
		}
		out[i] = [2]uint32{lo, hi}
	}
	return out
}

// rebuildSlices is how many ranges of vessels a day of positions_1m is rebuilt in; 1 or less rebuilds it whole.
var rebuildSlices = 8

// chWriter inserts a batch of positions under a deduplication token, so ClickHouse skips a batch it already
// holds; tests fake it.
type chWriter interface {
	insert(ctx context.Context, token string, points []trackPoint) error
}

// ownWriter inserts own-ship sightings into station_own; tests fake it.
type ownWriter interface {
	insertOwn(ctx context.Context, own map[ownKey]time.Time) error
}

// ownKey is one station's own vessel in one hour, the grain of station_own.
type ownKey struct {
	station string
	hour    int64 // unix hours
	mmsi    uint32
}

// chReader reads one vessel's history; tests fake it.
type chReader interface {
	history(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) ([]trackPoint, error)
}

// chStore is the attached ClickHouse: the writer, the reader, the batch waiting to be sent again, and what
// /metrics reports about it.
type chStore struct {
	w   chWriter
	r   chReader
	cov coverageSource // nil leaves the coverage map unavailable
	own ownWriter      // nil leaves own-ship sightings unwritten

	mu      sync.Mutex // one flush at a time, so a resend never races the batch it repeats
	failed  []trackPoint
	token   string
	refused time.Time // when ClickHouse first refused the failed batch; zero until it does

	written, failures, dropped atomic.Int64
	writeNanos                 atomic.Int64
	// Stale rebuilt copies the fold matched to a recent transmission, and those it kept as late reports of
	// their own. A late share far above what the sources' delays explain means copies are being served twice.
	rebuiltMatched, rebuiltLate atomic.Int64
	ownDropped                  atomic.Int64 // own-ship sightings lost past maxOwnPending: memory, not policy
	ownRefused                  atomic.Int64 // own-ship sightings past a sender's maxOwnPerStation: policy, not loss
	ownFailing                  atomic.Bool  // the last station_own insert failed; cleared when one is written
	failing                     atomic.Bool  // the last batch failed; cleared when one is written
}

// chConn writes positions through a native-protocol connection.
type chConn struct {
	conn  driver.Conn
	db    string
	table string // the table write inserts into: receptions, or a replay's staging table
	own   string // the table insertOwn writes: station_own when empty, or a replay's staging table
}

// openClickHouse connects to url, a clickhouse:// DSN, and creates the schema in the database it names, or
// in chDatabase.
func openClickHouse(ctx context.Context, url string, with ...func(*clickhouse.Options)) (*chConn, error) {
	c, err := dialClickHouse(url, with...)
	if err != nil {
		return nil, err
	}
	if err := c.migrate(ctx); err != nil {
		c.conn.Close()
		return nil, fmt.Errorf("clickhouse migration: %w", err)
	}
	return c, nil
}

// dialClickHouse connects to url without touching the schema. The connection itself opens on the server's
// default database, since the named one may not exist yet.
func dialClickHouse(url string, with ...func(*clickhouse.Options)) (*chConn, error) {
	opts, err := clickhouse.ParseDSN(url)
	if err != nil {
		return nil, err
	}
	for _, f := range with {
		f(opts)
	}
	db := opts.Auth.Database
	if db == "" {
		db = chDatabase
	}
	opts.Auth.Database = ""
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	return &chConn{conn: conn, db: db}, nil
}

// exec runs statements in order, with {db} the database.
func (c *chConn) exec(ctx context.Context, stmts ...string) error {
	for _, stmt := range stmts {
		if err := c.conn.Exec(ctx, strings.ReplaceAll(stmt, "{db}", c.db)); err != nil {
			return err
		}
	}
	return nil
}

// migrate runs the steps of chMigrations the database has not recorded, then replaces the positions view.
func (c *chConn) migrate(ctx context.Context) error {
	if err := c.exec(ctx, "CREATE DATABASE IF NOT EXISTS {db}",
		"CREATE TABLE IF NOT EXISTS {db}.schema_migrations (version UInt32, applied DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY version"); err != nil {
		return err
	}
	done, err := chColumn[uint32](ctx, c.conn, "SELECT version FROM "+c.db+".schema_migrations")
	if err != nil {
		return err
	}
	for v := 1; v < len(chMigrations); v++ {
		if slices.Contains(done, uint32(v)) {
			continue
		}
		if err := c.exec(ctx, chMigrations[v]); err != nil {
			return fmt.Errorf("step %d: %w", v, err)
		}
		if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".schema_migrations (version) VALUES (?)", uint32(v)); err != nil {
			return err
		}
		log.Printf("clickhouse: %s migrated to step %d", c.db, v)
	}
	if n := slices.Max(append(done, 0)); int(n) >= len(chMigrations) {
		log.Printf("clickhouse: %s is at step %d, past this build's %d; writing on, since every step only adds", c.db, n, len(chMigrations)-1)
	}
	legacy, err := c.legacy(ctx)
	if err != nil {
		return err
	}
	if legacy {
		if err := c.exec(ctx, "CREATE TABLE IF NOT EXISTS {db}.receptions_converted (day Date, rows UInt64) ENGINE = ReplacingMergeTree ORDER BY day"); err != nil {
			return err
		}
	}
	if err := c.exec(ctx, chPositionsView(legacy)); err != nil {
		return err
	}
	return c.coldTier(ctx)
}

// chColdDays is how many days receptions' parts stay on local disk before they move to the cold volume on R2.
const chColdDays = 30

// coldTier puts receptions on the tiered storage policy and moves its parts to the cold volume chColdDays past
// their rows' time, once the server's config has the policy (clickhouse-r2.xml). Each step checks what is
// already set, so a start that stopped between them finishes the second. The TTL recalculates each part's times
// from ts alone, without rewriting it, and ClickHouse moves the parts in the background.
func (c *chConn) coldTier(ctx context.Context) error {
	policy, err := chColumn[string](ctx, c.conn, "SELECT policy_name FROM system.storage_policies WHERE policy_name = 'tiered' LIMIT 1")
	if err != nil || len(policy) == 0 {
		return err
	}
	table, err := chColumn[string](ctx, c.conn, "SELECT storage_policy FROM system.tables WHERE database = ? AND name = 'receptions'", c.db)
	if err != nil {
		return err
	}
	if len(table) == 1 && table[0] != "tiered" {
		if err := c.exec(ctx, "ALTER TABLE {db}.receptions MODIFY SETTING storage_policy = 'tiered', materialize_ttl_recalculate_only = 1"); err != nil {
			return fmt.Errorf("cold tier: %w", err)
		}
		log.Printf("clickhouse: %s.receptions is on the tiered storage policy", c.db)
	}
	ttl, err := chColumn[string](ctx, c.conn, "SELECT engine_full FROM system.tables WHERE database = ? AND name = 'receptions'", c.db)
	if err != nil {
		return err
	}
	if len(ttl) == 1 && !strings.Contains(ttl[0], "TO VOLUME 'cold'") {
		if err := c.exec(ctx, fmt.Sprintf("ALTER TABLE {db}.receptions MODIFY TTL toDateTime(ts) + INTERVAL %d DAY TO VOLUME 'cold'", chColdDays)); err != nil {
			return fmt.Errorf("cold tier: %w", err)
		}
		log.Printf("clickhouse: %s.receptions moves its parts to R2 %d days past their rows' time", c.db, chColdDays)
	}
	return nil
}

// chDeleteSync makes a lightweight DELETE wait until its rows are gone before it returns, so what follows it, a
// reload's insert or a rebuild, never sees them. It is ClickHouse's default, set here so a server profile that
// changes it cannot break a reload.
func chDeleteSync(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"lightweight_deletes_sync": 2}))
}

// chColumn reads a query's single column.
func chColumn[T any](ctx context.Context, conn driver.Conn, query string, args ...any) ([]T, error) {
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// legacy reports whether receptions still has the columns of its first layout, tx and recv_ts.
func (c *chConn) legacy(ctx context.Context) (bool, error) {
	var n uint64
	err := c.conn.QueryRow(ctx, "SELECT count() FROM system.columns WHERE database = ? AND table = 'receptions' AND name = 'tx'", c.db).Scan(&n)
	return n > 0, err
}

// chFineSpan is the longest range a step under a minute reads from the positions view, which groups every copy
// in the range before it thins; a longer range reads positions_1m instead.
const chFineSpan = 31 * 24 * time.Hour

// chTable is the table that answers a step, and its window: every position, through the positions view, for a
// step under a minute over a range positions can group, and otherwise positions_1m, a window a minute wide. A
// step of whole minutes reads positions_1m's minutes grouped by the step, so a vessel underway gets the same
// answer from it as from every position, to within the minute; a still vessel gets its heartbeats. A range that
// starts inside the 48-hour window reads the positions view at any step, so recent tracks thin every position.
func chTable(from, to time.Time, step time.Duration, now time.Time) (string, time.Duration) {
	if (step < time.Minute || now.Sub(from) <= trackWindow) && to.Sub(from) <= chFineSpan {
		return "positions", 0
	}
	return "positions_1m", time.Minute
}

// chTime formats t for a positions view parameter, which takes a literal rather than an expression.
func chTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000") }

// history reads the newest limit+1 positions, oldest first, so the caller can tell the limit cut the range.
// Positions are thinned in the query by the same epoch buckets the caller thins by, a rollup's windows grouped
// into the step, so the limit counts what the answer keeps and a long range never comes back whole.
func (c *chConn) history(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) ([]trackPoint, error) {
	const row = "(ts, lat6, lon6, sog10, cog10, heading, navstat, source)"
	if step < time.Millisecond { // the caller's buckets are milliseconds; under one, it keeps every position
		step = 0
	}
	table, window := chTable(from, to, step, now)
	view := c.db + ".positions(mmsi = ?, from = ?, to = ?)"
	var q string
	var args []any
	switch {
	case window > 0:
		// FINAL keeps one row per window where parts have not merged yet. A heartbeat's window is 30 minutes, so
		// the slot bound reaches back to the start of the one from falls in, and rows are bounded by their own times.
		q = "SELECT f.1, f.2, f.3, f.4, f.5, f.6, f.7, f.8 FROM (SELECT argMin(" + row + ", ts) AS f FROM " + c.db + "." + table +
			" FINAL WHERE mmsi = ? AND slot >= ? AND slot <= ? AND ts >= ? AND ts <= ? GROUP BY intDiv(toUnixTimestamp64Milli(ts), ?))" +
			" ORDER BY f.1 DESC LIMIT ?"
		// Minutes group into the step only when they divide it; otherwise each minute comes back alone, and the
		// caller's thinning puts it in the step bucket of its own time.
		group := window
		if step%window == 0 {
			group = max(step, window)
		}
		args = []any{mmsi, from.UTC().Truncate(30 * time.Minute), to, from, to, group.Milliseconds(), limit + 1}
	case step > 0:
		q = "SELECT f.1, f.2, f.3, f.4, f.5, f.6, f.7, f.8 FROM (SELECT argMin(" + row + ", ts) AS f FROM " + view +
			" GROUP BY intDiv(toUnixTimestamp64Milli(ts), ?)) ORDER BY f.1 DESC LIMIT ?"
		args = []any{mmsi, chTime(from), chTime(to), step.Milliseconds(), limit + 1}
	default:
		q = "SELECT ts, lat6, lon6, sog10, cog10, heading, navstat, source FROM " + view + " ORDER BY ts DESC LIMIT ?"
		args = []any{mmsi, chTime(from), chTime(to), limit + 1}
	}
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []trackPoint
	for rows.Next() {
		pt := trackPoint{mmsi: mmsi}
		if err := rows.Scan(&pt.ts, &pt.lat6, &pt.lon6, &pt.sog10, &pt.cog10, &pt.heading, &pt.navStatus, &pt.source); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}
	slices.Reverse(points)
	return points, rows.Err()
}

func (c *chConn) insert(ctx context.Context, token string, points []trackPoint) error {
	return c.write(ctx, token, points, false)
}

// write inserts points; converted also writes each one's arrival to recv_ts, which marks a row aiscast
// convert-receptions wrote until cleanup drops the column.
func (c *chConn) write(ctx context.Context, token string, points []trackPoint, converted bool) error {
	// A report whose clock is far off can spread one batch over more daily partitions than ClickHouse allows in
	// an insert by default; it warns instead of refusing the batch.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"insert_deduplication_token":               token,
		"throw_on_max_partitions_per_insert_block": 0,
	}))
	cols := "mmsi, ts, tx_off, tx_disc, recv_delay, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad, moving, stale"
	if converted {
		cols += ", recv_ts"
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+"."+cmp.Or(c.table, "receptions")+" ("+cols+")")
	if err != nil {
		return refusal(err)
	}
	for _, pt := range points {
		// A position without a transmission named is its own, accepted when it was stamped; one loaded from
		// elsewhere arrived when it was sent, as far as anyone knows.
		txAt, disc := pt.txAt, pt.txDisc
		if txAt.IsZero() {
			txAt, disc = pt.ts, discOf(eventID(fmt.Sprint(pt.lat6, pt.lon6)))
		}
		var delay int64
		if !pt.recv.IsZero() {
			delay = pt.recv.Sub(pt.ts).Milliseconds()
		}
		row := []any{pt.mmsi, pt.ts, clampInt32(txAt.Sub(pt.ts).Milliseconds()), disc, clampInt32(delay),
			pt.lat6, pt.lon6, pt.sog10, pt.cog10, pt.heading, pt.navStatus, pt.source,
			pt.station, !pt.dup, !pt.uncorroborated, pt.implausible, pt.clockBad, !pt.still, pt.stale}
		if converted {
			row = append(row, pt.recv)
		}
		if err := batch.Append(row...); err != nil {
			batch.Abort()
			return chRefused{err}
		}
	}
	return refusal(batch.Send())
}

// chInsertTimeout bounds one batch, so a stalled server costs a flush, not the writer.
const chInsertTimeout = 30 * time.Second

// chRefuseFor is how long ClickHouse may keep refusing a batch before it is dropped, so one it refuses every
// time cannot stop every batch after it. It is long enough to outlast overload, which ClickHouse also answers
// with refusals such as a full memory budget or too many parts. Lost connections and timeouts never count:
// through an outage the batch waits, and the queue behind it buffers.
const chRefuseFor = 10 * time.Minute

// chOwnInsertTimeout bounds a station_own insert, a few rows, well under chInsertTimeout, so shutdown's flushes
// still fit the service's stop timeout with the positions first in line.
const chOwnInsertTimeout = 5 * time.Second

// chRefused is a batch ClickHouse answered with an error, or one the client could not encode: sending it again
// may well fail the same way.
type chRefused struct{ error }

func (e chRefused) Unwrap() error { return e.error }

// refusal marks err as a refusal when ClickHouse itself returned it.
func refusal(err error) error {
	var ex *clickhouse.Exception
	if errors.As(err, &ex) {
		return chRefused{err}
	}
	return err
}

func (p *Pipeline) attachClickHouse(c *chStore) {
	p.vmu.Lock()
	p.ch = c
	p.vmu.Unlock()
	p.chMu.Lock()
	p.chQueue = make([]trackPoint, 0, 1024)
	p.chOwn = map[ownKey]time.Time{}
	p.chOwnClaimed = map[ownKey]map[uint32]bool{}
	p.chOwnClaimHours = map[int64]int{}
	p.chMu.Unlock()
	p.chOn.Store(true)
}

// runClickHouse connects to url, retrying each minute until ClickHouse answers, then writes receptions once a
// second. Copies received before it connects are not written.
func (p *Pipeline) runClickHouse(url string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		conn, err := openClickHouse(ctx, url)
		cancel()
		if err == nil {
			p.attachClickHouse(&chStore{w: conn, r: conn, cov: conn, own: conn})
			log.Printf("clickhouse: writing receptions to %s", conn.db)
			break
		}
		log.Printf("clickhouse: %v; retrying in a minute", err)
		time.Sleep(time.Minute)
	}
	for range time.Tick(time.Second) {
		p.flushClickHouse()
	}
}

// drainClickHouse sends a failed batch and then the queue behind it, for shutdown.
func (p *Pipeline) drainClickHouse() error {
	var ownErr error
	for range 2 {
		err := p.flushClickHouse()
		// A failed station_own insert must not stop the positions queued behind it from going, and one the next
		// attempt wrote is not an error.
		if err != nil && !errors.As(err, new(ownFailed)) {
			return err
		}
		ownErr = err
	}
	return ownErr
}

// ownFailed is a station_own insert that failed while the receptions were written.
type ownFailed struct{ error }

func (e ownFailed) Unwrap() error { return e.error }

// flushClickHouse writes one batch: the one that failed last time, unchanged and under its token, or else
// everything queued since. Positions keep queueing behind a failed batch, within the queue's bound.
func (p *Pipeline) flushClickHouse() error {
	p.vmu.RLock()
	c := p.ch
	p.vmu.RUnlock()
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Own-ship sightings go whatever the receptions do, and their failure is reported once the receptions are written.
	ownErr := p.flushOwn(c)
	if c.failed == nil {
		p.chMu.Lock()
		points := p.chQueue
		if len(points) > 0 {
			p.chQueue = make([]trackPoint, 0, len(points))
		}
		p.chMu.Unlock()
		if len(points) == 0 {
			return ownErr
		}
		c.failed, c.token, c.refused = points, newDedupeToken(), time.Time{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), chInsertTimeout)
	start := time.Now()
	err := c.w.insert(ctx, c.token, c.failed)
	cancel()
	c.writeNanos.Add(int64(time.Since(start)))
	// Logged when writes start failing and when they recover, not once a second through an outage.
	if was := c.failing.Swap(err != nil); was != (err != nil) {
		if err != nil {
			log.Printf("clickhouse: %v; positions queue until a batch is written", err)
		} else {
			log.Printf("clickhouse: writing again")
		}
	}
	if err != nil {
		c.failures.Add(1)
		if !errors.As(err, new(chRefused)) {
			return err
		}
		if c.refused.IsZero() {
			c.refused = time.Now()
		}
		if time.Since(c.refused) >= chRefuseFor {
			log.Printf("clickhouse: dropped a batch of %d positions refused for %v: %v", len(c.failed), chRefuseFor, err)
			c.dropped.Add(int64(len(c.failed)))
			c.failed = nil
		}
		return err
	}
	c.written.Add(int64(len(c.failed)))
	c.failed = nil
	return ownErr
}

// flushOwn writes the own-ship sightings gathered since the last flush. A failed insert puts them back, keeping
// the later time where one arrived meanwhile, to go with the next; station_own's max makes a resend harmless.
func (p *Pipeline) flushOwn(c *chStore) error {
	if c.own == nil {
		return nil
	}
	p.chMu.Lock()
	own := p.chOwn
	if len(own) > 0 {
		p.chOwn = map[ownKey]time.Time{}
	}
	// Claims from before the last hour received no longer bound anything a station sends.
	p.pruneClaims(p.chOwnHW - 1)
	p.chMu.Unlock()
	if len(own) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), chOwnInsertTimeout)
	err := c.own.insertOwn(ctx, own)
	cancel()
	// Logged when inserts start failing and when they recover, as the receptions' are.
	if was := c.ownFailing.Swap(err != nil); was != (err != nil) {
		if err != nil {
			log.Printf("clickhouse: station_own: %v; own-ship sightings wait for the next flush", err)
		} else {
			log.Printf("clickhouse: station_own writing again")
		}
	}
	if err == nil {
		return nil
	}
	c.failures.Add(1)
	p.chMu.Lock()
	for k, t := range own {
		last, ok := p.chOwn[k]
		if !ok && len(p.chOwn) >= maxOwnPending {
			c.ownDropped.Add(1)
			continue
		}
		if t.After(last) {
			p.chOwn[k] = t
		}
	}
	p.chMu.Unlock()
	return ownFailed{fmt.Errorf("station_own: %w", err)}
}

// insertOwn writes own-ship sightings to station_own.
func (c *chConn) insertOwn(ctx context.Context, own map[ownKey]time.Time) error {
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+"."+cmp.Or(c.own, "station_own")+" (hour, station, mmsi, last_ts)")
	if err != nil {
		return err
	}
	for k, t := range own {
		if err := batch.Append(time.Unix(k.hour*3600, 0).UTC(), k.station, k.mmsi, t); err != nil {
			batch.Abort()
			return err
		}
	}
	return batch.Send()
}

func newDedupeToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// clampInt32 fits a millisecond span into Int32, about 24 days either way; a copy that far from its transmission
// or its arrival is clock_bad and never served.
func clampInt32(n int64) int32 {
	return int32(max(min(n, math.MaxInt32), math.MinInt32))
}
