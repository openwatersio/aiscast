package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
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
	days        []string // days with coverage
	cells       []covRow
	backfills   int
	first, last time.Time // the window last asked for
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
	f := &fakeCoverageSource{
		days: []string{"2026-09-20", "2026-09-23", "2026-09-24", "2026-09-26", "2026-09-27", "2026-09-28", "2026-09-29", "2026-09-30"},
		cells: []covRow{
			covRowOf(t, 3, osloRes3, 60, 6, osloRes3Outline),
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
	if len(fs) != 2 || fs[osloRes3].props["vessels"] != 10.0 || fs[osloRes3].props["days"] != uint64(6) {
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

func TestCoverageUnavailable(t *testing.T) {
	check := func(name string, p *Pipeline) {
		t.Helper()
		for _, path := range []string{"/v1/coverage/tiles.json", "/v1/coverage/tiles/0/0/0"} {
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
// that it drops afterward: the view fills coverage as receptions are written, counting each vessel once
// however many copies arrive and skipping implausible copies and those older than coverage keeps, a backfill over the same days counts nothing twice, and a day the backfill has done it never
// bins again.
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
			heading: 511, navStatus: 15, source: "kystverket"}
	}
	// Two vessels on the first day, one of them twice, and one of them again on the second. The second vessel's
	// transmission also arrives as a later copy from another station, which changes no count. A copy the fold
	// judged implausible, far from the others, adds no cell.
	first := at(2, day1.Add(3*time.Hour))
	first.txAt, first.txDisc = first.ts, 7
	again := first
	again.station, again.recv, again.dup = "station:other", first.ts.Add(2*time.Second), true
	wild := at(5, day1.Add(4*time.Hour))
	wild.lat6, wild.lon6, wild.implausible = int32(10*600000), int32(10*600000), true
	batch := []trackPoint{at(1, day1.Add(time.Hour)), at(1, day1.Add(2*time.Hour)), first, again, wild, at(1, day2.Add(time.Hour))}
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
		if oslo.res != 6 || oslo.vessels != 3 || oslo.days != 2 {
			t.Errorf("%s: Oslo at resolution 6 is %+v, want 2 + 1 vessels over 2 days", when, oslo)
		}
		if r := got[osloRes3]; r.res != 3 || r.vessels != 3 {
			t.Errorf("%s: the resolution-3 cell containing it is %+v", when, r)
		}
		if len(got) != len(coverageBands) {
			t.Errorf("%s: %d cells, want one per resolution", when, len(got))
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
	if err := conn.conn.Exec(ctx, "TRUNCATE TABLE "+db+".coverage"); err != nil {
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
	if err := conn.conn.Exec(ctx, "TRUNCATE TABLE "+db+".coverage_backfilled"); err != nil {
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
