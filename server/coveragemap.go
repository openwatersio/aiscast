package main

// The coverage map: where there are vessel positions, from any source. ClickHouse keeps coverage, the distinct
// vessels and stations in each H3 cell each day, filled by a materialized view as receptions are written and once from the
// receptions it already held. The server reads the last coverageDays complete days of it, holds each cell's outline and
// counts, and serves them as vector tiles: GET /v1/coverage/tiles/{z}/{x}/{y} and its TileJSON,
// /v1/coverage/tiles.json. The outlines come from ClickHouse's H3 functions in the same query, so the server
// has no H3 code of its own. The window moves once a day, so tiles are built once per load and kept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
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
}

type coverageMap struct {
	mu    sync.RWMutex
	d     *coverageData // nil until the first load that found a day of coverage
	tiles tileCache
}

func newCoverageMap() *coverageMap {
	return &coverageMap{tiles: tileCache{ttl: coverageReload}}
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
	// coverageBackfill fills coverage from the receptions written before its view existed, for each day from
	// today back to since.
	coverageBackfill(ctx context.Context, since time.Time) error
	// coverageDays lists the days from first to last that hold coverage, as YYYY-MM-DD.
	coverageDays(ctx context.Context, first, last time.Time) ([]string, error)
	// coverageCells hands each cell heard from first to last to each.
	coverageCells(ctx context.Context, first, last time.Time, each func(covRow) error) error
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

// load reads the coverageDays complete UTC days before now and publishes them. A window with no coverage
// leaves the map unavailable rather than empty, so a client can tell "not loaded" from "heard nothing".
func (c *coverageMap) load(ctx context.Context, src coverageSource, now time.Time) error {
	last := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	first := last.AddDate(0, 0, 1-coverageDays)
	days, err := src.coverageDays(ctx, first, last)
	if err != nil || len(days) == 0 {
		return err
	}
	d := &coverageData{from: first.Format("2006-01-02"), to: last.Format("2006-01-02"), days: len(days), loaded: now.UnixNano()}
	res := make(map[int]int, len(coverageBands))
	for b, band := range coverageBands {
		res[band.res] = b
	}
	err = src.coverageCells(ctx, first, last, func(r covRow) error {
		b, ok := res[r.res]
		if !ok {
			return nil
		}
		cell, err := covCellFrom(r.cell, r.lats, r.lons)
		if err != nil {
			return err
		}
		cell.vessels, cell.days, cell.stations = float64(r.vessels)/float64(d.days), r.days, r.stations
		d.cells[b] = append(d.cells[b], cell)
		return nil
	})
	if err != nil {
		return err
	}
	d.buildIndex()
	c.mu.Lock()
	c.d = d
	c.mu.Unlock()
	n := 0
	for _, cells := range d.cells {
		n += len(cells)
	}
	log.Printf("coverage: %d cells, %s to %s (%d days)", n, d.from, d.to, d.days)
	return nil
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
	var d *coverageData
	if p.coverage != nil {
		d = p.coverage.data()
	}
	if d == nil {
		http.Error(w, errNoCoverage.Error(), http.StatusServiceUnavailable)
		return
	}
	// Keyed by the load, so a reload never serves a tile built from the one before it, even when a day
	// inside the window was repackaged and the window's dates did not move.
	b := p.coverage.tiles.get(fmt.Sprintf("%d/%d/%d/%d", d.loaded, z, x, y), time.Now(), func() []byte {
		return gzipBytes(d.tile(z, x, y))
	})
	writeTile(w, r, b, "public, max-age=3600")
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
	var d *coverageData
	if p.coverage != nil {
		d = p.coverage.data()
	}
	if d == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": errNoCoverage.Error()})
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	json.NewEncoder(w).Encode(map[string]any{
		"tilejson":      "3.0.0",
		"name":          "Open Waters AIS coverage",
		"description":   fmt.Sprintf("Where there are vessel positions from any source for the %d days ending %s, by H3 cell", coverageDays, d.to),
		"attribution":   tileAttribution,
		"tiles":         []string{scheme + "://" + r.Host + "/v1/coverage/tiles/{z}/{x}/{y}"},
		"minzoom":       0,
		"maxzoom":       coverageMaxZoom,
		"bounds":        []float64{-180, -mercatorLat, 180, mercatorLat},
		"vector_layers": []map[string]any{{"id": coverageLayer, "fields": coverageFields, "minzoom": 0, "maxzoom": coverageMaxZoom}},
		"window":        map[string]any{"from": d.from, "to": d.to, "days": d.days},
	})
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

// chCoverageStation is the station a reception counts toward in coverage: a volunteer's own station, or the
// whole feed for a government feed or aggregator, whose receivers and paths (barentswatch/terra,
// kystverket/2573010) are one source. A volunteer's source is its kind, as client/app/lib/ais.ts lists them.
const chCoverageStation = `if(source IN ('station', 'udp', 'mmsi', 'v1', 'http'), station, source)`

// chCoverageSelect bins positions from a table into coverage rows, those within chCoverageKeep and matching
// and, when it is not empty: each position's cell at the finest resolution, and the cells that contain it at
// the coarser ones, so every resolution nests exactly. The argument order is set here because ClickHouse
// releases have disagreed on whether geoToH3 takes latitude or longitude first, and a view keeps the order it
// was created with.
func chCoverageSelect(table, and string) string {
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
		uniqExactState(mmsi) AS vessels, uniqExactState(%s) AS stations
	FROM %s ARRAY JOIN [%s] AS res %s
	GROUP BY day, res, cell
	SETTINGS geotoh3_argument_order = 'lat_lon'`, finest, chCoverageStation, table, strings.Join(res, ", "), where)
}

// coverageBackfill bins each day from today back to since, no further than chCoverageKeep, that the backfill
// has not yet covered: receptions loaded before the view existed, which never passed through it. It runs newest first so the window fills first, one
// day per query so the largest day stays within ClickHouse's memory, and records each day as it finishes, an
// empty one too. Days are counted back from today rather than read from partitions, so it never depends on how
// receptions is partitioned. A day the view and the backfill both cover counts each vessel once.
func (c *chConn) coverageBackfill(ctx context.Context, since time.Time) error {
	done := map[string]bool{}
	rows, err := c.conn.Query(ctx, "SELECT toString(day) FROM "+c.db+".coverage_backfilled FINAL")
	if err != nil {
		return err
	}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			rows.Close()
			return err
		}
		done[day] = true
	}
	rows.Close()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	oldest := today.AddDate(0, -chCoverageMonths, 0)
	if since = since.UTC().Truncate(24 * time.Hour); since.After(oldest) {
		oldest = since
	}
	q := "INSERT INTO " + c.db + ".coverage " + chCoverageSelect(c.db+".receptions", chUsable+" AND ts >= ? AND ts < ?")
	n := 0
	for day := today; !day.Before(oldest); day = day.AddDate(0, 0, -1) {
		if done[day.Format("2006-01-02")] {
			continue
		}
		if err := c.conn.Exec(ctx, q, day, day.AddDate(0, 0, 1)); err != nil {
			return fmt.Errorf("%s: %w", day.Format("2006-01-02"), err)
		}
		if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".coverage_backfilled VALUES (?)", day); err != nil {
			return err
		}
		n++
	}
	if n > 0 {
		log.Printf("coverage: binned %d days of receptions", n)
	}
	return nil
}

func (c *chConn) coverageDays(ctx context.Context, first, last time.Time) ([]string, error) {
	rows, err := c.conn.Query(ctx, "SELECT DISTINCT toString(day) FROM "+c.db+".coverage WHERE res = ? AND day >= ? AND day <= ? ORDER BY 1",
		uint8(coverageBands[0].res), first, last)
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

// coverageCells answers each cell's window from each day's distinct vessels and the window's distinct stations,
// with its outline as (lat, lon) pairs, the order the result setting fixes.
func (c *chConn) coverageCells(ctx context.Context, first, last time.Time, each func(covRow) error) error {
	rows, err := c.conn.Query(ctx, `WITH h3ToGeoBoundary(cell) AS b
		SELECT res, cell, sum(v), count(), uniqExactMerge(s), arrayMap(p -> p.1, b), arrayMap(p -> p.2, b)
		FROM (SELECT res, cell, day, uniqExactMerge(vessels) AS v, uniqExactMergeState(stations) AS s FROM `+c.db+`.coverage
			WHERE day >= ? AND day <= ? AND res >= ? AND res <= ? GROUP BY res, cell, day)
		GROUP BY res, cell
		SETTINGS h3togeo_lon_lat_result_order = 0`,
		first, last, uint8(coverageBands[0].res), uint8(coverageBands[len(coverageBands)-1].res))
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
