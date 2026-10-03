package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCH records every batch and token it is handed, and fails the first `fail` of them: with a refusal when
// refuse is set, else as a lost connection.
type fakeCH struct {
	mu      sync.Mutex
	fail    int
	refuse  bool
	tokens  []string
	batches [][]trackPoint
}

func (f *fakeCH) insert(_ context.Context, token string, points []trackPoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	f.batches = append(f.batches, append([]trackPoint(nil), points...))
	if f.fail > 0 {
		f.fail--
		if f.refuse {
			return chRefused{errors.New("too many partitions")}
		}
		return errors.New("clickhouse is down")
	}
	return nil
}

func ingestAt(p *Pipeline, mmsi uint32, at time.Time, lat float64) {
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(mmsi, lat, 10.7))
}

func TestClickHouseSendsAFailedBatchAgainUnderItsToken(t *testing.T) {
	p := testPipeline(t)
	f := &fakeCH{fail: 1}
	p.attachClickHouse(&chStore{w: f})
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	ingestAt(p, 257000001, start, 59.90)
	if err := p.flushClickHouse(); err == nil {
		t.Fatal("the first batch should fail")
	}
	if body := get(t, p, "/metrics").Body.String(); !strings.Contains(body, "aiscast_clickhouse_up 0\n") {
		t.Error("a failing batch reads as down")
	}
	ingestAt(p, 257000001, start.Add(time.Minute), 59.91)
	for range 2 {
		if err := p.flushClickHouse(); err != nil {
			t.Fatal(err)
		}
	}
	// A failure can follow a commit, so the batch goes again exactly as it was, for ClickHouse to recognize.
	if len(f.batches) != 3 || len(f.batches[1]) != 1 || !f.batches[1][0].ts.Equal(start) || f.tokens[1] != f.tokens[0] {
		t.Fatalf("the failed batch goes again alone under its token: %v %+v", f.tokens, f.batches)
	}
	if len(f.batches[2]) != 1 || !f.batches[2][0].ts.Equal(start.Add(time.Minute)) || f.tokens[2] == f.tokens[0] {
		t.Fatalf("what queued behind it follows under a new token: %v %+v", f.tokens, f.batches)
	}
	if p.ch.failures.Load() != 1 || p.ch.written.Load() != 2 {
		t.Errorf("failures %d written %d", p.ch.failures.Load(), p.ch.written.Load())
	}
	if body := get(t, p, "/metrics").Body.String(); !strings.Contains(body, "aiscast_clickhouse_up 1\n") {
		t.Error("a written batch reads as up again")
	}
}

func TestClickHouseDropsWhatItsQueueCannotHold(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	p.vmu.Lock()
	p.chQueue = make([]trackPoint, maxPending)
	p.chQueue[maxPending/10].mmsi = 1 // the oldest position that should survive
	p.vmu.Unlock()
	ingestAt(p, 257000001, time.Now(), 59.9)
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	if n := len(p.chQueue); p.ch.dropped.Load() != maxPending/10 || n != maxPending-maxPending/10+1 ||
		p.chQueue[0].mmsi != 1 || p.chQueue[n-1].mmsi != 257000001 {
		t.Errorf("a full queue drops its oldest tenth, counted, and keeps the newest: dropped %d, %d queued", p.ch.dropped.Load(), n)
	}
}

// TestClickHouseWritesPositionsAndRollups runs against a real server named by CLICKHOUSE_TEST_URL, such as
// clickhouse://127.0.0.1:9000, in a database of its own that it drops afterward.
func TestClickHouseWritesPositionsAndRollups(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })
	if _, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db); err != nil {
		t.Fatalf("the schema applies again over itself: %v", err)
	}

	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: conn})
	slot := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	ingestAt(p, 257000001, slot.Add(2*time.Minute), 59.90)
	ingestAt(p, 257000001, slot.Add(9*time.Minute), 59.91)
	if err := p.flushClickHouse(); err != nil {
		t.Fatal(err)
	}
	// The pipeline withholds a report older than the vessel's last as stale, but history loaded later from the
	// lake arrives out of order: an earlier position written after the others still wins its window.
	early := trackPoint{mmsi: 257000001, ts: slot.Add(time.Minute), lat6: int32(59.89 * 600000), lon6: int32(10.7 * 600000),
		sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket"}
	if err := conn.insert(context.Background(), "early", []trackPoint{early}); err != nil {
		t.Fatal(err)
	}
	// The same batch under the same token, as after an insert that failed once ClickHouse had committed it.
	if err := conn.insert(context.Background(), "early", []trackPoint{early}); err != nil {
		t.Fatal(err)
	}

	// A clock far off spreads a batch over more daily partitions than an insert takes by default.
	spread := make([]trackPoint, 120)
	for i := range spread {
		spread[i] = early
		spread[i].mmsi = 257000002
		spread[i].ts = slot.Add(-time.Duration(i) * 24 * time.Hour)
	}
	if err := conn.insert(context.Background(), "spread", spread); err != nil {
		t.Fatalf("a batch over 120 days: %v", err)
	}

	// An error ClickHouse itself returns counts as a refusal, toward dropping the batch.
	missing := &chConn{conn: conn.conn, db: db + "_missing"}
	if err := missing.insert(context.Background(), "missing", []trackPoint{early}); !errors.As(err, new(chRefused)) {
		t.Errorf("an insert into a missing database is a refusal: %v", err)
	}

	// Reads come back in time order from every table. A one-minute step reads every position, a 15-minute step
	// the earliest per window, and a day-long step the hourly rollup.
	now := time.Now()
	for _, c := range []struct {
		step time.Duration
		want []time.Time
	}{
		{time.Minute, []time.Time{slot.Add(time.Minute), slot.Add(2 * time.Minute), slot.Add(9 * time.Minute)}},
		{15 * time.Minute, []time.Time{slot.Add(time.Minute)}},
		{24 * time.Hour, []time.Time{slot.Add(time.Minute)}},
	} {
		points, err := conn.history(context.Background(), 257000001, slot.Add(-time.Hour), slot.Add(time.Hour), c.step, 1000, now)
		if err != nil {
			t.Fatal(c.step, err)
		}
		var got, want []time.Time
		for _, pt := range points {
			got = append(got, pt.ts.UTC())
		}
		for _, w := range c.want {
			want = append(want, w.UTC())
		}
		if fmt.Sprint(got) != fmt.Sprint(want) || points[0].lat6 != int32(59.89*600000) || points[0].source != "kystverket" {
			t.Errorf("step %v: %v, want %v", c.step, got, c.want)
		}
	}

	ctx := context.Background()
	var n uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions WHERE mmsi = 257000001").Scan(&n); err != nil || n != 3 {
		t.Fatalf("every position: %d %v", n, err)
	}
	for _, rollup := range []string{"positions_15m", "positions_1h"} {
		var first time.Time
		var lat6 int32
		q := "SELECT tupleElement(argMinMerge(first), 1), tupleElement(argMinMerge(first), 2) FROM " + db + "." + rollup + " WHERE mmsi = 257000001"
		if err := conn.conn.QueryRow(ctx, q).Scan(&first, &lat6); err != nil {
			t.Fatal(rollup, err)
		}
		if !first.Equal(slot.Add(time.Minute)) || lat6 != int32(59.89*600000) {
			t.Errorf("%s keeps the earliest position in its window: %v %d", rollup, first, lat6)
		}
	}
}

// slowCH holds each insert open a while and records the most inserts it ever saw in flight.
type slowCH struct {
	mu            sync.Mutex
	inFlight, max int
}

func (s *slowCH) insert(context.Context, string, []trackPoint) error {
	s.mu.Lock()
	s.inFlight++
	s.max = max(s.max, s.inFlight)
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return nil
}

func TestClickHouseFlushesOneAtATime(t *testing.T) {
	// The writer's tick and the shutdown flush can call at once; a second insert must wait for the first.
	p := testPipeline(t)
	s := &slowCH{}
	p.attachClickHouse(&chStore{w: s})
	var wg sync.WaitGroup
	for i := range 4 {
		ingestAt(p, 257000001, time.Now().Add(time.Duration(i)*time.Second), 59.9)
		wg.Go(func() { p.flushClickHouse() })
	}
	wg.Wait()
	if s.max != 1 {
		t.Errorf("%d inserts in flight at once", s.max)
	}
}

func TestClickHouseKeepsABatchThroughAnOutage(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{fail: 30}})
	ingestAt(p, 257000001, time.Now().Add(-time.Hour), 59.90)
	for range 31 {
		p.flushClickHouse()
	}
	if p.ch.dropped.Load() != 0 || p.ch.written.Load() != 1 {
		t.Errorf("a lost connection is waited out: dropped %d written %d", p.ch.dropped.Load(), p.ch.written.Load())
	}
}

func TestClickHouseDropsABatchItCannotWrite(t *testing.T) {
	p := testPipeline(t)
	f := &fakeCH{fail: 3, refuse: true}
	p.attachClickHouse(&chStore{w: f})
	ingestAt(p, 257000001, time.Now().Add(-time.Hour), 59.90)
	p.flushClickHouse()
	p.flushClickHouse()
	if p.ch.dropped.Load() != 0 {
		t.Fatal("refusals inside the window are retried, as overload may pass")
	}
	p.ch.refused = time.Now().Add(-chRefuseFor)
	p.flushClickHouse()
	if p.ch.dropped.Load() != 1 || p.ch.failed != nil {
		t.Fatalf("refused for %v, the batch goes, counted: dropped %d", chRefuseFor, p.ch.dropped.Load())
	}
	ingestAt(p, 257000001, time.Now(), 59.91)
	if err := p.flushClickHouse(); err != nil || p.ch.written.Load() != 1 {
		t.Errorf("the next batch writes: %v, written %d", err, p.ch.written.Load())
	}
}

func TestClickHouseShutdownSendsTheFailedBatchAndTheQueue(t *testing.T) {
	p := testPipeline(t)
	f := &fakeCH{fail: 1}
	p.attachClickHouse(&chStore{w: f})
	ingestAt(p, 257000001, time.Now().Add(-time.Hour), 59.90)
	p.flushClickHouse()
	ingestAt(p, 257000001, time.Now(), 59.91)
	if err := p.drainClickHouse(); err != nil || p.ch.written.Load() != 2 {
		t.Errorf("both reach ClickHouse: %v, written %d", err, p.ch.written.Load())
	}
}

func TestClickHouseTableForAStep(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		age    time.Duration
		step   time.Duration
		table  string
		window time.Duration
	}{
		{3 * 24 * time.Hour, 0, "positions", 0},
		{3 * 24 * time.Hour, 10 * time.Minute, "positions", 0},
		{3 * 24 * time.Hour, 15 * time.Minute, "positions_15m", 15 * time.Minute},
		{40 * 24 * time.Hour, time.Minute, "positions_15m", 15 * time.Minute}, // past the raw 30 days
		{3 * 24 * time.Hour, 2 * time.Hour, "positions_1h", time.Hour},
		{400 * 24 * time.Hour, 15 * time.Minute, "positions_1h", time.Hour}, // past the 15-minute rollup's 13 months
		{3 * 24 * time.Hour, 20 * time.Minute, "positions", 0},              // not whole 15-minute windows
		{3 * 24 * time.Hour, 90 * time.Minute, "positions_15m", 15 * time.Minute},
	} {
		if table, window := chTable(now.Add(-c.age), c.step, now); table != c.table || window != c.window {
			t.Errorf("%v back at %v: %s %v, want %s %v", c.age, c.step, table, window, c.table, c.window)
		}
	}
}

// fakeHistory answers history reads with fixed points, or an error.
type fakeHistory struct {
	points []trackPoint
	err    error
}

func (f *fakeHistory) history(context.Context, uint32, time.Time, time.Time, time.Duration, int, time.Time) ([]trackPoint, error) {
	return f.points, f.err
}

func TestTrackReadsHistoryFromClickHouse(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour).Truncate(time.Hour)
	ch := &fakeHistory{points: []trackPoint{{mmsi: 257000001, ts: old, lat6: int32(59.5 * 600000), lon6: int32(10.7 * 600000),
		sog10: 100, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"}}}
	p := lakePipeline(t, &fakeLake{positions: []map[string]any{lakePosition(old.Add(time.Hour), 59.6)}})
	p.attachClickHouse(&chStore{w: &fakeCH{}, r: ch})
	sail(t, p, 257000001, time.Hour)
	from := now.Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)

	tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from)
	if first, _ := time.Parse(time.RFC3339, tr.Properties.Times[0]); tr.Properties.Points != 2 || !first.Equal(old) || tr.Attribution["aishub"] == "" {
		t.Errorf("history comes from ClickHouse, not the lake: %+v %v", tr.Properties, tr.Attribution)
	}
	ch.err = errors.New("clickhouse is down")
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from); tr.Properties.Points != 2 || tr.Attribution["digitraffic"] == "" {
		t.Errorf("the lake answers while ClickHouse fails: %+v %v", tr.Properties, tr.Attribution)
	}
	// Past the lake's own reach, a failing ClickHouse fails the request rather than scan months of the lake.
	if w := get(t, p, "/v1/vessels/257000001/track?from="+now.Add(-20*24*time.Hour).UTC().Format(time.RFC3339)); w.Code != 500 {
		t.Errorf("20 days with ClickHouse down: %d", w.Code)
	}
	ch.err = nil
	if w := get(t, p, "/v1/vessels/257000001/track?from="+now.Add(-300*24*time.Hour).UTC().Format(time.RFC3339)); w.Code != 200 {
		t.Errorf("with ClickHouse a track reaches most of a year: %d %s", w.Code, w.Body)
	}
}

// TestTrackFromClickHouseEndToEnd serves tracks past the window from a real ClickHouse, through the endpoint:
// raw positions thinned in the query, the limit, and a rollup for a long step.
func TestTrackFromClickHouseEndToEnd(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })
	p, _ := trackPipeline(t)
	p.attachClickHouse(&chStore{w: conn, r: conn})
	sail(t, p, 257000001, time.Hour)

	// Three days back, a report every minute for two hours.
	start := time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Hour)
	var points []trackPoint
	for i := range 120 {
		points = append(points, trackPoint{mmsi: 257000001, ts: start.Add(time.Duration(i) * time.Minute), lat6: int32((59 + float64(i)/10000) * 600000),
			lon6: int32(10.7 * 600000), sog10: 100, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"})
	}
	// A second vessel, a report every minute for a whole day.
	for i := range 1440 {
		points = append(points, trackPoint{mmsi: 257000002, ts: start.Add(time.Duration(i) * time.Minute), lat6: int32(58 * 600000),
			lon6: int32(10.7 * 600000), sog10: 0, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"})
	}
	if err := conn.insert(context.Background(), "history", points); err != nil {
		t.Fatal(err)
	}
	// The thinning happens in ClickHouse: ten-minute buckets bring 12 rows back, not 120.
	if got, err := conn.history(context.Background(), 257000001, start, start.Add(2*time.Hour), 10*time.Minute, 1000, time.Now()); err != nil || len(got) != 12 {
		t.Errorf("thinned in the query: %d rows, %v", len(got), err)
	}
	from := start.Add(-time.Hour).UTC().Format(time.RFC3339)
	to := start.Add(3 * time.Hour).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		vessel string
		query  string
		points int
		first  time.Time
	}{
		{"257000001", "&interval=10m", 12, start},                              // raw, thinned in the query
		{"257000001", "&interval=0&limit=50", 50, start.Add(70 * time.Minute)}, // raw, the newest 50
		{"257000001", "&interval=2h", 1 + int(start.Unix()/3600%2), start},     // the hourly rollup: epoch-aligned 2-hour buckets, so two when the hours start odd
	} {
		tr := getTrack(t, p, "/v1/vessels/"+c.vessel+"/track?from="+from+"&to="+to+c.query)
		if len(tr.Properties.Times) == 0 {
			t.Errorf("%s: no positions", c.query)
			continue
		}
		first, _ := time.Parse(time.RFC3339, tr.Properties.Times[0])
		if tr.Properties.Points != c.points || !first.Equal(c.first) || tr.Attribution["aishub"] == "" {
			t.Errorf("%s: %d points from %v, want %d from %v", c.query, tr.Properties.Points, first, c.points, c.first)
		}
	}
	// A long step over the day reads hourly windows grouped by the step, so the limit counts what the answer
	// keeps: the newest ten two-hour buckets, and the answer says older ones were left out.
	dayTo := start.Add(25 * time.Hour).UTC().Format(time.RFC3339)
	if tr := getTrack(t, p, "/v1/vessels/257000002/track?from="+from+"&to="+dayTo+"&interval=2h&limit=10"); tr.Properties.Points != 10 || !tr.Properties.Truncated {
		t.Errorf("a rollup at the limit: %d points, truncated %v", tr.Properties.Points, tr.Properties.Truncated)
	}
	// A step under a millisecond keeps every position, as the track store does.
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from+"&to="+to+"&interval=500us"); tr.Properties.Points != 120 {
		t.Errorf("a sub-millisecond step: %d points", tr.Properties.Points)
	}
}
