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
}

func (f *fakeSeries) rebuildStationSeries(context.Context, time.Time) error  { return nil }
func (f *fakeSeries) backfillStationSeries(context.Context, time.Time) error { return nil }
func (f *fakeSeries) stationCounts(context.Context, time.Time) (map[string]stationCount, error) {
	f.reads++
	return f.counts, f.err
}
func (f *fakeSeries) stationTotals(context.Context) (map[string]stationCount, error) {
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
		shared, at("station:s2", 3, recent), echo, wild, old, stream}
	// The receptions were written before the series existed, as on a database at step 17: its tables and view
	// come with the upgrade, and the backfill bins what came before.
	if err := conn.exec(ctx, "DROP VIEW {db}.station_dirty_mv", "DROP TABLE {db}.station_dirty", "DROP TABLE {db}.station_hours",
		"DROP TABLE {db}.station_vessels", "DROP TABLE {db}.station_built", "DROP TABLE {db}.station_series_backfilled",
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
	if err := conn.insertOwn(ctx, map[ownKey]time.Time{{"station:s1", recent.Unix() / 3600, 9}: recent}); err != nil {
		t.Fatal(err)
	}
	if err := conn.backfillStationSeries(ctx, now); err != nil {
		t.Fatal(err)
	}
	// An older version of an hour, as a rebuild leaves for the moment between its insert and its delete, is not read.
	if err := conn.exec(ctx, "INSERT INTO {db}.station_hours (hour, station, source, receptions, first, built) VALUES ('"+
		cur.Format("2006-01-02 15:04:05")+"', 'station:s1', 'station', 1000, 1000, '2000-01-01 00:00:00')",
		"INSERT INTO {db}.station_vessels (hour, station, source, mmsi, receptions, last_ts, built) VALUES ('"+
			cur.Format("2006-01-02 15:04:05")+"', 'station:s1', 'station', 777, 1, now64(3), '2000-01-01 00:00:00')"); err != nil {
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
	if err := conn.rebuildStationSeries(ctx, now); err != nil {
		t.Fatal(err)
	}
	binHoursFails = nil
	built, err := chColumn[uint64](ctx, conn.conn, "SELECT uniqExact(hour) FROM "+db+".station_built FINAL WHERE marker > now64(3) - INTERVAL 1 MINUTE")
	if err != nil || built[0] != 24 {
		t.Errorf("hours rebuilt with today failing: %v %v; want yesterday's 24", built, err)
	}
	if err := conn.rebuildStationSeries(ctx, now); err != nil {
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
	if err := conn.rebuildStationSeries(ctx, now); err != nil {
		t.Fatal(err)
	}
	if again, _ := seriesCounts(t, conn, now); again["station:s2"] != counts["station:s2"] || again["station:s1"] != counts["station:s1"] {
		t.Errorf("a second rebuild changed the counts: %+v, was %+v", again, counts)
	}
	where := "station = 'station:s2' AND mmsi = 3"
	dirty, err := conn.dirtyHours(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	// A rebuild before the delete finds nothing marked, so it cannot record the hour as built with the rows still there.
	if err := conn.rebuildStationSeries(ctx, now); err != nil {
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
	if err := conn.rebuildStationSeries(nulls, now); err != nil {
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
		h.Format("2006-01-02 15:04:05")+"', 'station:s3', 'station', 999, 999, '2099-01-01 00:00:00')"); err != nil {
		t.Fatal(err)
	}
	if err := conn.markHours(ctx, []time.Time{h}); err != nil {
		t.Fatal(err)
	}
	if err := conn.rebuildStationSeries(ctx, now); err != nil {
		t.Fatal(err)
	}
	if counts, _ := seriesCounts(t, conn, now); counts["station:s3"].receptions != 1 {
		t.Errorf("a rebuild after a version from the future: s3 %+v", counts["station:s3"])
	}

	own, err := conn.ownCandidates(ctx, now.Add(-2*time.Hour))
	if err != nil || own["station:s1"][9] != recent.Unix() {
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

// A failed read keeps the last figures for the minute, rather than every request waiting on ClickHouse again.
func TestStationRollupsBackOffAfterAFailure(t *testing.T) {
	p := testPipeline(t)
	f := &fakeSeries{counts: map[string]stationCount{"s1": {day: 3}}}
	p.attachClickHouse(&chStore{w: &fakeCH{}, series: f})
	now := time.Now()
	p.rollups(now)
	f.err = fmt.Errorf("clickhouse is down")
	counts, _ := p.rollups(now.Add(2 * time.Minute))
	p.rollups(now.Add(2*time.Minute + 10*time.Second))
	if f.reads != 2 || counts["s1"].day != 3 {
		t.Errorf("%d reads, figures %v; want one failed read, then the last figures for the minute", f.reads, counts)
	} // Failing past stationRollupsStale, there are no figures rather than frozen ones.
	if counts, _ := p.rollups(now.Add(2*time.Minute + stationRollupsStale + time.Second)); counts != nil {
		t.Errorf("figures %v kept through %v of failed reads", counts, stationRollupsStale)
	}
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
