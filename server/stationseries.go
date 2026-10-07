package main

// Station series: what each station heard each hour, in station_hours, and which vessels, in station_vessels, from
// which the station list's uptime and vessel counts, /v1/stats' per-source counts, and MCP's coverage read. Both
// hold counts, which would double if a reception were binned twice, so they are rebuilt rather than appended to:
// a view marks the hour of every reception inserted in station_dirty, whichever path inserts it, and the one job
// in the server deletes each marked hour from both and bins it again from receptions. Paths that delete from
// receptions mark the hours they delete. A backfill bins every day receptions held before the view, newest first.

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// stationSeriesSettle is how old a marker must be to be rebuilt: a younger one may belong to an insert whose rows
// are not all visible yet. Longer than chInsertTimeout, so no insert still in flight has a marker that old.
var stationSeriesSettle = time.Minute

const (
	stationSeriesEvery  = 5 * time.Minute
	stationSeriesSpend  = 2 * time.Minute // backfill per run, so the marked hours never wait long behind it
	stationWindow       = 7 * 24          // hours of uptime
	chStationVesselDays = 35
)

// chStationHours keeps, per station and hour, its usable receptions and how many of them were the copy delivered
// first. It is kept as long as receptions are.
const chStationHours = `CREATE TABLE IF NOT EXISTS {db}.station_hours (
	hour       DateTime('UTC'),
	station    LowCardinality(String),
	source     LowCardinality(String),
	receptions UInt64,
	first      UInt64
) ENGINE = MergeTree
PARTITION BY toYYYYMM(hour)
ORDER BY (station, hour)`

// chStationVessels keeps, per station, hour, and vessel, its usable receptions that were not stale copies, and the
// time of the latest, so a rolling window reads the rows whose latest falls inside it.
var chStationVessels = `CREATE TABLE IF NOT EXISTS {db}.station_vessels (
	hour       DateTime('UTC'),
	station    LowCardinality(String),
	source     LowCardinality(String),
	mmsi       UInt32,
	receptions UInt32,
	last_ts    DateTime64(3, 'UTC')
) ENGINE = MergeTree
PARTITION BY toYYYYMM(hour)
ORDER BY (station, hour, mmsi)
TTL hour + INTERVAL ` + fmt.Sprint(chStationVesselDays) + ` DAY DELETE`

// chStationDirty marks the hour of every reception inserted, with when; chStationBuilt records, per hour, the
// newest marker its last rebuild took in. An hour whose newest settled marker is newer is rebuilt. Markers outlive
// the copies that trickle in for a day or two after their hour, and a server down or failing for up to two weeks;
// the built record outlives the markers.
const (
	chStationDirty   = `CREATE TABLE IF NOT EXISTS {db}.station_dirty (hour DateTime('UTC'), marked DateTime64(3, 'UTC') DEFAULT now64(3)) ENGINE = MergeTree ORDER BY (hour, marked) TTL toDateTime(marked) + INTERVAL 14 DAY DELETE`
	chStationDirtyMV = `CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.station_dirty_mv TO {db}.station_dirty AS SELECT DISTINCT toStartOfHour(toDateTime(ts, 'UTC')) AS hour FROM {db}.receptions`
	chStationBuilt   = `CREATE TABLE IF NOT EXISTS {db}.station_built (hour DateTime('UTC'), marker DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(marker) ORDER BY hour TTL toDateTime(marker) + INTERVAL 15 DAY DELETE`
	chSeriesLedger   = `CREATE TABLE IF NOT EXISTS {db}.station_series_backfilled (day Date) ENGINE = ReplacingMergeTree ORDER BY day`
)

// stationSeries is the job and the reads over station_hours and station_vessels.
type stationSeries interface {
	// rebuildStationSeries rebuilds the marked hours, then backfills older days for up to stationSeriesSpend.
	rebuildStationSeries(ctx context.Context, now time.Time) error
	// stationCounts is each station's uptime, vessels, and totals.
	stationCounts(ctx context.Context, now time.Time) (map[string]stationCount, error)
	// sourceCounts is, per source kind, the vessels its stations heard within vesselTTL and how many no other kind did.
	sourceCounts(ctx context.Context, now time.Time) (map[string][2]int, error)
	// ownCandidates is each station's own-ship MMSIs since a time, with the unix seconds of the last.
	ownCandidates(ctx context.Context, since time.Time) (map[string]map[uint32]int64, error)
	// stationPoints is the centers of each volunteer station's finest coverage cells since a day.
	stationPoints(ctx context.Context, since time.Time) (map[string][][2]float64, error)
}

// stationCount is one station's figures from the series.
type stationCount struct {
	first              time.Time // its first hour
	receptions, firsts uint64
	uptime             float64 // share of the hours since the later of 7 days ago and its first hour with a reception
	live, day, unique  int     // vessels within vesselTTL and stationVesselTTL, and of the latter, heard by no other station
}

// markDirty marks the hours of the receptions where selects, before they are deleted.
func (c *chConn) markDirty(ctx context.Context, where string, args ...any) error {
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_dirty (hour) SELECT DISTINCT toStartOfHour(toDateTime(ts, 'UTC')) FROM "+c.db+".receptions WHERE "+where, args...)
}

// markDay marks every hour of a UTC day, for a change to which of its receptions count.
func (c *chConn) markDay(ctx context.Context, day time.Time) error {
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_dirty (hour) SELECT toDateTime(?, 'UTC') + INTERVAL number HOUR FROM numbers(24)", day.UTC().Truncate(24*time.Hour))
}

// stationSeriesRun keeps one rebuild at a time in a process, as the one writer the counts rely on.
var stationSeriesRun sync.Mutex

func (c *chConn) rebuildStationSeries(ctx context.Context, now time.Time) error {
	stationSeriesRun.Lock()
	defer stationSeriesRun.Unlock()
	type mark struct {
		hour   time.Time
		marker time.Time
	}
	rows, err := c.conn.Query(ctx, `SELECT d.hour, d.m FROM
		(SELECT hour, max(marked) AS m FROM `+c.db+`.station_dirty WHERE marked < now64(3) - INTERVAL ? SECOND GROUP BY hour) AS d
		LEFT JOIN (SELECT hour, max(marker) AS b FROM `+c.db+`.station_built GROUP BY hour) AS s ON d.hour = s.hour
		WHERE d.m > s.b ORDER BY d.hour`, int(stationSeriesSettle.Seconds()))
	if err != nil {
		return err
	}
	byDay := map[time.Time][]mark{}
	for rows.Next() {
		var m mark
		if err := rows.Scan(&m.hour, &m.marker); err != nil {
			rows.Close()
			return err
		}
		day := m.hour.UTC().Truncate(24 * time.Hour)
		byDay[day] = append(byDay[day], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Newest day first; a day that fails is logged and left for the next run, so it holds up no other.
	days := slices.SortedFunc(maps.Keys(byDay), func(a, b time.Time) int { return b.Compare(a) })
	for _, day := range days {
		marks := byDay[day]
		hours := make([]time.Time, len(marks))
		for i, m := range marks {
			hours[i] = m.hour
		}
		if err := c.binHours(ctx, hours, now); err != nil {
			log.Printf("station series: %s: %v", day.Format("2006-01-02"), err)
			continue
		}
		batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+".station_built (hour, marker)")
		if err != nil {
			return err
		}
		for _, m := range marks {
			if err := batch.Append(m.hour, m.marker); err != nil {
				batch.Abort()
				return err
			}
		}
		if err := batch.Send(); err != nil {
			return err
		}
	}
	return c.backfillStationSeries(ctx, now, time.Now().Add(stationSeriesSpend))
}

// backfillStationSeries bins whole days the series has not, newest first from today, until until: the days
// receptions held before the view marked them. Its ledger records each day as it finishes.
func (c *chConn) backfillStationSeries(ctx context.Context, now, until time.Time) error {
	done, err := chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(day, 'UTC') FROM "+c.db+".station_series_backfilled FINAL")
	if err != nil {
		return err
	}
	// The oldest reception's day, from the parts' own bounds on ts, so it reads no rows.
	oldest, err := chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(toDate(min(ts)), 'UTC') FROM "+c.db+".receptions HAVING count() > 0")
	if err != nil || len(oldest) == 0 {
		return err
	}
	for day := now.UTC().Truncate(24 * time.Hour); !day.Before(oldest[0]) && time.Now().Before(until); day = day.AddDate(0, 0, -1) {
		if slices.ContainsFunc(done, day.Equal) {
			continue
		}
		hours := make([]time.Time, 24)
		for i := range hours {
			hours[i] = day.Add(time.Duration(i) * time.Hour)
		}
		if err := c.binHours(ctx, hours, now); err != nil {
			log.Printf("station series: backfill %s: %v", day.Format("2006-01-02"), err) // left for the next run
			continue
		}
		if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_series_backfilled VALUES (?)", day); err != nil {
			return err
		}
	}
	return nil
}

// binHoursFails, when set by a test, fails the binning of the day it names.
var binHoursFails func(day time.Time) error

// binHours replaces the rows of hours, all within one UTC day, in both tables with what receptions holds for them.
func (c *chConn) binHours(ctx context.Context, hours []time.Time, now time.Time) error {
	slices.SortFunc(hours, func(a, b time.Time) int { return a.Compare(b) })
	if binHoursFails != nil {
		if err := binHoursFails(hours[0].UTC().Truncate(24 * time.Hour)); err != nil {
			return err
		}
	}
	from, to := hours[0], hours[len(hours)-1].Add(time.Hour)
	where := "ts >= ? AND ts < ? AND toStartOfHour(toDateTime(ts, 'UTC')) IN ? AND " + chUsable
	legacy, err := c.legacy(ctx)
	if err != nil {
		return err
	}
	if legacy {
		where += " AND (tx = 0 OR toDate(ts) NOT IN (SELECT day FROM " + c.db + ".receptions_converted))"
	}
	del := chDeleteSync(ctx)
	// ponytail: a read between an hour's delete and its insert misses the hour, a second or so in each run, which
	// the minute's cache can hold; versioned rows read by their latest would close it.
	bctx, cancel := chBinning(ctx)
	defer cancel()
	if err := c.conn.Exec(del, "DELETE FROM "+c.db+".station_hours WHERE hour IN ?", hours); err != nil {
		return fmt.Errorf("station_hours: %w", err)
	}
	if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_hours (hour, station, source, receptions, first)
		SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, count(), countIf(accepted)
		FROM `+c.db+`.receptions WHERE `+where+` GROUP BY hour, station, source`, from, to, hours); err != nil {
		return fmt.Errorf("station_hours: %w", err)
	}
	if to.Before(now.AddDate(0, 0, -chStationVesselDays)) {
		return nil // past what station_vessels keeps
	}
	if err := c.conn.Exec(del, "DELETE FROM "+c.db+".station_vessels WHERE hour IN ?", hours); err != nil {
		return fmt.Errorf("station_vessels: %w", err)
	}
	if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_vessels (hour, station, source, mmsi, receptions, last_ts)
		SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, mmsi, count(), max(ts)
		FROM `+c.db+`.receptions WHERE `+where+` AND NOT stale GROUP BY hour, station, source, mmsi`, from, to, hours); err != nil {
		return fmt.Errorf("station_vessels: %w", err)
	}
	return nil
}

// chOwnIn leaves out, from station_vessels rows, each vessel a station sent as its own ship within a window starting
// at the query's first two arguments: a boat hearing itself is not reception, and every row of it goes.
const chOwnIn = `(station, mmsi) NOT IN (SELECT station, mmsi FROM {db}.station_own WHERE hour >= toStartOfHour(?) - INTERVAL 1 HOUR AND last_ts > ?)`

func (c *chConn) stationCounts(ctx context.Context, now time.Time) (map[string]stationCount, error) {
	out := map[string]stationCount{}
	cur := now.UTC().Truncate(time.Hour)
	start := cur.Add(-(stationWindow - 1) * time.Hour)
	rows, err := c.conn.Query(ctx, `SELECT station, min(hour), sum(receptions), sum(first), uniqExactIf(hour, hour >= ? AND hour < ?), max(hour = ?)
		FROM `+c.db+`.station_hours GROUP BY station`, start, cur, cur)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s stationCount
		var past uint64
		var now uint8
		if err := rows.Scan(&id, &s.first, &s.receptions, &s.firsts, &past, &now); err != nil {
			rows.Close()
			return nil, err
		}
		// Every hour since the later of the window's start and the station's first counts, and the current one once
		// it has a reception.
		from := start
		if s.first.After(from) {
			from = s.first.UTC()
		}
		hours := int(cur.Sub(from)/time.Hour) + int(now)
		if hours > 0 {
			s.uptime = float64(int(past)+int(now)) / float64(hours)
		}
		out[id] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	live, day := now.Add(-vesselTTL), now.Add(-stationVesselTTL)
	rows, err = c.conn.Query(ctx, strings.ReplaceAll(`SELECT station, countIf(last > ?), count(), countIf(hearers = 1) FROM (
			SELECT station, last, count() OVER (PARTITION BY mmsi) AS hearers FROM (
				SELECT station, mmsi, max(last_ts) AS last FROM {db}.station_vessels
				WHERE hour >= toStartOfHour(?) AND last_ts > ? AND `+chOwnIn+` GROUP BY station, mmsi))
		GROUP BY station`, "{db}", c.db), live, day, day, day, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var l, d, u uint64
		if err := rows.Scan(&id, &l, &d, &u); err != nil {
			return nil, err
		}
		s := out[id]
		s.live, s.day, s.unique = int(l), int(d), int(u)
		out[id] = s
	}
	return out, rows.Err()
}

func (c *chConn) sourceCounts(ctx context.Context, now time.Time) (map[string][2]int, error) {
	since := now.Add(-vesselTTL)
	rows, err := c.conn.Query(ctx, `SELECT source, count(), countIf(kinds = 1) FROM (
			SELECT source, count() OVER (PARTITION BY mmsi) AS kinds FROM (
				SELECT DISTINCT source, mmsi FROM `+c.db+`.station_vessels WHERE hour >= toStartOfHour(?) AND last_ts > ?))
		GROUP BY source`, since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int{}
	for rows.Next() {
		var src string
		var n, u uint64
		if err := rows.Scan(&src, &n, &u); err != nil {
			return nil, err
		}
		out[src] = [2]int{int(n), int(u)}
	}
	return out, rows.Err()
}

func (c *chConn) ownCandidates(ctx context.Context, since time.Time) (map[string]map[uint32]int64, error) {
	rows, err := c.conn.Query(ctx, "SELECT station, mmsi, toUnixTimestamp(max(last_ts)) FROM "+c.db+".station_own WHERE hour >= toStartOfHour(?) AND last_ts > ? GROUP BY station, mmsi", since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[uint32]int64{}
	for rows.Next() {
		var id string
		var mmsi uint32
		var at uint32
		if err := rows.Scan(&id, &mmsi, &at); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[uint32]int64{}
		}
		out[id][mmsi] = int64(at)
	}
	return out, rows.Err()
}

func (c *chConn) stationPoints(ctx context.Context, since time.Time) (map[string][][2]float64, error) {
	rows, err := c.conn.Query(ctx, `SELECT station, arrayMap(c -> h3ToGeo(c).1, cells), arrayMap(c -> h3ToGeo(c).2, cells) FROM (
			SELECT station, groupUniqArray(cell) AS cells FROM `+c.db+`.station_coverage
			WHERE day >= ? AND res = ? AND source IN `+chVolunteerSources+` GROUP BY station)
		SETTINGS h3togeo_lon_lat_result_order = 0`, since, uint8(coverageBands[len(coverageBands)-1].res))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][][2]float64{}
	for rows.Next() {
		var id string
		var lats, lons []float64
		if err := rows.Scan(&id, &lats, &lons); err != nil {
			return nil, err
		}
		for i := range lats {
			out[id] = append(out[id], [2]float64{lats[i], lons[i]})
		}
	}
	return out, rows.Err()
}

// runStationSeries waits for ClickHouse, seeds own-vessel candidates from station_own, then rebuilds the series
// every stationSeriesEvery.
func (p *Pipeline) runStationSeries() {
	ctx := context.Background()
	var s stationSeries
	for {
		p.vmu.RLock()
		if p.ch != nil {
			s = p.ch.series
		}
		p.vmu.RUnlock()
		if s != nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	for {
		cands, err := s.ownCandidates(ctx, time.Now().Add(-ownSettle-time.Hour))
		if err == nil {
			p.stations.restoreOwn(cands)
			break
		}
		log.Printf("station series: own candidates: %v", err)
		time.Sleep(time.Minute)
	}
	for {
		if err := s.rebuildStationSeries(ctx, time.Now()); err != nil {
			log.Printf("station series: %v", err)
		}
		time.Sleep(stationSeriesEvery)
	}
}

// stationRollups is the series' figures, read at most once a minute for every caller.
type stationRollups struct {
	mu      sync.Mutex
	at      time.Time
	counts  map[string]stationCount
	sources map[string][2]int
}

// rollups returns the station and source figures, nil without ClickHouse; a failed read keeps the last.
func (p *Pipeline) rollups(now time.Time) (map[string]stationCount, map[string][2]int) {
	p.vmu.RLock()
	var s stationSeries
	if p.ch != nil {
		s = p.ch.series
	}
	p.vmu.RUnlock()
	if s == nil {
		return nil, nil
	}
	r := &p.series
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.at) < time.Minute {
		return r.counts, r.sources
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	counts, err1 := s.stationCounts(ctx, now)
	sources, err2 := s.sourceCounts(ctx, now)
	if err1 != nil || err2 != nil {
		log.Printf("station series: %v %v", err1, err2)
		r.at = now // the last figures stand for the minute, rather than every request waiting on a failing read
		return r.counts, r.sources
	}
	r.at, r.counts, r.sources = now, counts, sources
	return counts, sources
}
