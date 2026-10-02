package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() { fdirMinVessels, fdirPause = 1, 0 }

// fakeFiskeridir answers the register's API with two pages: a full first page of filler plus the
// interesting rows, and a short second page that ends the walk.
func fakeFiskeridir(t *testing.T) (url string, requests *atomic.Int64) {
	t.Helper()
	interesting := []string{
		// A large vessel: London Convention tonnage and a company owner.
		`{"id":"100","name":"H ØSTERVOLD","registrationMark":"VL0148AV","radioCallSign":"3YPL","width":16,"length":80,
		  "buildYear":2020,"tonnage":3439,"tonnageType":"LC","owners":[{"entityType":"COMPANY","name":"H ØSTERVOLD AS"}]}`,
		// No call sign: nothing could match it, so the sync leaves it out.
		`{"id":"101","name":"ODDFRID","registrationMark":"R 0002HM","width":2.2,"length":6.65,"buildYear":1979,
		  "tonnageType":"OC","owners":[{"entityType":"COMPANY","name":"NESSA AS"}]}`,
		// A person as owner, dropped, and a 1947-measure tonnage, dropped with it.
		`{"id":"102","name":"ALISA II","registrationMark":"M 0012HU","radioCallSign":"LK6221","width":3.6,"length":9.12,
		  "buildYear":1997,"tonnage":5,"tonnageType":"OC","owners":[{"entityType":"PERSON","name":"OLA NORDMANN"}]}`,
	}
	var filler []string
	for i := range 27 {
		filler = append(filler, fmt.Sprintf(`{"id":"%d","name":"FILLER %d","registrationMark":"N %04dF","radioCallSign":"LK%04d",
			"width":3,"length":10,"buildYear":2000,"tonnageType":"OC","owners":[]}`, 200+i, i, i, 1000+i))
	}
	pages := map[string]string{
		"1": "[" + strings.Join(append(interesting, filler...), ",") + "]",
		"2": `[{"id":"300","name":"LAST ONE","registrationMark":"T 0001T","radioCallSign":"LM9999","width":4,"length":12,
			"buildYear":2010,"tonnageType":"OC","owners":[]}]`,
	}
	requests = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, ok := pages[r.URL.Query().Get("page")]
		if !ok {
			body = "[]"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

func TestFiskeridirSyncAndServe(t *testing.T) {
	p := storePipeline(t)
	url, requests := fakeFiskeridir(t)
	now := time.Now().UTC()

	// The AIS side: a Norwegian vessel whose name the register spells with Ø, one whose name disagrees
	// with the register's for the same call sign, and a non-Norwegian flag with a Norwegian-looking call sign.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 62.0, 5.0))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(257000001, "H OSTERVOLD", "3YPL"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(257000002, "SOMETHING ELSE", "LK6221"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(366000009, "H OSTERVOLD", "3YPL"))
	mustFlush(t, p)

	if !p.syncFiskeridirIfDue(now, url) {
		t.Fatal("first sync did not run")
	}
	// 30 rows with a call sign: the full page minus ODDFRID, plus the short page's one.
	if n := p.fiskeridir.vessels.Load(); n != 30 {
		t.Fatalf("stored %d vessels, want 30", n)
	}
	before := requests.Load()
	if p.syncFiskeridirIfDue(now.Add(6*24*time.Hour), url) || requests.Load() != before {
		t.Error("synced again within the week")
	}

	var f struct {
		Properties struct {
			Particulars *particulars         `json:"particulars"`
			Provenance  map[string]string    `json:"provenance"`
			Sources     map[string]sourceRef `json:"sources"`
		} `json:"properties"`
	}
	json.Unmarshal(get(t, p, "/v1/vessels/257000001").Body.Bytes(), &f)
	m := f.Properties.Particulars
	if m == nil || m.YearBuilt != 2020 || m.Length != 80 || m.Beam != 16 || m.GrossTonnage != 3439 ||
		m.Owner != "H ØSTERVOLD AS" || m.Identification != "VL0148AV" || m.RegisteredName != "H ØSTERVOLD" ||
		m.Registry != "Norway" {
		t.Errorf("particulars: %+v", m)
	}
	if f.Properties.Provenance["year_built"] != "fiskeridir" {
		t.Errorf("provenance: %v", f.Properties.Provenance)
	}
	if s := f.Properties.Sources["fiskeridir"]; s.Credit != "Norwegian Directorate of Fisheries" || s.License != "NLOD-2.0" || s.URL != "" {
		t.Errorf("sources: %+v", f.Properties.Sources)
	}

	// The name check turns away the register's ALISA II, and a person as owner was never stored. The
	// non-Norwegian flag never looks at the register.
	for _, mmsi := range []string{"257000002", "366000009"} {
		var g struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		json.Unmarshal(get(t, p, "/v1/vessels/"+mmsi).Body.Bytes(), &g)
		if g.Properties["particulars"] != nil {
			t.Errorf("%s: unexpected particulars %s", mmsi, g.Properties["particulars"])
		}
	}
	if byCS, err := p.store.fiskeridirByCallSign([]string{"LK6221"}); err != nil || len(byCS["LK6221"]) != 1 ||
		byCS["LK6221"][0].Owner != "" || byCS["LK6221"][0].GrossTonnage != 0 {
		t.Errorf("ALISA II stored: %+v, %v", byCS["LK6221"], err)
	}

	// A sync that collapses is refused, keeping the stored set.
	if err := p.store.replaceFiskeridir(map[string]*fdirVessel{"x": {ID: "x", CallSign: "LK0000", Name: "X"}}, now); err == nil {
		t.Error("a collapsed sync was stored")
	}
	if err := p.loadFiskeridirStats(); err != nil || p.fiskeridir.vessels.Load() != 30 {
		t.Errorf("stats from boot: %d, %v", p.fiskeridir.vessels.Load(), err)
	}
}

func TestFiskeridirRefusesTruncation(t *testing.T) {
	p := storePipeline(t)
	url, _ := fakeFiskeridir(t)
	old := fdirMaxPages
	fdirMaxPages = 1 // page 1 is full, so the walk wants page 2 and must refuse instead
	t.Cleanup(func() { fdirMaxPages = old })
	if p.syncFiskeridirIfDue(time.Now().UTC(), url) {
		t.Fatal("a truncated register was stored")
	}
	if p.fiskeridir.failures.Load() != 1 {
		t.Errorf("failures = %d, want 1", p.fiskeridir.failures.Load())
	}
}

func TestNormCallSignNO(t *testing.T) {
	for in, want := range map[string]string{
		"3YPL": "3YPL", "LK6221": "LK6221", "lf2087 ": "LF2087", "JWABC": "JWABC",
		"WDA1234": "", "LK 62": "", "LX9999": "", "": "", "LK1": "",
	} {
		if got := normCallSignNO(in); got != want {
			t.Errorf("normCallSignNO(%q) = %q, want %q", in, got, want)
		}
	}
}
