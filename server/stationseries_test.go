package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// fakeSeries answers the station series' reads from fixed figures.
type fakeSeries struct {
	counts  map[string]stationCount
	totals  map[string]stationCount
	sources map[string][2]int
	own     map[string]map[uint32]int64
	points  map[string][][2]float64
	reads   int
	err     error
	// totals' reads, and the error the next one returns
	totalsReads int
	totalsErr   error
}

func (f *fakeSeries) rebuildStationSeries(context.Context, time.Time) error  { return nil }
func (f *fakeSeries) backfillStationSeries(context.Context, time.Time) error { return nil }
func (f *fakeSeries) stationCounts(context.Context, time.Time) (map[string]stationCount, error) {
	f.reads++
	return f.counts, f.err
}
func (f *fakeSeries) stationTotals(context.Context) (map[string]stationCount, error) {
	f.totalsReads++
	if err := f.totalsErr; err != nil {
		f.totalsErr = nil
		return nil, err
	}
	return f.totals, nil
}
func (f *fakeSeries) sourceCounts(context.Context, time.Time) (map[string][2]int, error) {
	return f.sources, nil
}
func (f *fakeSeries) ownCandidates(context.Context, time.Time) (map[string]map[uint32]int64, error) {
	return f.own, nil
}
func (f *fakeSeries) stationPoints(context.Context, time.Time) (map[string][][2]float64, error) {
	return f.points, nil
}

// TestStationSeriesFromClickHouse runs against a real server named by CLICKHOUSE_TEST_URL, in a database of its own.
func TestStationSeriesFromClickHouse(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })
	was := stationSeriesSettle
	stationSeriesSettle = 0
	t.Cleanup(func() { stationSeriesSettle = was })

	now := time.Now().UTC()
	cur := now.Truncate(time.Hour)
	recent := cur.Add(now.Sub(cur) / 2) // in the current hour, within the last 30 minutes
	at := func(station string, mmsi uint32, ts time.Time) trackPoint {
		if mmsi < 1000 {
			mmsi += 257000000 // a Norwegian vessel, numbered for the test
		}
		source, _, _ := strings.Cut(station, ":")
		return trackPoint{mmsi: mmsi, ts: ts, lat6: int32(59.9 * 600000), lon6: int32(10.7 * 600000), sog10: 1023, cog10: 3600,
			heading: 511, navStatus: 15, source: source, station: station, txAt: ts, recv: ts}
	}
	shared := at("station:s2", 2, recent)
	shared.dup = true // s1 delivered it first; s2 heard it too
	echo := at("aishub", 1, cur.Add(-2*time.Hour+10*time.Minute))
	echo.stale = true // AISHub's late copy of s1's report, which must not take its uniqueness
	wild := at("station:s2", 5, recent)
	wild.implausible = true
	old := at("station:s3", 4, now.Add(-30*time.Hour))
	stream := at("station:old/n2k", 6, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)) // a TAG stream before the fold
	pts := []trackPoint{at("station:s1", 1, cur.Add(-2*time.Hour+10*time.Minute)), at("station:s1", 2, recent), at("station:s1", 9, recent),
		shared, at("station:s2", 3, recent), echo, wild, old, stream,
		at("station:s1", 123456789, recent)} // a default MMSI from before ingest kept them out, which counts as no vessel
	// The receptions were written before the series existed, as on a database at step 17: its tables and view
	// come with the upgrade, and the backfill bins what came before.
	if err := conn.exec(ctx, "DROP VIEW {db}.station_dirty_mv", "DROP TABLE {db}.station_dirty", "DROP TABLE {db}.station_hours",
		"DROP TABLE {db}.station_vessels", "DROP TABLE {db}.station_built", "DROP TABLE {db}.station_series_backfilled", "DROP TABLE {db}.station_versions",
		"ALTER TABLE {db}.schema_migrations DELETE WHERE version > 17 SETTINGS mutations_sync = 2"); err != nil {
		t.Fatal(err)
	}
	if err := conn.insert(ctx, "series", pts); err != nil {
		t.Fatal(err)
	}
	conn.conn.Close()
	if conn, err = openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db); err != nil {
		t.Fatal(err)
	}
	// s1 is on vessel 9.
	if err := conn.insertOwn(ctx, map[ownKey]time.Time{{"station:s1", recent.Unix() / 3600, 257000009}: recent, {"station:s1", recent.Unix() / 3600, 0}: recent}); err != nil {
		t.Fatal(err)
	}
	// A copy stamped decades back, as a bad clock gives, costs its own month of days, not every day since.
	ancient := at("station:s9", 8, time.Date(2001, 1, 15, 0, 0, 0, 0, time.UTC))
	ancient.clockBad = true
	if err := conn.insert(ctx, "ancient", []trackPoint{ancient}); err != nil {
		t.Fatal(err)
	}
	// and marks nothing, since no unusable copy changes the counts
	if marks, err := chColumn[uint64](ctx, conn.conn, "SELECT count() FROM "+db+".station_dirty WHERE toYear(hour) = 2001"); err != nil || marks[0] != 0 {
		t.Errorf("a bad-clock copy marked %v hours, %v", marks, err)
	}
	if err := conn.backfillStationSeries(ctx, now); err != nil {
		t.Fatal(err)
	}
	if days, err := chColumn[uint64](ctx, conn.conn, "SELECT count() FROM "+db+".station_series_backfilled FINAL"); err != nil || days[0] > 100 {
		t.Errorf("the backfill binned %v days for receptions in a few months, %v", days, err)
	}
	// An older version of an hour, as a rebuild leaves for the moment between its insert and its delete, is not read,
	// nor a newer one never recorded, as an insert that failed partway leaves.
	if err := conn.exec(ctx, "INSERT INTO {db}.station_hours (hour, station, source, receptions, first, built) VALUES ('"+
		cur.Format("2006-01-02 15:04:05")+"', 'station:s1', 'station', 1000, 1000, '2000-01-01 00:00:00')",
		"INSERT INTO {db}.station_vessels (hour, station, source, mmsi, receptions, last_ts, built) VALUES ('"+
			cur.Format("2006-01-02 15:04:05")+"', 'station:s1', 'station', 777, 1, now64(3), '2000-01-01 00:00:00')",
		"INSERT INTO {db}.station_hours (hour, station, source, receptions, first, built) VALUES ('"+
			cur.Format("2006-01-02 15:04:05")+"', 'station:s1', 'station', 5000, 5000, '2098-01-01 00:00:00')"); err != nil {
		t.Fatal(err)
	}
	counts, err := seriesCounts(t, conn, now)
	if err != nil {
		t.Fatal(err)
	}
	// A day that fails holds up neither the other marked days nor the backfill: with today failing, yesterday's hour
	// still rebuilds, and today's waits for the next run.
	binHoursFails = func(day time.Time) error {
		if day.Equal(cur.Truncate(24 * time.Hour)) {
			return fmt.Errorf("today fails")
		}
		return nil
	}
	if err := conn.markDay(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := conn.markDay(ctx, cur.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	binHoursFails = nil
	built, err := chColumn[uint64](ctx, conn.conn, "SELECT uniqExact(hour) FROM "+db+".station_built FINAL WHERE marker > now64(3) - INTERVAL 1 MINUTE AND toDate(hour) = toDate(?)", cur.AddDate(0, 0, -1))
	if err != nil || built[0] != 24 {
		t.Errorf("hours rebuilt with today failing: %v %v; want yesterday's 24", built, err)
	}
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	if s := counts["station:s1"]; s.day != 2 || s.live != 1 || s.unique != 1 || math.Abs(s.uptime-2.0/3) > 1e-9 || !s.first.Equal(cur.Add(-2*time.Hour)) {
		t.Errorf("s1: %+v; want 2 vessels without its own, 1 live, 1 unique despite the echo, and 2 of 3 hours", s)
	}
	if s := counts["station:s2"]; s.day != 2 || s.unique != 1 || s.receptions != 2 || s.firsts != 1 {
		t.Errorf("s2: %+v; want the shared and its own vessel, the implausible copy left out, and 1 of 2 receptions first", s)
	}
	if s := counts["station:s3"]; s.day != 0 || s.receptions != 1 {
		t.Errorf("s3, heard 30 hours ago: %+v", s)
	}
	if _, ok := counts["station:old"]; !ok {
		t.Errorf("the stream before the fold is not under its receiver: %v", counts)
	}
	sources, err := conn.sourceCounts(ctx, now)
	if err != nil || sources["station"] != [2]int{3, 3} || len(sources) != 1 {
		t.Errorf("sources %v, %v; want the volunteers' 3 live vessels, the stale echo left out", sources, err)
	}

	// Rebuilding again changes nothing; a delete that marks its hours takes the vessel out.
	if err := conn.markDay(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	if again, _ := seriesCounts(t, conn, now); again["station:s2"] != counts["station:s2"] || again["station:s1"] != counts["station:s1"] {
		t.Errorf("a second rebuild changed the counts: %+v, was %+v", again, counts)
	}
	where := "station = 'station:s2' AND mmsi = 257000003"
	dirty, err := conn.dirtyHours(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	// A rebuild before the delete finds nothing marked, so it cannot record the hour as built with the rows still there.
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	if err := conn.conn.Exec(chDeleteSync(ctx), "DELETE FROM "+db+".receptions WHERE "+where); err != nil {
		t.Fatal(err)
	}
	if err := conn.markHours(ctx, dirty); err != nil {
		t.Fatal(err)
	}
	// A late copy for an hour never rebuilt, on a day the backfill has done: under a profile that reads a missing join
	// row as NULL, its hour still rebuilds.
	if err := conn.insert(ctx, "late", []trackPoint{at("station:s4", 7, cur.Add(-72*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	nulls := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"join_use_nulls": 1}))
	if err := rebuildAll(nulls, conn, now); err != nil {
		t.Fatal(err)
	}
	if counts, _ := seriesCounts(t, conn, now); counts["station:s4"].receptions != 1 {
		t.Errorf("a late copy's hour was not rebuilt under join_use_nulls: %+v", counts["station:s4"])
	}
	if counts, _ := seriesCounts(t, conn, now); counts["station:s2"].day != 1 || counts["station:s2"].unique != 0 {
		t.Errorf("after deleting s2's own vessel: %+v", counts["station:s2"])
	}

	// A version from a clock that has since been set back, as after a restart: the next rebuild still supersedes it.
	h := old.ts.Truncate(time.Hour)
	if err := conn.exec(ctx, "INSERT INTO {db}.station_hours (hour, station, source, receptions, first, built) VALUES ('"+
		h.Format("2006-01-02 15:04:05")+"', 'station:s3', 'station', 999, 999, '2099-01-01 00:00:00')",
		"INSERT INTO {db}.station_versions VALUES ('"+h.Format("2006-01-02 15:04:05")+"', '2099-01-01 00:00:00')"); err != nil {
		t.Fatal(err)
	}
	if err := conn.markHours(ctx, []time.Time{h}); err != nil {
		t.Fatal(err)
	}
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	if counts, _ := seriesCounts(t, conn, now); counts["station:s3"].receptions != 1 {
		t.Errorf("a rebuild after a version from the future: s3 %+v", counts["station:s3"])
	}

	// An older day that is slow to bin, as a late copy's can be, does not hold today back: the run returns with
	// today rebuilt while the older day's worker still waits.
	release := make(chan struct{})
	binHoursFails = func(day time.Time) error {
		if day.Before(cur.AddDate(0, 0, -1).Truncate(24 * time.Hour)) {
			<-release
		}
		return nil
	}
	if err := conn.markDay(ctx, cur.AddDate(0, 0, -4)); err != nil {
		t.Fatal(err)
	}
	if err := conn.markDay(ctx, cur); err != nil {
		t.Fatal(err)
	}
	ran := make(chan error, 1)
	go func() { ran <- conn.rebuildStationSeries(ctx, now) }()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("today waited for an older day")
	}
	close(release)
	for olderSeries.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	binHoursFails = nil

	// A marker younger than the settle time waits for the next run.
	stationSeriesSettle = time.Hour
	fresh := cur.AddDate(0, 0, -5)
	if err := conn.markDay(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := rebuildAll(ctx, conn, now); err != nil {
		t.Fatal(err)
	}
	stationSeriesSettle = 0
	if built, err := chColumn[uint64](ctx, conn.conn, "SELECT count() FROM "+db+".station_built WHERE toDate(hour) = toDate(?)", fresh); err != nil || built[0] != 0 {
		t.Errorf("a day marked a moment ago was rebuilt: %v %v", built, err)
	}
	// rebuild-positions-1m's day marks the day for the series.
	if err := conn.rebuildDay(ctx, cur.AddDate(0, 0, -6)); err != nil {
		t.Fatal(err)
	}
	if hours, err := chColumn[uint64](ctx, conn.conn, "SELECT uniqExact(hour) FROM "+db+".station_dirty WHERE toDate(hour) = toDate(?)", cur.AddDate(0, 0, -6)); err != nil || hours[0] != 24 {
		t.Errorf("rebuildDay marked %v hours, %v; want 24", hours, err)
	}

	own, err := conn.ownCandidates(ctx, now.Add(-2*time.Hour))
	if err != nil || own["station:s1"][257000009] != recent.Unix() || len(own["station:s1"]) != 1 {
		t.Errorf("own candidates %v, %v", own, err)
	}
	if err := conn.coverageBackfill(ctx, cur.AddDate(0, 0, -2)); err != nil {
		t.Fatal(err)
	}
	points, err := conn.stationPoints(ctx, cur.AddDate(0, 0, -2))
	if err != nil || len(points["station:s1"]) != 1 || math.Abs(points["station:s1"][0][0]-59.9) > 0.05 || math.Abs(points["station:s1"][0][1]-10.7) > 0.05 || points["aishub"] != nil {
		t.Errorf("label points %v, %v; want s1's one cell near Oslo and no feed's", points, err)
	}
}

// A failed read keeps the last figures, until stationRollupsStale has passed with none succeeding; a failed read of
// the totals is tried again a minute later rather than an hour.
func TestStationRollupsSurviveFailedReads(t *testing.T) {
	p := testPipeline(t)
	f := &fakeSeries{counts: map[string]stationCount{"s1": {day: 3}}, totalsErr: fmt.Errorf("timed out")}
	now := time.Now()
	p.refreshRollups(f, now)
	p.refreshRollups(f, now.Add(30*time.Second))
	p.refreshRollups(f, now.Add(61*time.Second))
	if f.totalsReads != 2 {
		t.Errorf("%d reads of the totals, want the failed one and another a minute later", f.totalsReads)
	}
	f.err = fmt.Errorf("clickhouse is down")
	p.refreshRollups(f, now.Add(2*time.Minute))
	if counts, _ := p.rollups(now.Add(2 * time.Minute)); counts["s1"].day != 3 {
		t.Errorf("figures %v after a failed read, want the last", counts)
	}
	if counts, _ := p.rollups(now.Add(61*time.Second + stationRollupsStale + time.Second)); counts != nil {
		t.Errorf("figures %v kept through %v of failed reads", counts, stationRollupsStale)
	}
	// Totals that keep failing past stationTotalsStale drop out, so uptime and counts ever are not served frozen.
	q := testPipeline(t)
	g := &fakeSeries{counts: map[string]stationCount{"s1": {first: now.Truncate(time.Hour), now: true}},
		totals: map[string]stationCount{"s1": {first: now.Add(-48 * time.Hour).Truncate(time.Hour), receptions: 9, firsts: 9}}}
	q.refreshRollups(g, now)
	if c, _ := q.rollups(now); !c["s1"].totaled {
		t.Fatalf("totals not taken: %+v", c["s1"])
	}
	for at := now.Add(time.Hour); !at.After(now.Add(stationTotalsStale + time.Hour)); at = at.Add(time.Hour) {
		g.totalsErr = fmt.Errorf("timed out")
		q.refreshRollups(g, at)
	}
	if c, _ := q.rollups(now.Add(stationTotalsStale + time.Hour)); c["s1"].totaled {
		t.Errorf("totals %v old still served: %+v", stationTotalsStale+time.Hour, c["s1"])
	}
}

// Versions strictly increase, even within a millisecond and below an existing floor, and a day's binnings run one
// at a time while other days' run beside them.
func TestSeriesVersionsAndDayLocks(t *testing.T) {
	floor := time.Now().Add(time.Hour)
	a, b := seriesVersion(floor), seriesVersion(time.Time{})
	if !(a > floor.UTC().Format("2006-01-02 15:04:05.000") && b > a) {
		t.Errorf("versions %s then %s over floor %v", a, b, floor)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	unlock := lockDay(day)
	other := make(chan struct{})
	go func() { lockDay(day.AddDate(0, 0, 1))(); close(other) }()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("another day waited on a held one")
	}
	same := make(chan struct{})
	go func() { lockDay(day)(); close(same) }()
	select {
	case <-same:
		t.Error("one day binned twice at once")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	<-same
}

// seriesCounts is what rollups serves: the window's figures with the totals ever.
func seriesCounts(t *testing.T, conn *chConn, now time.Time) (map[string]stationCount, error) {
	t.Helper()
	counts, err := conn.stationCounts(context.Background(), now)
	if err != nil {
		return nil, err
	}
	totals, err := conn.stationTotals(context.Background())
	if err != nil {
		return nil, err
	}
	return mergeCounts(counts, totals, now), nil
}

// rebuildAll rebuilds the marked hours and waits for the worker that takes the older days.
func rebuildAll(ctx context.Context, conn *chConn, now time.Time) error {
	err := conn.rebuildStationSeries(ctx, now)
	for olderSeries.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
