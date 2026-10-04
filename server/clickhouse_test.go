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
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions WHERE mmsi = 257000001").Scan(&n); err != nil || n != 3 {
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
		span   time.Duration // the range's length; zero reaches to now
	}{
		{3 * 24 * time.Hour, 0, "positions", 0, 0},
		{24 * time.Hour, time.Hour, "positions", 0, 0}, // inside the 48-hour window
		{3 * 24 * time.Hour, 10 * time.Minute, "positions", 0, 0},
		{3 * 24 * time.Hour, 15 * time.Minute, "positions_15m", 15 * time.Minute, 0},
		{40 * 24 * time.Hour, time.Minute, "positions_15m", 15 * time.Minute, 0},  // longer than the positions view reads
		{40 * 24 * time.Hour, time.Minute, "positions", 0, 2 * time.Hour},         // any age, within its span
		{400 * 24 * time.Hour, 0, "positions", 0, 31 * 24 * time.Hour},            // every position, a month a year back
		{400 * 24 * time.Hour, 0, "positions_1h", time.Hour, 32 * 24 * time.Hour}, // and past the span, the rollup that holds it
		{3 * 24 * time.Hour, 2 * time.Hour, "positions_1h", time.Hour, 0},
		{400 * 24 * time.Hour, 15 * time.Minute, "positions_1h", time.Hour, 0}, // past the 15-minute rollup's 13 months
		{3 * 24 * time.Hour, 20 * time.Minute, "positions", 0, 0},              // not whole 15-minute windows
		{3 * 24 * time.Hour, 90 * time.Minute, "positions_15m", 15 * time.Minute, 0},
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
	if accepted.dup || accepted.tx == 0 || accepted.station != "kystverket" || accepted.recv.IsZero() {
		t.Errorf("the accepted copy: %+v", accepted)
	}
	if c := q[1]; !c.dup || c.tx != accepted.tx || c.station != "barentswatch" {
		t.Errorf("dedupe's copy names the accepted transmission: %+v", c)
	}
	if c := q[2]; c.dup || c.tx == accepted.tx {
		t.Errorf("the next report is a transmission of its own: %+v", c)
	}
	if c := q[3]; !c.dup || c.tx != accepted.tx || c.source != "aishub" {
		t.Errorf("AISHub's stale copy at the first position names that transmission: %+v", c)
	}
	if c := q[4]; c.dup || c.tx == accepted.tx || c.tx == q[2].tx {
		t.Errorf("a late report matching no recent position is accepted on its own: %+v", c)
	}
	if c := q[5]; !c.implausible {
		t.Errorf("the jump is kept and flagged: %+v", c)
	}
	if c := q[6]; !c.dup || c.tx != q[5].tx || !c.implausible {
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
	q := "SELECT tupleElement(argMinMerge(first), 8) FROM " + db + ".positions_15m WHERE mmsi = ?"
	if err := conn.conn.QueryRow(ctx, q, mmsi).Scan(&source); err != nil || source != "kystverket" {
		t.Errorf("the rollup keeps the accepted copy: %q %v", source, err)
	}

	// A transmission with one implausible copy is never served, through any of its copies.
	jump := trackPoint{mmsi: mmsi, ts: t0.Add(30 * time.Second), lat6: int32(59.0 * 600000), lon6: int32(10.7 * 600000),
		sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket", tx: 42, recv: t0.Add(30 * time.Second), implausible: true}
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
	jump.tx, copied.tx, jump.clockBad = 43, 43, true
	if err := conn.insert(ctx, "jump-clock", []trackPoint{jump, copied}); err != nil {
		t.Fatal(err)
	}
	if got := served(); len(got) != 1 || !got[0].ts.Equal(t0) {
		t.Errorf("an implausible copy with a bad clock still keeps its transmission out: %+v", got)
	}
	if err := conn.conn.Exec(ctx, "DELETE FROM "+db+".receptions WHERE tx IN (42, 43)"); err != nil {
		t.Fatal(err)
	}

	// Purging a source leaves its transmissions to the copies other sources delivered.
	if err := conn.conn.Exec(ctx, "DELETE FROM "+db+".receptions WHERE source = 'kystverket'"); err != nil {
		t.Fatal(err)
	}
	if got := served(); len(got) != 1 || got[0].source != "barentswatch" {
		t.Errorf("after the purge, the other copy: %+v", got)
	}

	// The lake load names transmissions in ClickHouse, so it must agree with txOf.
	id := eventID("payload" + "A")
	var tx uint64
	if err := conn.conn.QueryRow(ctx, "SELECT "+chTxOf("?", "toDateTime64(?, 3, 'UTC')"), id, chTime(t0)).Scan(&tx); err != nil || tx != txOf(id, t0) {
		t.Errorf("tx in ClickHouse %d, txOf %d: %v", tx, txOf(id, t0), err)
	}
}

func TestClickHouseMigratesThePositionsTable(t *testing.T) {
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
	t.Cleanup(func() { raw.Exec(ctx, "DROP DATABASE IF EXISTS "+db); raw.Close() })
	// The schema before receptions: a positions table with rollup views reading it.
	for _, stmt := range []string{
		"CREATE DATABASE " + db,
		"CREATE TABLE " + db + ".positions (mmsi UInt32, ts DateTime64(3, 'UTC'), lat6 Int32, lon6 Int32, sog10 UInt16, cog10 UInt16, heading UInt16, navstat UInt8, source LowCardinality(String)) ENGINE = MergeTree PARTITION BY toYYYYMMDD(ts) ORDER BY (mmsi, ts)",
		strings.ReplaceAll(chRollup("15m", ""), "{db}", db),
		"CREATE MATERIALIZED VIEW " + db + ".positions_15m_mv TO " + db + ".positions_15m AS SELECT mmsi, toDateTime(toStartOfInterval(ts, INTERVAL 15 MINUTE), 'UTC') AS slot, argMinState((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), ts) AS first FROM " + db + ".positions GROUP BY mmsi, slot",
		"INSERT INTO " + db + ".positions VALUES (257000001, now64(3), 1, 1, 1023, 3600, 511, 15, 'aishub')",
	} {
		if err := raw.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // and again over itself
		conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
		if err != nil {
			t.Fatal(err)
		}
		conn.conn.Close()
	}
	var engine string
	var kept uint64
	if err := raw.QueryRow(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = 'positions'", db).Scan(&engine); err != nil || engine != "View" {
		t.Errorf("positions is the view: %q %v", engine, err)
	}
	if err := raw.QueryRow(ctx, "SELECT count() FROM "+db+".positions_old").Scan(&kept); err != nil || kept != 1 {
		t.Errorf("the old table is kept for the load: %d %v", kept, err)
	}
	var source string
	if err := raw.QueryRow(ctx, "SELECT create_table_query FROM system.tables WHERE database = ? AND name = 'positions_15m_mv'", db).Scan(&source); err != nil || !strings.Contains(source, ".receptions") {
		t.Errorf("the rollup view reads receptions: %v %s", err, source)
	}
}

// chTxOf is txOf in ClickHouse, for a hex event id and a time: the id's first 64 bits, read big-endian as
// ParseUint reads them, XORed with the time in milliseconds. clickhouse-load.py uses the same expression.
func chTxOf(id, ts string) string {
	return "bitXor(reinterpretAsUInt64(reverse(unhex(substring(" + id + ", 1, 16)))), toUInt64(toUnixTimestamp64Milli(" + ts + ")))"
}

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
	if len(q) != 1 || !q[0].implausible || !q[0].dup || q[0].tx != txOf(eventID(key), t0) {
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
	if len(q) != 3 || !q[1].implausible || !q[2].dup || !q[2].implausible || q[2].tx != q[1].tx {
		t.Fatalf("the late report and its copy are both kept out: %+v", q)
	}
	if p.stats.implausible.Load() != 0 {
		t.Errorf("the stream counted a stale report implausible")
	}
}
