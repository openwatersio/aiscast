package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// memCH is ClickHouse in memory: it keeps every copy it is sent, and reads history the way chConn reads the
// positions view: one copy per transmission, the first to arrive with a believable clock, leaving out any
// transmission a copy of which is implausible; then the first position in each step bucket and the newest
// limit+1 of those. A copy that names no transmission is a transmission of its own, as the writer stores it.
type memCH struct {
	mu     sync.Mutex
	points []trackPoint
}

func (m *memCH) insert(_ context.Context, _ string, points []trackPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.points = append(m.points, points...)
	return nil
}

// view is the positions view over mmsi between from and to.
func (m *memCH) view(mmsi uint32, from, to time.Time) []trackPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	type tx struct {
		ms   int64
		disc uint8
	}
	served, bad := map[tx]trackPoint{}, map[tx]bool{}
	var own []trackPoint
	for _, pt := range m.points {
		if pt.mmsi != mmsi || pt.ts.Before(from) || pt.ts.After(to) {
			continue
		}
		if pt.txAt.IsZero() {
			if !pt.implausible && !pt.clockBad {
				own = append(own, pt)
			}
			continue
		}
		k := tx{pt.txAt.UnixMilli(), pt.txDisc}
		bad[k] = bad[k] || pt.implausible
		if first, ok := served[k]; !pt.clockBad && (!ok || pt.recv.Before(first.recv)) {
			served[k] = pt
		}
	}
	for tx, pt := range served {
		if !bad[tx] {
			own = append(own, pt)
		}
	}
	slices.SortStableFunc(own, func(a, b trackPoint) int { return a.ts.Compare(b.ts) })
	return own
}

func (m *memCH) history(_ context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, _ time.Time) ([]trackPoint, error) {
	out := m.view(mmsi, from, to)
	if ms := step.Milliseconds(); ms > 0 {
		kept, last := out[:0], int64(-1)
		for _, pt := range out {
			if b := pt.ts.UnixMilli() / ms; b != last {
				kept, last = append(kept, pt), b
			}
		}
		out = kept
	}
	return out[max(len(out)-limit-1, 0):], nil
}

func (m *memCH) first(_ context.Context, mmsi uint32, from, to time.Time) (time.Time, bool, error) {
	if out := m.view(mmsi, from, to); len(out) > 0 {
		return out[0].ts, true, nil
	}
	return time.Time{}, false, nil
}

// trackPipeline is a test pipeline with a vessel record in a temporary directory and ClickHouse in memory.
func trackPipeline(t testing.TB) (*Pipeline, *memCH) {
	t.Helper()
	p := testPipeline(nil)
	st, err := openStore(filepath.Join(t.TempDir(), "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.attachStore(st); err != nil {
		t.Fatal(err)
	}
	ch := &memCH{}
	p.attachClickHouse(&chStore{w: ch, r: ch})
	t.Cleanup(func() { p.closeStore() })
	return p, ch
}

// sail folds position reports for mmsi at the given ages, moving north a little each time, and flushes.
// Ages go oldest first: the cache treats a report older than the vessel's newest as stale and keeps it
// out of the track, as it does live.
func sail(t *testing.T, p *Pipeline, mmsi uint32, ages ...time.Duration) {
	t.Helper()
	for i, age := range ages {
		at := time.Now().Add(-age).Truncate(time.Second)
		p.ingestPacket("kystverket", "kystverket", at, at, posReport(mmsi, 59.9+float64(i)/1000, 10.7))
	}
	mustFlush(t, p)
	if err := p.flushClickHouse(); err != nil {
		t.Fatal(err)
	}
}

type testTrack struct {
	Geometry *struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties struct {
		From, To  string
		Points    int
		Interval  int64
		Truncated bool
		Name      string
		Times     []string
		Sog       []*float64
	} `json:"properties"`
	Attribution map[string]string `json:"attribution"`
}

func getTrack(t *testing.T, p *Pipeline, target string) testTrack {
	t.Helper()
	w := get(t, p, target)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/geo+json" {
		t.Fatalf("%s: %d %s", target, w.Code, w.Body)
	}
	var tr testTrack
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return tr
}

func TestTrackEndpoint(t *testing.T) {
	p, ch := trackPipeline(t)
	p.ingestPacket("kystverket", "kystverket", time.Now(), time.Now(), shipStatic(257000001, "NORDIC STAR"))
	sail(t, p, 257000001, 30*time.Hour, 3*time.Hour, 2*time.Hour, 90*time.Minute, time.Hour, 10*time.Minute)
	if n := len(ch.points); n != 6 {
		t.Errorf("each accepted position reaches ClickHouse once: %d receptions for 6 positions", n)
	}
	base := "/v1/vessels/257000001/track"

	tr := getTrack(t, p, base)
	if tr.Properties.Points != 5 || tr.Geometry == nil || tr.Geometry.Type != "LineString" || tr.Properties.Name != "NORDIC STAR" ||
		len(tr.Properties.Times) != 5 || tr.Properties.Times[0] >= tr.Properties.Times[4] || tr.Attribution["kystverket"] == "" {
		t.Errorf("default is the last 24 hours, oldest first: %+v", tr.Properties)
	}
	if tr.Properties.Sog[0] != nil {
		t.Errorf("speed not available is null: %v", *tr.Properties.Sog[0])
	}

	from := time.Now().Add(-40 * time.Hour).UTC().Format(time.RFC3339)
	if tr := getTrack(t, p, base+"?from="+from); tr.Properties.Points != 6 {
		t.Errorf("a range inside the window: %d points", tr.Properties.Points)
	}

	if tr := getTrack(t, p, base+"?interval=2h"); tr.Properties.Points >= 5 || tr.Properties.Points < 2 {
		t.Errorf("thinned to one per two hours: %d", tr.Properties.Points)
	}
	tr = getTrack(t, p, base+"?limit=2&interval=0")
	if tr.Properties.Points != 2 || !tr.Properties.Truncated {
		t.Errorf("limit keeps the newest: %+v", tr.Properties)
	}
	if last, _ := time.Parse(time.RFC3339, tr.Properties.Times[1]); time.Since(last) > 15*time.Minute {
		t.Errorf("limit kept older positions: %v", tr.Properties.Times)
	}

	w := get(t, p, base+"?format=gpx")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/gpx+xml" || strings.Count(w.Body.String(), "<trkpt ") != 5 ||
		!strings.Contains(w.Body.String(), "<name>NORDIC STAR (257000001)</name>") || !strings.Contains(w.Body.String(), "Norwegian Coastal Administration") {
		t.Errorf("gpx: %d %s", w.Code, w.Body)
	}

	// Known but silent in the range: an empty track, not a 404.
	to := time.Now().Add(-20 * time.Hour).UTC().Format(time.RFC3339)
	from = time.Now().Add(-21 * time.Hour).UTC().Format(time.RFC3339)
	if tr := getTrack(t, p, base+"?from="+from+"&to="+to); tr.Properties.Points != 0 || tr.Geometry != nil {
		t.Errorf("empty range: %+v", tr)
	}
	for target, code := range map[string]int{
		"/v1/vessels/999999999/track":        404,
		base + "?format=kml":                 400,
		base + "?from=yesterday":             400,
		base + "?from=" + to + "&to=" + from: 400,
		base + "?limit=0":                    400,
	} {
		if w := get(t, p, target); w.Code != code {
			t.Errorf("%s: %d, want %d", target, w.Code, code)
		}
	}
}

func TestTrackTierCap(t *testing.T) {
	p, ch := trackPipeline(t)
	now := time.Now()
	var points []trackPoint
	for i := range 300 {
		v := newVessel()
		v.Lat, v.Lon = 59.9, 10.7
		points = append(points, newTrackPoint(257000001, now.Add(-time.Duration(i)*time.Minute), v, "kystverket"))
	}
	ch.insert(context.Background(), "", points)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?limit=5000&interval=0"); tr.Properties.Points != 200 || !tr.Properties.Truncated {
		t.Errorf("anonymous cap: %d truncated %v", tr.Properties.Points, tr.Properties.Truncated)
	}
}

func TestTrackHistoryIsATier(t *testing.T) {
	p, _ := trackPipeline(t)
	sail(t, p, 257000001, time.Hour)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	from := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-2*24*time.Hour - time.Hour).UTC().Format(time.RFC3339)
	if w := get(t, p, "/v1/vessels/257000001/track?from="+from+"&to="+old); w.Code != 403 || !strings.Contains(w.Body.String(), "feeder") {
		t.Errorf("anonymous past the window: %d %s", w.Code, w.Body)
	}
	// A range that overlaps the window is clamped to it, as a client asking for 48 hours a moment early does.
	tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from)
	if at, _ := time.Parse(time.RFC3339, tr.Properties.From); tr.Properties.Points == 0 || time.Since(at) > trackWindow+time.Minute {
		t.Errorf("anonymous overlapping the window: %+v", tr.Properties)
	}
	var out mcpTrack
	if msg := mcpCall(t, mcpClient(t, p), "get_vessel_track", map[string]any{"mmsi": 257000001, "from": from, "to": old}, &out); !strings.Contains(msg, "feeder") {
		t.Errorf("MCP past the window: %q", msg)
	}
	allowAnon = true
	long := time.Now().Add(-400 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if w := get(t, p, "/v1/vessels/257000001/track?from="+long); w.Code != 400 {
		t.Errorf("more than a year: %d", w.Code)
	}
}

func TestReplayPipelineWritesNoHistory(t *testing.T) {
	p := testPipeline(t)
	p.ingestPacket("kystverket", "kystverket", time.Now(), time.Now(), posReport(257000001, 59.9, 10.7))
	if p.ch != nil || p.chQueue != nil {
		t.Fatalf("clickhouse %v queue %v", p.ch, p.chQueue)
	}
}

func TestMCPGetVesselTrack(t *testing.T) {
	p, _ := trackPipeline(t)
	var ages []time.Duration
	for m := 595; m >= 0; m -= 5 { // every five minutes for ten hours, oldest first as live traffic arrives
		ages = append(ages, time.Duration(m)*time.Minute)
	}
	sail(t, p, 257000001, ages...)
	cs := mcpClient(t, p)
	var out mcpTrack
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"mmsi": 257000001}, &out); msg != "" ||
		len(out.Positions) < 40 || len(out.Positions) > 51 || out.Attribution["kystverket"] == "" {
		t.Errorf("default spreads the limit over the vessel's ten hours: %q %d", msg, len(out.Positions))
	}
	first, _ := time.Parse(time.RFC3339, out.Positions[0].Seen)
	if time.Since(first) < 9*time.Hour {
		t.Errorf("spread should reach the oldest positions, first is %v", first)
	}
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"mmsi": 257000001, "interval_minutes": 60}, &out); msg != "" || len(out.Positions) < 9 || len(out.Positions) > 11 {
		t.Errorf("hourly: %q %d", msg, len(out.Positions))
	}
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"mmsi": 999999999}, &out); !strings.Contains(msg, "no vessel") {
		t.Errorf("unknown: %q", msg)
	}
}

func TestTrackRangeOutsideTheWindow(t *testing.T) {
	p, _ := trackPipeline(t)
	sail(t, p, 257000001, time.Hour)
	base := "/v1/vessels/257000001/track"
	long := time.Now().Add(-5 * 24 * time.Hour)
	for _, q := range []string{
		"?from=" + long.Format(time.RFC3339) + "&to=" + long.Add(time.Hour).Format(time.RFC3339),
		"?from=" + time.Now().Add(time.Hour).Format(time.RFC3339) + "&to=" + time.Now().Add(2*time.Hour).Format(time.RFC3339),
	} {
		tr := getTrack(t, p, base+q)
		from, _ := time.Parse(time.RFC3339, tr.Properties.From)
		to, _ := time.Parse(time.RFC3339, tr.Properties.To)
		if tr.Properties.Points != 0 || to.Before(from) {
			t.Errorf("%s: %d points, %s to %s", q, tr.Properties.Points, tr.Properties.From, tr.Properties.To)
		}
	}
}

// Reports whose stamps disagree with their fixes (AISHub snapshots, broken GPS) pass the ingest
// gate's teleport floor but draw kinks; the track leaves them out.
func TestTrackDespikesImpossibleSpeeds(t *testing.T) {
	p, ch := trackPipeline(t)
	now := time.Now()
	mk := func(mmsi uint32, age time.Duration, lat float64, sog float64) trackPoint {
		v := newVessel()
		v.Lat, v.Lon, v.Sog = lat, 10.7, sog
		return newTrackPoint(mmsi, now.Add(-age), v, "aishub")
	}
	points := []trackPoint{
		// north at 16 kn, one fix displaced 0.43 NM (102 kn implied), one 0.018 NM jitter pair
		mk(257000001, 10*time.Minute, 59.0, 16),
		mk(257000001, 9*time.Minute+30*time.Second, 59.00222, 16),
		mk(257000001, 9*time.Minute+15*time.Second, 59.00933, 16),
		mk(257000001, 9*time.Minute, 59.00444, 16),
		mk(257000001, 8*time.Minute+30*time.Second, 59.00666, 16),
		mk(257000001, 8*time.Minute+29*time.Second, 59.00696, 16),
		// a 45 kn vessel reporting 45 kn is fast, not implausible
		mk(257000002, 10*time.Minute, 59.0, 45),
		mk(257000002, 9*time.Minute+30*time.Second, 59.00625, 45),
		mk(257000002, 9*time.Minute, 59.0125, 45),
	}
	ch.insert(context.Background(), "", points)
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?interval=0"); tr.Properties.Points != 5 {
		t.Errorf("the displaced fix stays, jitter under the floor stays: %d points, want 5", tr.Properties.Points)
	}
	if tr := getTrack(t, p, "/v1/vessels/257000002/track?interval=0"); tr.Properties.Points != 3 {
		t.Errorf("a genuinely fast vessel lost fixes: %d points, want 3", tr.Properties.Points)
	}
}

// When every position disagrees with the anchor, the anchor is the bad fix (a stale or displaced
// first position): the run is capped, the uncorroborated anchor goes with it, and the track
// re-anchors instead of vanishing or drawing the jump.
func TestDespikeReanchorsAfterARun(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	pt := func(sec int, lat float64) trackPoint {
		return trackPoint{ts: now.Add(time.Duration(sec) * time.Second), lat6: int32(lat * 600000), lon6: 6420000, sog10: 1023}
	}
	// despike compacts its input in place, so each case builds its points fresh.
	far := func(head ...trackPoint) []trackPoint {
		points := head
		for i := 1; i <= 6; i++ {
			points = append(points, pt(i*30, 60+float64(i)*0.00001)) // 60 NM from the anchor, near-stationary
		}
		return points
	}
	at4 := pt(4*30, 0).ts // the fourth far point, where the cap re-anchors
	kept := despike(far(pt(0, 59.0)))
	if len(kept) != 3 || !kept[0].ts.Equal(at4) {
		t.Errorf("want the fourth far point onward, without the lone anchor; got %d points %v", len(kept), kept)
	}

	// A corroborated anchor stays: with real positions on both sides (a duplicate MMSI), the jump
	// is drawn once rather than either cluster being erased.
	kept = despike(far(pt(-30, 59.00001), pt(0, 59.0)))
	if len(kept) != 5 || kept[1].lat6 != int32(59.0*600000) || !kept[2].ts.Equal(at4) {
		t.Errorf("want both 59° points and the fourth far point onward; got %d points %v", len(kept), kept)
	}
}

func TestDefaultIntervalCoversTheRange(t *testing.T) {
	for _, c := range []struct {
		span  time.Duration
		limit int
		want  time.Duration
	}{
		{time.Hour, 1000, 5 * time.Second},           // 3.6 s a position, rounded up
		{20 * time.Minute, 1000, 0},                  // fits at full rate
		{24 * time.Hour, 1000, 2 * time.Minute},      // 86 s
		{24 * time.Hour, 200, 10 * time.Minute},      // anonymous: 432 s
		{7 * 24 * time.Hour, 1000, 15 * time.Minute}, // 605 s
		{7 * 24 * time.Hour, 1, 24 * time.Hour},      // the longest step
	} {
		if got := defaultInterval(c.span, c.limit); got != c.want {
			t.Errorf("%v over %d: %v, want %v", c.span, c.limit, got, c.want)
		}
	}

	// A vessel reporting every 10 seconds for six hours: without an interval the track spans all six hours
	// instead of the last half hour, and says what spacing it used.
	p, ch := trackPipeline(t)
	now := time.Now()
	var points []trackPoint
	v := newVessel()
	v.Lat, v.Lon = 59.9, 10.7
	for i := range 6 * 360 {
		points = append(points, newTrackPoint(257000001, now.Add(-time.Duration(i)*10*time.Second), v, "kystverket"))
	}
	ch.insert(context.Background(), "", points)
	var raw struct {
		Properties struct {
			Interval  int64
			Points    int
			Truncated bool
			Times     []string
		}
	}
	w := get(t, p, "/v1/vessels/257000001/track?limit=100")
	json.Unmarshal(w.Body.Bytes(), &raw)
	first, _ := time.Parse(time.RFC3339, raw.Properties.Times[0])
	if raw.Properties.Interval != 15*60 || raw.Properties.Truncated || now.Sub(first) < 5*time.Hour {
		t.Errorf("interval %d, %d points, truncated %v, first %v", raw.Properties.Interval, raw.Properties.Points, raw.Properties.Truncated, first)
	}
}
