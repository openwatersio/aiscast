package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func init() { amsaMinVessels = 1 }

// fakeAMSA serves the list's page, linking the spreadsheet by a dated path as the real page does, and the
// spreadsheet itself.
func fakeAMSA(t *testing.T) string {
	t.Helper()
	header := "A;s;Ship name|B;s;Official number|C;s;IMO number|D;s;Length|E;s;Year of completion|F;s;Type|G;s;Home port|H;s;Status"
	body := tcXlsx(t, [][2]string{
		{header, ""},
		// an official number written with a leading zero
		{"A;s;ABSOLUTE|B;s;062874|C;s;9869447|D;n;114.99|E;n;2019|F;s;Oil Tanker|G;s;Fremantle|H;s;Registered", ""},
		// no IMO: the row is dropped
		{"A;s;1 GIANT LEAP|B;n;861091|C;s;|D;n;13.3|E;n;2013|F;s;Yacht|G;s;Mooloolaba|H;s;Registered", ""},
		// an IMO that fails its check digit is dropped
		{"A;s;MISTYPED|B;n;852712|C;s;9869448|D;n;30.45|E;n;1986|F;s;Ferry|G;s;Hobart|H;s;Registered", ""},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/list-registered-ships", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/sites/default/files/fees.xlsx">Fees</a>
<a href="/sites/default/files/copy-of-list-of-registered-ships-06.10.26.xlsx">Download the list</a>`))
	})
	mux.HandleFunc("/sites/default/files/copy-of-list-of-registered-ships-06.10.26.xlsx", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/list-registered-ships"
}

func TestAMSASyncAndServe(t *testing.T) {
	p := storePipeline(t)
	page := fakeAMSA(t)
	now := time.Now().UTC()

	// An Australian vessel with the registered IMO, and a CA-flag vessel carrying the same IMO, which the
	// Australian list must not speak for.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(503001234, -32.0, 115.7))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(503001234, 9869447, "ABSOLUTE"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(316112233, 9869447, "ABSOLUTE"))
	mustFlush(t, p)

	if !p.syncAMSAIfDue(now, page) {
		t.Fatal("first sync did not run")
	}
	if n := p.amsa.vessels.Load(); n != 1 {
		t.Fatalf("stored %d vessels, want 1", n)
	}
	if p.syncAMSAIfDue(now.Add(6*24*time.Hour), page) {
		t.Error("synced again within the week")
	}

	type props struct {
		Properties struct {
			Particulars *particulars         `json:"particulars"`
			Provenance  map[string]string    `json:"provenance"`
			Sources     map[string]sourceRef `json:"sources"`
		} `json:"properties"`
	}
	var f props
	json.Unmarshal(get(t, p, "/v1/vessels/503001234").Body.Bytes(), &f)
	m := f.Properties.Particulars
	if m == nil || m.RegisteredName != "ABSOLUTE" || m.Identification != "062874" || m.ShipType != "Oil Tanker" ||
		m.Status != "Registered" || m.YearBuilt != 2019 || m.Length != 114.99 || m.HomePort != "Fremantle" ||
		m.Registry != "Australia" {
		t.Errorf("particulars: %+v", m)
	}
	src := f.Properties.Sources["amsa"]
	if f.Properties.Provenance["ship_type"] != "amsa" || src.License != "CC-BY-4.0" ||
		!strings.HasSuffix(src.URL, "?combine=9869447") {
		t.Errorf("provenance %v sources %+v", f.Properties.Provenance, f.Properties.Sources)
	}

	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/316112233").Body.Bytes(), &f)
	if f.Properties.Sources["amsa"].Credit != "" {
		t.Errorf("the list spoke for a CA-flag vessel: %+v", f.Properties.Sources)
	}

	// An AU vessel broadcasting the registered IMO under a disagreeing name gets nothing.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(503005678, -33.8, 151.2))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(503005678, 9869447, "SOMETHING ELSE"))
	mustFlush(t, p)
	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/503005678").Body.Bytes(), &f)
	if f.Properties.Sources["amsa"].Credit != "" {
		t.Errorf("a disagreeing name still got the list: %+v", f.Properties.Sources)
	}

	// The MCP path gates the same way.
	cs := mcpClient(t, p)
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{503001234, 316112233}}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 {
		t.Fatalf("get_vessels: %+v", out.Vessels)
	}
	for _, v := range out.Vessels {
		if (v.MMSI == 503001234) != (v.Sources["amsa"].Credit != "") {
			t.Errorf("mcp amsa gate for %d: %+v", v.MMSI, v.Sources)
		}
	}

	if err := p.store.replaceAMSA(map[uint32]*amsaVessel{}, now); err == nil {
		t.Error("a collapsed sync was stored")
	}
	// A sync that reads under half the stored set is refused, though it clears the minimum.
	three := map[uint32]*amsaVessel{9869447: {IMO: 9869447}, 8702783: {IMO: 8702783}, 9838644: {IMO: 9838644}}
	if err := p.store.replaceAMSA(three, now); err != nil {
		t.Fatal(err)
	}
	if err := p.store.replaceAMSA(map[uint32]*amsaVessel{9869447: three[9869447]}, now); err == nil {
		t.Error("a sync of one where three are stored was stored")
	}
	if err := p.loadAMSAStats(); err != nil || p.amsa.vessels.Load() != 3 {
		t.Errorf("stats from boot: %d, %v", p.amsa.vessels.Load(), err)
	}
}

func TestAMSASheetURL(t *testing.T) {
	for _, c := range []struct{ page, want string }{
		// the list's link wins over another spreadsheet, and a relative link resolves against the page
		{`<a href="/a/fees.xlsx"></a><a href="/files/list-of-registered-ships-01.02.27.xlsx"></a>`, "/files/list-of-registered-ships-01.02.27.xlsx"},
		// one spreadsheet under a new name is still the list
		{`<a href="https://cdn.example/register.xlsx?v=1&amp;x=2&#38;y=3"></a>`, "https://cdn.example/register.xlsx?v=1&x=2&y=3"},
		// a cache-busting query, single quotes, and an upper-case extension
		{`<a href='/files/List-Of-Registered-Ships.XLSX?v=123'></a>`, "/files/List-Of-Registered-Ships.XLSX?v=123"},
		{`<a href="/a.xlsx"></a><a href="/b.xlsx"></a>`, ""},
		{`<p>moved</p>`, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(c.page)) }))
		got, err := amsaSheetURL(context.Background(), srv.URL+"/page")
		srv.Close()
		if c.want == "" {
			if err == nil {
				t.Errorf("%q: got %q, want an error", c.page, got)
			}
			continue
		}
		if strings.HasPrefix(c.want, "/") {
			c.want = srv.URL + c.want
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.page, got, err, c.want)
		}
	}
}

func TestAMSAOfficial(t *testing.T) {
	for in, want := range map[string]string{"074784": "074784", " 862874 ": "862874", "862874.0": "862874", "": ""} {
		if got := amsaOfficial(in); got != want {
			t.Errorf("amsaOfficial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAMSAYear(t *testing.T) {
	for in, want := range map[string]int{"2019": 2019, " 1986 ": 1986, "0": 0, "": 0, "1799": 0, "2013.0": 2013, "2013.5": 0, "NaN": 0, "+Inf": 0, "9999": 0} {
		if got := amsaYear(in); got != want {
			t.Errorf("amsaYear(%q) = %d, want %d", in, got, want)
		}
	}
}
