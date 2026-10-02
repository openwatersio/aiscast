package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// The fixtures list six vessels, far under the minimum a real listing must find.
func init() { psixMinVessels = 1 }

// failDetails, when set, is a PSIX vessel ID whose dimensions and tonnage the fake service fails to answer.
var failDetails atomic.Int64

// fakePSIX answers the PSIX SOAP service from testdata/uscg, recorded from cgmix.uscg.mil: call-sign
// searches for six vessels, and their dimensions and tonnage. A listing by service type or build year is
// answered with the recorded rows that fit it. fail makes the recreational listing answer as the service
// does when a listing is too large to build.
func fakePSIX(t *testing.T, fail *atomic.Bool) (url string, requests *atomic.Int64) {
	t.Helper()
	psixPause, psixDetailPause = 0, 0
	type row struct {
		raw           string
		service, year string
	}
	var rows []row
	files, _ := filepath.Glob("testdata/uscg/summary_*.xml")
	elem := regexp.MustCompile(`(?s)<VesselSummary>.*?</VesselSummary>`)
	field := func(s, name string) string {
		m := regexp.MustCompile(`<` + name + `>(.*?)</` + name + `>`).FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		return m[1]
	}
	for _, f := range files {
		doc, err := psixFixture(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range elem.FindAllString(string(doc), -1) {
			rows = append(rows, row{raw: r, service: field(r, "ServiceType"), year: field(r, "ConstructionCompletedYear")})
		}
	}
	requests = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.Contains(r.UserAgent(), "@") {
			t.Errorf("User-Agent %q gives no contact", r.UserAgent())
		}
		body, _ := io.ReadAll(r.Body)
		param := func(name string) string { return field(string(body), name) }
		op := strings.Trim(strings.TrimPrefix(r.Header.Get("SOAPAction"), `"https://cgmix.uscg.mil/`), `"`)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		switch op {
		case "getVesselSummaryXMLString":
			if param("Flag") != "UNITED STATES" {
				t.Errorf("listing for flag %q", param("Flag"))
			}
			service, year := param("Service"), param("BuildYear")
			if service == "Recreational" && year == "1978" && fail != nil && fail.Load() {
				b, _ := os.ReadFile("testdata/uscg/no_result.xml")
				w.Write(b)
				return
			}
			var doc strings.Builder
			doc.WriteString(`<?xml version="1.0" encoding="utf-8"?><NewDataSet>`)
			for _, x := range rows {
				if x.service == service && (year == "" || x.year == year) {
					doc.WriteString(x.raw)
				}
			}
			doc.WriteString(`</NewDataSet>`)
			var esc bytes.Buffer
			xml.EscapeText(&esc, []byte(doc.String()))
			fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><%[1]sResponse xmlns="https://cgmix.uscg.mil"><%[1]sResult>%[2]s</%[1]sResult></%[1]sResponse></soap:Body></soap:Envelope>`, op, esc.String())
		case "getVesselDimensionsXMLString", "getVesselTonnageXMLString":
			if id, _ := strconv.Atoi(param("VesselID")); id != 0 && int64(id) == failDetails.Load() {
				http.Error(w, "server error", http.StatusInternalServerError)
				return
			}
			kind := map[string]string{"getVesselDimensionsXMLString": "dimensions", "getVesselTonnageXMLString": "tonnage"}[op]
			b, err := os.ReadFile("testdata/uscg/" + kind + "_" + param("VesselID") + ".xml")
			if err != nil {
				t.Errorf("no fixture for %s of %s", kind, param("VesselID"))
				http.Error(w, "no fixture", http.StatusInternalServerError)
				return
			}
			w.Write(b)
		default:
			t.Errorf("unexpected operation %q", op)
			http.Error(w, "unknown operation", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

// psixFixture is the result document inside a recorded SOAP response.
func psixFixture(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(b) }))
	defer srv.Close()
	return psixCall(context.Background(), srv.URL, "fixture", nil)
}

func TestNamesAgree(t *testing.T) {
	// Pairs from US traffic matched to PSIX by call sign: AIS name, then documented name.
	for _, c := range []struct {
		ais, psix string
		want      bool
	}{
		{"BR00KLYN MCALLISTER", "BROOKLYN MCALLISTER", true},
		{"F/V MELISSA K", "MELISSA K", true},
		{"GOV THOMAS H KEAN", "GOVERNOR THOMAS H. KEAN", true},
		{"NICOLE L REINAUER", "NICOLE LEIGH REINAUER", true},
		{"CG PENOBSCOT BAY", "USCGC PENOBSCOT BAY (WTGB 107)", true},
		{"DREDGE ILLINOIS", "ILLINOIS", true},
		{"THE MANHATTAN 2", "THE MANHATTAN II", true},
		{"J&S 1", "J & S 1", true},
		{"WEEKS 551", "BREAKWATER 551", false},
		{"2502", "2504", false},
		{"DB BEAVER", "ANNALISE", false},
		{"MARY B", "ROGER H", false},
		{"", "ANNALISE", false},
	} {
		if got := namesAgree(c.ais, c.psix); got != c.want {
			t.Errorf("namesAgree(%q, %q) = %v", c.ais, c.psix, got)
		}
	}
}

func TestNormCallSign(t *testing.T) {
	for in, want := range map[string]string{"wcz5696 ": "WCZ5696", "WMKN": "WMKN", "NIGY": "NIGY", "NONE": "", "N/A": "",
		"8": "", "--": "", "VOYAGER": "", "FORD": "", "C6XY7": ""} {
		if got := normCallSign(in); got != want {
			t.Errorf("normCallSign(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchPSIX(t *testing.T) {
	url, _ := fakePSIX(t, nil)
	got, err := fetchPSIX(t.Context(), url, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int]string{}
	for id, v := range got {
		ids[id] = v.Name
	}
	// The three DUPLICATE OF records under WCZ5696 are left out; CERULEAN comes from the 1978 listing.
	want := map[int]string{1097015: "MAERSK KENSINGTON", 507140: "NICOLE LEIGH REINAUER", 607128: "CONGRESSMAN ROBERT A. ROE",
		171966: "PAIGE MARIE", 872923: "ANNALISE", 167771: "CERULEAN"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("listed %v", ids)
	}
	if v := got[507140]; v.callsign != "WCZ5696" || v.Service != "Towing Vessel" || v.Status != "Active" || v.YearBuilt != 1999 || v.Identification == "" {
		t.Errorf("summary: %+v", *v)
	}
}

func TestPSIXDetails(t *testing.T) {
	url, _ := fakePSIX(t, nil)
	for id, want := range map[int]uscgVessel{
		1097015: {Length: 286.88, Beam: 39.99, Depth: 20.3, GrossTonnage: 74642, NetTonnage: 44243, TonnageMeasure: "Convention"},
		// measured under both systems; Convention wins over the older Regulatory
		507140: {Length: 36.15, Beam: 12.19, Depth: 7.01, GrossTonnage: 858, NetTonnage: 257, TonnageMeasure: "Convention"},
		// the formal hull measurement over the simplified, and not one hull of the catamaran
		607128: {Length: 23.93, Beam: 8.69, Depth: 2.53, GrossTonnage: 82, NetTonnage: 65, TonnageMeasure: "Simplified"},
		171966: {Length: 12.68, Beam: 4.39, Depth: 2.5, GrossTonnage: 33, NetTonnage: 25, TonnageMeasure: "Regulatory"},
	} {
		v := &uscgVessel{ID: id}
		if err := psixDetails(t.Context(), url, v); err != nil {
			t.Fatal(err)
		}
		want.ID = id
		if !reflect.DeepEqual(*v, want) {
			t.Errorf("%d: %+v, want %+v", id, *v, want)
		}
	}
}

// staticCallSign is a type 5 message with a name and call sign.
func staticCallSign(mmsi uint32, name, callsign string) ais.Packet {
	return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: name, CallSign: callsign, Type: 52}
}

func TestUSCGSync(t *testing.T) {
	var fail atomic.Bool
	url, requests := fakePSIX(t, &fail)
	p := storePipeline(t)
	now := time.Now().UTC()
	for _, v := range []struct {
		mmsi           uint32
		name, callsign string
	}{
		{367000001, "NICOLE L REINAUER", "WCZ5696"},
		{367000002, "CONG ROBERT A ROE", "WDH3491"}, // two active vessels share the call sign; the name picks
		{367000003, "DB BEAVER", "WCW6340"},         // the call sign is another vessel's
		{368168720, "CERULEAN", "WYC8908"},
		{366000004, "MAERSK KENSINGTON", "WMKN"},
		{257000009, "MAERSK KENSINGTON", "WMKN"}, // not US-flag
	} {
		p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(v.mmsi, v.name, v.callsign))
	}
	mustFlush(t, p)

	fail.Store(true)
	if p.syncUSCGIfDue(now, url) || p.uscg.failures.Load() != 1 {
		t.Fatalf("a failed listing counted as done: failures %d", p.uscg.failures.Load())
	}
	if last, _ := p.store.meta("uscg_sync"); last != "" {
		t.Errorf("failed listing recorded %q", last)
	}
	fail.Store(false)
	if !p.syncUSCGIfDue(now, url) || p.uscg.vessels.Load() != 6 {
		t.Fatalf("listing: vessels %d", p.uscg.vessels.Load())
	}
	before := requests.Load()
	if p.syncUSCGIfDue(now.Add(6*24*time.Hour), url) || requests.Load() != before {
		t.Error("listed again within the week")
	}

	type props struct {
		Properties struct {
			Particulars *particulars         `json:"particulars"`
			Sources     map[string]sourceRef `json:"sources"`
		} `json:"properties"`
	}
	vessel := func(mmsi uint32) props {
		var f props
		json.Unmarshal(get(t, p, fmt.Sprintf("/v1/vessels/%d", mmsi)).Body.Bytes(), &f)
		return f
	}
	psixURL := func(id int) string {
		return fmt.Sprintf("https://cgmix.uscg.mil/PSIX/PSIXDetails.aspx?VesselID=%d", id)
	}
	// Listed but not yet measured: the summary alone.
	if f := vessel(366000004); f.Properties.Particulars == nil || f.Properties.Sources["uscg"].URL != psixURL(1097015) ||
		f.Properties.Particulars.Service != "Freight Ship" || f.Properties.Particulars.YearBuilt != 2007 || f.Properties.Particulars.Length != 0 {
		t.Errorf("before the backfill: %+v", f.Properties)
	}

	if n := p.backfillUSCG(now, url, time.Minute); n != 4 || p.uscg.details.Load() != 4 {
		t.Errorf("backfilled %d vessels, want the 4 matched", n)
	}
	if n := p.backfillUSCG(now, url, time.Minute); n != 0 {
		t.Errorf("backfilled %d vessels again", n)
	}
	if n := p.backfillUSCG(now.Add(psixDetailsEvery+time.Hour), url, time.Minute); n != 4 {
		t.Errorf("refreshed %d vessels after 90 days, want 4", n)
	}
	// One vessel's record failing passes over it; the round reads the rest.
	if _, err := p.store.db.Exec(`UPDATE uscg SET details_at = 0`); err != nil {
		t.Fatal(err)
	}
	failDetails.Store(507140)
	if n := p.backfillUSCG(now, url, time.Minute); n != 3 || p.uscg.detailFailures.Load() != 1 {
		t.Errorf("with one record failing, backfilled %d vessels and failed %d", n, p.uscg.detailFailures.Load())
	}
	failDetails.Store(0)

	got := vessel(366000004).Properties.Particulars
	want := &particulars{RegisteredName: "MAERSK KENSINGTON", Identification: "1257726", Service: "Freight Ship",
		Status: "Active", YearBuilt: 2007, Length: 286.88, Beam: 39.99, Depth: 20.3,
		GrossTonnage: 74642, NetTonnage: 44243, TonnageMeasure: "Convention", Registry: "United States"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("/v1/vessels: %+v, want %+v", got, want)
	}
	for mmsi, id := range map[uint32]int{367000001: 507140, 367000002: 607128, 368168720: 167771, 367000003: 0, 257000009: 0} {
		f := vessel(mmsi)
		cg, ok := f.Properties.Sources["uscg"]
		if (id == 0) == ok || ok && cg.URL != psixURL(id) {
			t.Errorf("%d: %+v, want PSIX vessel %d", mmsi, f.Properties, id)
		}
	}

	cs := mcpClient(t, p)
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{367000002, 367000003}}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 || out.Vessels[0].Particulars == nil || out.Vessels[0].Particulars.Service != "Passenger (Inspected)" || out.Vessels[1].Particulars != nil {
		t.Errorf("get_vessels: %+v", out.Vessels)
	}

	if err := p.loadUSCGStats(); err != nil {
		t.Fatal(err)
	}
	body := get(t, p, "/metrics").Body.String()
	for _, want := range []string{"aiscast_uscg_vessels 6\n", fmt.Sprintf("aiscast_uscg_last_success_timestamp_seconds %d\n", now.Unix())} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

func TestReplaceUSCGListing(t *testing.T) {
	p := storePipeline(t)
	listing := func(ids ...int) map[int]*uscgVessel {
		m := map[int]*uscgVessel{}
		for _, id := range ids {
			m[id] = &uscgVessel{ID: id, callsign: fmt.Sprintf("WDA%04d", id), Name: fmt.Sprintf("VESSEL %d", id)}
		}
		return m
	}
	if err := p.store.replaceUSCGListing(listing(1, 2, 3, 4), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.store.setUSCGDetails(&uscgVessel{ID: 2, Length: 30, GrossTonnage: 90}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.store.replaceUSCGListing(listing(1), time.Now()); err == nil {
		t.Error("a listing with a quarter of the vessels replaced the stored set")
	}
	// The next listing drops vessel 4 and keeps what was read for vessel 2.
	if err := p.store.replaceUSCGListing(listing(1, 2, 3), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	byCS, err := p.store.uscgByCallSign([]string{"WDA0002", "WDA0004"})
	if err != nil {
		t.Fatal(err)
	}
	if v := byCS["WDA0002"]; len(v) != 1 || v[0].Length != 30 || v[0].GrossTonnage != 90 {
		t.Errorf("vessel 2 lost its details: %+v", v)
	}
	if len(byCS["WDA0004"]) != 0 {
		t.Error("vessel 4 is still listed")
	}
	defer func(n int) { psixMinVessels = n }(psixMinVessels)
	psixMinVessels = 10
	if err := p.store.replaceUSCGListing(listing(1, 2, 3, 4, 5), time.Now()); err == nil {
		t.Error("a listing under the minimum was stored")
	}
}
