package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

type pbField struct {
	num  int
	v    uint64
	data []byte
}

// pbDecode splits a protobuf message into its fields; enough of the wire format to read back a tile.
func pbDecode(t *testing.T, b []byte) []pbField {
	t.Helper()
	var out []pbField
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		b = b[n:]
		f := pbField{num: int(tag >> 3)}
		switch tag & 7 {
		case 0:
			f.v, n = binary.Uvarint(b)
			b = b[n:]
		case 1:
			f.v, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			f.data, b = b[n:n+int(l)], b[n+int(l):]
		default:
			t.Fatalf("wire type %d", tag&7)
		}
		out = append(out, f)
	}
	return out
}

func pbPacked(b []byte) []uint64 {
	var out []uint64
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		out, b = append(out, v), b[n:]
	}
	return out
}

type decodedFeature struct {
	x, y  int32
	props map[string]any
}

// decodeTile reads the vessels layer back into features keyed by id.
func decodeTile(t *testing.T, b []byte) map[uint64]decodedFeature {
	t.Helper()
	out := map[uint64]decodedFeature{}
	for _, lf := range pbDecode(t, b) {
		var keys []string
		var values []any
		var feats [][]byte
		for _, f := range pbDecode(t, lf.data) {
			switch f.num {
			case 1:
				if string(f.data) != tileLayer {
					t.Fatalf("layer %q", f.data)
				}
			case 2:
				feats = append(feats, f.data)
			case 3:
				keys = append(keys, string(f.data))
			case 4:
				v := pbDecode(t, f.data)[0]
				switch v.num {
				case 1:
					values = append(values, string(v.data))
				case 3:
					values = append(values, math.Float64frombits(v.v))
				case 5:
					values = append(values, v.v)
				}
			case 5:
				if f.v != tileExtent {
					t.Fatalf("extent %d", f.v)
				}
			}
		}
		for _, fb := range feats {
			var id uint64
			d := decodedFeature{props: map[string]any{}}
			for _, f := range pbDecode(t, fb) {
				switch f.num {
				case 1:
					id = f.v
				case 2:
					tags := pbPacked(f.data)
					for i := 0; i < len(tags); i += 2 {
						d.props[keys[tags[i]]] = values[tags[i+1]]
					}
				case 4:
					g := pbPacked(f.data)
					unzig := func(u uint64) int32 { return int32(u>>1) ^ -int32(u&1) }
					d.x, d.y = unzig(g[1]), unzig(g[2])
				}
			}
			out[id] = d
		}
	}
	return out
}

// tileOf is the z/x/y holding a coordinate.
func tileOf(lat, lon float64, z int) (x, y int) {
	n := float64(uint(1) << z)
	φ := lat * math.Pi / 180
	return int((lon + 180) / 360 * n), int((1 - math.Log(math.Tan(φ)+1/math.Cos(φ))/math.Pi) / 2 * n)
}

func TestVesselTile(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	put := func(mmsi uint32, lat, lon float64, mod func(*vessel)) {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen, v.Source = lat, lon, true, now, "kystverket"
		if mod != nil {
			mod(v)
		}
		p.putVesselLocked(mmsi, v)
	}
	put(257000001, 59.5, 10.6, func(v *vessel) {
		v.Name, v.Cog, v.Sog, v.Class, v.ShipType = "CARGO", 90, 12.5, "A", 70
		v.Length, v.Beam, v.Dim = 160, 22, ais.FieldDimension{A: 0, B: 160, C: 10, D: 12}
	})
	put(257000002, 59.51, 10.61, func(v *vessel) { v.Kind, v.Name = "aton", "BUOY" })
	put(257000003, 59.5, 10.6, func(v *vessel) { v.Seen = now.Add(-20 * time.Minute) })
	put(257000004, 10, 10, nil) // another tile entirely
	h := httpHandler(p)
	x, y := tileOf(59.5, 10.6, 10)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}

	w := get(fmt.Sprintf("/v1/vessels/tiles/10/%d/%d", x, y))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/vnd.mapbox-vector-tile" {
		t.Fatalf("status %d %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	fs := decodeTile(t, w.Body.Bytes())
	if len(fs) != 3 {
		t.Fatalf("features %d, want 3: %v", len(fs), fs)
	}
	c := fs[257000001]
	if c.props["name"] != "CARGO" || c.props["hdg"] != 90.0 || c.props["sog"] != 12.5 || c.props["type"] != uint64(70) || c.props["class"] != "A" || c.props["flag"] != "NO" {
		t.Fatalf("props %v", c.props)
	}
	if _, ok := c.props["heading"]; ok {
		t.Fatalf("heading 511 encoded: %v", c.props)
	}
	// the antenna offsets draw the hull around the reported point; a zero one is still sent
	if c.props["to_bow"] != uint64(0) || c.props["to_stern"] != uint64(160) || c.props["to_port"] != uint64(10) || c.props["to_starboard"] != uint64(12) {
		t.Fatalf("offsets %v", c.props)
	}
	if _, ok := fs[257000002].props["to_bow"]; ok {
		t.Fatalf("offsets sent for a vessel with no dimensions: %v", fs[257000002].props)
	}
	if c.x < 0 || c.x >= tileExtent || c.y < 0 || c.y >= tileExtent {
		t.Fatalf("point %d,%d outside the tile", c.x, c.y)
	}

	// filters, and the cache keys on them
	fs = decodeTile(t, get(fmt.Sprintf("/v1/vessels/tiles/10/%d/%d?kind=vessel&max_age=10m", x, y)).Body.Bytes())
	if len(fs) != 1 || fs[257000001].props == nil {
		t.Fatalf("kind+max_age: %v", fs)
	}
	fs = decodeTile(t, get(fmt.Sprintf("/v1/vessels/tiles/10/%d/%d?mmsi=257000002", x, y)).Body.Bytes())
	if len(fs) != 1 || fs[257000002].props["kind"] != "aton" {
		t.Fatalf("mmsi: %v", fs)
	}
	fs = decodeTile(t, get(fmt.Sprintf("/v1/vessels/tiles/10/%d/%d?type=60-79&min_sog=10", x, y)).Body.Bytes())
	if len(fs) != 1 {
		t.Fatalf("type+min_sog: %v", fs)
	}

	for _, bad := range []string{"/v1/vessels/tiles/10/0/0?bbox=1,2,3,4", "/v1/vessels/tiles/10/0/0?kind=ship", "/v1/vessels/tiles/10/0/0?type=9-3", "/v1/vessels/tiles/2/4/0", "/v1/vessels/tiles/23/0/0"} {
		if w := get(bad); w.Code != 400 {
			t.Errorf("%s: %d, want 400", bad, w.Code)
		}
	}

	// gzip passes through as built
	r := httptest.NewRequest("GET", fmt.Sprintf("/v1/vessels/tiles/10/%d/%d", x, y), nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" || w.Body.Bytes()[0] != 0x1f {
		t.Fatalf("gzip: %q", w.Header().Get("Content-Encoding"))
	}
}

func TestVesselTileThins(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	for i := range tileCap + 500 {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen = 59.5+float64(i%50)*0.02, 10+float64(i/50)*0.02, true, now.Add(-time.Duration(i)*time.Second)
		p.putVesselLocked(uint32(200000000+i), v)
	}
	fs := decodeTile(t, p.vesselTile(0, 0, 0, &tileFilter{}, now))
	if len(fs) == 0 || len(fs) >= tileCap {
		t.Fatalf("z0 kept %d of %d", len(fs), tileCap+500)
	}
	if _, ok := fs[200000000]; !ok {
		t.Fatalf("thinning dropped the newest vessel")
	}
}

func TestTileJSON(t *testing.T) {
	p := testPipeline(t)
	w := httptest.NewRecorder()
	httpHandler(p).ServeHTTP(w, httptest.NewRequest("GET", "http://ais.example/v1/vessels/tiles.json?kind=vessel&key=k", nil))
	var tj struct {
		Tiles       []string
		Attribution string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tj); err != nil || w.Code != 200 {
		t.Fatalf("%d %v: %s", w.Code, err, w.Body)
	}
	if len(tj.Tiles) != 1 || tj.Tiles[0] != "http://ais.example/v1/vessels/tiles/{z}/{x}/{y}?kind=vessel&key=k" {
		t.Fatalf("tiles %v", tj.Tiles)
	}
	if !strings.Contains(tj.Attribution, licensingURL) {
		t.Errorf("attribution %q does not link the licensing page", tj.Attribution)
	}
}

// Past the cache's 30 minutes a tile reaches into the record: a week back by default, and only for vessels
// whose last report was stationary unless max_age_moving says otherwise. /v1/vessels takes the same filters.
func TestVesselTileRecord(t *testing.T) {
	p := storePipeline(t)
	now := time.Now()
	old := func(mmsi uint32, age time.Duration, sog float64) record {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen, v.PosAt, v.Sog, v.Source = 59.5, 10.6, true, now.Add(-age), now.Add(-age), sog, "aishub"
		return record{mmsi: mmsi, v: v, firstSeen: v.Seen}
	}
	if err := p.store.upsert([]record{
		old(257000011, 48*time.Hour, 0),    // moored two days ago
		old(257000012, 48*time.Hour, 12),   // under way two days ago
		old(257000013, 10*24*time.Hour, 0), // moored ten days ago
	}); err != nil {
		t.Fatal(err)
	}
	live := newVessel()
	live.Lat, live.Lon, live.HasPos, live.Seen, live.Sog, live.Kind = 59.5, 10.6, true, now, 8, "vessel"
	p.vmu.Lock()
	p.putVesselLocked(257000014, live)
	p.vmu.Unlock()

	h := httpHandler(p)
	x, y := tileOf(59.5, 10.6, 10)
	ids := func(query string) []uint64 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/v1/vessels/tiles/10/%d/%d%s", x, y, query), nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		var out []uint64
		for id := range decodeTile(t, w.Body.Bytes()) {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	fcIDs := func(target string) []uint64 {
		var out []uint64
		for _, f := range getFC(t, p, target).Features {
			out = append(out, uint64(f.ID))
		}
		slices.Sort(out)
		return out
	}
	// An area: the tile, and the same query with the box as a bbox, answer with the same vessels.
	for query, want := range map[string][]uint64{
		"":                             {257000011, 257000014},
		"max_age_moving=all":           {257000011, 257000012, 257000014},
		"max_age=all":                  {257000011, 257000013, 257000014},
		"max_age=30m":                  {257000014},
		"min_sog=5&max_age_moving=all": {257000012, 257000014},
		"kind=aton&max_age=all":        nil,
	} {
		if got := ids("?" + query); !slices.Equal(got, want) {
			t.Errorf("tile?%s: %v, want %v", query, got, want)
		}
		if got := fcIDs("/v1/vessels?bbox=59,10,60,11&" + query); !slices.Equal(got, want) {
			t.Errorf("/v1/vessels?bbox=…&%s: %v, want %v", query, got, want)
		}
	}
	// Named vessels answer however old, moving or not, unless the query limits them.
	for query, want := range map[string][]uint64{
		"mmsi=257000012,257000013":  {257000012, 257000013},
		"mmsi=257000012&max_age=1h": nil,
	} {
		if got := ids("?" + query); !slices.Equal(got, want) {
			t.Errorf("tile?%s: %v, want %v", query, got, want)
		}
		if got := fcIDs("/v1/vessels?" + query); !slices.Equal(got, want) {
			t.Errorf("/v1/vessels?%s: %v, want %v", query, got, want)
		}
	}
}

// Past tileCap rows the record answers a tile with the newest row in each thinning cell, and the tile keeps the
// same vessels it keeps when the cache holds them all and thins them itself.
func TestTileRecordsThinLikeTheCache(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // the record keeps milliseconds
	r := rand.New(rand.NewPCG(5, 6))
	z, x, y := 3, 4, 2 // the North Sea and the Baltic
	box := tileBox(z, x, y)
	var rows []record
	for i := range tileCap * 3 {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos = box[0]+r.Float64()*(box[2]-box[0]), box[1]+r.Float64()*(box[3]-box[1]), true
		v.Seen = now.Add(-time.Hour - time.Duration(r.IntN(60))*time.Second) // many share a second
		v.PosAt, v.Sog = v.Seen, 0
		rows = append(rows, record{mmsi: uint32(300000000 + i), v: v, firstSeen: v.Seen})
	}
	rec := storePipeline(t)
	if err := rec.store.upsert(rows); err != nil {
		t.Fatal(err)
	}
	cached := testPipeline(t)
	cached.vmu.Lock()
	for _, row := range rows {
		cached.putVesselLocked(row.mmsi, row.v)
	}
	cached.vmu.Unlock()
	rules, _ := parseAgeRules(url.Values{})
	ids := func(p *Pipeline) []uint64 {
		var out []uint64
		for id := range decodeTile(t, p.vesselTile(z, x, y, &tileFilter{ageRules: *rules}, now)) {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	got, want := ids(rec), ids(cached)
	if len(want) >= len(rows) || !slices.Equal(got, want) {
		t.Fatalf("record tile kept %d, cache tile %d, of %d", len(got), len(want), len(rows))
	}
}

// A few vessels in one cell are not thinned, so the record answers with all of them.
func TestTileRecordsUnthinned(t *testing.T) {
	p := storePipeline(t)
	now := time.Now()
	var rows []record
	for i := range 3 {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen, v.Sog = 59.5, 10.6, true, now.Add(-time.Duration(i+1)*time.Hour), 0
		rows = append(rows, record{mmsi: uint32(257000020 + i), v: v, firstSeen: v.Seen})
	}
	if err := p.store.upsert(rows); err != nil {
		t.Fatal(err)
	}
	q := recordQuery{boxes: []bbox{tileBox(0, 0, 0)}, since: now.Add(-areaWindow), before: now.Add(-vesselTTL), hasPos: true, now: now}
	got, err := p.tileRecords(q, 0, 0, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("%d rows, %v; want all 3", len(got), err)
	}
}

func TestTileBuildsBounded(t *testing.T) {
	var c tileCache
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := range 4 * tileBuilds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get(strconv.Itoa(i), time.Now(), func() []byte {
				n := active.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if peak.Load() != tileBuilds {
		t.Fatalf("%d builds at once, want %d", peak.Load(), tileBuilds)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{"gzip": true, "br, gzip;q=0.5": true, "GZIP": true, "*": true, "gzip;q=0": false, "br": false, "": false, "identity, gzip; q=0": false} {
		if got := acceptsGzip(h); got != want {
			t.Errorf("acceptsGzip(%q) = %v", h, got)
		}
	}
}

func TestMinSog(t *testing.T) {
	for _, bad := range []string{"NaN", "Inf", "-1", "fast"} {
		if _, msg := parseVesselFilter(url.Values{"min_sog": {bad}}, 0); msg == "" {
			t.Errorf("min_sog=%s accepted", bad)
		}
	}
	f, _ := parseVesselFilter(url.Values{"min_sog": {"0"}}, 0)
	known, unknown := newVessel(), newVessel()
	known.Sog = 0
	if !f.match(known, time.Now()) || f.match(unknown, time.Now()) {
		t.Errorf("min_sog=0 should keep a known speed of 0 and drop an unknown one")
	}
}
