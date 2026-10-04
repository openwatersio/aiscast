package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func init() { tcMinVessels = 1 }

// tcXlsx builds the register's spreadsheet shape: shared strings for text cells, numbers inline, cells
// addressed by column letter.
func tcXlsx(t *testing.T, rows [][2]string) []byte {
	t.Helper()
	var shared []string
	sharedIdx := map[string]int{}
	sst := func(s string) int {
		if i, ok := sharedIdx[s]; ok {
			return i
		}
		shared = append(shared, s)
		sharedIdx[s] = len(shared) - 1
		return len(shared) - 1
	}
	var sheet strings.Builder
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rn, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, rn+1)
		for _, cell := range strings.Split(row[0], "|") {
			ref, kind, val := strings.SplitN(cell, ";", 3)[0], strings.SplitN(cell, ";", 3)[1], strings.SplitN(cell, ";", 3)[2]
			if kind == "s" {
				fmt.Fprintf(&sheet, `<c r="%s%d" t="s"><v>%d</v></c>`, ref, rn+1, sst(val))
			} else {
				fmt.Fprintf(&sheet, `<c r="%s%d"><v>%s</v></c>`, ref, rn+1, val)
			}
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	var ss strings.Builder
	ss.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range shared {
		fmt.Fprintf(&ss, `<si><t>%s</t></si>`, s)
	}
	ss.WriteString(`</sst>`)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"xl/sharedStrings.xml": ss.String(), "xl/worksheets/sheet1.xml": sheet.String()} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func fakeTC(t *testing.T) string {
	t.Helper()
	header := "A;s;Official Number|B;s;Vessel Name|C;s;IMO Vessel Number|E;s;Year of Build|G;s;Port of Registry|I;s;Vessel Descriptor|J;s;Gross Tonnage|K;s;Net Tonnage|N;s;Length|O;s;Breadth|P;s;Depth"
	rows := [][2]string{
		{header, ""},
		// year 196000 is 1960; the official number arrives as a float
		{"A;n;313730.0|B;s;OCEAN MASTER|C;s;5260423|E;n;196000|G;s;VANCOUVER|I;s;FISHING|J;n;500.77|K;n;340.2|N;n;36.48|O;n;9.48|P;n;4.2", ""},
		// junk year and no IMO: the row is dropped
		{"A;n;152527.0|B;s;PRODIGAL|C;s;|E;n;719|G;s;VANCOUVER|I;s;NON-COMMERCIAL|J;n;17.38|K;n;13.76|N;n;10.06|O;n;3.44|P;n;1.52", ""},
		// an IMO that fails its check digit is dropped
		{"A;n;999999.0|B;s;MISTYPED|C;s;5260424|E;n;200000|G;s;TORONTO|I;s;FISHING|J;n;100|K;n;80|N;n;20|O;n;6|P;n;2", ""},
	}
	body := tcXlsx(t, rows)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTCSyncAndServe(t *testing.T) {
	p := storePipeline(t)
	url := fakeTC(t)
	now := time.Now().UTC()

	// A Canadian vessel with the registered IMO, and a US-flag vessel carrying the same IMO, which the
	// Canadian register must not speak for.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(316001234, 49.3, -123.1))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(316001234, 5260423, "OCEAN MASTER"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(366112233, 5260423, "OCEAN MASTER"))
	mustFlush(t, p)

	if !p.syncTCIfDue(now, url) {
		t.Fatal("first sync did not run")
	}
	if n := p.tc.vessels.Load(); n != 1 {
		t.Fatalf("stored %d vessels, want 1", n)
	}
	if p.syncTCIfDue(now.Add(6*24*time.Hour), url) {
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
	json.Unmarshal(get(t, p, "/v1/vessels/316001234").Body.Bytes(), &f)
	m := f.Properties.Particulars
	if m == nil || m.RegisteredName != "OCEAN MASTER" || m.Identification != "313730" || m.Service != "FISHING" ||
		m.YearBuilt != 1960 || m.GrossTonnage != 501 || m.NetTonnage != 340 || m.Length != 36.48 ||
		m.Beam != 9.48 || m.Depth != 4.2 || m.HomePort != "VANCOUVER" || m.Registry != "Canada" {
		t.Errorf("particulars: %+v", m)
	}
	if f.Properties.Provenance["year_built"] != "tc" || f.Properties.Sources["tc"].License != "OGL-Canada-2.0" {
		t.Errorf("provenance %v sources %+v", f.Properties.Provenance, f.Properties.Sources)
	}

	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/366112233").Body.Bytes(), &f)
	if f.Properties.Sources["tc"].Credit != "" {
		t.Errorf("the register spoke for a US-flag vessel: %+v", f.Properties.Sources)
	}

	// A CA vessel broadcasting the registered IMO under a disagreeing name gets nothing: a copied or
	// mistyped IMO must not serve another registered ship's facts.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(316005678, 44.6, -63.6))
	p.ingestPacket("kystverket", "kystverket", now, now, staticIMO(316005678, 5260423, "SOMETHING ELSE"))
	mustFlush(t, p)
	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/316005678").Body.Bytes(), &f)
	if f.Properties.Sources["tc"].Credit != "" {
		t.Errorf("a disagreeing name still got the register: %+v", f.Properties.Sources)
	}

	// The MCP path gates the same way: the CA vessel gets the register, the US vessel does not.
	cs := mcpClient(t, p)
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{316001234, 366112233}}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 {
		t.Fatalf("get_vessels: %+v", out.Vessels)
	}
	for _, v := range out.Vessels {
		hasTC := v.Sources["tc"].Credit != ""
		if (v.MMSI == 316001234) != hasTC {
			t.Errorf("mcp tc gate for %d: %+v", v.MMSI, v.Sources)
		}
	}

	if err := p.store.replaceTC(map[uint32]*tcVessel{}, now); err == nil {
		t.Error("a collapsed sync was stored")
	}
	if err := p.loadTCStats(); err != nil || p.tc.vessels.Load() != 1 {
		t.Errorf("stats from boot: %d, %v", p.tc.vessels.Load(), err)
	}
}

func TestTCYear(t *testing.T) {
	for in, want := range map[string]int{"196000": 1960, "202608": 2026, "719": 0, "0": 0, "1121": 0, "1960": 1960, "": 0,
		"180000": 1800, "179900": 0, "202700": 2027, "999900": 0, "195999.9999": 1960, "NaN": 0, "+Inf": 0} {
		if got := tcYear(in); got != want {
			t.Errorf("tcYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTCFloatAndOfficial(t *testing.T) {
	for in, want := range map[string]float64{"36.48": 36.48, "NaN": 0, "Inf": 0, "-Inf": 0, "-3": 0, "0": 0, "1e9": 0, "": 0} {
		if got := tcFloat(in); got != want {
			t.Errorf("tcFloat(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]string{"152527.0": "152527", "313730.00": "313730", "3.1373E5": "313730",
		"C12345NB": "C12345NB", "152527.50": "152527.50", "": ""} {
		if got := tcOfficial(in); got != want {
			t.Errorf("tcOfficial(%q) = %q, want %q", in, got, want)
		}
	}
}
