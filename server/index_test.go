package main

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// scanMatch is the reference eachMatch is held to: every cached vessel the subscription's match accepts.
func scanMatch(p *Pipeline, s *v1Sub) []uint32 {
	var out []uint32
	for mmsi, v := range p.vessels {
		if s.match(&Event{MMSI: mmsi, Lat: v.Lat, Lon: v.Lon, HasPos: v.HasPos}) {
			out = append(out, mmsi)
		}
	}
	slices.Sort(out)
	return out
}

func indexMatch(p *Pipeline, s *v1Sub) []uint32 {
	var out []uint32
	p.eachMatch(s, func(mmsi uint32, _ *vessel) { out = append(out, mmsi) })
	slices.Sort(out)
	return out
}

// checkIndex asserts the invariant the queries rely on: a vessel is filed, once, in the cell of its
// position exactly when it has one, and no cell is left empty.
func checkIndex(t *testing.T, p *Pipeline) {
	t.Helper()
	filed := 0
	for c, m := range p.cells {
		if len(m) == 0 {
			t.Fatalf("empty cell %d left in the index", c)
		}
		for mmsi, v := range m {
			filed++
			if p.vessels[mmsi] != v || !v.HasPos || !v.indexed || v.cell != c || cellOf(v.Lat, v.Lon) != c {
				t.Fatalf("vessel %d filed in cell %d: %+v", mmsi, c, v)
			}
		}
	}
	withPos := 0
	for _, v := range p.vessels {
		if v.HasPos {
			withPos++
		}
	}
	if filed != withPos {
		t.Fatalf("index holds %d vessels, cache has %d with a position", filed, withPos)
	}
}

func TestSpatialIndexMatchesScan(t *testing.T) {
	p := testPipeline(t)
	r := rand.New(rand.NewPCG(3, 4))
	// Poles, the antimeridian, and whole-degree lines are where a cell calculation goes wrong.
	coord := func() (float64, float64) {
		switch r.IntN(4) {
		case 0:
			return []float64{-90, -89.5, 0, 45, 89.999, 90}[r.IntN(6)], []float64{-180, -179.5, 0, 10, 179.999, 180}[r.IntN(6)]
		case 1:
			return float64(r.IntN(181) - 90), float64(r.IntN(361) - 180)
		default:
			return r.Float64()*180 - 90, r.Float64()*360 - 180
		}
	}
	randBox := func() bbox {
		lat, lon := coord()
		lat2, lon2 := lat+r.Float64()*r.Float64()*40, lon+r.Float64()*r.Float64()*80
		if r.IntN(3) == 0 { // whole-degree and zero-width edges
			lat2, lon2 = float64(int(lat)), float64(int(lon))
		}
		return bbox{max(-90, min(lat, lat2)), max(-180, min(lon, lon2)), min(90, max(lat, lat2)), min(180, max(lon, lon2))}
	}
	now := time.Now()
	p.vmu.Lock()
	defer p.vmu.Unlock()
	for i := uint32(0); i < 3000; i++ {
		v := newVessel()
		v.Seen = now.Add(-time.Duration(r.IntN(60)) * time.Minute)
		if r.IntN(5) > 0 { // some vessels have only static data, and no position
			v.Lat, v.Lon = coord()
			v.HasPos = true
		}
		p.putVesselLocked(i, v)
	}
	for round := 0; round < 20; round++ {
		for i := 0; i < 300; i++ { // moves within and across cells, as updateVessel folds them
			mmsi := uint32(r.IntN(3000))
			if v := p.vessels[mmsi]; v != nil {
				v.Lat, v.Lon = coord()
				v.HasPos = true
				p.indexLocked(mmsi, v)
			}
		}
		if round%5 == 4 {
			p.sweepLocked(now.Add(-time.Duration(10+r.IntN(40)) * time.Minute))
		}
		checkIndex(t, p)
		for q := 0; q < 50; q++ {
			s := &v1Sub{}
			for n := r.IntN(4); n > 0; n-- {
				s.boxes = append(s.boxes, randBox())
			}
			if r.IntN(3) == 0 {
				s.mmsi = map[uint32]bool{}
				for n := 1 + r.IntN(5); n > 0; n-- {
					s.mmsi[uint32(r.IntN(3200))] = true // some are not cached at all
				}
			}
			s.everything = len(s.boxes) == 0 && len(s.mmsi) == 0
			if got, want := indexMatch(p, s), scanMatch(p, s); !slices.Equal(got, want) {
				t.Fatalf("round %d: boxes %v mmsi %v: index found %d vessels, scan %d", round, s.boxes, s.mmsi, len(got), len(want))
			}
		}
	}
}

// The index follows the real pipeline: a position fold refiles the vessel, the sweep drops it, and a
// snapshot restore files it again.
func TestSpatialIndexFollowsTheCache(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	pos := func(at time.Time, lat, lon float64) {
		p.ingestPacket("kystverket", "kystverket", at, at, ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 257000001}, Valid: true,
			Latitude: ais.FieldLatLonFine(lat), Longitude: ais.FieldLatLonFine(lon), Cog: 360, Sog: 102.3, TrueHeading: 511, NavigationalStatus: 15})
	}
	in := func(b bbox) bool {
		p.vmu.RLock()
		defer p.vmu.RUnlock()
		found := false
		p.vesselsIn([]bbox{b}, func(uint32, *vessel) { found = true })
		return found
	}
	west, east := bbox{59, 10, 60, 10.99}, bbox{59, 11, 60, 12}

	pos(now.Add(-20*time.Minute), 59.9, 10.7)
	if !in(west) || in(east) {
		t.Fatal("first position not filed in its cell")
	}
	pos(now.Add(-10*time.Minute), 59.9, 11.3) // 18 nm in 10 minutes: a plausible move into the next cell east
	if in(west) || !in(east) {
		t.Fatal("moved vessel not refiled")
	}
	checkIndex(t, p)

	snapshot := filepath.Join(t.TempDir(), "vessels.json")
	if err := p.saveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	restored := testPipeline(t)
	if _, err := restored.loadSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	checkIndex(t, restored)
	if len(restored.cells) != 1 {
		t.Fatalf("restored index has %d cells, want 1", len(restored.cells))
	}

	p.vmu.Lock()
	p.sweepLocked(now)
	p.vmu.Unlock()
	if in(east) || len(p.cells) != 0 {
		t.Fatal("swept vessel still in the index")
	}
}

// legacyFeature is the map-built Feature the struct encoding replaced. Kept as the reference that holds
// the /v1/vessels bytes steady for clients.
func legacyFeature(v *vessel, mmsi uint32) map[string]any {
	props := map[string]any{
		"mmsi": mmsi, "kind": v.Kind, "seen": v.Seen.UTC().Format(time.RFC3339),
		"source": v.Source, "station": v.Station, "msg_type": v.MsgType,
	}
	if v.Name != "" {
		props["name"] = v.Name
	}
	if v.ShipType != 0 {
		props["type"] = v.ShipType
	}
	if v.Cog < 360 {
		props["cog"] = v.Cog
	}
	if v.Sog < 102.3 {
		props["sog"] = v.Sog
	}
	if v.Heading < 511 {
		props["heading"] = v.Heading
	}
	if v.NavStatus != 15 {
		props["nav_status"] = v.NavStatus
	}
	if f := flagOf(mmsi); f != "" {
		props["flag"] = f
	}
	if v.IMO != 0 {
		props["imo"] = v.IMO
	}
	if v.CallSign != "" {
		props["callsign"] = v.CallSign
	}
	if v.Destination != "" {
		props["destination"] = v.Destination
	}
	if eta := etaString(v.ETA); eta != "" {
		props["eta"] = eta
	}
	if v.Draught > 0 {
		props["draught"] = v.Draught
	}
	if v.Length > 0 {
		props["length"] = v.Length
	}
	if v.Beam > 0 {
		props["beam"] = v.Beam
	}
	return map[string]any{
		"type": "Feature", "id": mmsi,
		"geometry":   map[string]any{"type": "Point", "coordinates": [2]float64{v.Lon, v.Lat}},
		"properties": props,
	}
}

func TestVesselsBytesUnchanged(t *testing.T) {
	p := testPipeline(t)
	r := rand.New(rand.NewPCG(5, 6))
	seen := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	p.vmu.Lock()
	for i := uint32(0); i < 500; i++ {
		v := newVessel()
		v.HasPos, v.Lat, v.Lon, v.Seen = true, 59+r.Float64(), 10+r.Float64(), seen
		v.Source, v.Station, v.MsgType = []string{"kystverket", "aishub", "station:abc"}[i%3], "st", "PositionReport"
		if i%2 == 0 { // zeros that are real values, and sentinels that mean unknown
			v.Cog, v.Sog, v.Heading, v.NavStatus = 0, 0, 0, 0
		}
		if i%3 == 0 {
			v.Name, v.ShipType, v.IMO, v.CallSign, v.Destination = "A&B <TEST>", 70, 9319466, "LAJB7", "NO OSL"
			v.ETA, v.Draught, v.Length, v.Beam = ais.FieldETA{Month: 9, Day: 19, Hour: 24, Minute: 60}, 5.2, 150, 22
		}
		mmsi := []uint32{257000000, 230000000, 111000000, 970000000}[i%4] + i
		p.putVesselLocked(mmsi, v)
	}
	var want bytes.Buffer
	features := []map[string]any{}
	attribution := map[string]string{}
	for mmsi, v := range p.vessels {
		legacy, _ := json.Marshal(legacyFeature(v, mmsi))
		if got := v.featureJSON(mmsi); !bytes.Equal(got, legacy) {
			t.Fatalf("feature %d:\n got %s\nwant %s", mmsi, got, legacy)
		}
		if got := v.featureJSON(mmsi); !bytes.Equal(got, legacy) { // the cached copy
			t.Fatalf("cached feature %d differs", mmsi)
		}
		features = append(features, legacyFeature(v, mmsi))
		noteAttribution(attribution, v.Source)
	}
	p.vmu.Unlock()

	// Whole response, everything subscribed: the collection wrapper and order of fields match too. Map
	// order differs run to run, so compare after sorting the features by id.
	rec := httptest.NewRecorder()
	p.serveVessels(rec, httptest.NewRequest("GET", "/v1/vessels", nil))
	json.NewEncoder(&want).Encode(map[string]any{"type": "FeatureCollection", "features": features, "attribution": attribution})
	if norm(t, rec.Body.Bytes()) != norm(t, want.Bytes()) {
		t.Fatalf("response differs:\n got %.300s\nwant %.300s", rec.Body.Bytes(), want.Bytes())
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte(`{"attribution":{`)) || !bytes.HasSuffix(rec.Body.Bytes(), []byte("],\"type\":\"FeatureCollection\"}\n")) {
		t.Fatalf("collection framing changed: %.80s ... %s", rec.Body.Bytes(), rec.Body.Bytes()[rec.Body.Len()-40:])
	}
}

// norm sorts a collection's features by id and re-encodes each exactly as received, so two responses
// compare equal when they hold the same bytes per feature in any order.
func norm(t *testing.T, b []byte) string {
	t.Helper()
	var c struct {
		Attribution json.RawMessage   `json:"attribution"`
		Features    []json.RawMessage `json:"features"`
		Type        string            `json:"type"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	id := func(f json.RawMessage) uint32 {
		var x struct{ ID uint32 }
		json.Unmarshal(f, &x)
		return x.ID
	}
	slices.SortFunc(c.Features, func(a, b json.RawMessage) int { return int(id(a)) - int(id(b)) })
	var out bytes.Buffer
	out.Write(c.Attribution)
	for _, f := range c.Features {
		out.Write(f)
	}
	return out.String() + c.Type
}
