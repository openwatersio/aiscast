package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Vector tiles of vessel positions: GET /v1/vessels/tiles/{z}/{x}/{y} answers a Mapbox Vector Tile with one
// point layer, vessels, and /v1/vessels/tiles.json the TileJSON that points a map at it. A map shows vessels
// with a style and a timer, and no stream code. A tile is a snapshot, so clients refresh it.

const (
	tileExtent  = 4096
	tileBuffer  = 128 // 16 px of a 512 px tile: an icon on a tile edge is drawn whole by both tiles
	tileMaxZoom = 14  // advertised in TileJSON; clients overzoom past it, though deeper tiles are served too
	tileCap     = 2000
	tileGrid    = 64 // past tileCap vessels, a tile keeps the newest in each 8 px cell
	tileTTL     = 10 * time.Second
	tileLayer   = "vessels"
	mercatorLat = 85.0511287798066
)

// tileParams are the parameters a tile accepts. Anything else is refused, so a typo in a filter fails loudly
// rather than returning every vessel, and a filter added later never changes an older client's tiles.
var tileParams = map[string]bool{"mmsi": true, "max_age": true, "key": true}

func init() {
	for k := range vesselFilterParams {
		tileParams[k] = true
	}
}

type tileFilter struct {
	ageRules
	mmsi map[uint32]bool
}

func parseTileFilter(vals url.Values, cl *Claims) (*tileFilter, string) {
	for k := range vals {
		if !tileParams[k] {
			return nil, "unknown parameter " + k
		}
	}
	s, msg := parseSub(url.Values{"mmsi": vals["mmsi"]}, cl, false)
	if msg != "" {
		return nil, msg
	}
	if cl.Area < 0 && len(s.mmsi) == 0 {
		return nil, "mmsi required for this key"
	}
	rules, msg := parseAgeRules(vals)
	if msg != "" {
		return nil, msg
	}
	return &tileFilter{ageRules: *rules, mmsi: s.mmsi}, ""
}

// match: a tile given mmsi shows only those vessels, and they are named ones.
func (f *tileFilter) match(mmsi uint32, v *vessel, now time.Time) bool {
	return (f.mmsi == nil || f.mmsi[mmsi]) && f.ageRules.match(f.mmsi != nil, v, now)
}

// serveVesselTile: GET /v1/vessels/tiles/{z}/{x}/{y} → the vessels in one tile, gzipped. The area
// cap does not apply: a tile bounds its own cost by thinning, and the tile rate limit bounds how many.
func (p *Pipeline) serveVesselTile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if p.limited(w, tileLimit, clientIP(r)) {
		return
	}
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	z, x, y, ok := tileCoords(r)
	if !ok {
		http.Error(w, "tile out of range", http.StatusBadRequest)
		return
	}
	vals := r.URL.Query()
	f, msg := parseTileFilter(vals, cl)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if len(cl.BBox) > 0 && !cl.allowsBox(tileBox(z, x, y)) {
		http.Error(w, "tile not allowed for this key", http.StatusBadRequest)
		return
	}
	// Everything above is about the caller; the tile itself depends only on the coordinates and filters.
	vals.Del("key")
	now := time.Now()
	b := p.tiles.get(fmt.Sprintf("%d/%d/%d?%s", z, x, y, vals.Encode()), now, func() []byte {
		return gzipBytes(p.vesselTile(z, x, y, f, now))
	})
	h := w.Header()
	h.Set("Content-Type", "application/vnd.mapbox-vector-tile")
	h.Set("Cache-Control", "public, max-age=10")
	h.Set("Vary", "Accept-Encoding")
	// Gzipped once per build and served as is. Caddy's encoder leaves a response that already has a
	// Content-Encoding alone.
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		w.Write(b)
		return
	}
	zr, _ := gzip.NewReader(bytes.NewReader(b))
	io.Copy(w, zr)
}

func tileCoords(r *http.Request) (z, x, y int, ok bool) {
	z, err1 := strconv.Atoi(r.PathValue("z"))
	x, err2 := strconv.Atoi(r.PathValue("x"))
	y, err3 := strconv.Atoi(r.PathValue("y"))
	if err1 != nil || err2 != nil || err3 != nil || z < 0 || z > 22 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

// tileLonLat is the corner of tile-space position (x, y) at a zoom with n tiles a side.
func tileLonLat(x, y, n float64) (lon, lat float64) {
	return x/n*360 - 180, math.Atan(math.Sinh(math.Pi*(1-2*y/n))) * 180 / math.Pi
}

// tileBox is a tile's bbox widened by the buffer. ponytail: the buffer does not wrap the antimeridian, so an
// icon straddling 180° is cut on one side.
func tileBox(z, x, y int) bbox {
	n, buf := float64(uint(1)<<z), float64(tileBuffer)/tileExtent
	west, north := tileLonLat(float64(x)-buf, float64(y)-buf, n)
	east, south := tileLonLat(float64(x+1)+buf, float64(y+1)+buf, n)
	return bbox{max(south, -90), max(west, -180), min(north, 90), min(east, 180)}
}

// tilePoint is what a tile shows of a vessel, copied out under the read lock so encoding does not hold it.
type tilePoint struct {
	mmsi                  uint32
	x, y                  int32
	seen                  time.Time
	name, kind, class     string
	source, station       string
	shipType, navStatus   uint8
	cog, sog              float64
	heading, length, beam uint16
}

// vesselTile encodes the vessels in a tile that match f.
func (p *Pipeline) vesselTile(z, x, y int, f *tileFilter, now time.Time) []byte {
	n := float64(uint(1) << z)
	box := tileBox(z, x, y)
	// The cache answers for the last 30 minutes and the record for what it no longer holds. A record that
	// fails leaves the tile to the cache rather than blanking the map. ponytail: a z0-z3 tile decodes every
	// matching row in its area, ~400 ms for 75,000 (BenchmarkTileRecordZ0), at most once per tileTTL; pick
	// the newest row per cell in SQL at low zooms if the route's latency histogram says so.
	var recs []record
	if age, vf := f.rule(f.mmsi != nil); p.store != nil && (age == 0 || age > vesselTTL) {
		q := recordQuery{boxes: []bbox{box}, since: since(now, age), before: now.Add(-vesselTTL), hasPos: true, filter: vf, now: now}
		if f.mmsi != nil {
			q.mmsis = slices.Collect(maps.Keys(f.mmsi))
		}
		var err error
		if recs, err = p.store.find(q); err != nil {
			log.Printf("store: %v", err)
		}
	}
	var pts []tilePoint
	add := func(mmsi uint32, v *vessel) {
		if math.Abs(v.Lat) > mercatorLat || !f.match(mmsi, v, now) {
			return
		}
		φ := v.Lat * math.Pi / 180
		px := ((v.Lon+180)/360*n - float64(x)) * tileExtent
		py := ((1-math.Log(math.Tan(φ)+1/math.Cos(φ))/math.Pi)/2*n - float64(y)) * tileExtent
		if px < -tileBuffer || py < -tileBuffer || px >= tileExtent+tileBuffer || py >= tileExtent+tileBuffer {
			return
		}
		pts = append(pts, tilePoint{mmsi: mmsi, x: int32(math.Round(px)), y: int32(math.Round(py)), seen: v.Seen,
			name: v.Name, kind: v.Kind, class: v.Class, source: sourceKind(v.Source), station: v.Station, shipType: v.ShipType,
			navStatus: v.NavStatus, cog: v.Cog, sog: v.Sog, heading: v.Heading, length: v.Length, beam: v.Beam})
	}
	p.vmu.RLock()
	p.vesselsIn([]bbox{box}, add)
	for _, rec := range recs {
		// ponytail: a vessel the cache holds is the cache's to answer, even in the minute after a restart
		// when the record can be a report ahead.
		if p.vessels[rec.mmsi] == nil {
			add(rec.mmsi, rec.v)
		}
	}
	p.vmu.RUnlock()
	slices.SortFunc(pts, func(a, b tilePoint) int { return cmp.Compare(a.mmsi, b.mmsi) })
	if len(pts) > tileCap {
		best := map[int32]int{}
		for i, pt := range pts {
			const cols = (tileExtent + 2*tileBuffer) / tileGrid
			c := (pt.y+tileBuffer)/tileGrid*cols + (pt.x+tileBuffer)/tileGrid
			if j, ok := best[c]; !ok || pt.seen.After(pts[j].seen) {
				best[c] = i
			}
		}
		kept := make([]int, 0, len(best))
		for _, i := range best {
			kept = append(kept, i)
		}
		slices.Sort(kept)
		thin := make([]tilePoint, len(kept))
		for k, i := range kept {
			thin[k] = pts[i]
		}
		pts = thin
	}
	l := newMVTLayer(tileLayer)
	for _, pt := range pts {
		props := []mvtProp{{"mmsi", uint64(pt.mmsi)}, {"kind", pt.kind}, {"source", pt.source},
			{"station", pt.station},
			{"age_s", uint64(max(0, now.Sub(pt.seen)/time.Second))}}
		if pt.name != "" {
			props = append(props, mvtProp{"name", pt.name})
		}
		if pt.class != "" {
			props = append(props, mvtProp{"class", pt.class})
		}
		if flag := flagOf(pt.mmsi); flag != "" {
			props = append(props, mvtProp{"flag", flag})
		}
		if pt.shipType != 0 {
			props = append(props, mvtProp{"type", uint64(pt.shipType)})
		}
		if pt.navStatus != 15 {
			props = append(props, mvtProp{"nav_status", uint64(pt.navStatus)})
		}
		if pt.length > 0 {
			props = append(props, mvtProp{"length", uint64(pt.length)})
		}
		if pt.beam > 0 {
			props = append(props, mvtProp{"beam", uint64(pt.beam)})
		}
		if pt.sog < 102.3 {
			props = append(props, mvtProp{"sog", pt.sog})
		}
		if pt.cog < 360 {
			props = append(props, mvtProp{"cog", pt.cog})
		}
		if pt.heading < 511 {
			props = append(props, mvtProp{"heading", uint64(pt.heading)}, mvtProp{"hdg", float64(pt.heading)})
		} else if pt.cog < 360 {
			props = append(props, mvtProp{"hdg", pt.cog})
		}
		l.point(uint64(pt.mmsi), pt.x, pt.y, props)
	}
	return l.tile()
}

// ---- tile cache: one build per tile and filter per tileTTL, however many clients ask ----

type tileCache struct {
	mu sync.Mutex
	m  map[string]*tileEntry
}

type tileEntry struct {
	once sync.Once
	at   time.Time
	b    []byte
}

// tileCacheMax bounds the entries. ponytail: entries live 10 s, so reaching it takes ~1,000 distinct tiles a
// second; past that the cache drops everything expired and, failing that, everything.
const tileCacheMax = 10000

// get returns the entry for key, building it when missing or expired. Concurrent requests for one tile wait
// on the same build.
func (c *tileCache) get(key string, now time.Time, build func() []byte) []byte {
	c.mu.Lock()
	e := c.m[key]
	if e == nil || now.Sub(e.at) >= tileTTL {
		if c.m == nil {
			c.m = map[string]*tileEntry{}
		}
		if len(c.m) >= tileCacheMax {
			for k, old := range c.m {
				if now.Sub(old.at) >= tileTTL {
					delete(c.m, k)
				}
			}
			if len(c.m) >= tileCacheMax {
				clear(c.m)
			}
		}
		e = &tileEntry{at: now}
		c.m[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.b = build() })
	return e.b
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// ---- TileJSON ----

// tileFields describes the layer's attributes in TileJSON, as the vessel Feature describes them in openapi.json.
var tileFields = map[string]string{
	"mmsi": "Number", "name": "String", "kind": "String: vessel, aton, base, or sar", "class": "String: A or B",
	"type": "Number: ITU ship and cargo type; the AtoN type for an aid to navigation", "flag": "String: ISO 3166 alpha-2 from the MMSI",
	"nav_status": "Number", "sog": "Number: knots", "cog": "Number: degrees", "heading": "Number: degrees",
	"hdg": "Number: heading, else course over ground, the angle to rotate an icon by", "length": "Number: metres",
	"beam": "Number: metres", "source": "String: source kind", "station": "String: the station that heard the last message", "age_s": "Number: seconds since the last report when the tile was built",
}

// tileAttribution is one linked credit, the web-map convention: a tile holds whichever sources heard its
// vessels, so the source credits every license asks for live on the page it links to.
const tileAttribution = `<a href="` + licensingURL + `">Open Waters AIS</a>`

const licensingURL = "https://openwaters.io/ais/#license"

// serveTileJSON: GET /v1/vessels/tiles.json → TileJSON for the vessel tiles. Its query string, filters and
// key alike, is carried into the tile URL, so a map needs only this URL.
func (p *Pipeline) serveTileJSON(w http.ResponseWriter, r *http.Request) {
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if _, msg := parseTileFilter(r.URL.Query(), cl); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	u := scheme + "://" + r.Host + "/v1/vessels/tiles/{z}/{x}/{y}"
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	json.NewEncoder(w).Encode(map[string]any{
		"tilejson":      "3.0.0",
		"name":          "Open Waters AIS vessels",
		"attribution":   tileAttribution,
		"tiles":         []string{u},
		"minzoom":       0,
		"maxzoom":       tileMaxZoom,
		"bounds":        []float64{-180, -mercatorLat, 180, mercatorLat},
		"vector_layers": []map[string]any{{"id": tileLayer, "fields": tileFields, "minzoom": 0, "maxzoom": tileMaxZoom}},
	})
}

// ---- Mapbox Vector Tile encoding (spec 2.1), points only ----

type mvtProp struct {
	key string
	val any // string, uint64, or float64
}

// mvtLayer accumulates one layer's features, with keys and values interned into the layer's tables.
type mvtLayer struct {
	name     string
	features []byte
	keys     []string
	values   [][]byte
	keyIdx   map[string]uint64
	valIdx   map[any]uint64
}

func newMVTLayer(name string) *mvtLayer {
	return &mvtLayer{name: name, keyIdx: map[string]uint64{}, valIdx: map[any]uint64{}}
}

func (l *mvtLayer) point(id uint64, x, y int32, props []mvtProp) {
	var tags []byte
	for _, p := range props {
		k, ok := l.keyIdx[p.key]
		if !ok {
			k = uint64(len(l.keys))
			l.keyIdx[p.key] = k
			l.keys = append(l.keys, p.key)
		}
		v, ok := l.valIdx[p.val]
		if !ok {
			v = uint64(len(l.values))
			l.valIdx[p.val] = v
			l.values = append(l.values, mvtValue(p.val))
		}
		tags = binary.AppendUvarint(binary.AppendUvarint(tags, k), v)
	}
	geom := binary.AppendUvarint([]byte{9}, zigzag(x)) // 9: MoveTo, count 1
	geom = binary.AppendUvarint(geom, zigzag(y))
	f := binary.AppendUvarint([]byte{0x08}, id) // 1: id
	f = pbBytes(f, 0x12, tags)                  // 2: tags, packed
	f = append(f, 0x18, 1)                      // 3: type POINT
	f = pbBytes(f, 0x22, geom)                  // 4: geometry, packed
	l.features = pbBytes(l.features, 0x12, f)   // layer 2: features
}

// tile is the encoded Tile holding this one layer.
func (l *mvtLayer) tile() []byte {
	b := []byte{0x78, 2}                 // 15: version 2
	b = pbBytes(b, 0x0a, []byte(l.name)) // 1: name
	b = append(b, l.features...)
	for _, k := range l.keys {
		b = pbBytes(b, 0x1a, []byte(k)) // 3: keys
	}
	for _, v := range l.values {
		b = pbBytes(b, 0x22, v) // 4: values
	}
	b = binary.AppendUvarint(append(b, 0x28), tileExtent) // 5: extent
	return pbBytes(nil, 0x1a, b)                          // Tile 3: layers
}

func mvtValue(v any) []byte {
	switch v := v.(type) {
	case string:
		return pbBytes(nil, 0x0a, []byte(v)) // 1: string_value
	case float64:
		return binary.LittleEndian.AppendUint64([]byte{0x19}, math.Float64bits(v)) // 3: double_value
	case uint64:
		return binary.AppendUvarint([]byte{0x28}, v) // 5: uint_value
	}
	panic("mvt: unsupported value type")
}

func pbBytes(b []byte, tag byte, data []byte) []byte {
	return append(binary.AppendUvarint(append(b, tag), uint64(len(data))), data...)
}

func zigzag(n int32) uint64 { return uint64(uint32((n << 1) ^ (n >> 31))) }
