package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// fakeWDQS answers each sync query with its fixture in testdata/wikidata, recorded from the Query Service
// with the query narrowed to five items: two cruise ships sharing IMO 9208617, one with every field, a
// coaster with former names, and an item whose IMO is malformed. fail makes every query a 503.
func fakeWDQS(t *testing.T, fail *atomic.Bool) (url string, queries *atomic.Int64) {
	t.Helper()
	wikidataPause = 0
	queries = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		if !strings.Contains(r.UserAgent(), "@") {
			t.Errorf("User-Agent %q gives no contact", r.UserAgent())
		}
		if fail != nil && fail.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		q := r.FormValue("query")
		if strings.HasPrefix(q, "SELECT ?v ?label WHERE { VALUES ?v { ") {
			for _, qid := range []string{"Q55", "Q233", "Q783", "Q705377", "Q1327429", "Q14552001"} {
				if !strings.Contains(q, "wd:"+qid+" ") {
					t.Errorf("label query leaves out %s: %s", qid, q)
				}
			}
			b, _ := os.ReadFile("testdata/wikidata/labels.json")
			w.Write(b)
			return
		}
		for _, wq := range wikidataQueries {
			if wq.sparql == q {
				b, err := os.ReadFile("testdata/wikidata/" + wq.field + ".json")
				if err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/sparql-results+json")
				w.Write(b)
				return
			}
		}
		t.Errorf("unexpected query %q", q)
		http.Error(w, "unknown query", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, queries
}

var wantWikidata = map[uint32]*wikidataShip{
	9404314: {ID: "Q1052819", Builder: "Meyer Werft", YearBuilt: 2010, GrossTonnage: 121878, Deadweight: 9500, Length: 317.2, Beam: 36.8, Registry: "Malta"},
	// Q2109568 and Q83569204 both carry IMO 9208617; the lower QID wins.
	9208617: {ID: "Q2109568", Builder: "Fincantieri", YearBuilt: 2001, GrossTonnage: 59925, Length: 215.45, Beam: 31.88, Registry: "Netherlands"},
	5358206: {ID: "Q52296707", Builder: "Jansen-Werft", YearBuilt: 1958, Length: 53.01, Registry: "Honduras",
		FormerNames: []string{"Thekla", "Heimar", "Delice", "Valery"}},
}

func TestFetchWikidata(t *testing.T) {
	url, queries := fakeWDQS(t, nil)
	ships, err := fetchWikidata(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	if n := queries.Load(); n != int64(len(wikidataQueries))+1 {
		t.Errorf("%d queries, want %d and one for labels", n, len(wikidataQueries))
	}
	if !reflect.DeepEqual(ships, wantWikidata) {
		for imo, s := range ships {
			t.Errorf("%d: %+v", imo, *s)
		}
	}
}

func TestWikidataSync(t *testing.T) {
	var fail atomic.Bool
	url, queries := fakeWDQS(t, &fail)
	p := storePipeline(t)
	now := time.Now().UTC()

	fail.Store(true)
	if p.syncWikidataIfDue(now, url) || p.wikidata.failures.Load() != 1 {
		t.Fatalf("a failed sync counted as done: failures %d", p.wikidata.failures.Load())
	}
	if last, _ := p.store.meta("wikidata_sync"); last != "" {
		t.Errorf("failed sync recorded %q", last)
	}

	fail.Store(false)
	if !p.syncWikidataIfDue(now, url) || p.wikidata.ships.Load() != 3 {
		t.Fatalf("sync: ships %d", p.wikidata.ships.Load())
	}
	before := queries.Load()
	if p.syncWikidataIfDue(now.Add(6*24*time.Hour), url) || queries.Load() != before {
		t.Error("synced again within the week")
	}
	if !p.syncWikidataIfDue(now.Add(wikidataEvery), url) {
		t.Error("no sync after a week")
	}

	got, err := p.store.wikidataShips([]uint32{9404314, 9208617, 5358206, 1234567})
	if err != nil {
		t.Fatal(err)
	}
	for imo, w := range wantWikidata {
		want := *w
		want.URL, want.License = "https://www.wikidata.org/wiki/"+w.ID, "CC0-1.0"
		if g := got[imo]; g == nil || !reflect.DeepEqual(*g, want) {
			t.Errorf("%d: %+v, want %+v", imo, g, want)
		}
	}
	if got[1234567] != nil {
		t.Error("an IMO without an item has particulars")
	}
}

func TestReplaceWikidataRefusesAShortSync(t *testing.T) {
	p := storePipeline(t)
	if err := p.store.replaceWikidata(wantWikidata); err != nil {
		t.Fatal(err)
	}
	if err := p.store.replaceWikidata(map[uint32]*wikidataShip{}); err == nil {
		t.Error("an empty sync replaced the stored set")
	}
	if err := p.store.replaceWikidata(map[uint32]*wikidataShip{9404314: wantWikidata[9404314]}); err == nil {
		t.Error("a sync with a third of the ships replaced the stored set")
	}
	// Every ship present, but one field's query came back short: the builders are gone.
	noBuilders := map[uint32]*wikidataShip{}
	for imo, w := range wantWikidata {
		c := *w
		c.Builder = ""
		noBuilders[imo] = &c
	}
	if err := p.store.replaceWikidata(noBuilders); err == nil || !strings.Contains(err.Error(), "builder") {
		t.Errorf("a sync that lost every builder: %v", err)
	}
	got, _ := p.store.wikidataShips([]uint32{5358206})
	if got[5358206] == nil || got[5358206].Builder != "Jansen-Werft" {
		t.Errorf("the stored set was lost: %+v", got[5358206])
	}
	// Losing one builder of three is under half, so it is taken as an edit on Wikidata.
	oneLess := map[uint32]*wikidataShip{}
	for imo, w := range wantWikidata {
		oneLess[imo] = w
	}
	c := *wantWikidata[5358206]
	c.Builder = ""
	oneLess[5358206] = &c
	if err := p.store.replaceWikidata(oneLess); err != nil {
		t.Errorf("an ordinary sync was refused: %v", err)
	}
}

// staticIMO is a type 5 message naming the vessel's IMO.
func staticIMO(mmsi, imo uint32, name string) ais.Packet {
	return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: name, Type: 60, ImoNumber: imo}
}

func TestVesselWikidata(t *testing.T) {
	p := storePipeline(t)
	if err := p.store.replaceWikidata(wantWikidata); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(256000001, 59.9, 10.7))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(256000001, 9404314, "CELEBRITY ECLIPSE"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(257000002, 1234567, "NO ITEM"))
	// 9404315 fails the check digit, so it is never looked up.
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(257000003, 9404315, "MISTYPED"))
	mustFlush(t, p)

	var f struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	json.Unmarshal(get(t, p, "/v1/vessels/256000001").Body.Bytes(), &f)
	var w map[string]any
	if err := json.Unmarshal(f.Properties["wikidata"], &w); err != nil {
		t.Fatalf("no wikidata: %s", f.Properties["wikidata"])
	}
	if w["id"] != "Q1052819" || w["url"] != "https://www.wikidata.org/wiki/Q1052819" || w["license"] != "CC0-1.0" ||
		w["builder"] != "Meyer Werft" || w["year_built"] != 2010.0 || w["gross_tonnage"] != 121878.0 || w["deadweight"] != 9500.0 ||
		w["length"] != 317.2 || w["beam"] != 36.8 || w["registry"] != "Malta" || w["former_names"] != nil {
		t.Errorf("wikidata: %v", w)
	}
	for _, mmsi := range []string{"257000002", "257000003"} {
		f.Properties = nil
		json.Unmarshal(get(t, p, "/v1/vessels/"+mmsi).Body.Bytes(), &f)
		if f.Properties == nil || f.Properties["wikidata"] != nil {
			t.Errorf("%s: %v", mmsi, f.Properties)
		}
	}

	// The collection stays as it was: particulars are on the one-vessel answer only.
	if body := get(t, p, "/v1/vessels?mmsi=256000001").Body.String(); strings.Contains(body, "wikidata") {
		t.Errorf("collection carries particulars: %s", body)
	}

	cs := mcpClient(t, p)
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{256000001, 257000002}}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 || out.Vessels[0].Wikidata == nil || out.Vessels[0].Wikidata.Builder != "Meyer Werft" || out.Vessels[1].Wikidata != nil {
		t.Errorf("get_vessels: %+v", out.Vessels)
	}
}

func TestWikidataRefInForce(t *testing.T) {
	row := func(qid, end string) wikidataBinding {
		return wikidataBinding{V: sparqlTerm{Value: "http://www.wikidata.org/entity/" + qid}, End: sparqlTerm{Value: end}}
	}
	var r wikidataRef
	r.take(row("Q55", "2015-01-01T00:00:00Z")) // left the Dutch registry
	r.take(row("Q233", ""))
	r.take(row("Q30", "2020-01-01T00:00:00Z"))
	r.take(row("Q783", ""))
	if r.qid != 233 || r.ended {
		t.Errorf("took %+v, want the lowest registry in force, Q233", r)
	}
}

func TestValidIMO(t *testing.T) {
	for n, want := range map[uint32]bool{9404314: true, 9208617: true, 5358206: true, 9319466: true, 9404315: false, 1014: false, 0: false, 10000000: false} {
		if validIMO(n) != want {
			t.Errorf("validIMO(%d) = %v", n, !want)
		}
	}
}
