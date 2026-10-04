package main

// History in ClickHouse: every copy of every position report the network receives, written once a second over
// the native protocol, and two rollups ClickHouse keeps as the copies arrive. receptions holds every copy,
// sorted by vessel and time, with the transmission it belongs to; the positions view keeps the earliest copy of
// each transmission. positions_15m and positions_1h hold each vessel's first accepted position in each
// epoch-aligned window, which is what the track endpoint's thinning keeps, so a step that is a whole number of
// windows reads the same answer from a rollup as from every position. ClickHouse being slow or down never holds
// up ingest: its queue is bounded, and what falls out is counted. A batch whose insert failed is sent again
// whole, under the same deduplication token, since a failure can come after ClickHouse committed it.

import (
	"context"
	"crypto/rand"
	"database/sql"
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

// chSchema creates what the writer and positions_1m need, in order; {db} is the database. Receptions use the
// track encodings, 15 in navstat for not available, and a source kind rather than a full source; station is
// the full one. Nothing expires: receptions are the record of history.
//
// A copy names its transmission without a hash that would not compress: the transmission is the vessel, the
// time its accepted copy was stamped, ts plus tx_off, and tx_disc, one byte of that copy's event id, which
// tells apart transmissions stamped in the same millisecond. Most copies are their transmission's accepted one,
// so tx_off is almost always 0. recv_delay is when the copy arrived, as milliseconds after ts. Measured on
// three hours of production receptions, this costs 11.4 bytes a row where a 64-bit hash and a second
// timestamp cost 19.8.
var chSchema = []string{
	`CREATE DATABASE IF NOT EXISTS {db}`,
	`CREATE TABLE IF NOT EXISTS {db}.receptions (
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
	`CREATE TABLE IF NOT EXISTS {db}.positions_1m (
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
	`CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.positions_1m_mv TO {db}.positions_1m AS
	SELECT mmsi, if(moving, toDateTime(toStartOfMinute(ts), 'UTC'), toDateTime(toStartOfDay(ts), 'UTC')) AS slot,
	       if(moving, 0, ` + chCell + `) AS cell, ts, lat6, lon6, sog10, cog10, heading, navstat, source
	FROM {db}.receptions WHERE accepted AND ` + chUsable,
	chPositionsView,
}

// positions_1m is each vessel's track at one position a minute while it moves, and one a day for each place it
// sat still: a heartbeat that says it was heard there that day. A vessel underway reports every few seconds, so
// a minute keeps the shape of its track; one moored or at anchor reports every few minutes from the same place,
// which a day's row says as well. Each window keeps its latest report, so a heartbeat is when the vessel was
// last heard there that day. On 2026-08-28 it kept 10 M rows of 42 M accepted positions, at 12.4 bytes a row
// in plain columns; a window reduced to an aggregate state cost twice that. Any step of a minute or more reads
// from it, grouped by the step, so coarser summaries are never needed to answer one. Places are cells of a
// hundredth of a degree, about a kilometer, so a vessel moored at two harbors in a day has a heartbeat at each.
const chCell = `toUInt32((intDiv(lat6, 6000) + 9000) * 36000 + (intDiv(lon6, 6000) + 18000))`

// chUsable is the condition every history read puts on receptions: copies the fold judged an impossible jump,
// or whose clock put them a day or more before they arrived, are kept but never served.
const chUsable = "NOT implausible AND NOT clock_bad"

// chPositionsView is one row per transmission: the copy that arrived first, among those with a believable
// clock, of each transmission no copy of which the fold judged implausible. A transmission is judged as a
// whole, over every copy, because every copy carries its position: filtering copies before grouping would
// serve a flagged position through an unflagged copy, and a bad clock only rules a copy out as the one served.
// It takes the vessel and range as parameters so the filter reaches receptions' sort key before the grouping;
// a view without them would group the vessel's whole history on every read. The 10 seconds either side catch
// the copies of a transmission near the edge of the range, whose stamps differ by a second or two. It holds no
// data, so it is replaced at every start and a database always reads by the current definition.
const chPositionsView = `CREATE OR REPLACE VIEW {db}.positions AS
	SELECT mmsi, f.1 AS ts, f.2 AS lat6, f.3 AS lon6, f.4 AS sog10, f.5 AS cog10, f.6 AS heading, f.7 AS navstat, f.8 AS source
	FROM (
		SELECT mmsi, argMinIf((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), toUnixTimestamp64Milli(ts) + recv_delay, NOT clock_bad) AS f
		FROM {db}.receptions
		WHERE mmsi = {mmsi:UInt32}
		  AND ts >= {from:DateTime64(3, 'UTC')} - INTERVAL 10 SECOND AND ts <= {to:DateTime64(3, 'UTC')} + INTERVAL 10 SECOND
		GROUP BY mmsi, toUnixTimestamp64Milli(ts) + tx_off, tx_disc
		HAVING NOT max(implausible) AND countIf(NOT clock_bad) > 0
	)
	WHERE ts >= {from:DateTime64(3, 'UTC')} AND ts <= {to:DateTime64(3, 'UTC')}`

// chMigrate moves a database to the current layout, in steps, each run once. A positions table from before
// receptions becomes positions_old. Receptions in the first layout, with a 64-bit tx and recv_ts, become
// receptions_v1, which clickhouse-load.py --convert copies into the current one. positions_15m and
// positions_1h, which positions_1m replaces, go with their views at either step: positions_1m fills from
// receptions as they are loaded or converted. Run before chSchema.
func chMigrate(ctx context.Context, conn driver.Conn, db string) error {
	exec := func(stmts ...string) error {
		for _, stmt := range stmts {
			if err := conn.Exec(ctx, strings.ReplaceAll(stmt, "{db}", db)); err != nil {
				return err
			}
		}
		return nil
	}
	var engine string
	err := conn.QueryRow(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = 'positions'", db).Scan(&engine)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && engine != "View" {
		if err := exec("DROP VIEW IF EXISTS {db}.positions_15m_mv", "DROP VIEW IF EXISTS {db}.positions_1h_mv",
			"DROP TABLE IF EXISTS {db}.positions_15m", "DROP TABLE IF EXISTS {db}.positions_1h",
			"RENAME TABLE {db}.positions TO {db}.positions_old"); err != nil {
			return err
		}
		log.Printf("clickhouse: renamed %s.positions to positions_old; receptions replaces it", db)
	}
	var v1 uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM system.columns WHERE database = ? AND table = 'receptions' AND name = 'tx'", db).Scan(&v1); err != nil {
		return err
	}
	if v1 > 0 {
		if err := exec("DROP VIEW IF EXISTS {db}.positions_15m_mv", "DROP VIEW IF EXISTS {db}.positions_1h_mv",
			"DROP TABLE IF EXISTS {db}.positions_15m", "DROP TABLE IF EXISTS {db}.positions_1h",
			"RENAME TABLE {db}.receptions TO {db}.receptions_v1"); err != nil {
			return err
		}
		log.Printf("clickhouse: renamed %s.receptions to receptions_v1 for conversion; positions_1m replaces positions_15m and positions_1h", db)
	}
	return nil
}

// chWriter inserts a batch of positions under a deduplication token, so ClickHouse skips a batch it already
// holds; tests fake it.
type chWriter interface {
	insert(ctx context.Context, token string, points []trackPoint) error
}

// chReader reads one vessel's history; tests fake it.
type chReader interface {
	history(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) ([]trackPoint, error)
}

// chStore is the attached ClickHouse: the writer, the reader, the batch waiting to be sent again, and what
// /metrics reports about it.
type chStore struct {
	w chWriter
	r chReader // nil keeps tracks on the lake

	mu      sync.Mutex // one flush at a time, so a resend never races the batch it repeats
	failed  []trackPoint
	token   string
	refused time.Time // when ClickHouse first refused the failed batch; zero until it does

	written, failures, dropped atomic.Int64
	writeNanos                 atomic.Int64
	// Stale rebuilt copies the fold matched to a recent transmission, and those it kept as late reports of
	// their own. A late share far above what the sources' delays explain means copies are being served twice.
	rebuiltMatched, rebuiltLate atomic.Int64
	failing                     atomic.Bool // the last batch failed; cleared when one is written
}

// chConn writes positions through a native-protocol connection.
type chConn struct {
	conn driver.Conn
	db   string
}

// openClickHouse connects to url, a clickhouse:// DSN, and creates the schema in the database it names, or
// in chDatabase. The connection itself opens on the server's default database, since the named one may not
// exist yet.
func openClickHouse(ctx context.Context, url string) (*chConn, error) {
	opts, err := clickhouse.ParseDSN(url)
	if err != nil {
		return nil, err
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
	if err := chMigrate(ctx, conn, db); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clickhouse migration: %w", err)
	}
	for _, stmt := range chSchema {
		if err := conn.Exec(ctx, strings.ReplaceAll(stmt, "{db}", db)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("clickhouse schema: %w", err)
		}
	}
	return &chConn{conn: conn, db: db}, nil
}

// chFineSpan is the longest range a step under a minute reads from the positions view, which groups every copy
// in the range before it thins; a longer range reads positions_1m instead.
const chFineSpan = 31 * 24 * time.Hour

// chTable is the table that answers a step, and its window: every position, through the positions view, for a
// step under a minute over a range positions can group, and otherwise positions_1m, a window a minute wide. A
// step of whole minutes reads positions_1m's minutes grouped by the step, so a vessel underway gets the same
// answer from it as from every position, to within the minute; a still vessel gets its heartbeats.
func chTable(from, to time.Time, step time.Duration, now time.Time) (string, time.Duration) {
	if step < time.Minute && to.Sub(from) <= chFineSpan {
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
		// FINAL keeps one row per window where parts have not merged yet. A heartbeat's window starts at
		// midnight, so the slot bound reaches back to the day's start, and the rows are bounded by their own times.
		q = "SELECT f.1, f.2, f.3, f.4, f.5, f.6, f.7, f.8 FROM (SELECT argMin(" + row + ", ts) AS f FROM " + c.db + "." + table +
			" FINAL WHERE mmsi = ? AND slot >= ? AND slot <= ? AND ts >= ? AND ts <= ? GROUP BY intDiv(toUnixTimestamp64Milli(ts), ?))" +
			" ORDER BY f.1 DESC LIMIT ?"
		// Minutes group into the step only when they divide it; otherwise each minute comes back alone, and the
		// caller's thinning puts it in the step bucket of its own time.
		group := window
		if step%window == 0 {
			group = max(step, window)
		}
		args = []any{mmsi, from.UTC().Truncate(24 * time.Hour), to, from, to, group.Milliseconds(), limit + 1}
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
	// A report whose clock is far off can spread one batch over more daily partitions than ClickHouse allows in
	// an insert by default; it warns instead of refusing the batch.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"insert_deduplication_token":               token,
		"throw_on_max_partitions_per_insert_block": 0,
	}))
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+".receptions (mmsi, ts, tx_off, tx_disc, recv_delay, lat6, lon6,"+
		" sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad, moving)")
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
		if err := batch.Append(pt.mmsi, pt.ts, clampInt32(txAt.Sub(pt.ts).Milliseconds()), disc, clampInt32(delay),
			pt.lat6, pt.lon6, pt.sog10, pt.cog10, pt.heading, pt.navStatus, pt.source,
			pt.station, !pt.dup, !pt.uncorroborated, pt.implausible, pt.clockBad, !pt.still); err != nil {
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
			p.attachClickHouse(&chStore{w: conn, r: conn})
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
	for range 2 {
		if err := p.flushClickHouse(); err != nil {
			return err
		}
	}
	return nil
}

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
	if c.failed == nil {
		p.chMu.Lock()
		points := p.chQueue
		if len(points) > 0 {
			p.chQueue = make([]trackPoint, 0, len(points))
		}
		p.chMu.Unlock()
		if len(points) == 0 {
			return nil
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
	return nil
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
