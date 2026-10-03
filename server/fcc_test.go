package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func init() { fccMinShips = 1 }

// fakeFCC serves a ULS zip: four licenses, one expired, one MMSI named by two licenses.
func fakeFCC(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hd, _ := zw.Create("HD.dat")
	hd.Write([]byte(strings.Join([]string{
		"HD|1000001|||WDA0001|A|SA|01/01/2020",
		"HD|1000002|||WDA0002|E|SA|01/01/2010", // expired: left out
		"HD|1000003|||WDA0003|A|SB|01/01/2021",
		"HD|1000004|||WDA0004|A|SA|01/01/2024", // the newer license for 367000100
		"HD|1000006|||WDA0006|A|SA|01/01/2023",
		"HD|1000007|||WDA0007|A|SA|01/01/2023",
		"", // ULS files end with a blank line now and then
	}, "\n")))
	sh, _ := zw.Create("SH.dat")
	sh.Write([]byte(strings.Join([]string{
		"SH|1000001|||WDA0001|R||MM|CIT|RESOLUTE|1257726|Y|Y|||100|30|W19|W50|11220|1502110|367000100|Y|N|N|N|Y",
		"SH|1000002|||WDA0002|R||MM|CIT|OLD BOAT|999999|Y|Y|||10||W19|W50|||367000200|Y|N|N|N|Y",
		"SH|1000003|||WDA0003|R||MM|CIT|SEA TOW 42|fl 8656-lb|Y|Y|||5||W19|W50|||367000300|Y|N|N|N|Y",
		"SH|1000006|||WDA0006|R||MM|CIT|S\xd8NDERJYDEN\x92S|0000000|Y|Y|||5||W19|W50|||367000400|Y|N|N|N|Y", // Windows-1252 bytes in the name
		"SH|1000007|||WDA0007|R||MM|CIT|WRONG FLAG|7654321|Y|Y|||5||W19|W50|||230999999|Y|N|N|N|Y",
		"SH|1000004|||WDA0004|R||MM|CIT|RESOLUTE II|1257727|Y|Y|||100|30|W19|W50|11220|1502110|367000100|Y|N|N|N|Y",
		"SH|1000005|||WDA0005|R||MM|CIT|NO MMSI ROW||Y|Y|||5||W19|W50|||||N|N|N|Y",
	}, "\n")))
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFCCSyncAndServe(t *testing.T) {
	p := storePipeline(t)
	url := fakeFCC(t)
	now := time.Now().UTC()

	if !p.syncFCCIfDue(now, url) {
		t.Fatal("first sync did not run")
	}
	// Three licensed MMSIs survive: the expired license, the MMSI-less row, and the mistyped Finnish
	// MMSI are out, and 367000100's
	// two licenses collapse to the newest.
	if n := p.fcc.ships.Load(); n != 3 {
		t.Fatalf("stored %d ships, want 3", n)
	}
	if p.syncFCCIfDue(now.Add(6*24*time.Hour), url) {
		t.Error("synced again within the week")
	}
	ships, err := p.store.fccByMMSI([]uint32{367000100, 367000300, 367000400})
	if err != nil || ships[367000100] == nil || ships[367000100].Name != "RESOLUTE II" || ships[367000100].Official != "1257727" ||
		ships[367000300] == nil || ships[367000300].Official != "FL8656LB" ||
		ships[367000400].Official != "" || ships[367000400].Name != "SØNDERJYDEN’S" {
		t.Fatalf("stored: %+v, %v", ships, err)
	}

	// A PSIX record whose call sign AIS mangles: only the license's official number can find it. And an
	// ident-only record with no call sign at all, listed under a spaced registration that must match the
	// license's normalized FL8656LB.
	if err := p.store.replaceUSCGListing(map[int]*uscgVessel{
		900001: {ID: 900001, License: psixLicense, Name: "RESOLUTE II", Identification: "1257727", officialKey: "1257727",
			Service: "Towing Vessel", Status: "Active", YearBuilt: 1999, callsign: "WXY9999"},
		900002: {ID: 900002, License: psixLicense, Name: "SEA TOW 42", Identification: "FL 8656-LB", officialKey: "FL8656LB",
			Service: "Recreational", Status: "Active", YearBuilt: 2015},
	}, now); err != nil {
		t.Fatal(err)
	}
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(367000100, 29.0, -90.0))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(367000100, "RESOLUTE II", "@@@@@@@"))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(367000300, "SEA TOW 42", "WDA0003"))
	mustFlush(t, p)

	type props struct {
		Properties struct {
			Particulars *particulars         `json:"particulars"`
			Provenance  map[string]string    `json:"provenance"`
			Sources     map[string]sourceRef `json:"sources"`
		} `json:"properties"`
	}
	var f props
	json.Unmarshal(get(t, p, "/v1/vessels/367000100").Body.Bytes(), &f)
	m := f.Properties.Particulars
	if m == nil || m.Service != "Towing Vessel" || m.YearBuilt != 1999 || m.Identification != "1257727" {
		t.Errorf("official number did not reach PSIX: %+v", m)
	}
	if f.Properties.Provenance["service"] != "uscg" || f.Properties.Sources["fcc"].URL !=
		"https://wireless2.fcc.gov/UlsApp/UlsSearch/license.jsp?licKey=1000004" {
		t.Errorf("provenance %v sources %+v", f.Properties.Provenance, f.Properties.Sources)
	}

	// The official-matched records are also what the dimension backfill works through, even though no
	// call sign joins them, and a vessel whose stored call sign is blank padding joins nothing.
	if _, err := p.store.db.Exec(`UPDATE vessels SET callsign = '   ' WHERE mmsi = 366000009`); err != nil {
		t.Fatal(err)
	}
	due, err := p.store.uscgDue(t.Context(), now.Add(time.Hour))
	ids := map[int]bool{}
	for _, v := range due {
		ids[v.ID] = true
	}
	if err != nil || len(due) != 2 || !ids[900001] || !ids[900002] {
		t.Errorf("backfill due: %+v, %v", due, err)
	}

	// A licensed vessel with no usable call sign anywhere reaches its ident-only PSIX record through the
	// official number alone, decoded into a fresh struct so nothing lingers from the previous answer.
	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/367000300").Body.Bytes(), &f)
	m = f.Properties.Particulars
	if m == nil || m.RegisteredName != "SEA TOW 42" || m.Service != "Recreational" || m.YearBuilt != 2015 ||
		m.Identification != "FL 8656-LB" || m.Registry != "United States" {
		t.Errorf("ident-only particulars: %+v", m)
	}
	if f.Properties.Provenance["service"] != "uscg" || f.Properties.Provenance["identification"] != "uscg" {
		t.Errorf("provenance: %v", f.Properties.Provenance)
	}

	// A collapsed sync is refused.
	if err := p.store.replaceFCC(map[uint32]*fccShip{}, now); err == nil {
		t.Error("a collapsed sync was stored")
	}
	if err := p.loadFCCStats(); err != nil || p.fcc.ships.Load() != 3 {
		t.Errorf("stats from boot: %d, %v", p.fcc.ships.Load(), err)
	}
}
