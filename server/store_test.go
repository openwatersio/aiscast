package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// storePipeline is a test pipeline with a vessel record in a temporary directory.
func storePipeline(t *testing.T) *Pipeline {
	t.Helper()
	p := testPipeline(t)
	path := filepath.Join(t.TempDir(), "aiscast.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.close() })
	if err := p.attachStore(st); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustFlush(t *testing.T, p *Pipeline) {
	t.Helper()
	if err := p.flushStore(); err != nil {
		t.Fatal(err)
	}
}

// forget empties the cache, as its 30-minute sweep would for vessels no longer heard.
func forget(p *Pipeline) {
	p.vmu.Lock()
	p.sweepLocked(time.Now().Add(24 * time.Hour))
	p.vmu.Unlock()
}

func shipStatic(mmsi uint32, name string) ais.Packet {
	return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: name, Type: 70,
		ImoNumber: 9000000 + mmsi%1000000, CallSign: "LAJB7", Destination: "NOOSL", MaximumStaticDraught: 5.2,
		Eta: ais.FieldETA{Month: 9, Day: 19, Hour: 6}, Dimension: ais.FieldDimension{A: 100, B: 50, C: 10, D: 12}}
}

// heardAgo folds a position and a static report for mmsi as received age ago, then flushes the record the
// way the once-a-second writer would long before the cache's 30-minute sweep. Without the flush, folding
// a vessel an hour after another would sweep the first from the cache before it was ever written.
func heardAgo(p *Pipeline, mmsi uint32, name string, lat, lon float64, age time.Duration) {
	at := time.Now().Add(-age).Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(mmsi, lat, lon))
	p.ingestPacket("kystverket", "kystverket", at.Add(time.Second), at.Add(time.Second), shipStatic(mmsi, name))
	if p.store != nil {
		p.flushStore()
	}
}

func get(t *testing.T, p *Pipeline, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	httpHandler(p).ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	return w
}

type testFC struct {
	Features []struct {
		ID         uint32         `json:"id"`
		Geometry   *pointGeometry `json:"geometry"`
		Properties map[string]any `json:"properties"`
	} `json:"features"`
	Attribution map[string]string `json:"attribution"`
	Truncated   bool              `json:"truncated"`
}

func getFC(t *testing.T, p *Pipeline, target string) testFC {
	t.Helper()
	w := get(t, p, target)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", target, w.Code, w.Body)
	}
	var fc testFC
	if err := json.Unmarshal(w.Body.Bytes(), &fc); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return fc
}

func ids(fc testFC) []uint32 {
	var out []uint32
	for _, f := range fc.Features {
		out = append(out, f.ID)
	}
	return out
}

func TestStoreMergeKeepsWhatAReturningVesselHasNotResent(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	full := newVessel()
	full.Name, full.IMO, full.CallSign, full.ShipType, full.Class, full.Draught = "NORDIC STAR", 9319466, "LAJB7", 70, "A", 5.2
	full.Lat, full.Lon, full.HasPos, full.PosAt, full.Seen, full.Source, full.NavStatus = 59.9, 10.7, true, t1, t1, "kystverket", 0
	if err := st.upsert([]record{{mmsi: 257000001, v: full}}); err != nil {
		t.Fatal(err)
	}

	// Swept from the cache and heard again: a static-less, positionless state with a newer seen.
	back := newVessel()
	back.Seen, back.Source = t1.Add(time.Hour), "aishub"
	if err := st.upsert([]record{{mmsi: 257000001, v: back}}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := st.get(257000001)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	v := rec.v
	if v.Name != "NORDIC STAR" || v.IMO != 9319466 || v.CallSign != "LAJB7" || v.ShipType != 70 || v.Class != "A" || v.Draught != 5.2 {
		t.Errorf("particulars lost: %+v", v)
	}
	if !v.HasPos || v.Lat != 59.9 || !v.PosAt.Equal(t1) || v.NavStatus != 0 {
		t.Errorf("position lost: %+v", v)
	}
	if !v.Seen.Equal(t1.Add(time.Hour)) || v.Source != "aishub" || !rec.firstSeen.Equal(t1) {
		t.Errorf("seen %v source %q first %v", v.Seen, v.Source, rec.firstSeen)
	}

	// An older position never replaces a newer one, and seen never moves back.
	old := newVessel()
	old.Lat, old.Lon, old.HasPos, old.PosAt, old.Seen = 58, 9, true, t1.Add(-time.Hour), t1.Add(-time.Hour)
	if err := st.upsert([]record{{mmsi: 257000001, v: old}}); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = st.get(257000001)
	if rec.v.Lat != 59.9 || !rec.v.Seen.Equal(t1.Add(time.Hour)) {
		t.Errorf("older write won: %+v", rec.v)
	}
}

func TestMMSIRange(t *testing.T) {
	cases := map[string][2]uint32{"368": {368000000, 368999999}, "368168720": {368168720, 368168720}, "002": {2000000, 2999999}}
	for p, want := range cases {
		lo, hi, ok := mmsiRange(p)
		if !ok || lo != want[0] || hi != want[1] {
			t.Errorf("%s: %d..%d %v, want %v", p, lo, hi, ok, want)
		}
	}
	if _, _, ok := mmsiRange("1234567890"); ok {
		t.Error("ten digits is no MMSI prefix")
	}
	if g := globPrefix("A*B?[C"); g != "A[*]B[?][[]C*" {
		t.Errorf("glob %q", g)
	}
}

func TestReplayPipelineKeepsNoRecord(t *testing.T) {
	p := testPipeline(t) // built as aiscast replay builds its pipeline: no store attached
	p.ingestPacket("kystverket", "kystverket", time.Now(), time.Now(), posReport(257000001, 59.9, 10.7))
	if p.store != nil || p.dirty != nil {
		t.Fatalf("store %v dirty %v", p.store, p.dirty)
	}
}

func TestVesselEndpoint(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	mustFlush(t, p)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)

	w := get(t, p, "/v1/vessels/257000001")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/geo+json" {
		t.Fatalf("from the record: %d %s", w.Code, w.Body)
	}
	var f struct {
		ID          uint32            `json:"id"`
		Geometry    *pointGeometry    `json:"geometry"`
		Properties  map[string]any    `json:"properties"`
		Attribution map[string]string `json:"attribution"`
	}
	json.Unmarshal(w.Body.Bytes(), &f)
	if f.ID != 257000001 || f.Geometry == nil || f.Geometry.Coordinates[1] < 59.89 || f.Properties["name"] != "NORDIC STAR" ||
		f.Properties["imo"] != float64(9000001) || f.Properties["first_seen"] == nil || f.Attribution["kystverket"] == "" {
		t.Errorf("from the record: %s", w.Body)
	}
	if seen, _ := time.Parse(time.RFC3339, f.Properties["seen"].(string)); time.Since(seen) < 119*time.Minute {
		t.Errorf("seen %v is not the record's", seen)
	}

	if w := get(t, p, "/v1/vessels/257000002"); w.Code != 200 || !strings.Contains(w.Body.String(), "OSLO FERRY") {
		t.Errorf("from the cache: %d %s", w.Code, w.Body)
	}

	// Back in the cache with only a static report: the record supplies the position.
	now := time.Now().Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", now, now, shipStatic(257000001, "NORDIC STAR"))
	json.Unmarshal(get(t, p, "/v1/vessels/257000001").Body.Bytes(), &f)
	if f.Geometry == nil || f.Geometry.Coordinates[1] < 59.89 {
		t.Errorf("returning vessel lost its last position: %+v", f)
	}

	// Heard only in a static report ever: known, with no position.
	p.ingestPacket("kystverket", "kystverket", now, now, shipStatic(257000009, "NO FIX"))
	w = get(t, p, "/v1/vessels/257000009")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"geometry":null`) {
		t.Errorf("positionless vessel: %d %s", w.Code, w.Body)
	}

	if w := get(t, p, "/v1/vessels/999999999"); w.Code != 404 || !strings.Contains(w.Body.String(), "unknown vessel") {
		t.Errorf("unknown: %d %s", w.Code, w.Body)
	}
	if w := get(t, p, "/v1/vessels/cerulean"); w.Code != 400 {
		t.Errorf("not a number: %d", w.Code)
	}
}

func TestVesselsFollowedMMSIKeepsItsLastPosition(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	mustFlush(t, p)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)

	if got := ids(getFC(t, p, "/v1/vessels?mmsi=257000001,257000002,999999999")); len(got) != 2 {
		t.Errorf("followed vessels: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?mmsi=257000001&max_age=30m")); len(got) != 0 {
		t.Errorf("max_age limits followed vessels too: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?mmsi=257000001&max_age=3h")); len(got) != 1 {
		t.Errorf("within max_age: %v", got)
	}
}

func TestVesselsMaxAgeReachesPastTheCache(t *testing.T) {
	p := storePipeline(t)
	for i := range 3 {
		heardAgo(p, uint32(257000010+i), fmt.Sprintf("OLD %d", i), 59.9, 10.7+float64(i)/100, time.Duration(2+i)*time.Hour)
	}
	heardAgo(p, 257000020, "FAR AWAY", 40, -70, 2*time.Hour)
	mustFlush(t, p)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)
	mustFlush(t, p)

	box := "/v1/vessels?bbox=59,10,60,11"
	if got := ids(getFC(t, p, box)); len(got) != 1 || got[0] != 257000002 {
		t.Errorf("default is the last 30 minutes: %v", got)
	}
	if got := ids(getFC(t, p, box+"&max_age=150m")); len(got) != 2 {
		t.Errorf("max_age=150m: %v", got)
	}
	fc := getFC(t, p, box+"&max_age=all")
	if len(fc.Features) != 4 || fc.Truncated || fc.Attribution["kystverket"] == "" {
		t.Errorf("max_age=all: %v truncated %v", ids(fc), fc.Truncated)
	}
	if got := ids(getFC(t, p, box+"&max_age=10m")); len(got) != 1 {
		t.Errorf("a short max_age still includes the fresh vessel: %v", got)
	}

	recordLimit = 2
	t.Cleanup(func() { recordLimit = 500 })
	fc = getFC(t, p, box+"&max_age=all")
	if len(fc.Features) != 3 || !fc.Truncated {
		t.Errorf("capped: %v truncated %v", ids(fc), fc.Truncated)
	}
	if fc.Features[1].ID != 257000010 {
		t.Errorf("the cap keeps the newest: %v", ids(fc))
	}
	if w := get(t, p, box+"&max_age=forever"); w.Code != 400 {
		t.Errorf("bad max_age: %d", w.Code)
	}
}

func TestVesselSearch(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 3*time.Hour)
	heardAgo(p, 257000003, "NORDIC SEA", 59.8, 10.5, 2*time.Hour)
	heardAgo(p, 230000001, "HELSINKI TUG", 60.1, 25.0, time.Hour)
	mustFlush(t, p)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)
	mustFlush(t, p)

	if got := ids(getFC(t, p, "/v1/vessels?q=nordic")); len(got) != 2 || got[0] != 257000003 {
		t.Errorf("name prefix, newest first: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=2570")); len(got) != 3 {
		t.Errorf("MMSI prefix: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=oslo")); len(got) != 1 || got[0] != 257000002 {
		t.Errorf("a vessel in the cache: %v", got)
	}
	named := newVessel()
	named.Name, named.HasPos, named.Lat, named.Lon, named.Seen = "2570 STAR", true, 50, 5, time.Now()
	p.store.upsert([]record{{mmsi: 311000001, v: named}})
	if got := ids(getFC(t, p, "/v1/vessels?q=2570")); len(got) != 3 {
		t.Errorf("digits match MMSIs only, not a name starting with them: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=star")); len(got) != 0 {
		t.Errorf("prefix, not substring: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=nordic&bbox=59.85,10,60,11")); len(got) != 1 || got[0] != 257000001 {
		t.Errorf("bbox narrows: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=nordic&max_age=150m")); len(got) != 1 || got[0] != 257000003 {
		t.Errorf("max_age narrows: %v", got)
	}
	for target, want := range map[string]int{"/v1/vessels?q=n": 400, "/v1/vessels?q=nordic&flag=NO": 400, "/v1/vessels?q=nordic&type=9-3": 400} {
		if w := get(t, p, target); w.Code != want {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}

	var bulk []record
	for i := range searchLimit + 5 {
		v := newVessel()
		v.Name, v.HasPos, v.Lat, v.Lon, v.Seen, v.PosAt = fmt.Sprintf("BULK %d", i), true, 50, 5, time.Now(), time.Now()
		bulk = append(bulk, record{mmsi: uint32(300000000 + i), v: v})
	}
	p.store.upsert(bulk)
	if fc := getFC(t, p, "/v1/vessels?q=bulk"); len(fc.Features) != searchLimit || !fc.Truncated {
		t.Errorf("search cap: %d truncated %v", len(fc.Features), fc.Truncated)
	}

	if w := get(t, testPipeline(t), "/v1/vessels?q=nordic"); w.Code != 503 {
		t.Errorf("no record: %d", w.Code)
	}
}

func TestMCPReachesTheRecord(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	mustFlush(t, p)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)
	cs := mcpClient(t, p)

	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{257000001, 999999999}}, &out); msg != "" ||
		len(out.Vessels) != 1 || out.Vessels[0].Lat == nil || out.Vessels[0].AgeS < 7000 || len(out.Unknown) != 1 {
		t.Errorf("get_vessels: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"imo": []uint32{9000001, 9000002}}, &out); msg != "" || len(out.Vessels) != 2 {
		t.Errorf("get_vessels by IMO, recorded and cached: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "star"}, &out); msg != "" ||
		len(out.Vessels) != 1 || out.Vessels[0].MMSI != 257000001 {
		t.Errorf("search: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "nordic", "flag": "FI"}, &out); msg != "" || len(out.Vessels) != 0 {
		t.Errorf("search keeps the flag filter: %q %+v", msg, out)
	}
}

func TestStoreMetrics(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 10*time.Second)
	mustFlush(t, p)
	w := httptest.NewRecorder()
	p.serveMetrics(w, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{"aiscast_store_up 1", "aiscast_store_rows_written_total 1", `route="/v1/vessels/{mmsi}"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestFlushRetriesAFailedWrite(t *testing.T) {
	p := storePipeline(t)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7))
	p.store.db.Close()
	if err := p.flushStore(); err == nil {
		t.Fatal("write to a closed database succeeded")
	}
	if _, ok := p.dirty[257000001]; !ok || p.store.flushFailures.Load() != 1 {
		t.Errorf("failed vessel not queued for the next flush: %v", p.dirty)
	}
}

// After a restart the cache comes from a snapshot up to a minute old, while the record was written every
// second, so the record can be ahead of the cache.
func TestLookupPrefersTheNewerState(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 10*time.Second)
	p.vmu.Lock()
	v := p.vessels[257000001]
	v.Lat, v.PosAt, v.Seen = 59.0, v.PosAt.Add(-time.Minute), v.Seen.Add(-time.Minute)
	v.feat.Store(nil)
	p.vmu.Unlock()

	var f struct {
		Geometry   *pointGeometry `json:"geometry"`
		Properties map[string]any `json:"properties"`
	}
	json.Unmarshal(get(t, p, "/v1/vessels/257000001").Body.Bytes(), &f)
	if f.Geometry == nil || f.Geometry.Coordinates[1] < 59.89 {
		t.Errorf("lookup returned the older cached position: %+v", f.Geometry)
	}
	for _, target := range []string{"/v1/vessels?mmsi=257000001", "/v1/vessels?q=nordic", "/v1/vessels?q=nordic&max_age=30s"} {
		fc := getFC(t, p, target)
		if len(fc.Features) != 1 || fc.Features[0].Geometry.Coordinates[1] < 59.89 {
			t.Errorf("%s returned the older cached position: %+v", target, fc.Features)
		}
	}
	var out mcpVessels
	if msg := mcpCall(t, mcpClient(t, p), "search_vessels_by_name", map[string]any{"name": "nordic"}, &out); msg != "" ||
		len(out.Vessels) != 1 || *out.Vessels[0].Lat < 59.89 {
		t.Errorf("search_vessels_by_name returned the older cached state: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, mcpClient(t, p), "get_vessels", map[string]any{"mmsi": []uint32{257000001}}, &out); msg != "" ||
		len(out.Vessels) != 1 || *out.Vessels[0].Lat < 59.89 || out.Vessels[0].AgeS > 30 {
		t.Errorf("get_vessels returned the older cached state: %q %+v", msg, out.Vessels)
	}
}

func TestMCPRecordFailureIsAnError(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 10*time.Second)
	p.store.db.Close()
	cs := mcpClient(t, p)
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{999999999}}, &out); !strings.Contains(msg, "unavailable") {
		t.Errorf("get_vessels called a vessel unknown when the record failed: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "nordic"}, &out); !strings.Contains(msg, "unavailable") {
		t.Errorf("search: %q", msg)
	}
}

func TestMCPSearchPagesTheRecord(t *testing.T) {
	p := storePipeline(t)
	var bulk []record
	for i := range 6 {
		for _, mid := range []uint32{230000000, 257000000} { // Finland, Norway
			v := newVessel()
			v.Name, v.Seen = fmt.Sprintf("BULK %d", i), time.Now()
			bulk = append(bulk, record{mmsi: mid + uint32(i), v: v})
		}
	}
	if err := p.store.upsert(bulk); err != nil {
		t.Fatal(err)
	}
	var out mcpVessels
	if msg := mcpCall(t, mcpClient(t, p), "search_vessels_by_name", map[string]any{"name": "bulk", "flag": "FI", "limit": 2}, &out); msg != "" ||
		len(out.Vessels) != 2 || out.Total != 6 || !out.Truncated || out.Vessels[0].Name != "BULK 0" || out.Vessels[1].Flag != "FI" {
		t.Errorf("flag and total from the whole record: %q %+v", msg, out)
	}
}

// Shutdown closes the record while the once-a-second writer may be mid-flush; run with -race.
func TestCloseStoreWaitsForTheWriter(t *testing.T) {
	p := testPipeline(t)
	path := filepath.Join(t.TempDir(), "aiscast.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.attachStore(st); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { // the writer, flushing as fast as it can until shutdown is over
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			now := time.Now()
			p.ingestPacket("kystverket", "kystverket", now, now, posReport(uint32(257000000+i%500), 59.9, 10.7))
			if err := p.flushStore(); err != nil {
				t.Errorf("flush raced the close: %v", err)
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257999999, 60.1, 11.1))
	if err := p.closeStore(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // the writer keeps flushing after the close, as runStore's ticker would
	close(stop)
	<-done
	if err := p.flushStore(); err != nil {
		t.Errorf("flush after close: %v", err)
	}

	again, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.close()
	if _, ok, err := again.get(257999999); err != nil || !ok {
		t.Errorf("the vessel folded before shutdown was not written: %v %v", ok, err)
	}
}

// The record orders a search by the seen it stored, and the cache runs up to a second ahead of it.
func TestSearchOrdersByTheNewestSeen(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 10*time.Minute)
	heardAgo(p, 257000003, "NORDIC SEA", 59.8, 10.5, 5*time.Minute)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.91, 10.71)) // not yet flushed
	if got := ids(getFC(t, p, "/v1/vessels?q=nordic")); len(got) != 2 || got[0] != 257000001 {
		t.Errorf("newest first by the cache's seen: %v", got)
	}
}

func TestFirstSeenBeforeTheFirstWrite(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7)) // not yet flushed
	var f struct {
		Properties map[string]any `json:"properties"`
	}
	json.Unmarshal(get(t, p, "/v1/vessels/257000001").Body.Bytes(), &f)
	if f.Properties["first_seen"] == nil || f.Properties["first_seen"] != f.Properties["seen"] {
		t.Errorf("first_seen before the first write: %v", f.Properties)
	}
	mustFlush(t, p)
	json.Unmarshal(get(t, p, "/v1/vessels/257000001").Body.Bytes(), &f)
	if f.Properties["first_seen"] != f.Properties["seen"] {
		t.Errorf("first_seen after the write changed: %v", f.Properties)
	}
}

// A merge from history carries an older first sighting; the record keeps the earliest and nothing else
// moves backward.
func TestStoreKeepsTheEarliestFirstSeen(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	live := newVessel()
	live.Name, live.HasPos, live.Lat, live.Lon, live.PosAt, live.Seen = "NORDIC STAR", true, 59.9, 10.7, now, now
	st.upsert([]record{{mmsi: 257000001, v: live}})
	old := newVessel()
	old.Name, old.HasPos, old.Lat, old.Lon, old.PosAt, old.Seen = "OLD NAME", true, 58, 9, now.AddDate(0, -1, 0), now.AddDate(0, -1, 0)
	st.upsert([]record{{mmsi: 257000001, v: old}})
	rec, _, _ := st.get(257000001)
	if !rec.firstSeen.Equal(now.AddDate(0, -1, 0)) || rec.v.Lat != 59.9 || !rec.v.Seen.Equal(now) {
		t.Errorf("first %v lat %v seen %v", rec.firstSeen, rec.v.Lat, rec.v.Seen)
	}
}

// restartedPipeline is a fresh pipeline attached to p's record file, as the next process after a restart.
func restartedPipeline(t *testing.T, p *Pipeline) *Pipeline {
	t.Helper()
	st, err := openStore(p.store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.close() })
	next := testPipeline(t)
	if err := next.attachStore(st); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestRestartRestoresTheCacheFromTheRecord(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	heardAgo(p, 257000002, "OSLO FERRY", 59.5, 10.6, 10*time.Second)
	mustFlush(t, p)

	next := restartedPipeline(t, p)
	next.vmu.RLock()
	defer next.vmu.RUnlock()
	if len(next.vessels) != 1 {
		t.Fatalf("restored %d vessels, want the one heard in the last 30 minutes", len(next.vessels))
	}
	v := next.vessels[257000002]
	if v == nil || v.Name != "OSLO FERRY" || !v.HasPos || v.TrustedAt.IsZero() || v.StaticAt.IsZero() || v.IMO == 0 {
		t.Fatalf("restored vessel lost state: %+v", v)
	}
	if !v.indexed {
		t.Error("restored vessel not filed in the spatial index")
	}
}

// A vessel back from the sweep sends positions before it resends its name, and the record still has it.
func TestSearchFillsWhatTheCacheHasNotHeardAgain(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	forget(p)
	now := time.Now().Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.8, 10.6))
	mustFlush(t, p)

	fc := getFC(t, p, "/v1/vessels?q=nordic")
	if len(fc.Features) != 1 || fc.Features[0].Properties["name"] != "NORDIC STAR" || fc.Features[0].Geometry.Coordinates[1] > 59.81 {
		t.Errorf("search: %+v", fc.Features)
	}
	fc = getFC(t, p, "/v1/vessels?mmsi=257000001")
	if len(fc.Features) != 1 || fc.Features[0].Properties["name"] != "NORDIC STAR" {
		t.Errorf("followed MMSI: %+v", fc.Features)
	}
	var out mcpVessels
	if msg := mcpCall(t, mcpClient(t, p), "search_vessels_by_name", map[string]any{"name": "nordic"}, &out); msg != "" ||
		len(out.Vessels) != 1 || out.Vessels[0].Name != "NORDIC STAR" {
		t.Errorf("search_vessels_by_name: %q %+v", msg, out.Vessels)
	}
}

func TestFindNearCentresOnTheLastKnownPosition(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 2*time.Hour)
	forget(p)
	heardAgo(p, 257000002, "OSLO FERRY", 59.91, 10.71, 10*time.Second)
	cs := mcpClient(t, p)

	var out mcpVessels
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"mmsi": 257000001}, &out); msg != "" ||
		len(out.Vessels) != 1 || out.Vessels[0].MMSI != 257000002 {
		t.Errorf("near a vessel the cache swept: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"mmsi": 999999999}, &out); !strings.Contains(msg, "never been heard") {
		t.Errorf("unknown vessel: %q", msg)
	}
}

func TestOpenStoreAddsMissingColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiscast.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	schema := storeSchema
	for _, col := range []string{"trusted_at", "static_at"} {
		i := strings.Index(schema, "\t"+col)
		schema = schema[:i] + schema[i+strings.Index(schema[i:], "\n")+1:]
	}
	if _, err := old.Exec(schema); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for range 2 { // and again on a file that has them
		st, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		v := newVessel()
		v.Seen, v.TrustedAt = time.Now(), time.Now()
		if err := st.upsert([]record{{mmsi: 257000001, v: v}}); err != nil {
			t.Fatal(err)
		}
		if rec, ok, err := st.get(257000001); err != nil || !ok || rec.v.TrustedAt.IsZero() {
			t.Fatalf("%v %v %+v", err, ok, rec.v)
		}
		st.close()
	}
}

// A vessel back from the sweep with only no-fix reports is off the map, and a restart keeps it off.
func TestRestartKeepsAnOldFixOffTheMap(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, 3*time.Hour)
	forget(p)
	now := time.Now().Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 0, 0))
	mustFlush(t, p)
	if fc := getFC(t, p, "/v1/vessels?bbox=59,10,61,11"); len(fc.Features) != 0 {
		t.Fatalf("before the restart: %+v", fc.Features)
	}
	next := restartedPipeline(t, p)
	if fc := getFC(t, next, "/v1/vessels?bbox=59,10,61,11"); len(fc.Features) != 0 {
		t.Errorf("the restart put an old fix on the map: %+v", fc.Features)
	}
	if fc := getFC(t, next, "/v1/vessels?mmsi=257000001"); len(fc.Features) != 1 || fc.Features[0].Geometry.Coordinates[1] < 59.89 {
		t.Errorf("a followed MMSI still answers with its last known position: %+v", fc.Features)
	}
}
