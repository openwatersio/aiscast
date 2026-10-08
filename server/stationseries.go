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
	"sync/atomic"
	"time"
)

// stationSeriesSettle is how old a marker must be to be rebuilt: a younger one may belong to an insert whose rows
// are not all visible yet. Longer than chInsertTimeout, so no live batch still in flight has a marker that old. A
// long INSERT ... SELECT, as replay and archive loads run, marks each block as it lands, so an hour rebuilt while it
// runs is marked again by its later blocks and rebuilt once they settle.
var stationSeriesSettle = time.Minute

const (
	stationSeriesEvery  = 5 * time.Minute
	stationWindow       = 7 * 24 // hours of uptime
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
	chStationDirtyMV = `CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.station_dirty_mv TO {db}.station_dirty AS SELECT DISTINCT toStartOfHour(toDateTime(ts, 'UTC')) AS hour FROM {db}.receptions WHERE ` + chUsable
	chStationBuilt   = `CREATE TABLE IF NOT EXISTS {db}.station_built (hour DateTime('UTC'), marker DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(marker) ORDER BY hour TTL toDateTime(marker) + INTERVAL 15 DAY DELETE`
	chSeriesLedger   = `CREATE TABLE IF NOT EXISTS {db}.station_series_backfilled (day Date) ENGINE = ReplacingMergeTree ORDER BY day`
	// chStationVersions records each hour's versions once both tables hold them whole: an INSERT ... SELECT commits
	// its parts one by one, so reads take the newest version recorded here, never one still being written or one
	// whose insert failed partway.
	chStationVersions = `CREATE TABLE IF NOT EXISTS {db}.station_versions (hour DateTime('UTC'), built DateTime64(3, 'UTC')) ENGINE = MergeTree ORDER BY (hour, built)`
)

// chNewestFrom keeps rows of each hour's newest recorded version, for hours from the next argument on.
const chNewestFrom = `(hour, built) IN (SELECT hour, max(built) FROM {db}.station_versions WHERE hour >= toStartOfHour(?) GROUP BY hour)`

// stationSeries is the job and the reads over station_hours and station_vessels.
type stationSeries interface {
	// rebuildStationSeries rebuilds the marked hours.
	rebuildStationSeries(ctx context.Context, now time.Time) error
	// backfillStationSeries bins the days receptions held before the view, newest first, until every one is done.
	backfillStationSeries(ctx context.Context, now time.Time) error
	// stationCounts is each station's hours and vessels in the window.
	stationCounts(ctx context.Context, now time.Time) (map[string]stationCount, error)
	// stationTotals is each station's first hour and receptions ever, a read of its whole history.
	stationTotals(ctx context.Context) (map[string]stationCount, error)
	// sourceCounts is, per source kind, the vessels its stations heard within vesselTTL and how many no other kind did.
	sourceCounts(ctx context.Context, now time.Time) (map[string][2]int, error)
	// ownCandidates is each station's own-ship MMSIs since a time, with the unix seconds of the last.
	ownCandidates(ctx context.Context, since time.Time) (map[string]map[uint32]int64, error)
	// stationPoints is the centers of each volunteer station's finest coverage cells since a day.
	stationPoints(ctx context.Context, since time.Time) (map[string][][2]float64, error)
}

// stationCount is one station's figures from the series.
type stationCount struct {
	first              time.Time // its first hour: in the window from stationCounts, ever from stationTotals
	receptions, firsts uint64    // ever, from stationTotals
	totaled            bool      // whether the totals ever have the station, which a station new since their read does not
	past               int       // hours of the window before the current one with a reception
	now                bool      // whether the current hour has one
	uptime             float64   // share of the hours since the later of 7 days ago and its first hour with a reception
	live, day, unique  int       // vessels within vesselTTL and stationVesselTTL, and of the latter, heard by no other station
}

// withTotals is s with a station's totals ever and its uptime: every hour since the later of the window's start and
// the station's first counts, and the current one once it has a reception. Without its totals the station's first
// hour is unknown, so it has no uptime: one back after a week away would otherwise read as up all along.
func (s stationCount) withTotals(t stationCount, now time.Time) stationCount {
	s.totaled = !t.first.IsZero()
	if !s.totaled {
		return s
	}
	if t.first.Before(s.first) || s.first.IsZero() {
		s.first = t.first
	}
	s.receptions, s.firsts = t.receptions, t.firsts
	cur := now.UTC().Truncate(time.Hour)
	from := cur.Add(-(stationWindow - 1) * time.Hour)
	if s.first.After(from) {
		from = s.first.UTC()
	}
	now1 := 0
	if s.now {
		now1 = 1
	}
	if hours := int(cur.Sub(from)/time.Hour) + now1; hours > 0 {
		s.uptime = float64(s.past+now1) / float64(hours)
	}
	return s
}

// mergeCounts adds each station's totals ever to its window's figures, and its uptime; a station heard only before
// the window has its totals and an uptime of 0.
func mergeCounts(counts, totals map[string]stationCount, now time.Time) map[string]stationCount {
	out := make(map[string]stationCount, max(len(counts), len(totals)))
	for id := range totals {
		out[id] = stationCount{}
	}
	maps.Copy(out, counts)
	for id, c := range out {
		out[id] = c.withTotals(totals[id], now)
	}
	return out
}

func (c *chConn) stationTotals(ctx context.Context) (map[string]stationCount, error) {
	rows, err := c.conn.Query(ctx, `SELECT station, min(hour), sum(receptions), sum(first) FROM `+c.db+`.station_hours
		WHERE (hour, built) IN (SELECT hour, max(built) FROM `+c.db+`.station_versions GROUP BY hour) GROUP BY station`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]stationCount{}
	for rows.Next() {
		var id string
		var s stationCount
		if err := rows.Scan(&id, &s.first, &s.receptions, &s.firsts); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
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

// stationSeriesRun keeps one run of the job at a time in a process.
var stationSeriesRun sync.Mutex

// seriesDays keeps one binning of a day at a time in a process, the job's or the backfill's: a version is taken
// before its binning reads, so two binnings of one hour at once could give the later read the older version. The
// job waits for the backfill only while both are on one day.
var seriesDays struct {
	sync.Mutex
	m map[time.Time]*sync.Mutex
}

// lockDay locks one UTC day's binning and returns its unlock.
func lockDay(day time.Time) func() {
	seriesDays.Lock()
	if seriesDays.m == nil {
		seriesDays.m = map[time.Time]*sync.Mutex{}
	}
	l := seriesDays.m[day]
	if l == nil {
		l = &sync.Mutex{}
		seriesDays.m[day] = l
	}
	seriesDays.Unlock()
	l.Lock()
	return l.Unlock
}

func (c *chConn) rebuildStationSeries(ctx context.Context, now time.Time) error {
	stationSeriesRun.Lock()
	defer stationSeriesRun.Unlock()
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
	// Today and yesterday, where live receptions land, rebuild in this run, newest first. Older marked days, from
	// late copies, replays, and reloads, go to one worker of their own, so a slow one never holds today back. A day
	// that fails is logged and left for the next run.
	days := slices.SortedFunc(maps.Keys(byDay), func(a, b time.Time) int { return b.Compare(a) })
	recent := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	var older []time.Time
	for _, day := range days {
		if day.Before(recent) {
			older = append(older, day)
			continue
		}
		c.rebuildMarkedDay(ctx, day, byDay[day], now)
	}
	if len(older) > 0 && olderSeries.CompareAndSwap(false, true) {
		go func() {
			defer olderSeries.Store(false)
			for _, day := range older {
				c.rebuildMarkedDay(ctx, day, byDay[day], now)
			}
		}()
	}
	return nil
}

// mark is a marked hour and the newest marker a rebuild of it takes in.
type mark struct {
	hour   time.Time
	marker time.Time
}

// olderSeries is set while the worker for older marked days runs, so there is one at a time.
var olderSeries atomic.Bool

// rebuildMarkedDay rebuilds one day's marked hours, a run of consecutive hours per binning so each reads only the
// receptions of its own span, and records the markers it took in. A run that fails is logged and left marked.
func (c *chConn) rebuildMarkedDay(ctx context.Context, day time.Time, marks []mark, now time.Time) {
	slices.SortFunc(marks, func(a, b mark) int { return a.hour.Compare(b.hour) })
	unlock := lockDay(day)
	defer unlock()
	for start := 0; start < len(marks); {
		end := start + 1
		for end < len(marks) && marks[end].hour.Equal(marks[end-1].hour.Add(time.Hour)) {
			end++
		}
		run := marks[start:end]
		hours := make([]time.Time, len(run))
		for i, m := range run {
			hours[i] = m.hour
		}
		err := c.binHours(ctx, hours, now)
		if err == nil {
			err = c.recordBuilt(ctx, run)
		}
		if err != nil {
			log.Printf("station series: %s: %v", run[0].hour.Format("2006-01-02 15:04"), err) // left for the next run
		}
		start = end
	}
}

// recordBuilt records the markers a rebuild took in.
func (c *chConn) recordBuilt(ctx context.Context, marks []mark) error {
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
	return batch.Send()
}

// backfillStationSeries bins whole days the series has not, newest first from today: the days receptions held
// before the view marked them. Its ledger records each day as it finishes, and a day that fails is left for the
// next pass. It runs beside the marked hours' rebuild, which versions make safe.
func (c *chConn) backfillStationSeries(ctx context.Context, now time.Time) error {
	done, err := chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(day, 'UTC') FROM "+c.db+".station_series_backfilled FINAL")
	if err != nil {
		return err
	}
	// The months receptions holds, from its partitions' names, so a stray row stamped years back costs a month of
	// days, not every day between, and no rows are read to find them.
	parts, err := chColumn[string](ctx, c.conn, "SELECT DISTINCT partition FROM system.parts WHERE database = ? AND table = 'receptions' AND active", c.db)
	if err != nil {
		return err
	}
	months := map[string]bool{}
	oldest := now.UTC()
	for _, p := range parts {
		m, err := time.Parse("200601", p)
		if err != nil {
			return fmt.Errorf("receptions partition %q is not a month", p)
		}
		months[p] = true
		if m.Before(oldest) {
			oldest = m
		}
	}
	failed := 0
	for day := now.UTC().Truncate(24 * time.Hour); !day.Before(oldest); day = day.AddDate(0, 0, -1) {
		if !months[day.Format("200601")] || slices.ContainsFunc(done, day.Equal) {
			continue
		}
		hours := make([]time.Time, 24)
		for i := range hours {
			hours[i] = day.Add(time.Duration(i) * time.Hour)
		}
		unlock := lockDay(day)
		err := c.binHours(ctx, hours, now)
		unlock()
		if err != nil {
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
// two rebuilds of one hour in a second would otherwise share a version and both be read. Each is later than the last
// and than floor, the newest version the hours hold, so a clock set back since never makes a rebuild the older.
func seriesVersion(floor time.Time) string {
	seriesVersions.Lock()
	defer seriesVersions.Unlock()
	v := time.Now().UTC().Truncate(time.Millisecond)
	last := seriesVersions.last
	if floor.After(last) {
		last = floor.UTC() // formatted below in UTC, as ClickHouse reads it, whatever zone the floor came in
	}
	if !v.After(last) {
		v = last.Add(time.Millisecond)
	}
	seriesVersions.last = v
	return v.Format("2006-01-02 15:04:05.000")
}

var seriesVersions struct {
	sync.Mutex
	last time.Time
}

// chValidMMSI is validMMSI in SQL: live ingest keeps other MMSIs out of receptions, but rows written before it
// did can hold them, and a backfill must not count them as vessels heard.
var chValidMMSI = func() string {
	var conds []string
	for _, r := range invalidMMSIRanges {
		conds = append(conds, fmt.Sprintf("mmsi BETWEEN %d AND %d", r[0], r[1]))
	}
	defaults := slices.Sorted(maps.Keys(defaultMMSIs))
	conds = append(conds, "mmsi IN ("+strings.Trim(strings.Join(strings.Fields(fmt.Sprint(defaults)), ", "), "[]")+")")
	return "NOT (" + strings.Join(conds, " OR ") + ")"
}()

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
	where := "ts >= ? AND ts < ? AND toStartOfHour(toDateTime(ts, 'UTC')) IN ? AND " + chUsable + " AND " + chValidMMSI
	legacy, err := c.legacy(ctx)
	if err != nil {
		return err
	}
	if legacy {
		where += " AND (tx = 0 OR toDate(ts) NOT IN (SELECT day FROM " + c.db + ".receptions_converted))"
	}
	// Both tables get the hours' new version, which is recorded once both inserts are whole, and then the older
	// versions go. Reads take each hour's newest recorded version, so they see the old rows or the new, never part
	// of either or both, and an insert that fails leaves the old in place. An hour whose receptions were all deleted
	// records a version with no rows, and reads as empty.
	bctx, cancel := chBinning(ctx)
	defer cancel()
	floor, err := chColumn[time.Time](ctx, c.conn, "SELECT greatest((SELECT max(built) FROM "+c.db+".station_hours WHERE hour IN ?), "+
		"(SELECT max(built) FROM "+c.db+".station_vessels WHERE hour IN ?), (SELECT max(built) FROM "+c.db+".station_versions WHERE hour IN ?))", hours, hours, hours)
	if err != nil {
		return err
	}
	built := seriesVersion(floor[0])
	if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_hours (hour, station, source, receptions, first, built)
		SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, count(), countIf(accepted), toDateTime64(?, 3, 'UTC')
		FROM `+c.db+`.receptions WHERE `+where+` GROUP BY hour, station, source`, built, from, to, hours); err != nil {
		return fmt.Errorf("station_hours: %w", err)
	}
	if !to.Before(now.AddDate(0, 0, -chStationVesselDays)) { // past what station_vessels keeps, it has none to write
		if err := c.conn.Exec(bctx, `INSERT INTO `+c.db+`.station_vessels (hour, station, source, mmsi, receptions, last_ts, built)
			SELECT toStartOfHour(toDateTime(ts, 'UTC')) AS hour, `+chStationKey+` AS station, source, mmsi, count(), max(ts), toDateTime64(?, 3, 'UTC')
			FROM `+c.db+`.receptions WHERE `+where+` AND NOT stale GROUP BY hour, station, source, mmsi`, built, from, to, hours); err != nil {
			return fmt.Errorf("station_vessels: %w", err)
		}
	}
	if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_versions (hour, built) SELECT arrayJoin(?), toDateTime64(?, 3, 'UTC')", hours, built); err != nil {
		return fmt.Errorf("station_versions: %w", err)
	}
	del := chDeleteSync(bctx)
	for _, table := range []string{"station_hours", "station_vessels", "station_versions"} {
		if err := c.conn.Exec(del, "DELETE FROM "+c.db+"."+table+" WHERE hour IN ? AND built < toDateTime64(?, 3, 'UTC')", hours, built); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
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
	rows, err := c.conn.Query(ctx, `SELECT station, min(hour), uniqExactIf(hour, hour < ?), max(hour = ?) FROM `+c.db+`.station_hours
		WHERE hour >= ? AND `+strings.ReplaceAll(chNewestFrom, "{db}", c.db)+`
		GROUP BY station`, cur, cur, start, start)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s stationCount
		var past uint64
		var heard uint8
		if err := rows.Scan(&id, &s.first, &past, &heard); err != nil {
			rows.Close()
			return nil, err
		}
		s.past, s.now = int(past), heard == 1
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
				WHERE hour >= toStartOfHour(?) AND last_ts > ? AND `+chNewestFrom+` AND `+chOwnIn+` GROUP BY station, mmsi))
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
				SELECT DISTINCT source, mmsi FROM `+strings.ReplaceAll(`{db}.station_vessels WHERE hour >= toStartOfHour(?) AND last_ts > ? AND `+chNewestFrom, "{db}", c.db)+`))
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
	rows, err := c.conn.Query(ctx, "SELECT station, mmsi, toUnixTimestamp(max(last_ts)) FROM "+c.db+".station_own WHERE hour >= toStartOfHour(?) AND last_ts > ? AND "+chValidMMSI+" GROUP BY station, mmsi", since, since)
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
	go func() { // own-vessel decisions wait for these, and nothing else does
		for {
			cands, err := s.ownCandidates(ctx, time.Now().Add(-ownSettle-time.Hour))
			if err == nil {
				p.stations.restoreOwn(cands)
				return
			}
			log.Printf("station series: own candidates: %v", err)
			time.Sleep(time.Minute)
		}
	}()
	go func() {
		for {
			p.refreshRollups(s, time.Now())
			time.Sleep(time.Minute)
		}
	}()
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

// stationRollups is the series' figures, read once a minute in the background for every caller.
type stationRollups struct {
	mu       sync.Mutex
	ok       time.Time // the last read that succeeded
	counts   map[string]stationCount
	sources  map[string][2]int
	totals   map[string]stationCount // read at most hourly, since it reads all of history
	totalsAt time.Time               // when the totals were last tried
	totalsOk time.Time               // when they were last read
}

// stationTotalsStale is how old the totals may be and still stand: past it, figures that need them, uptime and the
// counts ever, are left out rather than served frozen.
const stationTotalsStale = 3 * time.Hour

// stationRollupsStale is how long the last figures stand while reads fail, after which there are none rather than
// figures frozen through an outage.
const stationRollupsStale = 10 * time.Minute

// rollups returns the station and source figures runStationSeries last read, nil without ClickHouse or once
// reads have failed for stationRollupsStale. It never waits on ClickHouse.
func (p *Pipeline) rollups(now time.Time) (map[string]stationCount, map[string][2]int) {
	r := &p.series
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fresh(now)
}

// refreshRollups reads the series' figures for rollups: the window's each minute, and the totals ever, a read of
// all of history, each hour, or a minute after one fails. A failed read keeps the last figures.
func (p *Pipeline) refreshRollups(s stationSeries, now time.Time) {
	r := &p.series
	r.mu.Lock()
	totals, due := r.totals, now.Sub(r.totalsAt) >= time.Hour
	r.mu.Unlock()
	read := func(timeout time.Duration, f func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return f(ctx)
	}
	totalsAt := now
	if due {
		var fresh map[string]stationCount
		if err := read(5*time.Minute, func(ctx context.Context) (err error) { fresh, err = s.stationTotals(ctx); return }); err != nil {
			log.Printf("station series: totals: %v", err)
			totalsAt = now.Add(time.Minute - time.Hour) // tried again in a minute
		} else {
			totals = fresh
		}
	}
	var counts map[string]stationCount
	var sources map[string][2]int
	err1 := read(20*time.Second, func(ctx context.Context) (err error) { counts, err = s.stationCounts(ctx, now); return })
	err2 := read(20*time.Second, func(ctx context.Context) (err error) { sources, err = s.sourceCounts(ctx, now); return })
	r.mu.Lock()
	defer r.mu.Unlock()
	if due {
		r.totals, r.totalsAt = totals, totalsAt
		if totalsAt.Equal(now) {
			r.totalsOk = now
		}
	}
	if now.Sub(r.totalsOk) > stationTotalsStale {
		totals = nil
	}
	if err1 != nil || err2 != nil {
		log.Printf("station series: %v %v", err1, err2)
		return
	}
	r.ok, r.counts, r.sources = now, mergeCounts(counts, totals, now), sources
}

// fresh is the last figures while they are within stationRollupsStale of their read; the caller holds mu.
func (r *stationRollups) fresh(now time.Time) (map[string]stationCount, map[string][2]int) {
	if now.Sub(r.ok) > stationRollupsStale {
		return nil, nil
	}
	return r.counts, r.sources
}
