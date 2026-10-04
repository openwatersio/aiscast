package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/ClickHouse/clickhouse-go/v2"
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

	// Reads come back in time order. A one-minute step reads positions_1m's minutes, a 15-minute step the
	// earliest of them in each window, and a day-long step the earliest of the day.
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
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions WHERE mmsi = 257000001").Scan(&n); err != nil || n != 3 {
		t.Fatalf("every position: %d %v", n, err)
	}
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions_1m FINAL WHERE mmsi = 257000001").Scan(&n); err != nil || n != 3 {
		t.Errorf("positions_1m keeps a moving vessel's minutes: %d %v", n, err)
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
		span   time.Duration // the range's length; zero reaches to now
	}{
		{3 * 24 * time.Hour, 0, "positions", 0, 0},
		{24 * time.Hour, time.Hour, "positions", 0, 0}, // inside the 48-hour window
		{3 * 24 * time.Hour, 10 * time.Minute, "positions_1m", time.Minute, 0},
		{3 * 24 * time.Hour, 15 * time.Minute, "positions_1m", time.Minute, 0},
		{40 * 24 * time.Hour, 0, "positions_1m", time.Minute, 0},                    // longer than the positions view reads
		{40 * 24 * time.Hour, time.Second, "positions", 0, 2 * time.Hour},           // any age, within its span
		{400 * 24 * time.Hour, 0, "positions", 0, 31 * 24 * time.Hour},              // every position, a month a year back
		{400 * 24 * time.Hour, 0, "positions_1m", time.Minute, 32 * 24 * time.Hour}, // and past the span, positions_1m
		{3 * 24 * time.Hour, 2 * time.Hour, "positions_1m", time.Minute, 0},
		{400 * 24 * time.Hour, 15 * time.Minute, "positions_1m", time.Minute, 0},
		{3 * 24 * time.Hour, 20 * time.Minute, "positions_1m", time.Minute, 0},
		{3 * 24 * time.Hour, 90 * time.Minute, "positions_1m", time.Minute, 0},
		{3 * 24 * time.Hour, 30 * time.Second, "positions", 0, 0},
	} {
		from, to := now.Add(-c.age), now
		if c.span > 0 {
			to = from.Add(c.span)
		}
		if table, window := chTable(from, to, c.step, now); table != c.table || window != c.window {
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

func (f *fakeHistory) first(context.Context, uint32, time.Time, time.Time) (time.Time, bool, error) {
	if len(f.points) == 0 {
		return time.Time{}, false, f.err
	}
	return f.points[0].ts, f.err == nil, f.err
}

func TestTrackReadsHistoryFromClickHouse(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour).Truncate(time.Hour)
	ch := &fakeHistory{points: []trackPoint{{mmsi: 257000001, ts: old, lat6: int32(59.5 * 600000), lon6: int32(10.7 * 600000),
		sog10: 100, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"}}}
	p, _ := trackPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}, r: ch})
	from := now.Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)

	tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from)
	if first, _ := time.Parse(time.RFC3339, tr.Properties.Times[0]); tr.Properties.Points != 1 || !first.Equal(old) || tr.Attribution["aishub"] == "" {
		t.Errorf("history comes from ClickHouse: %+v %v", tr.Properties, tr.Attribution)
	}
	if w := get(t, p, "/v1/vessels/257000001/track?from="+now.Add(-300*24*time.Hour).UTC().Format(time.RFC3339)); w.Code != 200 {
		t.Errorf("a track reaches most of a year: %d %s", w.Code, w.Body)
	}
	ch.err = errors.New("clickhouse is down")
	if w := get(t, p, "/v1/vessels/257000001/track?from="+from); w.Code != 500 {
		t.Errorf("ClickHouse down: %d", w.Code)
	}
	p.attachClickHouse(nil)
	if w := get(t, p, "/v1/vessels/257000001/track"); w.Code != 503 {
		t.Errorf("without ClickHouse: %d", w.Code)
	}
	var out mcpTrack
	if msg := mcpCall(t, mcpClient(t, p), "get_vessel_track", map[string]any{"mmsi": 257000001}, &out); !strings.Contains(msg, "not available") {
		t.Errorf("MCP without ClickHouse: %q", msg)
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
	if got, ok, err := conn.first(context.Background(), 257000001, start.Add(-time.Hour), start.Add(time.Hour)); !ok || err != nil || !got.Equal(start) {
		t.Errorf("first position: %v %v %v", got, ok, err)
	}
	if _, ok, err := conn.first(context.Background(), 257000009, start, start.Add(time.Hour)); ok || err != nil {
		t.Errorf("first position of a vessel with none: %v %v", ok, err)
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
	// 40 days back, a 20-minute step reads every position and keeps the first in each 20-minute bucket.
	old := time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Hour)
	var sparse []trackPoint
	for _, m := range []int{5, 21, 31} {
		sparse = append(sparse, trackPoint{mmsi: 257000003, ts: old.Add(time.Duration(m) * time.Minute), lat6: int32(57 * 600000),
			lon6: int32(10.7 * 600000), sog10: 100, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"})
	}
	if err := conn.insert(context.Background(), "sparse", sparse); err != nil {
		t.Fatal(err)
	}
	oldFrom, oldTo := old.Add(-time.Hour).UTC().Format(time.RFC3339), old.Add(time.Hour).UTC().Format(time.RFC3339)
	tr := getTrack(t, p, "/v1/vessels/257000003/track?from="+oldFrom+"&to="+oldTo+"&interval=20m")
	if want := fmt.Sprint([]string{old.Add(5 * time.Minute).UTC().Format(time.RFC3339), old.Add(21 * time.Minute).UTC().Format(time.RFC3339)}); fmt.Sprint(tr.Properties.Times) != want {
		t.Errorf("a 20-minute step over 15-minute windows: %v, want %v", tr.Properties.Times, want)
	}

	// Every position 40 days back: receptions keep everything, so a short range is every position at any age.
	if tr := getTrack(t, p, "/v1/vessels/257000003/track?from="+oldFrom+"&to="+oldTo+"&interval=0"); tr.Properties.Interval != 0 || tr.Properties.Points != 3 {
		t.Errorf("interval=0 40 days back: interval %d, %d points; want 0 and 3", tr.Properties.Interval, tr.Properties.Points)
	}

	// A step under a millisecond keeps every position, as ClickHouse stores them.
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from+"&to="+to+"&interval=500us"); tr.Properties.Points != 120 || tr.Properties.Interval != 0 {
		t.Errorf("a sub-millisecond step: %d points", tr.Properties.Points)
	}
}

func TestTrackKeepsTheExtraHistoryRowAsTheSpikeAnchor(t *testing.T) {
	// ClickHouse returns one row past the limit, the oldest, which judges the next row for impossible speed
	// before it is dropped. Here the page holds two rows, and the second-oldest is a fix 120 nm off.
	now := time.Now()
	t0 := now.Add(-5 * 24 * time.Hour).Truncate(time.Hour)
	pt := func(at time.Duration, lat float64) trackPoint {
		return trackPoint{mmsi: 257000001, ts: t0.Add(at), lat6: int32(lat * 600000), lon6: int32(10.7 * 600000),
			sog10: 100, cog10: 3600, heading: 511, navStatus: 15, source: "aishub"}
	}
	p, _ := trackPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}, r: &fakeHistory{points: []trackPoint{pt(0, 59.0), pt(time.Minute, 61.0), pt(2*time.Minute, 59.001)}}})
	from := t0.Add(-time.Hour).UTC().Format(time.RFC3339)
	to := t0.Add(time.Hour).UTC().Format(time.RFC3339)
	tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from+"&to="+to+"&interval=0&limit=2")
	want := fmt.Sprint([]string{t0.UTC().Format(time.RFC3339), t0.Add(2 * time.Minute).UTC().Format(time.RFC3339)})
	var got []string
	for _, ts := range tr.Properties.Times {
		parsed, _ := time.Parse(time.RFC3339, ts)
		got = append(got, parsed.UTC().Format(time.RFC3339))
	}
	if fmt.Sprint(got) != want {
		t.Fatalf("the spike is judged against the row past the limit: %v, want %v", got, want)
	}
	if !tr.Properties.Truncated {
		t.Error("older positions were left out, and the answer should say so")
	}
}

// queued is what the pipeline has queued for ClickHouse for one vessel.
func queued(p *Pipeline, mmsi uint32) []trackPoint {
	p.chMu.Lock()
	defer p.chMu.Unlock()
	var out []trackPoint
	for _, pt := range p.chQueue {
		if pt.mmsi == mmsi {
			out = append(out, pt)
		}
	}
	return out
}

func TestClickHouseQueuesEveryCopy(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	at := func(source string, d time.Duration, pkt ais.Packet) {
		p.ingestPacket(source, source, t0.Add(d), t0.Add(d), pkt)
	}
	first := posReport(mmsi, 59.90, 10.7)
	at("kystverket", 0, first)
	at("barentswatch", time.Second, first) // the same payload: dedupe's copy
	at("kystverket", 10*time.Second, posReport(mmsi, 59.91, 10.7))
	// AISHub's copy of the first report: a minute late, so stale, its own payload, and a stamp seconds off.
	again := posReport(mmsi, 59.90, 10.7).(ais.PositionReport)
	again.Sog = 5
	p.ingestPacket("aishub", "aishub", t0.Add(3*time.Second), t0.Add(time.Minute), again)
	// A late report at a position the vessel never accepted is a report of its own.
	p.ingestPacket("aishub", "aishub", t0.Add(5*time.Second), t0.Add(time.Minute), posReport(mmsi, 59.905, 10.7))
	// A jump of a few hundred miles in seconds, and another source's copy of it.
	at("kystverket", 20*time.Second, posReport(mmsi, 65.0, 10.7))
	at("barentswatch", 21*time.Second, posReport(mmsi, 65.0, 10.7))
	// A late report from a sender that cannot be authenticated, near where the vessel was.
	p.ingestPacket("udp:feeder", "udp:feeder", t0.Add(6*time.Second), t0.Add(70*time.Second), posReport(mmsi, 59.906, 10.7))
	// A late report from a trusted source 60 miles from where the vessel was seconds later.
	p.ingestPacket("aishub", "aishub", t0.Add(7*time.Second), t0.Add(70*time.Second), posReport(mmsi, 61.0, 10.7))
	// A late report from a station with a token, near where the vessel was.
	p.ingestPacket("station:someone", "station:someone", t0.Add(8*time.Second), t0.Add(70*time.Second), posReport(mmsi, 59.907, 10.7))

	q := queued(p, mmsi)
	if len(q) != 10 {
		t.Fatalf("%d copies queued, want 10: %+v", len(q), q)
	}
	accepted := q[0]
	if accepted.dup || accepted.txAt.IsZero() || accepted.station != "kystverket" || accepted.recv.IsZero() {
		t.Errorf("the accepted copy: %+v", accepted)
	}
	if c := q[1]; !c.dup || txKey(c) != txKey(accepted) || c.station != "barentswatch" {
		t.Errorf("dedupe's copy names the accepted transmission: %+v", c)
	}
	if c := q[2]; c.dup || txKey(c) == txKey(accepted) {
		t.Errorf("the next report is a transmission of its own: %+v", c)
	}
	if c := q[3]; !c.dup || txKey(c) != txKey(accepted) || c.source != "aishub" {
		t.Errorf("AISHub's stale copy at the first position names that transmission: %+v", c)
	}
	if c := q[4]; c.dup || txKey(c) == txKey(accepted) || txKey(c) == txKey(q[2]) {
		t.Errorf("a late report matching no recent position is accepted on its own: %+v", c)
	}
	if c := q[5]; !c.implausible {
		t.Errorf("the jump is kept and flagged: %+v", c)
	}
	if c := q[6]; !c.dup || txKey(c) != txKey(q[5]) || !c.implausible {
		t.Errorf("a copy of the jump is flagged with it, so no read serves the jump through it: %+v", c)
	}
	if c := q[7]; !c.implausible {
		t.Errorf("an unauthenticated sender's late report stays out of history: %+v", c)
	}
	if c := q[8]; !c.implausible || c.dup {
		t.Errorf("a late report the fold never tested is tested for the jump: %+v", c)
	}
	if c := q[9]; !c.implausible {
		t.Errorf("a volunteer station's late report stays out of history, token or not: %+v", c)
	}
	body := get(t, p, "/metrics").Body.String()
	for _, want := range []string{`aiscast_clickhouse_rebuilt_copies_total{matched="true"} 1`, `aiscast_clickhouse_rebuilt_copies_total{matched="false"} 4`} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

func TestClickHouseKeepsEveryCopyAndServesOne(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })

	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: conn, r: conn})
	const mmsi = 257000001
	t0 := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	first := posReport(mmsi, 59.90, 10.7)
	p.ingestPacket("kystverket", "kystverket", t0, t0, first)
	p.ingestPacket("barentswatch", "barentswatch/terra", t0.Add(time.Second), t0.Add(time.Second), first)
	p.ingestPacket("kystverket", "kystverket", t0.Add(20*time.Second), t0.Add(20*time.Second), posReport(mmsi, 65.0, 10.7))
	if err := p.flushClickHouse(); err != nil {
		t.Fatal(err)
	}

	var n uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions WHERE mmsi = ?", mmsi).Scan(&n); err != nil || n != 3 {
		t.Fatalf("every copy, the implausible one too: %d %v", n, err)
	}
	served := func() []trackPoint {
		t.Helper()
		points, err := conn.history(ctx, mmsi, t0.Add(-time.Minute), t0.Add(time.Minute), 0, 100, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return points
	}
	if got := served(); len(got) != 1 || got[0].source != "kystverket" || !got[0].ts.Equal(t0) {
		t.Fatalf("one transmission, its first copy: %+v", got)
	}
	var source string
	q := "SELECT argMax(source, ts) FROM " + db + ".positions_1m FINAL WHERE mmsi = ?"
	if err := conn.conn.QueryRow(ctx, q, mmsi).Scan(&source); err != nil || source != "kystverket" {
		t.Errorf("the rollup keeps the accepted copy: %q %v", source, err)
	}

	// A transmission with one implausible copy is never served, through any of its copies.
	jump := trackPoint{mmsi: mmsi, ts: t0.Add(30 * time.Second), lat6: int32(59.0 * 600000), lon6: int32(10.7 * 600000),
		sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket", txAt: t0.Add(30 * time.Second), txDisc: 42, recv: t0.Add(30 * time.Second), implausible: true}
	copied := jump
	copied.source, copied.recv, copied.implausible, copied.dup = "barentswatch", t0.Add(31*time.Second), false, true
	if err := conn.insert(ctx, "jump", []trackPoint{jump, copied}); err != nil {
		t.Fatal(err)
	}
	if got := served(); len(got) != 1 || !got[0].ts.Equal(t0) {
		t.Errorf("only the plausible transmission is served: %+v", got)
	}
	// The same when the implausible copy also has a bad clock: the clock picks which copy is served, never
	// whether a flagged transmission is.
	jump.txDisc, copied.txDisc, jump.clockBad = 43, 43, true
	if err := conn.insert(ctx, "jump-clock", []trackPoint{jump, copied}); err != nil {
		t.Fatal(err)
	}
	if got := served(); len(got) != 1 || !got[0].ts.Equal(t0) {
		t.Errorf("an implausible copy with a bad clock still keeps its transmission out: %+v", got)
	}
	if err := conn.conn.Exec(ctx, "DELETE FROM "+db+".receptions WHERE tx_disc IN (42, 43)"); err != nil {
		t.Fatal(err)
	}

	// Purging a source leaves its transmissions to the copies other sources delivered.
	if err := conn.conn.Exec(ctx, "DELETE FROM "+db+".receptions WHERE source = 'kystverket'"); err != nil {
		t.Fatal(err)
	}
	if got := served(); len(got) != 1 || got[0].source != "barentswatch" {
		t.Errorf("after the purge, the other copy: %+v", got)
	}

	// The lake load names transmissions in ClickHouse, so its byte must agree with discOf.
	id := eventID("payload" + "A")
	var disc uint8
	if err := conn.conn.QueryRow(ctx, "SELECT "+chDiscOf("?"), id).Scan(&disc); err != nil || disc != discOf(id) {
		t.Errorf("tx_disc in ClickHouse %d, discOf %d: %v", disc, discOf(id), err)
	}
}

// A database in the first layout, as main leaves it, gains the current columns in place. Nothing is dropped or
// renamed at start, so a view over receptions, like the coverage map's, keeps filling, and a server rolled back
// to main still writes.
func TestClickHouseMigratesInPlace(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	opts, err := clickhouse.ParseDSN(url)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	t.Cleanup(func() { raw.Exec(ctx, "DROP DATABASE IF EXISTS "+db); raw.Close() })
	mainInsert := "INSERT INTO " + db + ".receptions (mmsi, ts, tx, recv_ts, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad)" +
		" VALUES (257000001, now64(3), 7, now64(3), 1, 1, 1023, 3600, 511, 15, 'aishub', 'aishub', true, true, false, false)"
	for _, stmt := range []string{
		"CREATE DATABASE " + db,
		"CREATE TABLE " + db + ".receptions (mmsi UInt32, ts DateTime64(3, 'UTC'), tx UInt64, recv_ts DateTime64(3, 'UTC'), lat6 Int32, lon6 Int32, sog10 UInt16, cog10 UInt16, heading UInt16, navstat UInt8, source LowCardinality(String), station LowCardinality(String), accepted Bool DEFAULT true, corroborated Bool DEFAULT true, implausible Bool DEFAULT false, clock_bad Bool DEFAULT false) ENGINE = MergeTree PARTITION BY toYYYYMM(ts) ORDER BY (mmsi, ts)",
		"CREATE TABLE " + db + ".positions_1h (mmsi UInt32, slot DateTime('UTC'), first AggregateFunction(argMin, Tuple(DateTime64(3, 'UTC'), Int32, Int32, UInt16, UInt16, UInt16, UInt8, String), DateTime64(3, 'UTC'))) ENGINE = AggregatingMergeTree ORDER BY (mmsi, slot)",
		"CREATE MATERIALIZED VIEW " + db + ".positions_1h_mv TO " + db + ".positions_1h AS SELECT mmsi, toDateTime(toStartOfInterval(ts, INTERVAL 1 HOUR), 'UTC') AS slot, argMinState((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), ts) AS first FROM " + db + ".receptions WHERE accepted GROUP BY mmsi, slot",
		"CREATE TABLE " + db + ".heard (mmsi UInt32) ENGINE = MergeTree ORDER BY mmsi",
		"CREATE MATERIALIZED VIEW " + db + ".heard_mv TO " + db + ".heard AS SELECT mmsi FROM " + db + ".receptions WHERE NOT implausible AND NOT clock_bad",
		mainInsert,
	} {
		if err := raw.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var conn *chConn
	for range 2 { // and again over itself
		if conn, err = openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conn.conn.Close() })
	steps, err := chColumn[uint32](ctx, raw, "SELECT version FROM "+db+".schema_migrations ORDER BY version")
	if err != nil || len(steps) != len(chMigrations)-1 {
		t.Fatalf("each step recorded once: %v %v", steps, err)
	}
	now := time.Now().Truncate(time.Millisecond)
	if err := conn.insert(ctx, "new", []trackPoint{{mmsi: 257000001, ts: now, lat6: 2, lon6: 2, sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket", station: "kystverket"}}); err != nil {
		t.Fatal("the current writer:", err)
	}
	if err := raw.Exec(ctx, mainInsert); err != nil {
		t.Fatal("main's writer, after a rollback:", err)
	}
	tables, err := chColumn[string](ctx, raw, "SELECT name FROM system.tables WHERE database = ? ORDER BY name", db)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"positions", "positions_1h", "positions_1h_mv", "positions_1m", "positions_1m_mv", "receptions", "receptions_converted"} {
		if !slices.Contains(tables, want) {
			t.Errorf("no %s: %v", want, tables)
		}
	}
	var heard, minutes, served uint64
	raw.QueryRow(ctx, "SELECT count() FROM "+db+".heard").Scan(&heard)
	raw.QueryRow(ctx, "SELECT count() FROM "+db+".positions_1m").Scan(&minutes)
	if heard != 3 || minutes != 2 {
		t.Errorf("a view over receptions sees every insert, before and after: %d; positions_1m sees those after: %d", heard, minutes)
	}
	if err := raw.QueryRow(ctx, "SELECT count() FROM "+db+".positions(mmsi = 257000001, from = now64(3) - INTERVAL 1 HOUR, to = now64(3) + INTERVAL 1 MINUTE)").Scan(&served); err != nil || served != 2 {
		t.Errorf("the view serves the first layout's transmission and the current one: %d %v", served, err)
	}
}

// chDiscOf is discOf in ClickHouse, for a hex event id: the low byte of the id's first 64 bits, read
// big-endian as ParseUint reads them, for a loader that names transmissions in ClickHouse.
func chDiscOf(id string) string {
	return "toUInt8(reinterpretAsUInt64(reverse(unhex(substring(" + id + ", 1, 16)))) % 256)"
}

// txKey is a copy's transmission, for comparing two copies.
func txKey(pt trackPoint) string { return fmt.Sprint(pt.txAt.UnixMilli(), "/", pt.txDisc) }

// A copy dedupe matches while its transmission is still folding waits for the fold's verdict, so a copy of an
// implausible transmission is never written unflagged.
func TestClickHouseHoldsACopyUntilTheFoldDecides(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	key := "payload" + "A"
	p.mu.Lock()
	p.seen[key], p.folding[key] = t0, nil // accepted, and still folding
	p.mu.Unlock()
	p.emit(&Event{Payload: []byte("payload"), Channel: 'A', Time: t0.Add(time.Second), RecvTime: t0.Add(time.Second),
		Source: "barentswatch", Station: "barentswatch", Packet: posReport(mmsi, 65.0, 10.7)})
	if q := queued(p, mmsi); len(q) != 0 {
		t.Fatalf("a copy is written before the fold decides: %+v", q)
	}
	p.settleFold(key, &Event{Time: t0, Implausible: true})
	q := queued(p, mmsi)
	if len(q) != 1 || !q[0].implausible || !q[0].dup || !q[0].txAt.Equal(t0) || q[0].txDisc != discOf(eventID(key)) {
		t.Fatalf("the waiting copy is written with the verdict: %+v", q)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, folding := p.folding[key]; folding || !p.bad[key].Equal(t0) {
		t.Errorf("the key is settled: folding %v, bad %v", folding, p.bad[key])
	}
}

// A position folded before ClickHouse connects is neither written nor remembered, so a stale copy of it that
// arrives after is kept as the only copy, accepted, rather than as a copy of a transmission never written.
func TestClickHouseRemembersOnlyWhatItWrites(t *testing.T) {
	p := testPipeline(t)
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", t0, t0, posReport(mmsi, 59.90, 10.7))
	p.ingestPacket("kystverket", "kystverket", t0.Add(10*time.Second), t0.Add(10*time.Second), posReport(mmsi, 59.91, 10.7))
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	late := posReport(mmsi, 59.90, 10.7).(ais.PositionReport)
	late.Sog = 5
	p.ingestPacket("aishub", "aishub", t0.Add(3*time.Second), t0.Add(time.Minute), late)
	q := queued(p, mmsi)
	if len(q) != 1 || q[0].dup || q[0].implausible || q[0].source != "aishub" {
		t.Fatalf("the stale copy is the only one written, accepted: %+v", q)
	}
}

// A stale report kept out of history passes that on to the copies dedupe matches to it, so purging the
// report's source cannot bring it back through one of them. The stream's own flags stay as they were.
func TestClickHouseCopiesOfAnUnservedReportAreFlagged(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", t0.Add(10*time.Second), t0.Add(10*time.Second), posReport(mmsi, 59.91, 10.7))
	late := posReport(mmsi, 59.906, 10.7)
	p.ingestPacket("udp:feeder", "udp:feeder", t0.Add(6*time.Second), t0.Add(70*time.Second), late)
	p.ingestPacket("barentswatch", "barentswatch", t0.Add(7*time.Second), t0.Add(71*time.Second), late)
	q := queued(p, mmsi)
	if len(q) != 3 || !q[1].implausible || !q[2].dup || !q[2].implausible || txKey(q[2]) != txKey(q[1]) {
		t.Fatalf("the late report and its copy are both kept out: %+v", q)
	}
	if p.stats.implausible.Load() != 0 {
		t.Errorf("the stream counted a stale report implausible")
	}
}

func TestAnchorDecidesMoving(t *testing.T) {
	at := func(dLatM float64, sog10 uint16) trackPoint { // a report dLatM meters north of 59.9 N
		return trackPoint{lat6: int32(math.Round((59.9 + dLatM/111320) * 600000)), lon6: int32(10.7 * 600000), sog10: sog10}
	}
	seed := &[2]int32{int32(59.9 * 600000), int32(10.7 * 600000)}

	var a anchor
	if a.still(at(0, 3), nil, true) {
		t.Error("with no anchor and no seed, a report is moving")
	}
	a = anchor{}
	if !a.still(at(10, 3), seed, true) || !a.set {
		t.Error("an unset anchor starts at the seed, the vessel's restored position")
	}
	if a.still(at(20, 60), nil, true) {
		t.Error("reported speed over half a knot is moving, however near")
	}
	// Drifting at 0.3 kn, a report every 10 s, 1.5 m apart: still until the drift adds up past movedM.
	a = anchor{lat6: seed[0], lon6: seed[1], set: true}
	moved := 0
	for i := 1; i <= 60; i++ {
		if !a.still(at(float64(i)*1.5, 3), nil, true) {
			moved++
		}
	}
	if moved != 1 {
		t.Errorf("90 m of drift past a 50 m anchor moves once, then re-anchors: moved %d times", moved)
	}
	// No speed: distance alone decides.
	a = anchor{lat6: seed[0], lon6: seed[1], set: true}
	if !a.still(at(30, 1023), nil, true) || a.still(at(200, 1023), nil, true) {
		t.Error("without speed, within 50 m is still and beyond it moving")
	}
	// A report that does not enter positions_1m never moves the anchor.
	a = anchor{lat6: seed[0], lon6: seed[1], set: true}
	a.still(at(500, 1023), nil, false)
	if !a.still(at(10, 1023), nil, true) {
		t.Error("an unadvanced report left the anchor where it was")
	}
}

func TestClickHousePositions1mKeepsMinutesAndHeartbeats(t *testing.T) {
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
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	day := time.Now().Add(-5 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	pt := func(mmsi uint32, at time.Duration, lat float64, still bool) trackPoint {
		return trackPoint{mmsi: mmsi, ts: day.Add(at), lat6: int32(lat * 600000), lon6: int32(10.7 * 600000),
			sog10: 0, cog10: 3600, heading: 511, navStatus: 5, source: "kystverket", still: still}
	}
	var points []trackPoint
	// Underway for ten minutes, a report every 10 seconds: one row a minute.
	for i := range 60 {
		points = append(points, pt(257000001, 8*time.Hour+time.Duration(i)*10*time.Second, 59+float64(i)/1000, false))
	}
	// Moored all morning at one harbor, then all afternoon at another, a report every 3 minutes: a heartbeat at each.
	for i := range 80 {
		points = append(points, pt(257000002, time.Duration(i)*3*time.Minute, 59.9, true))
		points = append(points, pt(257000002, 12*time.Hour+time.Duration(i)*3*time.Minute, 58.1, true))
	}
	if err := conn.insert(ctx, "p1m", points); err != nil {
		t.Fatal(err)
	}
	var n uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions_1m FINAL WHERE mmsi = 257000001").Scan(&n); err != nil || n != 10 {
		t.Errorf("a moving vessel's ten minutes: %d rows, %v", n, err)
	}
	rows, err := conn.conn.Query(ctx, "SELECT ts FROM "+db+".positions_1m FINAL WHERE mmsi = 257000002 ORDER BY ts")
	if err != nil {
		t.Fatal(err)
	}
	var beats []time.Time
	for rows.Next() {
		var ts time.Time
		rows.Scan(&ts)
		beats = append(beats, ts.UTC())
	}
	rows.Close()
	var want []time.Time // a heartbeat each 30 minutes it sat at each place, the last report there
	for _, at := range []time.Duration{0, 12 * time.Hour} {
		for i := range 8 {
			want = append(want, day.Add(at+time.Duration(i)*30*time.Minute+27*time.Minute))
		}
	}
	if fmt.Sprint(beats) != fmt.Sprint(want) {
		t.Errorf("heartbeats %v, want %v", beats, want)
	}
	// Any step of a minute or more reads positions_1m grouped by the step: the moving vessel's 5-minute track
	// has two points, and the moored one's track is its heartbeats, a morning hour of them included.
	for _, c := range []struct {
		mmsi     uint32
		from, to time.Duration
		step     time.Duration
		want     int
	}{
		{257000001, 0, 24 * time.Hour, 5 * time.Minute, 2},
		{257000001, 0, 24 * time.Hour, time.Minute, 10},
		{257000002, 0, 24 * time.Hour, 15 * time.Minute, 16},
		{257000002, time.Hour, 2 * time.Hour, time.Minute, 2},
	} {
		got, err := conn.history(ctx, c.mmsi, day.Add(c.from), day.Add(c.to-time.Millisecond), c.step, 1000, time.Now())
		if err != nil || len(got) != c.want {
			t.Errorf("%d from %v at %v: %d points, want %d: %v", c.mmsi, c.from, c.step, len(got), c.want, err)
		}
	}
}

func TestConvertReceptionsMatchesTheLiveWriter(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	opts, err := clickhouse.ParseDSN(url)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// The first layout, as main created it; the server adds the current columns beside its own.
	for _, stmt := range []string{"CREATE DATABASE " + db, "CREATE TABLE " + db + ".receptions (mmsi UInt32, ts DateTime64(3, 'UTC'), tx UInt64, recv_ts DateTime64(3, 'UTC')," +
		" lat6 Int32, lon6 Int32, sog10 UInt16, cog10 UInt16, heading UInt16, navstat UInt8, source LowCardinality(String), station LowCardinality(String)," +
		" accepted Bool DEFAULT true, corroborated Bool DEFAULT true, implausible Bool DEFAULT false, clock_bad Bool DEFAULT false) ENGINE = MergeTree PARTITION BY toYYYYMM(ts) ORDER BY (mmsi, ts)" +
		" SETTINGS non_replicated_deduplication_window = 1000"} {
		if err := raw.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	v1 := "INSERT INTO " + db + ".receptions (mmsi, ts, tx, recv_ts, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad)"
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	noon := day.Add(12 * time.Hour)
	type copyOf struct {
		mmsi          uint32
		ts, at, recv  time.Duration // after noon: the copy's stamp, its transmission's, and its arrival
		id            string
		latM          float64 // meters north of 59.9 N
		sog10         uint16
		source        string
		accepted, bad bool
	}
	ida, idb, idc := eventID("a"), eventID("b"), eventID("c")
	copies := []copyOf{
		{257000001, 0, 0, time.Second, ida, 0, 100, "kystverket", true, false},
		{257000001, 2 * time.Second, 0, time.Minute, ida, 0, 100, "aishub", false, false}, // a rebuilt copy, stamped 2 s off
		{257000001, time.Minute, time.Minute, time.Minute + time.Second, idb, 500, 100, "kystverket", true, false},
		{257000002, 0, 0, time.Second, idc, 0, 1023, "aishub", true, false},
		{257000002, 3 * time.Minute, 3 * time.Minute, 3*time.Minute + time.Second, eventID("d"), 10, 1023, "aishub", true, false},
		{257000002, 6 * time.Minute, 6 * time.Minute, 6*time.Minute + time.Second, eventID("e"), 500, 1023, "aishub", true, true},
		// Drifting under half a knot. The report at 60 m arrives after the one at 30 m stamped later, so it is stale
		// and leaves the anchor at 0 m, and the report at 100 m is moving from there.
		{257000004, 0, 0, time.Second, eventID("f"), 0, 0, "kystverket", true, false},
		{257000004, 2 * time.Minute, 2 * time.Minute, 5 * time.Minute, eventID("g"), 60, 0, "aishub", true, false},
		{257000004, 4 * time.Minute, 4 * time.Minute, 4*time.Minute + time.Second, eventID("h"), 30, 0, "kystverket", true, false},
		{257000004, 6 * time.Minute, 6 * time.Minute, 6*time.Minute + time.Second, eventID("i"), 100, 0, "kystverket", true, false},
		// Still at 0 m, then moving at 100 m: AISHub's copy of the still report, stamped after, is still with it.
		{257000005, 0, 0, time.Second, eventID("j"), 0, 0, "kystverket", true, false},
		{257000005, 3 * time.Minute, 3 * time.Minute, 3*time.Minute + time.Second, eventID("k"), 0, 0, "kystverket", true, false},
		{257000005, 3*time.Minute + time.Second, 3*time.Minute + time.Second, 3*time.Minute + 2*time.Second, eventID("l"), 100, 0, "kystverket", true, false},
		{257000005, 3*time.Minute + 2*time.Second, 3 * time.Minute, 4 * time.Minute, eventID("k"), 0, 0, "aishub", false, false},
	}
	batch, err := conn.conn.PrepareBatch(ctx, v1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range copies {
		h, _ := strconv.ParseUint(c.id[:16], 16, 64)
		tx := h ^ uint64(noon.Add(c.at).UnixMilli())
		lat6 := int32(math.Round((59.9 + c.latM/111320) * 600000))
		if err := batch.Append(c.mmsi, noon.Add(c.ts), tx, noon.Add(c.recv), lat6, int32(10.7*600000), c.sog10, uint16(3600), uint16(511), uint8(15),
			c.source, c.source, c.accepted, true, c.bad, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
	// Inserts go out while the day's read is still streaming, as they do for any real day: a batch of 2 here.
	was := convertBatch
	convertBatch = 2
	t.Cleanup(func() { convertBatch = was })
	// Two more transmissions of a third vessel, stamped in one millisecond, whose ids share a byte.
	var idx, idy string
	for i := 0; idy == ""; i++ {
		id := eventID(fmt.Sprint("collide", i))
		switch {
		case idx == "":
			idx = id
		case discOf(id) == discOf(idx):
			idy = id
		}
	}
	for i, id := range []string{idx, idy} {
		h, _ := strconv.ParseUint(id[:16], 16, 64)
		tx := h ^ uint64(noon.UnixMilli())
		if err := conn.conn.Exec(ctx, v1+" VALUES (257000003, ?, ?, ?, ?, ?, 0, 3600, 511, 15, 'aishub', 'aishub', true, true, false, false)",
			noon, tx, noon.Add(time.Duration(i+1)*time.Second), int32(58*600000)+int32(i*1000), int32(10.7*600000)); err != nil {
			t.Fatal(err)
		}
	}
	// Cleanup keeps the first layout while a day is left to convert.
	if err := conn.cleanup(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if legacy, _ := conn.legacy(ctx); !legacy {
		t.Fatal("cleanup dropped the first layout before its rows were converted")
	}
	// A run that stopped partway through the day, after its first batch, and the run that does the day again.
	if err := conn.write(ctx, "", []trackPoint{{mmsi: 257000001, ts: noon, recv: noon.Add(time.Second)}}, true); err != nil {
		t.Fatal(err)
	}
	days, err := conn.unconverted(ctx)
	if err != nil || len(days) != 1 || !days[0].Equal(day) {
		t.Fatalf("the day to convert: %v %v", days, err)
	}
	n, err := conn.convertDay(ctx, day, map[uint32]*anchor{})
	if err != nil || n != len(copies)+2 {
		t.Fatalf("converted %d of %d: %v", n, len(copies), err)
	}
	if days, err := conn.unconverted(ctx); err != nil || len(days) != 0 {
		t.Fatalf("the day is recorded as converted: %v %v", days, err)
	}
	// Once more, after the day's record is lost: the same rows again, which deduplication must not swallow.
	if err := conn.conn.Exec(ctx, "TRUNCATE TABLE "+db+".receptions_converted"); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.convertDay(ctx, day, map[uint32]*anchor{}); err != nil || n != len(copies)+2 {
		t.Fatalf("converted again %d: %v", n, err)
	}
	// A server rolled back to the first layout writes into the converted day: the day is to do again, and
	// cleanup waits for it.
	if err := conn.conn.Exec(ctx, v1+" VALUES (257000006, ?, 99, ?, 1, 1, 1023, 3600, 511, 15, 'aishub', 'aishub', true, true, false, false)",
		noon, noon.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if days, err := conn.unconverted(ctx); err != nil || len(days) != 1 {
		t.Fatalf("the day is to convert again: %v %v", days, err)
	}
	if err := conn.cleanup(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if legacy, _ := conn.legacy(ctx); !legacy {
		t.Fatal("cleanup dropped a row written since its day was converted")
	}
	if n, err := conn.convertDay(ctx, day, map[uint32]*anchor{}); err != nil || n != len(copies)+3 {
		t.Fatalf("converted again with the new row %d: %v", n, err)
	}
	var converted uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions WHERE tx = 0").Scan(&converted); err != nil || converted != uint64(len(copies)+3) {
		t.Fatalf("a rerun leaves each row once: %d %v", converted, err)
	}
	rows, err := conn.conn.Query(ctx, "SELECT mmsi, ts, tx_off, tx_disc, recv_delay, accepted, implausible, moving FROM "+db+".receptions WHERE tx = 0 AND mmsi NOT IN (257000003, 257000006) ORDER BY mmsi, ts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantMoving := []bool{true, true, true, true, false, true, true, true, false, true, true, false, true, false}
	i := 0
	for rows.Next() {
		var mmsi uint32
		var ts time.Time
		var off, delay int32
		var disc uint8
		var accepted, implausible, moving bool
		if err := rows.Scan(&mmsi, &ts, &off, &disc, &delay, &accepted, &implausible, &moving); err != nil {
			t.Fatal(err)
		}
		c := copies[i]
		if want := int32((c.at - c.ts).Milliseconds()); off != want || disc != discOf(c.id) {
			t.Errorf("row %d: tx_off %d disc %d, want %d %d: the transmission the live writer would name", i, off, disc, want, discOf(c.id))
		}
		if delay != int32((c.recv-c.ts).Milliseconds()) || accepted != c.accepted || implausible != c.bad || moving != wantMoving[i] {
			t.Errorf("row %d: delay %d accepted %v implausible %v moving %v", i, delay, accepted, implausible, moving)
		}
		i++
	}
	var served uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions(mmsi = 257000001, from = '2026-09-01 00:00:00', to = '2026-09-02 00:00:00')").Scan(&served); err != nil || served != 2 {
		t.Errorf("the view serves each transmission once: %d %v", served, err)
	}
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions(mmsi = 257000003, from = '2026-09-01 00:00:00', to = '2026-09-02 00:00:00')").Scan(&served); err != nil || served != 2 {
		t.Errorf("two transmissions in one millisecond with one byte are still two: %d %v", served, err)
	}
	// With every day converted, cleanup drops the first layout, and the view serves the same.
	if err := conn.cleanup(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if legacy, _ := conn.legacy(ctx); legacy {
		t.Fatal("cleanup kept tx")
	}
	var left uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions").Scan(&left); err != nil || left != converted {
		t.Errorf("only the converted rows are left: %d of %d, %v", left, converted, err)
	}
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions(mmsi = 257000001, from = '2026-09-01 00:00:00', to = '2026-09-02 00:00:00')").Scan(&served); err != nil || served != 2 {
		t.Errorf("after cleanup the view serves each transmission once: %d %v", served, err)
	}
}

// A report that arrives a day or more after its stamp is kept out of positions_1m, so it must not move the
// anchor either: later reports are judged against where the vessel was last moving in positions_1m.
func TestALateReportDoesNotMoveTheAnchor(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	v := newVessel()
	v.moved = anchor{lat6: int32(59.9 * 600000), lon6: int32(10.7 * 600000), set: true}
	was := v.moved
	// Two kilometers away, newer than anything the vessel sent, but arriving 25 hours after its stamp.
	u := newVessel()
	u.Lat, u.Lon, u.Sog = 59.918, 10.7, 0
	ev := &Event{MMSI: mmsi, Time: t0, RecvTime: t0.Add(25 * time.Hour), ID: eventID("late"), Source: "aishub", Station: "aishub"}
	p.vmu.Lock()
	p.noteFolded(ev, v, u, false, true, 59.9, 10.7)
	p.vmu.Unlock()
	q := queued(p, mmsi)
	if len(q) != 1 || !q[0].clockBad || v.moved != was {
		t.Fatalf("the late report is clock_bad and leaves the anchor where it was: %+v, anchor %+v", q, v.moved)
	}
}

// Two distinct reports of a vessel stamped in one millisecond whose ids share a byte are still two
// transmissions: the second takes the next free byte, and a copy of it follows it there.
func TestATransmissionsByteIsFreeInItsMillisecond(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a, b := "first", ""
	for i := 0; b == ""; i++ {
		if c := fmt.Sprint("second", i); discOf(eventID(c+"A")) == discOf(eventID(a+"A")) {
			b = c
		}
	}
	report := func(payload, source string, recv time.Duration, lat float64) {
		p.emit(&Event{Payload: []byte(payload), Channel: 'A', Time: t0, RecvTime: t0.Add(recv),
			Source: source, Station: source, Packet: posReport(mmsi, lat, 10.7)})
	}
	report(a, "aishub", 0, 59.900)
	report(b, "aishub", time.Second, 59.901)
	report(b, "barentswatch", 2*time.Second, 59.901) // dedupe's copy of the second
	q := queued(p, mmsi)
	if len(q) != 3 || q[0].txDisc == q[1].txDisc || q[1].dup || !q[2].dup || txKey(q[2]) != txKey(q[1]) {
		t.Fatalf("two transmissions with two bytes, and the copy with the second's: %+v", q)
	}
}

// An implausible report keeps its byte too, so a valid report in its millisecond is not hidden with it.
func TestAnImplausibleTransmissionKeepsItsByte(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a, b := "jump", ""
	for i := 0; b == ""; i++ {
		if c := fmt.Sprint("valid", i); discOf(eventID(c+"A")) == discOf(eventID(a+"A")) {
			b = c
		}
	}
	report := func(payload string, at time.Time, lat float64) {
		p.emit(&Event{Payload: []byte(payload), Channel: 'A', Time: at, RecvTime: at,
			Source: "kystverket", Station: "kystverket", Packet: posReport(mmsi, lat, 10.7)})
	}
	report("before", t0.Add(-time.Minute), 59.900)
	report(a, t0, 10.0) // 2,900 nm in a minute
	report(b, t0, 59.901)
	q := queued(p, mmsi)
	if len(q) != 3 || !q[1].implausible || q[2].implausible || q[1].txDisc == q[2].txDisc {
		t.Fatalf("the implausible report and the valid one in its millisecond take two bytes: %+v", q)
	}
}

// A flood of impossible positions for a vessel never pushes its plausible recent ones out of the ring.
func TestImplausibleReportsNeverEvictPlausibleOnes(t *testing.T) {
	v := &vessel{}
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range recentMax + 40 {
		v.remember(trackPoint{ts: t0.Add(time.Duration(i) * time.Second), lat6: int32(i), implausible: i >= recentMax-1})
	}
	good := 0
	for _, r := range v.recent {
		if !r.bad {
			good++
		}
	}
	if len(v.recent) != recentMax || good != recentMax-1 || v.recent[recentMax-1].ms != t0.Add(time.Duration(recentMax+39)*time.Second).UnixMilli() {
		t.Fatalf("the plausible ones stay and the newest implausible one replaces the last: %d of %d %+v", good, len(v.recent), v.recent)
	}
}

// A copy is still or moving with its transmission, not by where the vessel has gone since, so positions_1m
// rebuilt from it after its source is purged keeps the track's shape.
func TestACopyIsStillWithItsTransmission(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	const mmsi = 257000001
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	report := func(payload, source string, at, recv time.Time, lat float64) {
		p.emit(&Event{Payload: []byte(payload), Channel: 'A', Time: at, RecvTime: recv,
			Source: source, Station: source, Packet: posReport(mmsi, lat, 10.7)})
	}
	report("a", "kystverket", t0, t0, 59.900)
	report("b", "kystverket", t0.Add(time.Minute), t0.Add(time.Minute), 59.900)
	report("c", "kystverket", t0.Add(70*time.Second), t0.Add(70*time.Second), 59.901) // 111 m on: moving
	report("b", "barentswatch", t0.Add(time.Minute), t0.Add(75*time.Second), 59.900)
	q := queued(p, mmsi)
	if len(q) != 4 || !q[1].still || q[2].still || !q[3].dup || !q[3].still {
		t.Fatalf("the copy of the still report is still: %+v", q)
	}
}

// A copy stamped off its transmission, as AISHub's run, belongs to the page its transmission falls in, so two
// pages that split between the stamps serve the transmission once.
func TestClickHousePositionsServesATransmissionOnceAcrossPages(t *testing.T) {
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
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	tx := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	pt := trackPoint{mmsi: 257000001, ts: tx, txAt: tx, txDisc: 9, recv: tx.Add(time.Second), lat6: 1, lon6: 1,
		sog10: 100, cog10: 3600, heading: 511, navStatus: 0, source: "kystverket", station: "kystverket"}
	late := pt
	late.ts, late.recv, late.source, late.station, late.dup = tx.Add(40*time.Second), tx.Add(time.Minute), "aishub", "aishub", true
	if err := conn.insert(ctx, "edge", []trackPoint{pt, late}); err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, page := range [][2]time.Time{{tx.Add(-time.Minute), tx.Add(20 * time.Second)}, {tx.Add(20*time.Second + time.Millisecond), tx.Add(time.Minute)}} {
		var n uint64
		if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions(mmsi = 257000001, from = ?, to = ?)", page[0], page[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		served += int(n)
	}
	if served != 1 {
		t.Errorf("the transmission is served %d times across the two pages", served)
	}
}

// Purging the source of a day's accepted copies and rebuilding the day leaves positions_1m as it was, from the
// copies left: the same minutes underway and heartbeats moored.
func TestClickHouseRebuildsPositions1mAfterAPurge(t *testing.T) {
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
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var points []trackPoint
	add := func(mmsi uint32, at time.Duration, lat float64, still bool, disc uint8) {
		ts := day.Add(at)
		pt := trackPoint{mmsi: mmsi, ts: ts, txAt: ts, txDisc: disc, lat6: int32(lat * 600000), lon6: int32(10.7 * 600000),
			sog10: 0, cog10: 3600, heading: 511, navStatus: 5, still: still}
		first, copy := pt, pt
		first.source, first.station, first.recv = "aishub", "aishub", ts.Add(time.Second)
		copy.source, copy.station, copy.recv, copy.dup = "kystverket", "kystverket", ts.Add(2*time.Second), true
		points = append(points, first, copy)
	}
	for i := range 60 {
		add(257000001, 8*time.Hour+time.Duration(i)*10*time.Second, 59+float64(i)/1000, false, uint8(i))
	}
	for i := range 80 {
		add(257000002, time.Duration(i)*3*time.Minute, 59.9, true, uint8(i))
	}
	add(257000003, 24*time.Hour-5*time.Millisecond, 57, false, 1) // the day's last moment
	if err := conn.insert(ctx, "day", points); err != nil {
		t.Fatal(err)
	}
	dump := func() string {
		rows, err := conn.conn.Query(ctx, "SELECT mmsi, slot, cell, ts, lat6, toString(source) FROM "+db+".positions_1m FINAL ORDER BY mmsi, slot, cell")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var mmsi, cell uint32
			var slot, ts time.Time
			var lat int32
			var src string
			rows.Scan(&mmsi, &slot, &cell, &ts, &lat, &src)
			fmt.Fprintln(&b, mmsi, slot.UTC(), cell, ts.UTC(), lat, src)
		}
		return b.String()
	}
	before := dump()
	if err := conn.conn.Exec(ctx, "ALTER TABLE "+db+".receptions DELETE WHERE source = 'aishub' SETTINGS mutations_sync = 2"); err != nil {
		t.Fatal(err)
	}
	if err := conn.rebuildPositions1m(ctx, day); err != nil {
		t.Fatal(err)
	}
	if after := dump(); !strings.Contains(before, "aishub") || strings.ReplaceAll(before, "aishub", "kystverket") != after {
		t.Errorf("before:\n%s\nafter:\n%s", before, after)
	}
}
