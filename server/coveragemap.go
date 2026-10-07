package main

// The coverage map: where there are vessel positions, from any source. ClickHouse keeps station_coverage, the
// distinct vessels each station heard in each H3 cell each day, filled by a materialized view as receptions are
// written and once from the receptions it already held. The server reads the last coverageDays complete days of
// it, merged across stations, holds each cell's outline and counts, and serves them as vector tiles:
// GET /v1/coverage/tiles/{z}/{x}/{y} and its TileJSON, /v1/coverage/tiles.json. With ?station=, they serve one
// station's cells, loaded when first asked for. The outlines come from ClickHouse's H3 functions in the same query, so the server
// has no H3 code of its own. The window moves once a day, so tiles are built once per load and kept.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	coverageDays    = 7
	coverageLayer   = "coverage"
	coverageMaxZoom = 10 // advertised; resolution 6 is the finest the server holds, and clients overzoom it
	coverageReload  = time.Hour
	coverageRetry   = 10 * time.Minute
)

// coverageBands is the H3 resolution drawn in each zoom band, so a cell stays a few to a few tens of pixels
// across. A band's first zoom is also the zoom its cells are indexed at.
var coverageBands = [...]struct{ minZoom, res int }{{0, 3}, {5, 4}, {7, 5}, {9, 6}}

func coverageBand(z int) int {
	b := 0
	for i, band := range coverageBands {
		if z >= band.minZoom {
			b = i
		}
	}
	return b
}

// covCell is one cell: its outline in Web Mercator world units (0..1 west to east and north to south), and
// what the window heard there.
type covCell struct {
	id                     uint64
	ring                   []float32 // x, y pairs, without the closing point
	minX, minY, maxX, maxY float32
	vessels                float64 // distinct vessels a day, over the window
	days                   int     // days in the window the cell was heard
	stations               int     // distinct stations that heard it in the window
}

// coverageData is one load. It is never modified once published, so tiles read it without a lock.
type coverageData struct {
	from, to string // the window's first and last day
	days     int    // days in the window ClickHouse holds coverage for
	loaded   int64  // when this load was read, in nanoseconds; tiles are cached per load
	cells    [len(coverageBands)][]covCell
	index    [len(coverageBands)]map[[2]int][]int32 // tile at the band's first zoom -> cells overlapping it
	stations map[string]bool                        // the network's: every station with coverage in the window
}

type coverageMap struct {
	mu    sync.RWMutex
	d     *coverageData // nil until the first load that found a day of coverage
	tiles tileCache

	src       coverageSource // set by each network load; station loads read through it
	stationMu sync.Mutex     // guards stations, and is never held across a load
	stations  map[string]stationCoverage
	loading   chan struct{} // one station load at a time, so a burst of requests for one station loads it once
}

// stationCoverage is one station's cells, loaded for the network load net names.
type stationCoverage struct {
	net int64
	d   *coverageData
}

// stationCoverageMax bounds the stations whose cells are kept, so requests for many stations cost loads, not memory.
const stationCoverageMax = 256

func newCoverageMap() *coverageMap {
	return &coverageMap{tiles: tileCache{ttl: coverageReload}, loading: make(chan struct{}, 1)}
}

func (c *coverageMap) data() *coverageData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.d
}

// covRow is one cell of a window as ClickHouse answers it: the window's sum of each day's distinct vessels,
// the days it was heard, the distinct stations that heard it, and its outline's vertices.
type covRow struct {
	res        int
	cell       uint64
	vessels    uint64
	days       int
	stations   int
	lats, lons []float64
}

// coverageSource is where coverage comes from: ClickHouse (chConn), or a fake in tests.
type coverageSource interface {
	// coverageBackfill fills the coverage tables from the receptions written before their views existed, for
	// each day from today back to since.
	coverageBackfill(ctx context.Context, since time.Time) error
	// coverageDays lists the days from first to last that hold coverage, as YYYY-MM-DD.
	coverageDays(ctx context.Context, first, last time.Time) ([]string, error)
	// coverageCells hands each cell heard from first to last to each.
	coverageCells(ctx context.Context, first, last time.Time, each func(covRow) error) error
	// coverageStations lists the stations with coverage from first to last.
	coverageStations(ctx context.Context, first, last time.Time) ([]string, error)
	// stationCoverageCells hands each cell one station heard from first to last to each.
	stationCoverageCells(ctx context.Context, station string, first, last time.Time, each func(covRow) error) error
}

// runCoverage waits for ClickHouse, backfills the map's window from the receptions it held before, then loads
// the map and reloads it every coverageReload, or sooner after a failure. The rest of what coverage keeps is
// backfilled after the first load, so a database with months of receptions, archives among them, shows the map
// within minutes rather than once every month is binned.
func (p *Pipeline) runCoverage() {
	ctx := context.Background()
	var src coverageSource
	for {
		p.vmu.RLock()
		if p.ch != nil {
			src = p.ch.cov
		}
		p.vmu.RUnlock()
		if src != nil {
			break
		}
		time.Sleep(5 * time.Second) // ClickHouse connects in its own goroutine, usually within moments of start
	}
	backfill := func(since time.Time) {
		for {
			err := src.coverageBackfill(ctx, since)
			if err == nil {
				return
			}
			log.Printf("coverage: backfill: %v", err)
			time.Sleep(coverageRetry)
		}
	}
	now := time.Now().UTC()
	backfill(now.AddDate(0, 0, -coverageDays-1))
	go backfill(now.AddDate(0, -chCoverageMonths, 0))
	for {
		wait := coverageReload
		if err := p.coverage.load(ctx, src, time.Now()); err != nil {
			log.Printf("coverage: %v", err)
			wait = coverageRetry
		}
		time.Sleep(wait)
	}
}

// coverageWindow is the coverageDays complete UTC days before now that the map shows.
func coverageWindow(now time.Time) (first, last time.Time) {
	last = now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	return last.AddDate(0, 0, 1-coverageDays), last
}

// load reads the network's coverage for the window and publishes it. A window with no coverage leaves the map
// unavailable rather than empty, so a client can tell "not loaded" from "heard nothing".
func (c *coverageMap) load(ctx context.Context, src coverageSource, now time.Time) error {
	first, last := coverageWindow(now)
	days, err := src.coverageDays(ctx, first, last)
	if err != nil || len(days) == 0 {
		return err
	}
	d, err := buildCoverage(first, last, len(days), now, func(each func(covRow) error) error {
		return src.coverageCells(ctx, first, last, each)
	})
	if err != nil {
		return err
	}
	ids, err := src.coverageStations(ctx, first, last)
	if err != nil {
		return err
	}
	d.stations = make(map[string]bool, len(ids))
	for _, id := range ids {
		d.stations[id] = true
	}
	c.mu.Lock()
	c.d, c.src = d, src
	c.mu.Unlock()
	n := 0
	for _, cells := range d.cells {
		n += len(cells)
	}
	log.Printf("coverage: %d cells, %s to %s (%d days)", n, d.from, d.to, d.days)
	return nil
}

// stationData is one station's coverage for the network's window, loaded from ClickHouse the first time it is
// asked for after each network load and kept until the next, its vessels averaged over the days the network has
// coverage for, as the network's cells are. A station that heard nothing in the window has an empty one.
func (c *coverageMap) stationData(ctx context.Context, station string, now time.Time) (*coverageData, error) {
	c.mu.RLock()
	src, net := c.src, c.d
	c.mu.RUnlock()
	if net == nil {
		return nil, errNoCoverage
	}
	if !net.stations[station] {
		return &coverageData{}, nil // heard nothing in the window, or no such station: no query for an id anyone can make up
	}
	cached := func() *coverageData {
		c.stationMu.Lock()
		defer c.stationMu.Unlock()
		if s, ok := c.stations[station]; ok && s.net == net.loaded {
			return s.d
		}
		return nil
	}
	if d := cached(); d != nil {
		return d, nil
	}
	// Waiting for another station's load leaves cached stations and unknown ids answering, and gives up with the request.
	select {
	case c.loading <- struct{}{}:
		defer func() { <-c.loading }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if d := cached(); d != nil { // loaded by the request this one waited behind
		return d, nil
	}
	first, err1 := time.Parse("2006-01-02", net.from)
	last, err2 := time.Parse("2006-01-02", net.to)
	if err := cmp.Or(err1, err2); err != nil {
		return nil, err
	}
	d, err := buildCoverage(first, last, net.days, now, func(each func(covRow) error) error {
		return src.stationCoverageCells(ctx, station, first, last, each)
	})
	if err != nil {
		return nil, err
	}
	c.stationMu.Lock()
	defer c.stationMu.Unlock()
	if c.stations == nil {
		c.stations = map[string]stationCoverage{}
	}
	// ponytail: past the bound, drop the stations from earlier loads, and failing that any one.
	if len(c.stations) >= stationCoverageMax {
		maps.DeleteFunc(c.stations, func(_ string, s stationCoverage) bool { return s.net != net.loaded })
		for k := range c.stations {
			if len(c.stations) < stationCoverageMax {
				break
			}
			delete(c.stations, k)
		}
	}
	c.stations[station] = stationCoverage{net: net.loaded, d: d}
	return d, nil
}

// buildCoverage reads cells through fetch into a window's coverage, its vessels averaged over days.
func buildCoverage(first, last time.Time, days int, now time.Time, fetch func(each func(covRow) error) error) (*coverageData, error) {
	d := &coverageData{from: first.Format("2006-01-02"), to: last.Format("2006-01-02"), days: days, loaded: now.UnixNano()}
	res := make(map[int]int, len(coverageBands))
	for b, band := range coverageBands {
		res[band.res] = b
	}
	err := fetch(func(r covRow) error {
		b, ok := res[r.res]
		if !ok {
			return nil
		}
		cell, err := covCellFrom(r.cell, r.lats, r.lons)
		if err != nil {
			return err
		}
		cell.vessels, cell.days, cell.stations = float64(r.vessels)/float64(max(d.days, 1)), r.days, r.stations
		d.cells[b] = append(d.cells[b], cell)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.buildIndex()
	return d, nil
}

// empty reports whether the window holds no cells.
func (d *coverageData) empty() bool {
	for _, cells := range d.cells {
		if len(cells) > 0 {
			return false
		}
	}
	return true
}

// bounds is the extent of the cells at the finest resolution as [west, south, east, north], from their centers
// between the trim and 1-trim quantiles on each axis: 0 for every cell, as TileJSON's bounds, past which a map
// requests no tiles, or 0.05 for a view that a few bad or spoofed positions an ocean away do not stretch.
// ponytail: centers sort plainly, so cells either side of the antimeridian give a box the long way round.
func (d *coverageData) bounds(trim float64) []float64 {
	cells := d.cells[len(coverageBands)-1]
	if len(cells) == 0 {
		return []float64{-180, -mercatorLat, 180, mercatorLat}
	}
	xs, ys := make([]float64, len(cells)), make([]float64, len(cells))
	for i, c := range cells {
		xs[i], ys[i] = float64(c.minX+c.maxX)/2, float64(c.minY+c.maxY)/2
	}
	slices.Sort(xs)
	slices.Sort(ys)
	at := func(v []float64, q float64) float64 { return v[int(math.Round(q*float64(len(v)-1)))] }
	lon := func(x float64) float64 { return x*360 - 180 }
	lat := func(y float64) float64 { return math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi }
	// The cell's own size around the trimmed centers, so a station of one cell still has a box.
	pad := float64(cells[0].maxX - cells[0].minX)
	return []float64{lon(at(xs, trim) - pad), lat(at(ys, 1-trim) + pad), lon(at(xs, 1-trim) + pad), lat(at(ys, trim) - pad)}
}

// covCellFrom projects a cell's outline to world units. A cell that crosses the antimeridian is drawn whole on
// the side of its center, reaching a little past the edge of the world, and not at all on the other side.
func covCellFrom(id uint64, lats, lons []float64) (covCell, error) {
	if len(lats) != len(lons) || len(lats) < 3 {
		return covCell{}, fmt.Errorf("coverage: cell %x has an outline of %d latitudes and %d longitudes", id, len(lats), len(lons))
	}
	lons = slices.Clone(lons)
	if n := len(lons); lats[0] == lats[n-1] && lons[0] == lons[n-1] {
		lats, lons = lats[:n-1], lons[:n-1]
	}
	var sum float64
	for i := range lons {
		if i > 0 && lons[i]-lons[0] > 180 {
			lons[i] -= 360
		} else if i > 0 && lons[0]-lons[i] > 180 {
			lons[i] += 360
		}
		sum += lons[i]
	}
	shift := 0.0
	if center := sum / float64(len(lons)); center < -180 {
		shift = 360
	} else if center > 180 {
		shift = -360
	}
	c := covCell{id: id, ring: make([]float32, 0, 2*len(lons)), minX: math.MaxFloat32, minY: math.MaxFloat32, maxX: -math.MaxFloat32, maxY: -math.MaxFloat32}
	for i, lon := range lons {
		lon += shift
		lat := max(-mercatorLat, min(mercatorLat, lats[i]))
		φ := lat * math.Pi / 180
		x := float32((lon + 180) / 360)
		y := float32((1 - math.Log(math.Tan(φ)+1/math.Cos(φ))/math.Pi) / 2)
		c.ring = append(c.ring, x, y)
		c.minX, c.maxX = min(c.minX, x), max(c.maxX, x)
		c.minY, c.maxY = min(c.minY, y), max(c.maxY, y)
	}
	return c, nil
}

func (d *coverageData) buildIndex() {
	for b, band := range coverageBands {
		n := float32(int(1) << band.minZoom)
		clamp := func(v float32) int { return max(0, min(int(n)-1, int(v*n))) }
		idx := map[[2]int][]int32{}
		for i, c := range d.cells[b] {
			for tx := clamp(c.minX); tx <= clamp(c.maxX); tx++ {
				for ty := clamp(c.minY); ty <= clamp(c.maxY); ty++ {
					idx[[2]int{tx, ty}] = append(idx[[2]int{tx, ty}], int32(i))
				}
			}
		}
		d.index[b] = idx
	}
}

// tile encodes the cells of the zoom's band that overlap the tile, buffer included.
func (d *coverageData) tile(z, x, y int) []byte {
	b := coverageBand(z)
	shift := z - coverageBands[b].minZoom
	n := float64(uint(1) << z)
	buf := float64(tileBuffer) / tileExtent
	west, north := (float64(x)-buf)/n, (float64(y)-buf)/n
	east, south := (float64(x+1)+buf)/n, (float64(y+1)+buf)/n
	l := newMVTLayer(coverageLayer)
	ring := make([][2]int32, 0, 7)
	for _, i := range d.index[b][[2]int{x >> shift, y >> shift}] {
		c := &d.cells[b][i]
		if float64(c.maxX) < west || float64(c.minX) > east || float64(c.maxY) < north || float64(c.minY) > south {
			continue
		}
		ring = ring[:0]
		for k := 0; k < len(c.ring); k += 2 {
			pt := [2]int32{int32(math.Round((float64(c.ring[k])*n - float64(x)) * tileExtent)),
				int32(math.Round((float64(c.ring[k+1])*n - float64(y)) * tileExtent))}
			if len(ring) == 0 || pt != ring[len(ring)-1] {
				ring = append(ring, pt)
			}
		}
		if len(ring) > 1 && ring[0] == ring[len(ring)-1] {
			ring = ring[:len(ring)-1]
		}
		if len(ring) < 3 {
			continue // smaller than a tile unit at this zoom
		}
		// The spec reads a ring with positive area, with y down, as an exterior ring.
		var area int64
		for k := range ring {
			p, q := ring[k], ring[(k+1)%len(ring)]
			area += int64(p[0])*int64(q[1]) - int64(q[0])*int64(p[1])
		}
		if area == 0 {
			continue
		}
		if area < 0 {
			for a, e := 0, len(ring)-1; a < e; a, e = a+1, e-1 {
				ring[a], ring[e] = ring[e], ring[a]
			}
		}
		l.polygon(c.id, ring, []mvtProp{{"vessels", math.Round(c.vessels*10) / 10}, {"days", uint64(c.days)}, {"stations", uint64(c.stations)}})
	}
	return l.tile()
}

var errNoCoverage = errors.New("coverage is not available: no day of it has been loaded")

// serveCoverageTile: GET /v1/coverage/tiles/{z}/{x}/{y} → the coverage cells in one tile, gzipped. The cells
// carry no vessel identity, so no token is needed and no area cap applies; the tile rate limit bounds how many.
func (p *Pipeline) serveCoverageTile(w http.ResponseWriter, r *http.Request) {
	if preflight(w, r, corsHeaders) {
		return
	}
	if p.limited(w, tileLimit, clientIP(r)) {
		return
	}
	z, x, y, ok := tileCoords(r)
	if !ok {
		http.Error(w, "tile out of range", http.StatusBadRequest)
		return
	}
	station := r.URL.Query().Get("station")
	d, status, err := p.coverageFor(r.Context(), station)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	// Keyed by the station and the load, so a reload never serves a tile built from the one before it, even when
	// a day inside the window was repackaged and the window's dates did not move.
	b := p.coverage.tiles.get(fmt.Sprintf("%s/%d/%d/%d/%d", station, d.loaded, z, x, y), time.Now(), func() []byte {
		return gzipBytes(d.tile(z, x, y))
	})
	writeTile(w, r, b, "public, max-age=3600")
}

// errNoStationCoverage answers a station that heard nothing in the window, or that no station is called.
var errNoStationCoverage = errors.New("this station has no coverage in the window")

// coverageFor is the network's coverage, or one station's when station is set, with the status to answer when
// there is none.
func (p *Pipeline) coverageFor(ctx context.Context, station string) (*coverageData, int, error) {
	if p.coverage == nil || p.coverage.data() == nil {
		return nil, http.StatusServiceUnavailable, errNoCoverage
	}
	if station == "" {
		return p.coverage.data(), 0, nil
	}
	if len(station) > 256 {
		return nil, http.StatusBadRequest, errors.New("station id too long")
	}
	d, err := p.coverage.stationData(ctx, station, time.Now())
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	if d.empty() {
		return nil, http.StatusNotFound, errNoStationCoverage
	}
	return d, 0, nil
}

// coverageFields describes the layer's attributes in TileJSON, as openapi.json does.
var coverageFields = map[string]string{
	"vessels":  "Number: distinct vessels heard in the cell a day, averaged over the window",
	"days":     "Number: days in the window the network heard the cell",
	"stations": "Number: distinct stations that heard the cell in the window; each aggregator and government feed counts as one",
}

// serveCoverageTileJSON: GET /v1/coverage/tiles.json → TileJSON for the coverage tiles, plus the window they
// cover.
func (p *Pipeline) serveCoverageTileJSON(w http.ResponseWriter, r *http.Request) {
	station := r.URL.Query().Get("station")
	d, status, err := p.coverageFor(r.Context(), station)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	name, desc, query, bounds, fit := "Open Waters AIS coverage", "Where there are vessel positions from any source", "", []float64{-180, -mercatorLat, 180, mercatorLat}, []float64(nil)
	if station != "" {
		name, desc, query, bounds, fit = "Open Waters AIS coverage of "+station, "Where "+station+" heard vessel positions", "?station="+url.QueryEscape(station), d.bounds(0), d.bounds(0.05)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	tj := map[string]any{
		"tilejson":      "3.0.0",
		"name":          name,
		"description":   fmt.Sprintf("%s for the %d days ending %s, by H3 cell", desc, coverageDays, d.to),
		"attribution":   tileAttribution,
		"tiles":         []string{scheme + "://" + r.Host + "/v1/coverage/tiles/{z}/{x}/{y}" + query},
		"minzoom":       0,
		"maxzoom":       coverageMaxZoom,
		"bounds":        bounds,
		"vector_layers": []map[string]any{{"id": coverageLayer, "fields": coverageFields, "minzoom": 0, "maxzoom": coverageMaxZoom}},
		"window":        map[string]any{"from": d.from, "to": d.to, "days": d.days},
	}
	if fit != nil {
		tj["fit"] = fit // the extent to frame the station by, without its few farthest cells
	}
	json.NewEncoder(w).Encode(tj)
}

// ---- coverage in ClickHouse ----

// chCoverageKeep is how long coverage keeps a day: its TTL, and the age past which the view and the backfill
// bin nothing, so a load of old history computes no cells the TTL would only delete. chCoverageMonths is the
// same span for Go.
const (
	chCoverageKeep   = "13 MONTH"
	chCoverageMonths = 13
)

// chCoverageTable keeps each day's distinct vessels, and the distinct stations that heard them, per cell at each
// resolution the map draws. Distinct sets merge as unions, so every copy of a transmission, and the same copies
// counted again by a backfill that overlaps the view, count each vessel and station once.
var chCoverageTable = `CREATE TABLE IF NOT EXISTS {db}.coverage (
	day     Date,
	res     UInt8,
	cell    UInt64,
	vessels AggregateFunction(uniqExact, UInt32),
	stations AggregateFunction(uniqExact, String)
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (res, day, cell)
TTL day + INTERVAL ` + chCoverageKeep + ` DELETE`

// chCoverageStation is the station a reception counts toward in coverage: a volunteer's own station, whatever
// stream it tags (station:mmsi:368168720/n2k), or the whole feed for a government feed or aggregator, whose
// receivers and paths (barentswatch/terra, kystverket/2573010) are one source. A volunteer's source is its kind,
// as client/app/lib/ais.ts lists them.
const chCoverageStation = `if(source IN ('station', 'udp', 'mmsi', 'v1', 'http'), splitByChar('/', station)[1], source)`

// chStreamFoldBefore is when ingest began keeping a volunteer's receptions under its own station whatever
// stream its TAG block names: the deploy of 2026-10-05 finished at 15:05 UTC, and the cutoff leaves 25 minutes
// of margin. Receptions before it carry the stream as a suffix (station:mmsi:368168720/n2k), which chStationKey
// folds away. It goes by the reception's time, so the rows replay writes for those days fold too.
const chStreamFoldBefore = "2026-10-05 15:30:00"

// chVolunteerSources are the source kinds a volunteer's receiver sends under, as client/app/lib/ais.ts lists them.
const chVolunteerSources = "('station', 'udp', 'mmsi', 'v1', 'http')"

// chStationKey is the station a reception counts toward in station_coverage: the station as receptions holds
// it, a feed's receiver or path included, and for a volunteer's receptions from before chStreamFoldBefore, the
// part before the first slash, which can merge two stations whose token subjects hold a slash in that period.
const chStationKey = `if(source IN ` + chVolunteerSources + ` AND ts < toDateTime64('` + chStreamFoldBefore + `', 3, 'UTC'), splitByChar('/', station)[1], station)`

// chStationCoverageTable keeps each day's distinct vessels per cell and station at each resolution the map
// draws, so the network map merges stations per cell and a station's page reads its own cells. Ordered by day
// first: the network map reads every resolution of the window, and one station's cells are then a range in
// each day. Distinct sets merge as unions, as in coverage. A station's source is kept beside it, so the network
// map counts a feed's receivers and paths as the one source they are.
var chStationCoverageTable = `CREATE TABLE IF NOT EXISTS {db}.station_coverage (
	day     Date,
	station LowCardinality(String),
	res     UInt8,
	cell    UInt64,
	source  LowCardinality(String),
	vessels AggregateFunction(uniqExact, UInt32)
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (day, station, res, cell, source)
TTL day + INTERVAL ` + chCoverageKeep + ` DELETE`

// chCoverageSources counts a row of station_coverage toward the network's stations: a volunteer's station
// whole, and a feed or aggregator once by its source, as chCoverageStation does.
const chCoverageSources = `if(source IN ` + chVolunteerSources + `, station, source)`

// chStationCoverageSelect bins positions from a table into station_coverage rows, as chCoverageSelect bins coverage.
func chStationCoverageSelect(table, and string) string {
	return chCoverageBin(table, and, "source, "+chStationKey+" AS station, uniqExactState(mmsi) AS vessels", "station, source")
}

// chCoverageSelect bins positions from a table into coverage rows, those within chCoverageKeep and matching
// and, when it is not empty: each position's cell at the finest resolution, and the cells that contain it at
// the coarser ones, so every resolution nests exactly. The argument order is set here because ClickHouse
// releases have disagreed on whether geoToH3 takes latitude or longitude first, and a view keeps the order it
// was created with.
func chCoverageSelect(table, and string) string {
	return chCoverageBin(table, and, "uniqExactState(mmsi) AS vessels, uniqExactState("+chCoverageStation+") AS stations", "")
}

// chCoverageBin is the binning both coverage tables share: cols after day, res, and cell, grouped by those and by.
func chCoverageBin(table, and, cols, by string) string {
	if by != "" {
		by = ", " + by
	}
	res := make([]string, len(coverageBands))
	for i, b := range coverageBands {
		res[i] = fmt.Sprint(b.res)
	}
	where := "WHERE ts >= now() - INTERVAL " + chCoverageKeep
	if and != "" {
		where += " AND " + and
	}
	finest := coverageBands[len(coverageBands)-1].res
	return fmt.Sprintf(`SELECT toDate(ts) AS day, res, h3ToParent(geoToH3(lat6 / 600000, lon6 / 600000, %d), res) AS cell,
		%s
	FROM %s ARRAY JOIN [%s] AS res %s
	GROUP BY day, res, cell%s
	SETTINGS geotoh3_argument_order = 'lat_lon'`, finest, cols, table, strings.Join(res, ", "), where, by)
}

// chCoverageTables are the tables coverageBackfill fills, each with the ledger of the days it has binned and the
// query that bins one day.
var chCoverageTables = []struct{ table, ledger, insert string }{
	{"station_coverage", "station_coverage_backfilled", "INSERT INTO {db}.station_coverage (day, res, cell, source, station, vessels) " + chStationCoverageSelect("{db}.receptions", chUsable+" AND ts >= ? AND ts < ?")},
	{"coverage", "coverage_backfilled", "INSERT INTO {db}.coverage " + chCoverageSelect("{db}.receptions", chUsable+" AND ts >= ? AND ts < ?")},
}

// coverageBackfill bins each day from today back to since, no further than chCoverageKeep, that a table's backfill
// has not yet covered: receptions loaded before its view existed, which never passed through it. It runs newest
// first so the window fills first, one day and table per query so the largest day stays within ClickHouse's
// memory, and records each day as it finishes, an empty one too. Days are counted back from today rather than
// read from partitions, so it never depends on how receptions is partitioned. A day the view and the backfill
// both cover counts each vessel once.
func (c *chConn) coverageBackfill(ctx context.Context, since time.Time) error {
	done := map[string]map[string]bool{}
	for _, t := range chCoverageTables {
		days, err := chColumn[string](ctx, c.conn, "SELECT toString(day) FROM "+c.db+"."+t.ledger+" FINAL")
		if err != nil {
			return err
		}
		done[t.table] = map[string]bool{}
		for _, d := range days {
			done[t.table][d] = true
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	oldest := today.AddDate(0, -chCoverageMonths, 0)
	if since = since.UTC().Truncate(24 * time.Hour); since.After(oldest) {
		oldest = since
	}
	n := 0
	for day := today; !day.Before(oldest); day = day.AddDate(0, 0, -1) {
		for _, t := range chCoverageTables {
			if done[t.table][day.Format("2006-01-02")] {
				continue
			}
			if err := c.conn.Exec(ctx, strings.ReplaceAll(t.insert, "{db}", c.db), day, day.AddDate(0, 0, 1)); err != nil {
				return fmt.Errorf("%s %s: %w", t.table, day.Format("2006-01-02"), err)
			}
			if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+"."+t.ledger+" VALUES (?)", day); err != nil {
				return err
			}
			n++
		}
	}
	if n > 0 {
		log.Printf("coverage: binned %d days of receptions", n)
	}
	return nil
}

func (c *chConn) coverageDays(ctx context.Context, first, last time.Time) ([]string, error) {
	rows, err := c.conn.Query(ctx, "SELECT DISTINCT toString(day) FROM "+c.db+".station_coverage WHERE day >= ? AND day <= ? ORDER BY 1",
		first, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// coverageCells answers each cell's window from each day's distinct vessels across stations and the window's
// distinct stations, a feed's counted once by its source, with its outline as (lat, lon) pairs, the order the
// result setting fixes.
func (c *chConn) coverageCells(ctx context.Context, first, last time.Time, each func(covRow) error) error {
	return c.coverageQuery(ctx, `SELECT res, cell, day, uniqExactMerge(vessels) AS v, uniqExactState(`+chCoverageSources+`) AS s
		FROM `+c.db+`.station_coverage WHERE day >= ? AND day <= ? AND res >= ? AND res <= ? GROUP BY res, cell, day`,
		each, first, last, uint8(coverageBands[0].res), uint8(coverageBands[len(coverageBands)-1].res))
}

func (c *chConn) coverageStations(ctx context.Context, first, last time.Time) ([]string, error) {
	return chColumn[string](ctx, c.conn, "SELECT DISTINCT station FROM "+c.db+".station_coverage WHERE day >= ? AND day <= ?", first, last)
}

// stationCoverageCells answers coverageCells for the cells one station heard, each counting the one station.
func (c *chConn) stationCoverageCells(ctx context.Context, station string, first, last time.Time, each func(covRow) error) error {
	return c.coverageQuery(ctx, `SELECT res, cell, day, uniqExactMerge(vessels) AS v, uniqExactState(station) AS s
		FROM `+c.db+`.station_coverage WHERE day >= ? AND day <= ? AND station = ? AND res >= ? AND res <= ? GROUP BY res, cell, day`,
		each, first, last, station, uint8(coverageBands[0].res), uint8(coverageBands[len(coverageBands)-1].res))
}

// coverageQuery folds days, a query answering each cell's vessels v and stations s for each day, into the window.
func (c *chConn) coverageQuery(ctx context.Context, days string, each func(covRow) error, args ...any) error {
	rows, err := c.conn.Query(ctx, `WITH h3ToGeoBoundary(cell) AS b
		SELECT res, cell, sum(v), count(), uniqExactMerge(s), arrayMap(p -> p.1, b), arrayMap(p -> p.2, b)
		FROM (`+days+`)
		GROUP BY res, cell
		SETTINGS h3togeo_lon_lat_result_order = 0`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r covRow
		var res uint8
		var days, stations uint64
		if err := rows.Scan(&res, &r.cell, &r.vessels, &days, &stations, &r.lats, &r.lons); err != nil {
			return err
		}
		r.res, r.days, r.stations = int(res), int(days), int(stations)
		if err := each(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
