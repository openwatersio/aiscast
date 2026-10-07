package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// Outlines as ClickHouse's h3ToGeoBoundary gives them, written as "lat lon" pairs.
const (
	osloRes3        = 590140463659352063
	osloRes3Outline = "59.952276 9.350687, 59.466549 9.435996, 59.303039 10.432407, 59.622978 11.359873, 60.110099 11.292042, 60.275949 10.279089"
	osloRes6        = 603651209793896447
	osloRes6Outline = "59.910064 10.638236, 59.883714 10.622234, 59.867001 10.664831, 59.876626 10.723448, 59.902972 10.739506, 59.919696 10.696891"
	beringRes3      = 590364764031418367 // straddles the antimeridian
	beringOutline   = "51.534796 -179.919138, 52.143367 -179.774261, 52.543947 179.402582, 52.331315 178.435503, 51.720163 178.307893, 51.324156 179.129901"
)

// outline splits a fixture into the latitudes and longitudes ClickHouse returns.
func outline(t *testing.T, s string) (lats, lons []float64) {
	t.Helper()
	for _, pt := range strings.Split(s, ", ") {
		var lat, lon float64
		if _, err := fmt.Sscanf(pt, "%g %g", &lat, &lon); err != nil {
			t.Fatalf("outline %q: %v", pt, err)
		}
		lats, lons = append(lats, lat), append(lons, lon)
	}
	return lats, lons
}

func covRowOf(t *testing.T, res int, cell uint64, vessels uint64, days int, s string) covRow {
	lats, lons := outline(t, s)
	return covRow{res: res, cell: cell, vessels: vessels, days: days, lats: lats, lons: lons}
}

// fakeCoverageSource holds coverage for some days and answers the window the map asks for.
type fakeCoverageSource struct {
	days         []string // days with coverage
	cells        []covRow
	stations     map[string][]covRow // each station's cells
	stationLoads int
	loading      chan string   // when set, each station load sends its station here
	release      chan struct{} // when set, each station load waits for it
	backfills    int
	first, last  time.Time // the window last asked for
}

func (f *fakeCoverageSource) coverageBackfill(context.Context, time.Time) error {
	f.backfills++
	return nil
}

func (f *fakeCoverageSource) coverageDays(_ context.Context, first, last time.Time) ([]string, error) {
	f.first, f.last = first, last
	var out []string
	for _, d := range f.days {
		if day, _ := time.Parse("2006-01-02", d); !day.Before(first) && !day.After(last) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeCoverageSource) coverageCells(_ context.Context, _, _ time.Time, each func(covRow) error) error {
	for _, r := range f.cells {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCoverageSource) coverageStations(context.Context, time.Time, time.Time) ([]string, error) {
	return slices.Collect(maps.Keys(f.stations)), nil
}

func (f *fakeCoverageSource) stationCoverageCells(_ context.Context, station string, first, last time.Time, each func(covRow) error) error {
	f.stationLoads++
	f.first, f.last = first, last
	if f.loading != nil {
		f.loading <- station
		<-f.release
	}
	for _, r := range f.stations[station] {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func coveragePipeline(t *testing.T, f *fakeCoverageSource) *Pipeline {
	t.Helper()
	p := testPipeline(t)
	p.coverage = newCoverageMap()
	if err := p.coverage.load(context.Background(), f, time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return p
}

type covFeature struct {
	ring  [][2]int32
	props map[string]any
}

// decodeCoverageTile reads the coverage layer back into polygons keyed by cell, checking each is a single
// exterior ring: MoveTo, LineTo, ClosePath, with positive area.
func decodeCoverageTile(t *testing.T, b []byte) map[uint64]covFeature {
	t.Helper()
	out := map[uint64]covFeature{}
	for _, lf := range pbDecode(t, b) {
		var keys []string
		var values []any
		var feats [][]byte
		for _, f := range pbDecode(t, lf.data) {
			switch f.num {
			case 1:
				if string(f.data) != coverageLayer {
					t.Fatalf("layer %q", f.data)
				}
			case 2:
				feats = append(feats, f.data)
			case 3:
				keys = append(keys, string(f.data))
			case 4:
				v := pbDecode(t, f.data)[0]
				if v.num == 3 {
					values = append(values, math.Float64frombits(v.v))
				} else {
					values = append(values, v.v)
				}
			}
		}
		for _, fb := range feats {
			var id uint64
			c := covFeature{props: map[string]any{}}
			for _, f := range pbDecode(t, fb) {
				switch f.num {
				case 1:
					id = f.v
				case 2:
					tags := pbPacked(f.data)
					for i := 0; i < len(tags); i += 2 {
						c.props[keys[tags[i]]] = values[tags[i+1]]
					}
				case 3:
					if f.v != 3 {
						t.Fatalf("feature type %d, want polygon", f.v)
					}
				case 4:
					g := pbPacked(f.data)
					unzig := func(u uint64) int32 { return int32(u>>1) ^ -int32(u&1) }
					n := int(g[3] >> 3)
					if g[0] != 9 || g[3]&7 != 2 || len(g) != 4+2*n+1 || g[len(g)-1] != 15 {
						t.Fatalf("geometry %v is not one closed ring", g)
					}
					pt := [2]int32{unzig(g[1]), unzig(g[2])}
					c.ring = append(c.ring, pt)
					for i := 0; i < n; i++ {
						pt[0] += unzig(g[4+2*i])
						pt[1] += unzig(g[5+2*i])
						c.ring = append(c.ring, pt)
					}
					var area int64
					for k := range c.ring {
						p, q := c.ring[k], c.ring[(k+1)%len(c.ring)]
						area += int64(p[0])*int64(q[1]) - int64(q[0])*int64(p[1])
					}
					if area <= 0 {
						t.Fatalf("ring %v has area %d; an exterior ring's is positive", c.ring, area)
					}
				}
			}
			out[id] = c
		}
	}
	return out
}

func TestCoverageTiles(t *testing.T) {
	// The window is the 7 complete days before now: 2026-09-20 is before it, 2026-09-25 has no coverage, and
	// 2026-09-30 is today and not over.
	oslo3 := covRowOf(t, 3, osloRes3, 60, 6, osloRes3Outline)
	oslo3.stations = 3
	f := &fakeCoverageSource{
		days: []string{"2026-09-20", "2026-09-23", "2026-09-24", "2026-09-26", "2026-09-27", "2026-09-28", "2026-09-29", "2026-09-30"},
		cells: []covRow{
			oslo3,
			covRowOf(t, 6, osloRes6, 12, 3, osloRes6Outline),
			covRowOf(t, 3, beringRes3, 3, 1, beringOutline),
		},
	}
	p := coveragePipeline(t, f)
	if f.first.Format("2006-01-02") != "2026-09-23" || f.last.Format("2006-01-02") != "2026-09-29" {
		t.Fatalf("window asked for %v to %v, want 2026-09-23 to 2026-09-29", f.first, f.last)
	}
	h := httpHandler(p)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}

	w := get("http://ais.example/v1/coverage/tiles.json")
	var tj struct {
		Tiles   []string
		MaxZoom int
		Window  struct {
			From, To string
			Days     int
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tj); err != nil || w.Code != 200 {
		t.Fatalf("%d %v: %s", w.Code, err, w.Body)
	}
	if len(tj.Tiles) != 1 || tj.Tiles[0] != "http://ais.example/v1/coverage/tiles/{z}/{x}/{y}" || tj.MaxZoom != coverageMaxZoom {
		t.Fatalf("tilejson %+v", tj)
	}
	if tj.Window.From != "2026-09-23" || tj.Window.To != "2026-09-29" || tj.Window.Days != 6 {
		t.Fatalf("window %+v, want 2026-09-23 to 2026-09-29 with 6 packaged days", tj.Window)
	}

	tile := func(z, x, y int) map[uint64]covFeature {
		t.Helper()
		w := get(fmt.Sprintf("/v1/coverage/tiles/%d/%d/%d", z, x, y))
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/vnd.mapbox-vector-tile" {
			t.Fatalf("tile %d/%d/%d: %d %s", z, x, y, w.Code, w.Body)
		}
		return decodeCoverageTile(t, w.Body.Bytes())
	}

	// z0 draws resolution 3: vessels a day is the window's sum over its packaged days
	fs := tile(0, 0, 0)
	if len(fs) != 2 || fs[osloRes3].props["vessels"] != 10.0 || fs[osloRes3].props["days"] != uint64(6) || fs[osloRes3].props["stations"] != uint64(3) {
		t.Fatalf("z0: %v", fs)
	}
	if b := fs[beringRes3]; len(b.ring) != 6 {
		t.Fatalf("antimeridian cell: %v", b)
	}
	// resolution 3 runs to z4, found through the z0 index
	x, y := tileOf(59.9, 10.7, 4)
	if fs := tile(4, x, y); len(fs) != 1 || fs[osloRes3].props == nil {
		t.Fatalf("z4: %v", fs)
	}
	// z9 draws resolution 6, and only the cells in the tile
	x, y = tileOf(59.9, 10.7, 9)
	fs = tile(9, x, y)
	if len(fs) != 1 || fs[osloRes6].props["vessels"] != 2.0 || fs[osloRes6].props["days"] != uint64(3) {
		t.Fatalf("z9: %v", fs)
	}
	inside := false
	for _, pt := range fs[osloRes6].ring {
		inside = inside || pt[0] >= 0 && pt[0] <= tileExtent && pt[1] >= 0 && pt[1] <= tileExtent
	}
	if !inside {
		t.Fatalf("no vertex of the cell is in the tile: %v", fs[osloRes6].ring)
	}
	if fs := tile(9, x+2, y); len(fs) != 0 {
		t.Fatalf("a tile nobody heard: %v", fs)
	}
	if w := get("/v1/coverage/tiles/2/4/0"); w.Code != 400 {
		t.Errorf("out of range: %d", w.Code)
	}
}

// ?station= answers one station's cells over the network's window, with a TileJSON whose tiles carry the station
// and whose bounds are its cells. A station is loaded once per network load, a station with no cells is 404
// without a query, so ids anyone can make up cost nothing, and its tiles never come from the network's cache
// entries or another station's.
func TestCoverageStationTiles(t *testing.T) {
	f := &fakeCoverageSource{
		days:  []string{"2026-09-23", "2026-09-24", "2026-09-26", "2026-09-27", "2026-09-28", "2026-09-29"},
		cells: []covRow{covRowOf(t, 3, osloRes3, 60, 6, osloRes3Outline), covRowOf(t, 6, osloRes6, 12, 3, osloRes6Outline)},
		stations: map[string][]covRow{
			"station:harbor/east": {covRowOf(t, 3, osloRes3, 6, 2, osloRes3Outline), covRowOf(t, 6, osloRes6, 3, 2, osloRes6Outline)},
			"station:harbor/west": {covRowOf(t, 3, osloRes3, 12, 2, osloRes3Outline)},
		},
	}
	p := coveragePipeline(t, f)
	h := httpHandler(p)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	vessels := func(path string, cell uint64) any {
		t.Helper()
		w := get(path)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return decodeCoverageTile(t, w.Body.Bytes())[cell].props["vessels"]
	}

	w := get("http://ais.example/v1/coverage/tiles.json?station=station:harbor/east")
	var tj struct {
		Tiles  []string
		Bounds []float64
		Fit    []float64
		Window struct{ From, To string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tj); err != nil || w.Code != 200 {
		t.Fatalf("%d %v: %s", w.Code, err, w.Body)
	}
	if len(tj.Tiles) != 1 || tj.Tiles[0] != "http://ais.example/v1/coverage/tiles/{z}/{x}/{y}?station=station%3Aharbor%2Feast" {
		t.Errorf("tiles %v", tj.Tiles)
	}
	// bounds take in the resolution-3 cell drawn at low zooms, and fit only the finest cell
	if b := tj.Bounds; len(b) != 4 || b[0] > 9.36 || b[2] < 11.35 || b[1] > 59.31 || b[3] < 60.27 {
		t.Errorf("bounds %v, want the resolution-3 Oslo cell's extent", b)
	}
	if b := tj.Fit; len(b) != 4 || b[0] > 10.7 || b[2] < 10.7 || b[1] > 59.9 || b[3] < 59.9 || b[2]-b[0] > 0.2 || b[3]-b[1] > 0.2 {
		t.Errorf("fit %v, want a box around the resolution-6 Oslo cell", b)
	}
	if tj.Window.From != "2026-09-23" || tj.Window.To != "2026-09-29" || f.first.Format("2006-01-02") != "2026-09-23" || f.last.Format("2006-01-02") != "2026-09-29" {
		t.Errorf("window %+v, asked for %v to %v; want the network's", tj.Window, f.first, f.last)
	}

	// The network's tile first, then each station's at the same address: each its own, averaged over the
	// network's 6 days.
	if v := vessels("/v1/coverage/tiles/0/0/0", osloRes3); v != 10.0 {
		t.Errorf("network: %v vessels a day", v)
	}
	if v := vessels("/v1/coverage/tiles/0/0/0?station=station:harbor/east", osloRes3); v != 1.0 {
		t.Errorf("east: %v vessels a day", v)
	}
	if v := vessels("/v1/coverage/tiles/0/0/0?station=station%3Aharbor%2Fwest", osloRes3); v != 2.0 {
		t.Errorf("west: %v vessels a day", v)
	}
	x, y := tileOf(59.9, 10.7, 9)
	if v := vessels(fmt.Sprintf("/v1/coverage/tiles/9/%d/%d?station=station:harbor/east", x, y), osloRes6); v != 0.5 {
		t.Errorf("east at z9: %v vessels a day", v)
	}
	if f.stationLoads != 2 {
		t.Errorf("%d station loads for 2 stations", f.stationLoads)
	}

	for _, path := range []string{"/v1/coverage/tiles.json?station=station:nobody", "/v1/coverage/tiles/0/0/0?station=station:nobody"} {
		if w := get(path); w.Code != 404 {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
	if f.stationLoads != 2 {
		t.Errorf("a station with no cells loaded %d times, want none", f.stationLoads-2)
	}
	if w := get("/v1/coverage/tiles.json?station=" + strings.Repeat("x", 300)); w.Code != 400 {
		t.Errorf("a 300-byte station: %d, want 400", w.Code)
	}

	// A network load replaces every station's, and the stations it knows.
	f.stations["station:harbor/east"] = []covRow{covRowOf(t, 3, osloRes3, 30, 2, osloRes3Outline)}
	f.stations["station:new"] = []covRow{covRowOf(t, 3, osloRes3, 6, 1, osloRes3Outline)}
	if err := p.coverage.load(context.Background(), f, time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if v := vessels("/v1/coverage/tiles/0/0/0?station=station:harbor/east", osloRes3); v != 5.0 {
		t.Errorf("east after a reload: %v vessels a day", v)
	}
	if v := vessels("/v1/coverage/tiles/0/0/0?station=station:new", osloRes3); v != 1.0 {
		t.Errorf("a station new to the reload: %v vessels a day", v)
	}
}

func TestCoverageUnavailable(t *testing.T) {
	check := func(name string, p *Pipeline) {
		t.Helper()
		for _, path := range []string{"/v1/coverage/tiles.json", "/v1/coverage/tiles/0/0/0", "/v1/coverage/tiles.json?station=station:a", "/v1/coverage/tiles/0/0/0?station=station:a"} {
			w := httptest.NewRecorder()
			httpHandler(p).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 503 {
				t.Errorf("%s %s: %d, want 503", name, path, w.Code)
			}
		}
	}
	check("without ClickHouse", testPipeline(t))
	check("with no coverage in the window", coveragePipeline(t, &fakeCoverageSource{days: []string{"2026-09-01", "2026-09-30"}}))
}

func TestCoverageBands(t *testing.T) {
	want := []int{3, 3, 3, 3, 3, 4, 4, 5, 5, 6, 6, 6, 6}
	for z, res := range want {
		if got := coverageBands[coverageBand(z)].res; got != res {
			t.Errorf("z%d draws resolution %d, want %d", z, got, res)
		}
	}
}

func TestCoverageCellKeepsAntimeridianCellsWhole(t *testing.T) {
	lats, lons := outline(t, beringOutline)
	c, err := covCellFrom(beringRes3, lats, lons)
	if err != nil {
		t.Fatal(err)
	}
	if c.maxX-c.minX > 0.01 || c.minX < 0.99 {
		t.Fatalf("x from %v to %v: the cell split across the world", c.minX, c.maxX)
	}
	for _, bad := range [][2][]float64{{nil, nil}, {{1, 2}, {3, 4}}, {{1, 2, 3}, {4, 5}}} {
		if _, err := covCellFrom(1, bad[0], bad[1]); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

// TestCoverageFromClickHouse runs against a real server named by CLICKHOUSE_TEST_URL, in a database of its own
// that it drops afterward: the views fill station_coverage and coverage as receptions are written, counting each
// vessel once however many copies arrive and skipping implausible copies and those older than coverage keeps, a
// backfill over the same days counts nothing twice, and a day the backfill has done it never bins again. The
// network's cells merge stations, counting a feed's receivers as its one source, and a station's are its own.
func TestCoverageFromClickHouse(t *testing.T) {
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

	day1 := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	day2 := day1.AddDate(0, 0, 1)
	at := func(mmsi uint32, ts time.Time) trackPoint {
		return trackPoint{mmsi: mmsi, ts: ts, lat6: int32(59.9 * 600000), lon6: int32(10.7 * 600000), sog10: 1023, cog10: 3600,
			heading: 511, navStatus: 15, source: "kystverket", station: "kystverket"}
	}
	// Two vessels on the first day, one of them twice, and one of them again on the second. The second vessel's
	// transmission also arrives as a later copy from another station, which adds a station but no vessel, and
	// from another receiver of the same feed, which adds neither. A copy the fold judged implausible, far from
	// the others, from a third station, adds no cell and no station.
	first := at(2, day1.Add(3*time.Hour))
	first.txAt, first.txDisc = first.ts, 7
	again := first
	again.source, again.station, again.recv, again.dup = "station", "station:other", first.ts.Add(2*time.Second), true
	path := first
	path.station, path.recv, path.dup = "kystverket/2573010", first.ts.Add(time.Second), true
	wild := at(5, day1.Add(4*time.Hour))
	wild.lat6, wild.lon6, wild.implausible, wild.station = int32(10*600000), int32(10*600000), true, "station:wild"
	batch := []trackPoint{at(1, day1.Add(time.Hour)), at(1, day1.Add(2*time.Hour)), first, again, path, wild, at(1, day2.Add(time.Hour))}
	if err := conn.insert(ctx, "coverage", batch); err != nil {
		t.Fatal(err)
	}

	cells := func() map[uint64]covRow {
		t.Helper()
		out := map[uint64]covRow{}
		if err := conn.coverageCells(ctx, day1, day2, func(r covRow) error { out[r.cell] = r; return nil }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(when string) {
		t.Helper()
		got := cells()
		oslo, ok := got[osloRes6]
		if !ok {
			t.Fatalf("%s: no resolution-6 cell for Oslo among %d cells", when, len(got))
		}
		if oslo.res != 6 || oslo.vessels != 3 || oslo.days != 2 || oslo.stations != 2 {
			t.Errorf("%s: Oslo at resolution 6 is %+v, want 2 + 1 vessels over 2 days from 2 stations", when, oslo)
		}
		if r := got[osloRes3]; r.res != 3 || r.vessels != 3 || r.stations != 2 {
			t.Errorf("%s: the resolution-3 cell containing it is %+v", when, r)
		}
		if len(got) != len(coverageBands) {
			t.Errorf("%s: %d cells, want one per resolution", when, len(got))
		}
		for station, want := range map[string]covRow{"kystverket": {vessels: 3, days: 2}, "kystverket/2573010": {vessels: 1, days: 1}, "station:other": {vessels: 1, days: 1}} {
			var r covRow
			if err := conn.stationCoverageCells(ctx, station, day1, day2, func(c covRow) error {
				if c.cell == osloRes6 {
					r = c
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if r.vessels != want.vessels || r.days != want.days || r.stations != 1 {
				t.Errorf("%s: %s's Oslo cell is %+v, want %d vessels over %d days", when, station, r, want.vessels, want.days)
			}
		}
		if ids, err := conn.coverageStations(ctx, day1, day2); err != nil || !slices.Equal(slices.Sorted(slices.Values(ids)), []string{"kystverket", "kystverket/2573010", "station:other"}) {
			t.Errorf("%s: stations %v, %v", when, ids, err)
		}
		if err := conn.stationCoverageCells(ctx, "station:wild", day1, day2, func(c covRow) error {
			t.Errorf("%s: the implausible station has cell %d", when, c.cell)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// coverage is written as well, so a server rolled back to reading it still has the map.
		var vessels, stations uint64
		if err := conn.conn.QueryRow(ctx, "SELECT uniqExactMerge(vessels), uniqExactMerge(stations) FROM "+db+".coverage WHERE res = 6 AND cell = ? AND day = ?",
			osloRes6, day1).Scan(&vessels, &stations); err != nil || vessels != 2 || stations != 2 {
			t.Errorf("%s: coverage holds %d vessels from %d stations on the first day, %v; want 2 from 2", when, vessels, stations, err)
		}
		if _, err := covCellFrom(oslo.cell, oslo.lats, oslo.lons); err != nil || len(oslo.lats) != 6 || math.Abs(oslo.lats[0]-59.9) > 0.1 || math.Abs(oslo.lons[0]-10.7) > 0.2 {
			t.Errorf("%s: outline %v %v: %v", when, oslo.lats, oslo.lons, err)
		}
	}
	check("from the view")

	// A load of old history: positions older than coverage keeps bin nothing.
	old := time.Now().UTC().AddDate(0, -14, 0).Truncate(24 * time.Hour)
	if err := conn.insert(ctx, "old", []trackPoint{at(3, old.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if days, err := conn.coverageDays(ctx, old, old); err != nil || len(days) != 0 {
		t.Errorf("a position 14 months old was binned: days %v, %v", days, err)
	}
	days, err := conn.coverageDays(ctx, day1, day2.AddDate(0, 0, 5))
	if err != nil || len(days) != 2 || days[0] != day1.Format("2006-01-02") {
		t.Fatalf("days %v: %v", days, err)
	}

	if err := conn.coverageBackfill(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	check("after a backfill over the same days")

	// With coverage emptied, a second backfill finds both days done and leaves it empty.
	if err := conn.exec(ctx, "TRUNCATE TABLE {db}.coverage", "TRUNCATE TABLE {db}.station_coverage"); err != nil {
		t.Fatal(err)
	}
	if err := conn.coverageBackfill(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := cells(); len(got) != 0 {
		t.Errorf("a backfilled day was binned again: %d cells", len(got))
	}

	// With the record cleared too, a backfill since the second day bins only it, as the map's window is binned
	// before the rest, and a backfill of everything then adds the first.
	if err := conn.exec(ctx, "TRUNCATE TABLE {db}.coverage_backfilled", "TRUNCATE TABLE {db}.station_coverage_backfilled"); err != nil {
		t.Fatal(err)
	}
	if err := conn.coverageBackfill(ctx, day2); err != nil {
		t.Fatal(err)
	}
	if days, err := conn.coverageDays(ctx, day1, day2); err != nil || len(days) != 1 || days[0] != day2.Format("2006-01-02") {
		t.Errorf("a backfill since %s binned %v, %v", day2.Format("2006-01-02"), days, err)
	}
	if err := conn.coverageBackfill(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	check("after the rest is backfilled")
}

// A database at step 9 has coverage without stations and a view that does not fill them, and no
// station_coverage. Steps 10 to 12 add the column, point the view at it, and clear the backfill's record, so the
// backfill bins the old days again with their stations and new receptions arrive with theirs. Steps 15 to 17 add
// station_coverage, which the backfill bins from the same receptions and the network map then reads.
func TestCoverageStationsMigrateInPlace(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	dsn := strings.TrimRight(url, "/") + "/" + db
	conn, err := openClickHouse(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })

	step9Select := strings.Replace(chCoverageSelect("{db}.receptions", chUsable), ", uniqExactState("+chCoverageStation+") AS stations", "", 1)
	step9Table := strings.Replace(chCoverageTable, ",\n\tstations AggregateFunction(uniqExact, String)", "", 1)
	if step9Select == chCoverageSelect("{db}.receptions", chUsable) || step9Table == chCoverageTable {
		t.Fatal("the step-9 coverage definitions no longer differ from the current ones by stations")
	}
	if err := conn.exec(ctx,
		"DROP VIEW {db}.coverage_mv",
		"DROP TABLE {db}.coverage",
		"DROP VIEW {db}.station_coverage_mv",
		"DROP TABLE {db}.station_coverage",
		"DROP TABLE {db}.station_coverage_backfilled",
		step9Table,
		"CREATE MATERIALIZED VIEW {db}.coverage_mv TO {db}.coverage AS "+step9Select,
		"ALTER TABLE {db}.schema_migrations DELETE WHERE version > 9 SETTINGS mutations_sync = 2",
	); err != nil {
		t.Fatal(err)
	}

	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	at := func(mmsi uint32, source, station string, ts time.Time) trackPoint {
		return trackPoint{mmsi: mmsi, ts: ts, lat6: int32(59.9 * 600000), lon6: int32(10.7 * 600000), sog10: 1023, cog10: 3600,
			heading: 511, navStatus: 15, source: source, station: station}
	}
	if err := conn.insert(ctx, "old", []trackPoint{at(1, "aishub", "aishub", day.Add(time.Hour)), at(2, "station", "station:a", day.Add(2*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	// as a step-9 server's backfill leaves it, with the day binned without stations
	if err := conn.exec(ctx, "INSERT INTO {db}.coverage_backfilled VALUES ('"+day.Format("2006-01-02")+"')"); err != nil {
		t.Fatal(err)
	}
	// coverage, read as a server rolled back to it does, and the network map's cells from station_coverage
	oslo := func() (coverage, network covRow) {
		t.Helper()
		var stations uint64
		if err := conn.conn.QueryRow(ctx, "SELECT uniqExactMerge(vessels), uniqExactMerge(stations) FROM "+db+".coverage WHERE res = 6 AND cell = ? AND day = ?",
			osloRes6, day).Scan(&coverage.vessels, &stations); err != nil {
			t.Fatal(err)
		}
		coverage.stations = int(stations)
		if err := conn.coverageCells(ctx, day, day, func(r covRow) error {
			if r.cell == osloRes6 {
				network = r
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return coverage, network
	}

	conn.conn.Close()
	if conn, err = openClickHouse(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if err := conn.coverageBackfill(ctx, day); err != nil {
		t.Fatal(err)
	}
	if c, n := oslo(); c.vessels != 2 || c.stations != 2 || n.vessels != 2 || n.stations != 2 {
		t.Errorf("after steps 10 to 17 and the backfill Oslo is %+v in coverage and %+v on the map, want 2 vessels from 2 stations", c, n)
	}
	if err := conn.insert(ctx, "new", []trackPoint{at(3, "udp", "udp:b", day.Add(3*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if c, n := oslo(); c.vessels != 3 || c.stations != 3 || n.vessels != 3 || n.stations != 3 {
		t.Errorf("after a reception from a third station Oslo is %+v in coverage and %+v on the map, want 3 vessels from 3 stations", c, n)
	}
}

// chStationKey keeps a station whole, a slash in a token's subject or a feed's receiver included, and folds a
// volunteer's TAG stream away only from receptions written before ingest kept them under the receiver.
func TestStationKey(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/default")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.conn.Close()
	before, after := "2026-10-05 15:29:59.999", "2026-10-05 15:30:00.000"
	for _, c := range []struct{ source, station, ts, want string }{
		{"station", "station:mmsi:368168720/n2k", before, "station:mmsi:368168720"},
		{"udp", "udp:1.2.3.4/ch", before, "udp:1.2.3.4"},
		{"station", "station:harbor/east", after, "station:harbor/east"},
		{"station", "station:harbor", before, "station:harbor"},
		{"kystverket", "kystverket/2573010", before, "kystverket/2573010"},
		{"barentswatch", "barentswatch/terra", after, "barentswatch/terra"},
	} {
		var got string
		if err := conn.conn.QueryRow(ctx, "SELECT "+chStationKey+" FROM (SELECT ? AS source, ? AS station, toDateTime64(?, 3, 'UTC') AS ts)",
			c.source, c.station, c.ts).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s %s at %s: %q, want %q", c.source, c.station, c.ts, got, c.want)
		}
	}
	// The view's own query, where the folded station is aliased to the column it folds and grouped by.
	row := "(SELECT toUInt32(1) AS mmsi, toDateTime64('" + before + "', 3, 'UTC') AS ts, toInt32(36000000) AS lat6, toInt32(6000000) AS lon6," +
		" 'station' AS source, 'station:a/n2k' AS station, false AS implausible, false AS clock_bad)"
	q := chStationCoverageSelect(row, chUsable)
	got, err := chColumn[string](ctx, conn.conn, "SELECT DISTINCT station FROM ("+q[:strings.LastIndex(q, "SETTINGS")]+") SETTINGS geotoh3_argument_order = 'lat_lon'")
	if err != nil || !slices.Equal(got, []string{"station:a"}) {
		t.Errorf("the view bins a stream from before the cutoff as %v, %v; want station:a", got, err)
	}
}

// square is a cell outline a hundredth of a degree across at lat, lon, in coverageCells' order.
func square(lat, lon float64) ([]float64, []float64) {
	return []float64{lat, lat - 0.01, lat - 0.01, lat}, []float64{lon, lon, lon + 0.01, lon + 0.01}
}

// A station's TileJSON bounds take in every cell, since a map requests no tiles past them, and its fit leaves out
// the few farthest.
func TestCoverageStationBounds(t *testing.T) {
	var cells []covRow
	for i := range 40 {
		lats, lons := square(59.9, 10.5+float64(i)*0.01)
		cells = append(cells, covRow{res: 6, cell: uint64(i + 1), vessels: 1, days: 1, lats: lats, lons: lons})
	}
	lats, lons := square(55.7, 12.6) // Copenhagen, once
	cells = append(cells, covRow{res: 6, cell: 99, vessels: 1, days: 1, lats: lats, lons: lons})
	// and a cell half a degree wide, the farthest east, which bounds take in whole whatever the others' sizes
	cells = append(cells, covRow{res: 6, cell: 98, vessels: 1, days: 1, lats: []float64{70.0, 69.9, 69.9, 70.0}, lons: []float64{20.0, 20.0, 20.5, 20.5}})
	f := &fakeCoverageSource{days: []string{"2026-09-29"}, cells: cells, stations: map[string][]covRow{"station:roving": cells}}
	p := coveragePipeline(t, f)
	w := httptest.NewRecorder()
	httpHandler(p).ServeHTTP(w, httptest.NewRequest("GET", "/v1/coverage/tiles.json?station=station:roving", nil))
	var tj struct{ Bounds, Fit []float64 }
	if err := json.Unmarshal(w.Body.Bytes(), &tj); err != nil || w.Code != 200 {
		t.Fatalf("%d %v: %s", w.Code, err, w.Body)
	}
	if b := tj.Bounds; len(b) != 4 || b[1] > 55.7 || b[2] < 20.49 || b[0] > 10.5 || b[3] < 69.99 {
		t.Errorf("bounds %v leave out a cell the tiles draw", b)
	}
	if b := tj.Fit; len(b) != 4 || b[1] < 59 || b[0] > 10.6 || b[2] < 10.85 || b[2] > 11 || b[3] > 60 {
		t.Errorf("fit %v, want the Oslo cells without Copenhagen", b)
	}
	w = httptest.NewRecorder()
	httpHandler(p).ServeHTTP(w, httptest.NewRequest("GET", "/v1/coverage/tiles.json", nil))
	if strings.Contains(w.Body.String(), `"fit"`) {
		t.Errorf("the network's TileJSON has a fit: %s", w.Body)
	}
}

// One station's load, however slow, leaves stations already loaded and ids no station has answering, and a
// request waiting behind it gives up when its client does.
func TestCoverageStationLoadBlocksNoOne(t *testing.T) {
	row := covRowOf(t, 3, osloRes3, 6, 1, osloRes3Outline)
	f := &fakeCoverageSource{days: []string{"2026-09-29"}, cells: []covRow{row},
		stations: map[string][]covRow{"station:a": {row}, "station:slow": {row}, "station:next": {row}}}
	p := coveragePipeline(t, f)
	h := httpHandler(p)
	get := func(ctx context.Context, station string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/coverage/tiles/0/0/0?station="+station, nil).WithContext(ctx))
		return w.Code
	}
	if code := get(context.Background(), "station:a"); code != 200 {
		t.Fatalf("station:a: %d", code)
	}
	f.loading, f.release = make(chan string), make(chan struct{})
	done := make(chan int)
	go func() { done <- get(context.Background(), "station:slow") }()
	<-f.loading // the slow load has started and holds the loader
	answered := make(chan [2]int)
	go func() {
		answered <- [2]int{get(context.Background(), "station:a"), get(context.Background(), "station:nobody")}
	}()
	select {
	case codes := <-answered:
		if codes != [2]int{200, 404} {
			t.Errorf("cached and unknown stations answered %v, want 200 and 404", codes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cached or unknown station waited on another station's load")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan int)
	go func() { waited <- get(ctx, "station:next") }()
	cancel()
	select {
	case code := <-waited:
		if code == 200 {
			t.Errorf("a request its client gave up on loaded anyway")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request waiting for the loader kept waiting after its client left")
	}
	close(f.release)
	if code := <-done; code != 200 {
		t.Errorf("the slow station: %d", code)
	}
}

// Stations' tiles are cached apart from the network's, and the cells kept for stations are bounded by size, a
// multiple of the network's, so a station walked past is loaded again rather than held.
func TestCoverageStationCaches(t *testing.T) {
	row := covRowOf(t, 3, osloRes3, 6, 1, osloRes3Outline)
	three := []covRow{row, covRowOf(t, 6, osloRes6, 3, 1, osloRes6Outline), covRowOf(t, 3, beringRes3, 1, 1, beringOutline)}
	f := &fakeCoverageSource{days: []string{"2026-09-29"}, cells: []covRow{row},
		stations: map[string][]covRow{"station:a": three, "station:b": three, "station:big": append(slices.Clone(three), three...)}}
	p := coveragePipeline(t, f)
	h := httpHandler(p)
	get := func(path string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	get("/v1/coverage/tiles/0/0/0")
	get("/v1/coverage/tiles/0/0/0?station=station:a")
	if len(p.coverage.tiles.m) != 1 || len(p.coverage.stationTiles.m) != 1 {
		t.Errorf("%d network and %d station tiles cached, want one each", len(p.coverage.tiles.m), len(p.coverage.stationTiles.m))
	}
	// The network holds 1 cell, so stations may hold 4: b's 3 leave no room for a's.
	get("/v1/coverage/tiles/0/0/0?station=station:b")
	get("/v1/coverage/tiles/0/0/0?station=station:a")
	if f.stationLoads != 3 || len(p.coverage.stations) != 1 {
		t.Errorf("%d loads with %d stations held, want a loaded again after b took its room", f.stationLoads, len(p.coverage.stations))
	}
	// A station past the bound on its own, as a feed can be when ClickHouse holds more than the network's load, is
	// kept alone, so its tiles do not each load it again.
	get("/v1/coverage/tiles/0/0/0?station=station:big")
	get("/v1/coverage/tiles/1/1/0?station=station:big")
	if _, held := p.coverage.stations["station:big"]; !held || len(p.coverage.stations) != 1 || f.stationLoads != 4 {
		t.Errorf("a station of 6 cells against a bound of 4: held %v with %d others, %d loads", held, len(p.coverage.stations)-1, f.stationLoads)
	}
}

// A backfill's day and a rebuild wait for each other, so a backfill that read a day before a reload changed it
// cannot insert after the reload's rebuild and put its old cells back.
func TestCoverageBackfillWaitsForARebuild(t *testing.T) {
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
	day := time.Now().UTC().Truncate(24 * time.Hour)
	for name, run := range map[string]func() error{
		"backfill": func() error { return conn.coverageBackfill(ctx, day) },
		"rebuild":  func() error { return conn.rebuildCoverage(ctx, day) },
	} {
		coverageBinning.Lock() // as a rebuild or a backfill's day in progress
		done := make(chan error, 1)
		go func() { done <- run() }()
		var err error
		select {
		case err = <-done:
			t.Errorf("%s ran while another held the day", name)
		case <-time.After(500 * time.Millisecond):
			coverageBinning.Unlock()
			err = <-done
			coverageBinning.Lock()
		}
		coverageBinning.Unlock()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "backfill" {
			conn.exec(ctx, "TRUNCATE TABLE {db}.station_coverage_backfilled", "TRUNCATE TABLE {db}.coverage_backfilled")
		}
	}
}

// A listed station whose load comes back empty, as while a rebuild has deleted its day and not yet binned it,
// answers 404 that once and is loaded again next time, not held empty until the next network load.
func TestCoverageStationEmptyLoadIsNotKept(t *testing.T) {
	row := covRowOf(t, 3, osloRes3, 6, 1, osloRes3Outline)
	f := &fakeCoverageSource{days: []string{"2026-09-29"}, cells: []covRow{row}, stations: map[string][]covRow{"station:a": {row}}}
	p := coveragePipeline(t, f)
	f.stations["station:a"] = nil // the day deleted and not yet binned again, after the network load listed it
	h := httpHandler(p)
	get := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/coverage/tiles/0/0/0?station=station:a", nil))
		return w.Code
	}
	if code := get(); code != 404 {
		t.Fatalf("during the rebuild: %d", code)
	}
	f.stations["station:a"] = []covRow{row}
	if code := get(); code != 200 {
		t.Errorf("after the rebuild: %d, want the station's cells", code)
	}
}
