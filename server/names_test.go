package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestDecideOwn(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	for _, c := range []struct {
		name    string
		cands   map[uint32]int64
		want    uint32
		decided bool
	}{
		{"none", nil, 0, false},
		{"one", map[uint32]int64{368168720: ago(time.Minute)}, 368168720, true},
		{"no country", map[uint32]int64{111111111: ago(time.Minute)}, 0, false},
		{"two in the hour", map[uint32]int64{368168720: ago(time.Minute), 227006760: ago(30 * time.Minute)}, 0, true},
		{"old one stopped", map[uint32]int64{368168720: ago(time.Minute), 227006760: ago(2 * time.Hour)}, 368168720, true},
		{"default MMSI beside the real one", map[uint32]int64{368168720: ago(time.Minute), 123456789: ago(time.Second)}, 368168720, true},
	} {
		if got, decided := decideOwn(c.cands, now); got != c.want || decided != c.decided {
			t.Errorf("%s: %d %v", c.name, got, decided)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"  Quissett   Harbor ": "Quissett Harbor",
		"Badminton Bay":        "Badminton Bay",
		"Pier 39 (east) #2":    "Pier 39 (east) #2",
		"Île d'Yeu":            "Île d'Yeu",
		"":                     "",
	} {
		if got, err := normalizeName(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{
		"Coast Guard Station", "USCG Sector Boston", "Open Waters HQ", "the aiscast station", "Official",
		"www.example.com", "me@example.com", "http://x", "⚓ Harbor", "Bad‮Name", strings.Repeat("x", 41),
	} {
		if got, err := normalizeName(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestNearLabel(t *testing.T) {
	for _, c := range []struct {
		lat, lon float64
		want     string
	}{
		{34.005, -118.51, "Santa Monica, CA"},
		{39.447, -0.313, "Valencia, Spain"},         // the city, not the suburb Sedaví
		{34.717, -76.671, "Morehead City, NC"},      // the harbor town, not Havelock inland
		{51.556, -9.82, "Munster, Ireland"},         // no town of 5,000 within 25 km
		{43.045, 16.405, "Split-Dalmatia, Croatia"}, // the islands
		{30, -40, ""}, // mid-Atlantic
	} {
		if got := nearLabel(c.lat, c.lon); got != c.want {
			t.Errorf("(%v, %v): %q, want %q", c.lat, c.lon, got, c.want)
		}
	}
}

func TestMedianPointAntimeridian(t *testing.T) {
	lat, lon := medianPoint([]float64{-17, -18, -17.5}, []float64{179, -179.5, 179.5})
	if lat != -17.5 || lon != 179.5 {
		t.Errorf("%v %v", lat, lon)
	}
	if _, lon := medianPoint([]float64{1, 2, 3}, []float64{10, 11, 12}); lon != 11 {
		t.Errorf("plain median: %v", lon)
	}
}

func TestCoverageLabel(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	for i := range 5 {
		p.ingestPacket("udp:abc", "udp:abc", now, now, posReport(uint32(366000001+i), 34.0+float64(i)*0.002, -118.51))
	}
	p.ingestPacket("udp:few", "udp:few", now, now, posReport(366000010, 34.0, -118.51))
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7))
	p.refreshLabels(now)
	rows := p.stationRows(now)
	if r := rowOf(t, rows, "udp:abc"); r.Near != "Santa Monica, CA" {
		t.Errorf("label: %+v", r)
	}
	if r := rowOf(t, rows, "udp:few"); r.Near != "" {
		t.Errorf("labeled from one vessel: %+v", r)
	}
	if r := rowOf(t, rows, "kystverket"); r.Near != "" {
		t.Errorf("feed labeled: %+v", r)
	}
}

func TestOwnVesselName(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("aishub", "aishub", now, now, ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 227006760}, Valid: true, Name: "CERULEAN"})
	p.Ingest(Reception{Source: "station:ed25519:k", Station: "station:ed25519:k", RecvTime: now, Body: `\s:n2k*7E\!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21`})
	p.refreshOwn(now)
	r := rowOf(t, p.stationRows(now), "station:ed25519:k/n2k")
	if r.Name != "CERULEAN" || r.NameFrom != "vessel" || r.MMSI != 227006760 {
		t.Errorf("own vessel: %+v", r)
	}
}

// ---- signed mints ----

var mintIP atomic.Int32

type testKey struct {
	pub  string
	priv ed25519.PrivateKey
}

func newTestKey() testKey {
	pk, sk, _ := ed25519.GenerateKey(rand.Reader)
	return testKey{base64.RawURLEncoding.EncodeToString(pk), sk}
}

// signed builds a mint request body signed by k, with names when given.
func (k testKey) signed(ts int64, bind bool, name, vessel *string) string {
	req := mintRequest{Pubkey: k.pub, BindIP: bind, Name: name, VesselName: vessel, TS: ts}
	req.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.priv, mintSignature(k.pub, ts, bind, deref(name), deref(vessel))))
	b, _ := json.Marshal(req)
	return string(b)
}

func mintPipeline(t *testing.T) *Pipeline {
	t.Helper()
	p := testPipeline(t)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	_, seed := newIssuerKey()
	t.Setenv("PERSONAL_ISSUER_KEY", "p:"+seed)
	return p
}

// mint posts body to /v1/keys from ip (a fresh address when empty, to stay under the per-address limit).
func mint(t *testing.T, p *Pipeline, body, ip string) (int, map[string]any) {
	t.Helper()
	if ip == "" {
		ip = fmt.Sprintf("10.9.%d.%d", mintIP.Add(1)/250, mintIP.Load()%250+1)
	}
	r := httptest.NewRequest("POST", "/v1/keys", strings.NewReader(body))
	r.RemoteAddr = ip + ":1"
	w := httptest.NewRecorder()
	p.serveKeys(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"body": w.Body.String()}
	}
	return w.Code, out
}

func strp(s string) *string { return &s }

func TestSignedMint(t *testing.T) {
	p := mintPipeline(t)
	now := time.Now().Unix()
	k := newTestKey()
	id := "station:ed25519:" + k.pub

	if code, out := mint(t, p, `{"pubkey":"`+k.pub+`"}`, ""); code != 200 {
		t.Fatalf("unsigned mint before the key signs: %d %v", code, out)
	}
	if code, out := mint(t, p, `{"pubkey":"`+k.pub+`","name":"Quissett Harbor"}`, ""); code != 403 {
		t.Errorf("unsigned mint with a name: %d %v", code, out)
	}
	if code, out := mint(t, p, k.signed(now, false, strp("Quissett Harbor"), nil), ""); code != 200 || out["name_error"] != nil {
		t.Fatalf("signed mint: %d %v", code, out)
	}
	if code, _ := mint(t, p, `{"pubkey":"`+k.pub+`"}`, ""); code != 403 {
		t.Errorf("unsigned mint after the key signed: %d", code)
	}
	if code, _ := mint(t, p, k.signed(now, false, nil, nil), ""); code != 403 {
		t.Errorf("replayed ts: %d", code)
	}
	if code, _ := mint(t, p, k.signed(now-600, false, nil, nil), ""); code != 403 {
		t.Errorf("stale ts: %d", code)
	}
	other := newTestKey()
	forged := strings.Replace(other.signed(now+1, false, strp("Mine Now"), nil), other.pub, k.pub, 1) // signed by the wrong key
	if code, _ := mint(t, p, forged, ""); code != 403 {
		t.Errorf("forged signature: %d", code)
	}
	if got := p.stats.keysSigned.Load(); got != 1 {
		t.Errorf("signed mints counted: %d", got)
	}

	p.stations.event(&Event{Station: id, Source: id, Time: time.Now(), MMSI: 1})
	if r := rowOf(t, p.stationRows(time.Now()), id); r.Name != "Quissett Harbor" || r.NameFrom != "operator" {
		t.Errorf("row: %+v", r)
	}

	// another station may not take the name, but still gets its token
	if code, out := mint(t, p, other.signed(now+2, false, strp("quissett harbor"), nil), ""); code != 200 || out["name_error"] == nil || out["token"] == nil {
		t.Errorf("taken name: %d %v", code, out)
	}
	// a name that breaks a rule never blocks the token
	if code, out := mint(t, p, other.signed(now+3, false, nil, strp("⚓")), ""); code != 200 || out["name_error"] == nil {
		t.Errorf("bad vessel name: %d %v", code, out)
	}
	// renaming, then clearing
	if code, _ := mint(t, p, k.signed(now+4, false, strp("Quissett"), nil), ""); code != 200 {
		t.Errorf("rename: %d", code)
	}
	if code, _ := mint(t, p, k.signed(now+5, false, strp(""), nil), ""); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if r := rowOf(t, p.stationRows(time.Now()), id); r.Name != "" {
		t.Errorf("cleared name still shown: %+v", r)
	}
}

func TestSignedMintNamesBoundUDPStation(t *testing.T) {
	p := mintPipeline(t)
	k := newTestKey()
	if code, out := mint(t, p, k.signed(time.Now().Unix(), true, strp("Home Receiver"), nil), "203.0.113.9"); code != 200 || out["name_error"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	udp := udpStation("203.0.113.9")
	p.stations.event(&Event{Station: udp, Source: udp, Time: time.Now(), MMSI: 1})
	if r := rowOf(t, p.stationRows(time.Now()), udp); r.Name != "Home Receiver" {
		t.Errorf("bound UDP station: %+v", r)
	}
}

func TestNameOrderAndLock(t *testing.T) {
	p := testPipeline(t)
	n := p.names
	n.m["station:ed25519:a"] = &stationMeta{Name: "Chosen", VesselName: "SERENITY", ownName: "SERENITY II", Own: 368168720, Near: "Falmouth, MA"}
	n.m["station:ed25519:b"] = &stationMeta{VesselName: "SERENITY", ownName: "SERENITY II"}
	n.m["station:ed25519:c"] = &stationMeta{ownName: "SERENITY II"}
	n.m["station:ed25519:d"] = &stationMeta{Name: "Rude", Near: "Bangor, ME"}
	n.locked["station:ed25519:d"] = true
	for id, want := range map[string]string{"a": "Chosen", "b": "SERENITY", "c": "SERENITY II", "d": ""} {
		r := stationRow{Station: "station:ed25519:" + id + "/n2k"}
		n.decorate(&r)
		if r.Name != want {
			t.Errorf("%s: %+v", id, r)
		}
	}
	r := stationRow{Station: "station:ed25519:a"}
	n.decorate(&r)
	if r.Near != "Falmouth, MA" || r.MMSI != 368168720 || r.NameFrom != "operator" {
		t.Errorf("fields: %+v", r)
	}
}

func TestStationNamesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiscast.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	n := newStationNames()
	if err := n.attach(st); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	*n.meta("udp:abc") = stationMeta{Near: "Santa Monica, CA", Own: 368168720, Name: "Pier", VesselName: "CERULEAN", SignedTS: 5, SignedAt: 6}
	n.dirty["udp:abc"] = true
	n.mu.Unlock()
	if err := n.flush(); err != nil {
		t.Fatal(err)
	}
	st.close()
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	m := newStationNames()
	if err := m.attach(st); err != nil {
		t.Fatal(err)
	}
	if got := *m.m["udp:abc"]; got != (stationMeta{Near: "Santa Monica, CA", Own: 368168720, Name: "Pier", VesselName: "CERULEAN", SignedTS: 5, SignedAt: 6}) {
		t.Errorf("restored: %+v", got)
	}
}

func TestSignedRegisterFrame(t *testing.T) {
	p, write, read := registerConn(t, keysPerMinute)
	k := newTestKey()
	body := k.signed(time.Now().Unix(), false, nil, strp("CERULEAN"))
	write(`{"type":"register",` + strings.TrimPrefix(body, "{"))
	if key := read(); key["type"] != "key" || key["name_error"] != nil {
		t.Fatalf("key frame: %v", key)
	}
	id := "station:ed25519:" + k.pub
	p.stations.event(&Event{Station: id, Source: id, Time: time.Now(), MMSI: 1})
	if r := rowOf(t, p.stationRows(time.Now()), id); r.Name != "CERULEAN" || r.NameFrom != "vessel" {
		t.Errorf("row: %+v", r)
	}
}
