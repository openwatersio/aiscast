package main

// Station series: what each station heard each hour, in station_hours, and which vessels, in station_vessels, from
// which the station list's uptime and vessel counts, /v1/stats' per-source counts, and MCP's coverage read. Both
// hold counts, which would double if a reception were binned twice, so they are rebuilt rather than appended to:
// a view marks the hour of every reception inserted in station_dirty, whichever path inserts it, and a job in the
// server bins each marked hour again from receptions as a new version, then deletes the hour's older versions;
// reads take each hour's newest, so an hour is never read empty or twice while it is rebuilt. Paths that delete
// from receptions mark the hours they delete. A backfill of its own bins every day receptions held before the
// view, newest first.

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
	first      UInt64,
	built      DateTime64(3, 'UTC') -- the rebuild that wrote the row; reads take each hour's newest
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
	last_ts    DateTime64(3, 'UTC'),
	built      DateTime64(3, 'UTC') -- the rebuild that wrote the row; reads take each hour's newest
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
	// rebuildStationSeries rebuilds the marked hours.
	rebuildStationSeries(ctx context.Context, now time.Time) error
	// backfillStationSeries bins the days receptions held before the view, newest first, until every one is done.
	backfillStationSeries(ctx context.Context, now time.Time) error
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

// dirtyHours is the hours of the receptions where selects, read before they are deleted so markHours can mark them
// after: a marker written before the delete could be rebuilt, and recorded as built, while the rows still stand.
func (c *chConn) dirtyHours(ctx context.Context, where string, args ...any) ([]time.Time, error) {
	return chColumn[time.Time](ctx, c.conn, "SELECT DISTINCT toStartOfHour(toDateTime(ts, 'UTC')) FROM "+c.db+".receptions WHERE "+where, args...)
}

// markHours marks hours for the station series to rebuild.
func (c *chConn) markHours(ctx context.Context, hours []time.Time) error {
	if len(hours) == 0 {
		return nil
	}
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_dirty (hour) SELECT arrayJoin(?)", hours)
}

// markDay marks every hour of a UTC day, for a change to which of its receptions count, once the change is made.
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
		WHERE d.m > ifNull(s.b, toDateTime64(0, 3, 'UTC')) ORDER BY d.hour`, int(stationSeriesSettle.Seconds()))
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
	return nil
}

// backfillStationSeries bins whole days the series has not, newest first from today: the days receptions held
// before the view marked them. Its ledger records each day as it finishes, and a day that fails is left for the
// next pass. It runs beside the marked hours' rebuild, which versions make safe.
func (c *chConn) backfillStationSeries(ctx context.Context, now time.Time) error {
	done, err := chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(day, 'UTC') FROM "+c.db+".station_series_backfilled FINAL")
	if err != nil {
		return err
	}
	// The oldest reception's day, from the parts' own bounds on ts, so it reads no rows.
	oldest, err := chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(toDate(min(ts)), 'UTC') FROM "+c.db+".receptions HAVING count() > 0")
	if err != nil || len(oldest) == 0 {
		return err
	}
	failed := 0
	for day := now.UTC().Truncate(24 * time.Hour); !day.Before(oldest[0]); day = day.AddDate(0, 0, -1) {
		if slices.ContainsFunc(done, day.Equal) {
			continue
		}
		hours := make([]time.Time, 24)
		for i := range hours {
			hours[i] = day.Add(time.Duration(i) * time.Hour)
		}
		if err := c.binHours(ctx, hours, now); err != nil {
			log.Printf("station series: backfill %s: %v", day.Format("2006-01-02"), err) // left for the next pass
			failed++
			continue
		}
		if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_series_backfilled VALUES (?)", day); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d days failed", failed)
	}
	return nil
}

// seriesVersion is a rebuild's version, as text to the millisecond, since the driver binds a time to the second and
// two rebuilds of one hour in a second would otherwise share a version and both be read. Each is later than the last.
func seriesVersion() string {
	seriesVersions.Lock()
	defer seriesVersions.Unlock()
	v := time.Now().UTC().Truncate(time.Millisecond)
	if !v.After(seriesVersions.last) {
		v = seriesVersions.last.Add(time.Millisecond)
	}
	seriesVersions.last = v
	return v.Format("2006-01-02 15:04:05.000")
}

var seriesVersions struct {
	sync.Mutex
	last time.Time
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
	// Each table gets the hours' new version, then loses their older ones: a read takes each hour's newest version,
	// so it sees the old rows or the new, never neither or both, and an insert that fails leaves the old in place.
	// An hour whose receptions were all deleted has no new rows, and reads the old until they go.
	bctx, cancel := chBinning(ctx)
	defer cancel()
	built := seriesVersion()
	if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_hours (hour, station, source, receptions, first, built)
		SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, count(), countIf(accepted), toDateTime64(?, 3, 'UTC')
		FROM `+c.db+`.receptions WHERE `+where+` GROUP BY hour, station, source`, built, from, to, hours); err != nil {
		return fmt.Errorf("station_hours: %w", err)
	}
	del := chDeleteSync(bctx)
	if err := c.conn.Exec(del, "DELETE FROM "+c.db+".station_hours WHERE hour IN ? AND built < toDateTime64(?, 3, 'UTC')", hours, built); err != nil {
		return fmt.Errorf("station_hours: %w", err)
	}
	if to.Before(now.AddDate(0, 0, -chStationVesselDays)) {
		return nil // past what station_vessels keeps
	}
	if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_vessels (hour, station, source, mmsi, receptions, last_ts, built)
		SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, mmsi, count(), max(ts), toDateTime64(?, 3, 'UTC')
		FROM `+c.db+`.receptions WHERE `+where+` AND NOT stale GROUP BY hour, station, source, mmsi`, built, from, to, hours); err != nil {
		return fmt.Errorf("station_vessels: %w", err)
	}
	if err := c.conn.Exec(del, "DELETE FROM "+c.db+".station_vessels WHERE hour IN ? AND built < toDateTime64(?, 3, 'UTC')", hours, built); err != nil {
		return fmt.Errorf("station_vessels: %w", err)
	}
	return nil
}

// chNewest keeps, from station_vessels rows, each hour's newest version, for hours from the next argument on.
const chNewest = `(hour, built) IN (SELECT hour, max(built) FROM {db}.station_vessels WHERE hour >= toStartOfHour(?) GROUP BY hour)`

// chOwnIn leaves out, from station_vessels rows, each vessel a station sent as its own ship within a window starting
// at the query's first two arguments: a boat hearing itself is not reception, and every row of it goes.
const chOwnIn = `(station, mmsi) NOT IN (SELECT station, mmsi FROM {db}.station_own WHERE hour >= toStartOfHour(?) - INTERVAL 1 HOUR AND last_ts > ?)`

func (c *chConn) stationCounts(ctx context.Context, now time.Time) (map[string]stationCount, error) {
	out := map[string]stationCount{}
	cur := now.UTC().Truncate(time.Hour)
	start := cur.Add(-(stationWindow - 1) * time.Hour)
	rows, err := c.conn.Query(ctx, `SELECT station, min(hour), sum(receptions), sum(first), uniqExactIf(hour, hour >= ? AND hour < ?), max(hour = ?)
		FROM `+c.db+`.station_hours WHERE (hour, built) IN (SELECT hour, max(built) FROM `+c.db+`.station_hours GROUP BY hour)
		GROUP BY station`, start, cur, cur)
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
				WHERE hour >= toStartOfHour(?) AND last_ts > ? AND `+chNewest+` AND `+chOwnIn+` GROUP BY station, mmsi))
		GROUP BY station`, "{db}", c.db), live, day, day, day, day, day)
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
				SELECT DISTINCT source, mmsi FROM `+strings.ReplaceAll(`{db}.station_vessels WHERE hour >= toStartOfHour(?) AND last_ts > ? AND `+chNewest, "{db}", c.db)+`))
		GROUP BY source`, since, since, since)
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

// runStationSeries waits for ClickHouse, seeds own-vessel candidates from station_own, then backfills the series
// once in the background and rebuilds its marked hours every stationSeriesEvery.
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
	go func() {
		for {
			err := s.backfillStationSeries(ctx, time.Now())
			if err == nil {
				return
			}
			log.Printf("station series: backfill: %v", err)
			time.Sleep(stationSeriesEvery)
		}
	}()
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
	at      time.Time // the last read, failed or not
	ok      time.Time // the last read that succeeded
	counts  map[string]stationCount
	sources map[string][2]int
}

// stationRollupsStale is how long the last figures stand while reads fail, after which there are none rather than
// figures frozen through an outage.
const stationRollupsStale = 10 * time.Minute

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
		return r.fresh(now)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	counts, err1 := s.stationCounts(ctx, now)
	sources, err2 := s.sourceCounts(ctx, now)
	if err1 != nil || err2 != nil {
		log.Printf("station series: %v %v", err1, err2)
		r.at = now // the last figures stand for the minute, rather than every request waiting on a failing read
		return r.fresh(now)
	}
	r.at, r.ok, r.counts, r.sources = now, now, counts, sources
	return counts, sources
}

// fresh is the last figures while they are within stationRollupsStale of their read; the caller holds mu.
func (r *stationRollups) fresh(now time.Time) (map[string]stationCount, map[string][2]int) {
	if now.Sub(r.ok) > stationRollupsStale {
		return nil, nil
	}
	return r.counts, r.sources
}
