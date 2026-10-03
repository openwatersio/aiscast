package main

// History in ClickHouse: every accepted position, written once a second over the native protocol, and two
// rollups ClickHouse keeps as the positions arrive. positions holds every position for 30 days, sorted by
// vessel and time. positions_15m and positions_1h hold each vessel's first position in each epoch-aligned
// window, which is what the track endpoint's thinning keeps, so a step that is a whole number of windows reads
// the same answer from a rollup as from every position. ClickHouse being slow or down never holds up ingest:
// its queue is bounded, and what falls out is counted. A batch whose insert failed is
// sent again whole, under the same deduplication token, since a failure can come after ClickHouse committed it.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
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

// chSchema creates what the writer and the rollups need, in order; {db} is the database. Positions use the
// track encodings, 15 in navstat for not available, and a source kind rather than a full source.
var chSchema = []string{
	`CREATE DATABASE IF NOT EXISTS {db}`,
	`CREATE TABLE IF NOT EXISTS {db}.positions (
		mmsi    UInt32,
		ts      DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
		lat6    Int32 CODEC(Delta, ZSTD),
		lon6    Int32 CODEC(Delta, ZSTD),
		sog10   UInt16 CODEC(ZSTD),
		cog10   UInt16 CODEC(ZSTD),
		heading UInt16 CODEC(ZSTD),
		navstat UInt8,
		source  LowCardinality(String)
	) ENGINE = MergeTree
	PARTITION BY toYYYYMMDD(ts)
	ORDER BY (mmsi, ts)
	TTL toDateTime(ts) + INTERVAL 30 DAY DELETE
	SETTINGS non_replicated_deduplication_window = 1000`,
	chRollup("15m", "TTL slot + INTERVAL 13 MONTH DELETE"),
	chRollupView("15m", "15 MINUTE"),
	chRollup("1h", ""),
	chRollupView("1h", "1 HOUR"),
}

// chFirst is a rollup's per-window aggregate: the earliest position, whenever a late report arrives.
const chFirst = `AggregateFunction(argMin, Tuple(DateTime64(3, 'UTC'), Int32, Int32, UInt16, UInt16, UInt16, UInt8, String), DateTime64(3, 'UTC'))`

func chRollup(name, ttl string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {db}.positions_%s (
		mmsi  UInt32,
		slot  DateTime('UTC'),
		first %s
	) ENGINE = AggregatingMergeTree
	PARTITION BY toYYYYMM(slot)
	ORDER BY (mmsi, slot)
	%s`, name, chFirst, ttl)
}

func chRollupView(name, window string) string {
	return fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.positions_%s_mv TO {db}.positions_%s AS
	SELECT mmsi, toDateTime(toStartOfInterval(ts, INTERVAL %s), 'UTC') AS slot,
	       argMinState((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), ts) AS first
	FROM {db}.positions GROUP BY mmsi, slot`, name, name, window)
}

// chWriter inserts a batch of positions under a deduplication token, so ClickHouse skips a batch it already
// holds; tests fake it.
type chWriter interface {
	insert(ctx context.Context, token string, points []trackPoint) error
}

// chReader reads one vessel's history; tests fake it.
type chReader interface {
	history(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) ([]trackPoint, error)
	first(ctx context.Context, mmsi uint32, from, to time.Time) (time.Time, bool, error)
}

// chStore is the attached ClickHouse: the writer, the reader, the batch waiting to be sent again, and what
// /metrics reports about it.
type chStore struct {
	w chWriter
	r chReader

	mu      sync.Mutex // one flush at a time, so a resend never races the batch it repeats
	failed  []trackPoint
	token   string
	refused time.Time // when ClickHouse first refused the failed batch; zero until it does

	written, failures, dropped atomic.Int64
	writeNanos                 atomic.Int64
	failing                    atomic.Bool // the last batch failed; cleared when one is written
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
	for _, stmt := range chSchema {
		if err := conn.Exec(ctx, strings.ReplaceAll(stmt, "{db}", db)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("clickhouse schema: %w", err)
		}
	}
	return &chConn{conn: conn, db: db}, nil
}

// chRawKeep and chRollupKeep are how long positions and positions_15m hold their rows; the schema's TTLs.
const (
	chRawKeep    = 30 * 24 * time.Hour
	chRollupKeep = 13 * 30 * 24 * time.Hour
)

// chTable is the table that answers a step, and its window: the coarsest that holds the range and whose window
// divides the step. A rollup keeps the first position per window, which is what thinning every position keeps,
// so a step of whole windows reads the same answer from it. A range older than every finer table reads the
// rollup that holds it, at one position per window, which still keeps the step's at-most-one promise; a step
// its windows do not divide can then show a later position in a bucket, or none, since a window keeps only its
// first. Every default step divides its window. A range inside the 48-hour window reads every position: a
// rollup window that starts before from would leave out the positions in its part of the range.
func chTable(from time.Time, step time.Duration, now time.Time) (string, time.Duration) {
	age := now.Sub(from)
	switch {
	case age <= trackWindow || age < chRawKeep && (step < 15*time.Minute || step%(15*time.Minute) != 0):
		return "positions", 0
	case age < chRollupKeep && (step < time.Hour || step%time.Hour != 0):
		return "positions_15m", 15 * time.Minute
	}
	return "positions_1h", time.Hour
}

// history reads the newest limit+1 positions, oldest first, so the caller can tell the limit cut the range.
// Positions are thinned in the query by the same epoch buckets the caller thins by, a rollup's windows grouped
// into the step, so the limit counts what the answer keeps and a long range never comes back whole.
func (c *chConn) history(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) ([]trackPoint, error) {
	const row = "(ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source))"
	if step < time.Millisecond { // the caller's buckets are milliseconds; under one, it keeps every position
		step = 0
	}
	table, window := chTable(from, step, now)
	var q string
	var args []any
	switch {
	case window > 0:
		// ponytail: a window that starts before from and whose first position is before from is left out, so a
		// range not aligned to the window can miss positions in its first window
		q = "SELECT f.1, f.2, f.3, f.4, f.5, f.6, f.7, f.8 FROM (SELECT argMinMerge(first) AS f FROM " + c.db + "." + table +
			" WHERE mmsi = ? AND slot >= ? AND slot <= ? GROUP BY intDiv(toUnixTimestamp(slot), ?)) WHERE f.1 >= ? AND f.1 <= ? ORDER BY f.1 DESC LIMIT ?"
		// Windows group into the step only when they divide it; otherwise each window's first position comes
		// back alone, and the caller's thinning puts it in the step bucket of its own time.
		group := window
		if step%window == 0 {
			group = max(step, window)
		}
		args = []any{mmsi, from.Truncate(window), to, int64(group / time.Second), from, to, limit + 1}
	case step > 0:
		q = "SELECT f.1, f.2, f.3, f.4, f.5, f.6, f.7, f.8 FROM (SELECT argMin(" + row + ", ts) AS f FROM " + c.db +
			".positions WHERE mmsi = ? AND ts >= ? AND ts <= ? GROUP BY intDiv(toUnixTimestamp64Milli(ts), ?)) ORDER BY f.1 DESC LIMIT ?"
		args = []any{mmsi, from, to, step.Milliseconds(), limit + 1}
	default:
		q = "SELECT ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source) FROM " + c.db +
			".positions WHERE mmsi = ? AND ts >= ? AND ts <= ? ORDER BY ts DESC LIMIT ?"
		args = []any{mmsi, from, to, limit + 1}
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

// first is the time of the vessel's first position between from and to in positions, so within its 30 days;
// ok is false when it has none there.
func (c *chConn) first(ctx context.Context, mmsi uint32, from, to time.Time) (ts time.Time, ok bool, err error) {
	err = c.conn.QueryRow(ctx, "SELECT ts FROM "+c.db+".positions WHERE mmsi = ? AND ts >= ? AND ts <= ? ORDER BY ts LIMIT 1", mmsi, from, to).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return ts, false, nil
	}
	return ts, err == nil, err
}

func (c *chConn) insert(ctx context.Context, token string, points []trackPoint) error {
	// A report whose clock is far off can spread one batch over more daily partitions than ClickHouse allows in
	// an insert by default; it warns instead of refusing the batch.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"insert_deduplication_token":               token,
		"throw_on_max_partitions_per_insert_block": 0,
	}))
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+".positions")
	if err != nil {
		return refusal(err)
	}
	for _, pt := range points {
		if err := batch.Append(pt.mmsi, pt.ts, pt.lat6, pt.lon6, pt.sog10, pt.cog10, pt.heading, pt.navStatus, pt.source); err != nil {
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
	p.chQueue = make([]trackPoint, 0, 1024)
	p.vmu.Unlock()
}

// runClickHouse connects to url, retrying each minute until ClickHouse answers, then writes positions once a
// second. Positions accepted before it connects are not written.
func (p *Pipeline) runClickHouse(url string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		conn, err := openClickHouse(ctx, url)
		cancel()
		if err == nil {
			p.attachClickHouse(&chStore{w: conn, r: conn})
			log.Printf("clickhouse: writing positions to %s", conn.db)
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
		p.vmu.Lock()
		points := p.chQueue
		if len(points) > 0 {
			p.chQueue = make([]trackPoint, 0, len(points))
		}
		p.vmu.Unlock()
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
