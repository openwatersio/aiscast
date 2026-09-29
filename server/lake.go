package main

// The lake: packaged days of positions in R2 Data Catalog, the Iceberg tables the nightly packager writes. Tracks
// read it for the days before the 48-hour window the track store holds, through DuckDB (lake_duckdb.go). Each
// vessel-day is read once and cached in the track store's file: a packaged day changes only if the packager
// repackages it, which it does only within a week. Reads past the window cost server time and R2 requests, which
// is why they are a feeder-tier capability.
//
// ais.positions is partitioned by day and by a bucket of the MMSI and sorted by MMSI and time, so one vessel's
// day is a few row groups of one file. Each position carries the source that delivered it, and a vessel-day's
// credit lines come from those.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// lakeClient runs one SQL query against the lake. The interface is what tests fake.
type lakeClient interface {
	query(ctx context.Context, sql string) (rows []map[string]json.RawMessage, err error)
}

// errLakeEmpty is a query against a table the catalog does not have yet: an empty lake, not a failure.
var errLakeEmpty = errors.New("lake table not found")

// lakeDay is one vessel's positions for one packaged day, oldest first, each with the source kind that
// delivered it.
type lakeDay struct {
	points []trackPoint
}

// lake reads vessel-days from the lake through the cache.
type lake struct {
	client lakeClient
	cache  *trackStore // the cache lives in the track store's file

	// read by /metrics
	queries, failures, hits, misses atomic.Int64
	queryNanos                      atomic.Int64
}

const (
	// lakeRecent is how long the packager keeps repackaging a day whose inputs changed. A cached day inside it
	// is refetched after lakeRecentTTL; an older one never is.
	lakeRecent    = 8 * 24 * time.Hour
	lakeRecentTTL = 6 * time.Hour
	// lakeCacheBytes bounds the cached positions. Most vessel-days are a few kilobytes; one reporting every
	// second is about 2 MB. The cache is trimmed each time another lakeTrimEvery has been written.
	lakeCacheBytes = 2 << 30
	lakeTrimEvery  = 64 << 20
	// lakeParallel is the vessel-days read from the lake at once for one request.
	lakeParallel = 4
)

// days returns the vessel's positions in each lake partition that can hold a position timed between first and
// last, reading the lake once for the partitions the cache lacks. The lake partitions by the day the server
// received a position, not the day it was sent, and a satellite relay arrives up to about ten hours late, so
// the partition after last's day is read too. The positions are not ordered across partitions.
func (l *lake) days(ctx context.Context, mmsi uint32, first, last time.Time, now time.Time) ([]lakeDay, error) {
	var want []string
	end := last.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	if today := now.UTC().Truncate(24 * time.Hour); end.After(today) {
		end = today
	}
	for d := first.UTC().Truncate(24 * time.Hour); !d.After(end); d = d.Add(24 * time.Hour) {
		want = append(want, d.Format("2006-01-02"))
	}
	cached, err := l.cache.lakeCached(mmsi, want, now)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, d := range want {
		if _, ok := cached[d]; !ok {
			missing = append(missing, d)
		}
	}
	l.hits.Add(int64(len(want) - len(missing)))
	l.misses.Add(int64(len(missing)))
	// One query per day, a few at a time, so a week takes about as long as its slowest day and each day lands
	// in the cache on its own.
	fetched := make([]map[string]lakeDay, len(missing))
	errs := make([]error, len(missing))
	sem := make(chan struct{}, lakeParallel)
	var wg sync.WaitGroup
	for i, d := range missing {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fetched[i], errs[i] = l.fetch(ctx, mmsi, d, d)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	for i, d := range missing {
		// fetch keys days by the partition a row came from, which is the day asked for.
		day := fetched[i][d]
		cached[d] = day
		if err := l.cache.lakeStore(mmsi, d, day, now); err != nil {
			return nil, err
		}
	}
	out := make([]lakeDay, 0, len(want))
	for _, d := range want {
		out = append(out, cached[d])
	}
	return out, nil
}

// fetch reads the positions and their sources for one vessel over a range of days.
func (l *lake) fetch(ctx context.Context, mmsi uint32, first, last string) (map[string]lakeDay, error) {
	days := map[string]lakeDay{}
	rows, err := l.run(ctx, fmt.Sprintf(`SELECT day, ts, lat6, lon6, sog10, cog10, heading, navstat, source FROM lake.ais.positions
		WHERE mmsi = %d AND day >= DATE '%s' AND day <= DATE '%s' AND lat6 IS NOT NULL`, mmsi, first, last))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		day, err := lakeDate(r["day"])
		if err != nil {
			return nil, err
		}
		ts, err := lakeTime(r["ts"])
		if err != nil {
			return nil, err
		}
		// A null is left as the not-available value: the packager writes one when a message lacks the field.
		pt := trackPoint{mmsi: mmsi, ts: ts, sog10: 1023, cog10: 3600, heading: 511}
		nav := -1
		for _, f := range []struct {
			key string
			dst any
		}{{"lat6", &pt.lat6}, {"lon6", &pt.lon6}, {"sog10", &pt.sog10}, {"cog10", &pt.cog10}, {"heading", &pt.heading}, {"navstat", &nav}} {
			if err := json.Unmarshal(r[f.key], f.dst); err != nil {
				return nil, fmt.Errorf("lake %s: %w", f.key, err)
			}
		}
		pt.navStatus = 15 // the lake stores -1 for not available; the stream's sentinel is 15
		if nav >= 0 && nav < 15 {
			pt.navStatus = uint8(nav)
		}
		var source string // null on days packaged before positions carried it
		if err := json.Unmarshal(r["source"], &source); err != nil {
			return nil, fmt.Errorf("lake source: %w", err)
		}
		if source != "" {
			pt.source = sourceKind(source)
		}
		d := days[day]
		d.points = append(d.points, pt)
		days[day] = d
	}
	for day, d := range days {
		sort.Slice(d.points, func(i, j int) bool { return d.points[i].ts.Before(d.points[j].ts) })
		days[day] = d
	}
	return days, nil
}

func (l *lake) run(ctx context.Context, q string) ([]map[string]json.RawMessage, error) {
	start := time.Now()
	rows, err := l.client.query(ctx, q)
	l.queryNanos.Add(int64(time.Since(start)))
	l.queries.Add(1)
	if errors.Is(err, errLakeEmpty) {
		return nil, nil
	}
	if err != nil {
		l.failures.Add(1)
	}
	return rows, err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// lakeDate reads a date as the lake returns it: an ISO date string, or days since the epoch.
func lakeDate(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if len(s) >= 10 {
			return s[:10], nil
		}
		return "", fmt.Errorf("lake day %q", s)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", fmt.Errorf("lake day %s", raw)
	}
	return time.Unix(n*86400, 0).UTC().Format("2006-01-02"), nil
}

// lakeTime reads a timestamp as the lake returns it: an ISO string with or without a zone, or microseconds
// since the epoch. Timestamps without a zone are UTC, as the packager writes them.
func lakeTime(raw json.RawMessage) (time.Time, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMicro(n).UTC(), nil
		}
		return time.Time{}, fmt.Errorf("lake ts %q", s)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return time.Time{}, fmt.Errorf("lake ts %s", raw)
	}
	return time.UnixMicro(n).UTC(), nil
}

// ---- the cache, in tracks.db ----

const lakeCacheSchema = `CREATE TABLE IF NOT EXISTS lake_days (
	mmsi    INTEGER NOT NULL,
	day     TEXT    NOT NULL,
	fetched INTEGER NOT NULL,  -- unix ms
	points  BLOB    NOT NULL,  -- trackPoint records, 24 bytes each: ts ms, lat6, lon6, sog10, cog10, heading, nav status, source
	sources TEXT    NOT NULL,  -- the source kinds a record's last byte indexes, comma-separated
	PRIMARY KEY (mmsi, day)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS lake_days_fetched ON lake_days (fetched)`

const lakePointSize = 24

// noSource is a record's source byte for a position without one.
const noSource = 0xff

// encodeLakePoints returns the records and the source kinds their last byte indexes.
func encodeLakePoints(points []trackPoint) ([]byte, string) {
	var sources []string
	b := make([]byte, 0, len(points)*lakePointSize)
	for _, pt := range points {
		src := byte(noSource)
		if pt.source != "" {
			i := slices.Index(sources, pt.source)
			if i < 0 {
				i, sources = len(sources), append(sources, pt.source)
			}
			if i < noSource { // ponytail: a vessel-day with 255 source kinds credits the first 255; there are a handful
				src = byte(i)
			}
		}
		b = binary.BigEndian.AppendUint64(b, uint64(pt.ts.UnixMilli()))
		b = binary.BigEndian.AppendUint32(b, uint32(pt.lat6))
		b = binary.BigEndian.AppendUint32(b, uint32(pt.lon6))
		b = binary.BigEndian.AppendUint16(b, pt.sog10)
		b = binary.BigEndian.AppendUint16(b, pt.cog10)
		b = binary.BigEndian.AppendUint16(b, pt.heading)
		b = append(b, pt.navStatus, src)
	}
	return b, strings.Join(sources, ",")
}

func decodeLakePoints(mmsi uint32, b []byte, sources string) []trackPoint {
	kinds := strings.Split(sources, ",")
	points := make([]trackPoint, 0, len(b)/lakePointSize)
	for ; len(b) >= lakePointSize; b = b[lakePointSize:] {
		pt := trackPoint{mmsi: mmsi,
			ts:   time.UnixMilli(int64(binary.BigEndian.Uint64(b))).UTC(),
			lat6: int32(binary.BigEndian.Uint32(b[8:])), lon6: int32(binary.BigEndian.Uint32(b[12:])),
			sog10: binary.BigEndian.Uint16(b[16:]), cog10: binary.BigEndian.Uint16(b[18:]),
			heading: binary.BigEndian.Uint16(b[20:]), navStatus: b[22]}
		if i := int(b[23]); i < len(kinds) {
			pt.source = kinds[i]
		}
		points = append(points, pt)
	}
	return points
}

// lakeCached returns the cached vessel-days among days that are still fresh.
func (t *trackStore) lakeCached(mmsi uint32, days []string, now time.Time) (map[string]lakeDay, error) {
	out := map[string]lakeDay{}
	err := t.read(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT day, fetched, points, sources FROM lake_days WHERE mmsi = ? AND day >= ? AND day <= ?`, mmsi, days[0], days[len(days)-1])
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day, sources string
			var fetched int64
			var points []byte
			if err := rows.Scan(&day, &fetched, &points, &sources); err != nil {
				return err
			}
			// A day inside the repackaging week can change, and an empty day may not have been packaged yet,
			// as during a backfill; both are read again once stale. A packaged day older than a week never
			// changes.
			d, _ := time.Parse("2006-01-02", day)
			if (now.Sub(d) < lakeRecent || len(points) == 0) && now.Sub(time.UnixMilli(fetched)) > lakeRecentTTL {
				continue
			}
			out[day] = lakeDay{points: decodeLakePoints(mmsi, points, sources)}
		}
		return rows.Err()
	})
	return out, err
}

// lakeStore caches a vessel-day, and every lakeTrimEvery bytes trims the cache to lakeCacheBytes.
func (t *trackStore) lakeStore(mmsi uint32, day string, d lakeDay, now time.Time) error {
	b, sources := encodeLakePoints(d.points)
	if _, err := t.db.Exec(`INSERT OR REPLACE INTO lake_days (mmsi, day, fetched, points, sources) VALUES (?, ?, ?, ?, ?)`,
		mmsi, day, now.UnixMilli(), b, sources); err != nil {
		return err
	}
	n := int64(len(b))
	if after := t.lakeBytes.Add(n); after/lakeTrimEvery != (after-n)/lakeTrimEvery {
		return t.lakeTrim(lakeCacheBytes)
	}
	return nil
}

// lakeTrim drops the least recently fetched vessel-days until the positions cached total at most max bytes.
func (t *trackStore) lakeTrim(max int64) error {
	_, err := t.db.Exec(`DELETE FROM lake_days WHERE fetched <= (
		SELECT fetched FROM (SELECT fetched, sum(length(points)) OVER (ORDER BY fetched DESC) AS total FROM lake_days)
		WHERE total > ? LIMIT 1)`, max)
	return err
}
