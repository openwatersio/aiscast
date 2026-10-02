package main

import (
	"encoding/json"
	"math/rand/v2"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trackPipeline is a test pipeline with a vessel record and a track store in a temporary directory.
func trackPipeline(t testing.TB) (*Pipeline, string) {
	t.Helper()
	p := testPipeline(nil)
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.attachStore(st); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tracks.db")
	ts, err := openTracks(path)
	if err != nil {
		t.Fatal(err)
	}
	p.attachTracks(ts)
	t.Cleanup(func() { p.closeStore() })
	return p, path
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
}

type testTrack struct {
	Geometry *struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties struct {
		From, To  string
		Points    int
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
	p, _ := trackPipeline(t)
	p.ingestPacket("kystverket", "kystverket", time.Now(), time.Now(), shipStatic(257000001, "NORDIC STAR"))
	sail(t, p, 257000001, 30*time.Hour, 3*time.Hour, 2*time.Hour, 90*time.Minute, time.Hour, 10*time.Minute)
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
	old := time.Now().Add(-100 * time.Hour).UTC().Format(time.RFC3339)
	tr = getTrack(t, p, base+"?from="+old)
	if at, _ := time.Parse(time.RFC3339, tr.Properties.From); time.Since(at) > trackWindow+time.Minute {
		t.Errorf("from is clamped to the window: %s", tr.Properties.From)
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
	p, _ := trackPipeline(t)
	now := time.Now()
	var points []trackPoint
	for i := range 300 {
		v := newVessel()
		v.Lat, v.Lon = 59.9, 10.7
		points = append(points, newTrackPoint(257000001, now.Add(-time.Duration(i)*time.Minute), v, "kystverket"))
	}
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?limit=5000&interval=0"); tr.Properties.Points != 200 || !tr.Properties.Truncated {
		t.Errorf("anonymous cap: %d truncated %v", tr.Properties.Points, tr.Properties.Truncated)
	}
}

func TestTrackDaysExpire(t *testing.T) {
	p, path := trackPipeline(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	v := newVessel()
	v.Lat, v.Lon = 59.9, 10.7
	points := []trackPoint{
		newTrackPoint(257000001, now.Add(-72*time.Hour), v, "kystverket"), // older than any window: skipped
		newTrackPoint(257000001, now.Add(-36*time.Hour), v, "kystverket"),
		newTrackPoint(257000001, now.Add(-time.Hour), v, "digitraffic"),
	}
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
	if !p.tracks.days["20260927"] || !p.tracks.days["20260928"] || p.tracks.days["20260925"] {
		t.Errorf("day tables: %v", p.tracks.days)
	}
	got, _, err := p.tracks.track(257000001, now.Add(-48*time.Hour), now, 0, 10)
	if err != nil || len(got) != 2 || got[1].source != "digitraffic" {
		t.Errorf("read across days: %v %+v", err, got)
	}

	// Two days on, the window has left the 27th.
	if err := p.tracks.write(nil, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.tracks.days["20260927"] || !p.tracks.days["20260928"] {
		t.Errorf("expiry: %v", p.tracks.days)
	}

	// The day tables and source names survive a reopen.
	again, err := openTracks(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.close()
	got, _, err = again.track(257000001, now.Add(-2*time.Hour), now, 0, 10)
	if err != nil || len(got) != 1 || got[0].source != "digitraffic" || !again.days["20260928"] {
		t.Errorf("after reopen: %v %+v %v", err, got, again.days)
	}
}

func TestTrackWriteFailureRetries(t *testing.T) {
	p, _ := trackPipeline(t)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7))
	p.tracks.db.Close()
	if err := p.flushStore(); err == nil {
		t.Fatal("write to a closed database succeeded")
	}
	if len(p.trackQueue) != 1 || p.tracks.writeFailures.Load() != 1 {
		t.Errorf("failed positions not kept for the next flush: %d", len(p.trackQueue))
	}
}

func TestReplayPipelineKeepsNoTracks(t *testing.T) {
	p := testPipeline(t)
	p.ingestPacket("kystverket", "kystverket", time.Now(), time.Now(), posReport(257000001, 59.9, 10.7))
	if p.tracks != nil || p.trackQueue != nil {
		t.Fatalf("tracks %v queue %v", p.tracks, p.trackQueue)
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

// BenchmarkTrackWrite times a once-a-second flush: 400 positions from a 60k-vessel fleet into a day table
// already holding a few hundred thousand rows.
func BenchmarkTrackWrite(b *testing.B) {
	p, _ := trackPipeline(b)
	r := rand.New(rand.NewPCG(1, 2))
	v := newVessel()
	v.Lat, v.Lon, v.Sog, v.Cog = 59.9, 10.7, 11.2, 123.4
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	at := now.Add(-6 * time.Hour)
	batch := func() []trackPoint {
		pts := make([]trackPoint, 400)
		for i := range pts {
			at = at.Add(2500 * time.Microsecond)
			pts[i] = newTrackPoint(uint32(200000000+r.IntN(60000)), at, v, "aishub")
		}
		return pts
	}
	for range 1000 {
		if err := p.tracks.write(batch(), now); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := p.tracks.write(batch(), now); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/400, "ns/point")
}

// Raw reports with equal stamps survive dedupe as distinct data, so a track keeps both unless they are the
// same point.
func TestTrackKeepsEqualTimeReports(t *testing.T) {
	p, _ := trackPipeline(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	here, there := newVessel(), newVessel()
	here.Lat, here.Lon, there.Lat, there.Lon = 59.9, 10.7, 59.91, 10.71
	points := []trackPoint{
		newTrackPoint(257000001, at, here, "kystverket"),
		newTrackPoint(257000001, at, there, "station"),
		newTrackPoint(257000001, at, here, "aishub"), // the same point again
	}
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
	got, _, err := p.tracks.track(257000001, now.Add(-2*time.Hour), now, 0, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	sources := map[string]bool{got[0].source: true, got[1].source: true}
	if !sources["kystverket"] || !sources["station"] {
		t.Errorf("the first write of a point stays, and each keeps its source: %+v", got)
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
	w := httptest.NewRecorder()
	p.serveMetrics(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "aiscast_tracks_up 1") {
		t.Error("aiscast_tracks_up missing")
	}
}

// Every connection holds its own page cache, so both pools are bounded, and many readers wait their turn
// beside the writer rather than opening more.
func TestTrackStorePoolsAreBounded(t *testing.T) {
	p, _ := trackPipeline(t)
	if w, r := p.tracks.db.Stats().MaxOpenConnections, p.tracks.rdb.Stats().MaxOpenConnections; w != 1 || r != trackReaders {
		t.Fatalf("writer pool %d, reader pool %d", w, r)
	}
	if n := p.store.db.Stats().MaxOpenConnections; n != storeConns {
		t.Fatalf("record pool %d", n)
	}
	now := time.Now()
	v := newVessel()
	v.Lat, v.Lon = 59.9, 10.7
	done := make(chan error, 32)
	for i := range 32 {
		go func() {
			_, _, err := p.tracks.track(257000001, now.Add(-time.Hour), now, 0, 100)
			done <- err
		}()
		if err := p.tracks.write([]trackPoint{newTrackPoint(257000001, now.Add(-time.Duration(i)*time.Second), v, "kystverket")}, now); err != nil {
			t.Fatal(err)
		}
	}
	for range 32 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if open := p.tracks.rdb.Stats().OpenConnections; open > trackReaders {
		t.Errorf("%d reader connections open", open)
	}
}

// Reports whose stamps disagree with their fixes (AISHub snapshots, broken GPS) pass the ingest
// gate's teleport floor but draw kinks; the track leaves them out.
func TestTrackDespikesImpossibleSpeeds(t *testing.T) {
	p, _ := trackPipeline(t)
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
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
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

// The row past the limit anchors despiking: the page's oldest row is judged the same way a larger
// request would judge it, not kept unconditionally as the first point seen.
func TestTrackDespikeAnchorsAcrossTheLimit(t *testing.T) {
	p, _ := trackPipeline(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	mk := func(age time.Duration, lat float64) trackPoint {
		v := newVessel()
		v.Lat, v.Lon, v.Sog = lat, 10.7, 16
		return newTrackPoint(257000001, now.Add(-age), v, "aishub")
	}
	points := []trackPoint{
		mk(2*time.Minute, 59.0),
		mk(90*time.Second, 59.00111),
		mk(60*time.Second, 59.00933), // displaced 0.49 NM ahead: 59 kn against the row past the limit
		mk(30*time.Second, 59.00333),
	}
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
	got, more, err := p.tracks.track(257000001, now.Add(-time.Hour), now, 0, 2)
	if err != nil || !more || len(got) != 1 || got[0].lat6 != points[3].lat6 {
		t.Errorf("the displaced fix should fall to the anchor past the limit: more=%v err=%v %+v", more, err, got)
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
	p, _ := trackPipeline(t)
	now := time.Now()
	var points []trackPoint
	v := newVessel()
	v.Lat, v.Lon = 59.9, 10.7
	for i := range 6 * 360 {
		points = append(points, newTrackPoint(257000001, now.Add(-time.Duration(i)*10*time.Second), v, "kystverket"))
	}
	if err := p.tracks.write(points, now); err != nil {
		t.Fatal(err)
	}
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
