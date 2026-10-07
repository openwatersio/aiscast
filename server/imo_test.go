package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// heardIMO folds a position and a static report naming imo for mmsi as received age ago, and flushes them.
func heardIMO(p *Pipeline, mmsi, imo uint32, name string, age time.Duration) {
	at := time.Now().Add(-age).Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(mmsi, 59.9, 10.7))
	p.ingestPacket("kystverket", "kystverket", at.Add(time.Second), at.Add(time.Second), staticIMO(mmsi, imo, name))
	if p.store != nil {
		p.flushStore()
	}
}

// imoScene is a record holding a ship that changed flag, IMO 9241061 under a quiet MMSI and an active one, and a
// placeholder IMO 1234567 reported by 12 unrelated vessels.
func imoScene(t *testing.T) *Pipeline {
	t.Helper()
	p, _ := trackPipeline(t)
	httpLimit = newLimiter(httpLimit.max) // package-level, so requests otherwise accumulate across tests
	heardIMO(p, 257000001, 9241061, "OLD FLAG", 40*24*time.Hour)
	for i := range uint32(12) {
		heardIMO(p, 230000100+i, 1234567, fmt.Sprintf("PLACEHOLDER %d", i), time.Duration(12-i)*time.Hour)
	}
	heardIMO(p, 538000001, 9241061, "NEW FLAG", time.Minute)
	return p
}

// imoTokens are an anonymous caller, a personal token, and a personal token whose station has earned the feeder tier.
func imoTokens(t *testing.T, p *Pipeline) (personal, feeder string, partner func(mmsis int) string) {
	t.Helper()
	kid, priv := testIssuer(t, p)
	now := time.Now()
	personal, _ = signToken(priv, personalClaims(kid, "ed25519:p", now))
	feeder, _ = signToken(priv, personalClaims(kid, "ed25519:f", now))
	for i := range feederMinEvents24h {
		p.stations.event(&Event{Station: "station:ed25519:f", Source: "station:ed25519:f", Time: now.Add(-time.Duration(i) * time.Second), MMSI: 1})
	}
	partner = func(mmsis int) string {
		tok, _ := signToken(priv, Claims{Kid: kid, Sub: "fleet", Role: "partner", MMSIs: mmsis, Exp: now.Add(time.Hour).Unix()})
		return tok
	}
	return personal, feeder, partner
}

func withKey(target, tok string) string {
	if tok == "" {
		return target
	}
	if strings.Contains(target, "?") {
		return target + "&key=" + tok
	}
	return target + "?key=" + tok
}

func TestIMOLookupNeedsTheRawFeedTier(t *testing.T) {
	p := imoScene(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	personal, feeder, partner := imoTokens(t, p)
	targets := []string{
		"/v1/vessels?imo=9241061",
		"/v1/vessels?q=IMO9241061",
		"/v1/vessels?q=imo%209241061",
		"/v1/vessels/IMO9241061",
		"/v1/vessels/IMO9241061/track",
		"/v1/vessels/tiles/0/0/0?imo=9241061",
		"/v1/vessels/tiles.json?imo=9241061",
	}
	for _, target := range targets {
		for who, tok := range map[string]string{"anonymous": "", "personal": personal} {
			if w := get(t, p, withKey(target, tok)); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feeder") || !strings.Contains(w.Body.String(), "docs/limits.md") {
				t.Errorf("%s %s: %d %s", who, target, w.Code, w.Body)
			}
		}
		for who, tok := range map[string]string{"earned feeder": feeder, "partner": partner(0)} {
			if w := get(t, p, withKey(target, tok)); w.Code != http.StatusOK {
				t.Errorf("%s %s: %d %s", who, target, w.Code, w.Body)
			}
		}
	}
	// SSE refuses before the stream opens
	for who, tok := range map[string]string{"anonymous": "", "personal": personal} {
		if w := get(t, p, withKey("/v1/stream?imo=9241061", tok)); w.Code != http.StatusForbidden {
			t.Errorf("%s SSE: %d %s", who, w.Code, w.Body)
		}
	}
	// a bare seven-digit search stays an MMSI prefix for the tiers that may not look up by IMO
	if fc := getFC(t, p, "/v1/vessels?q=9241061"); len(fc.Features) != 0 {
		t.Errorf("anonymous bare number found the IMO: %v", ids(fc))
	}
	// every tier still sees imo in what it gets back
	if fc := getFC(t, p, "/v1/vessels?mmsi=538000001"); len(fc.Features) != 1 || fc.Features[0].Properties["imo"] != float64(9241061) {
		t.Errorf("anonymous lost imo: %+v", fc)
	}

	// the subscribe frame
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for who, tok := range map[string]string{"anonymous": "", "personal": personal, "earned feeder": feeder} {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(withKey(srv.URL+"/v1/stream", tok), "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Read(ctx) // welcome
		wsWriteJSON(ctx, c, map[string]any{"type": "subscribe", "imo": []uint32{9241061}, "mmsi": []uint32{999999999}, "snapshot": true})
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		refused := strings.Contains(string(msg), errIMOTier.Error())
		if refused != (who != "earned feeder") {
			t.Errorf("%s subscribe: %s", who, msg)
		}
		c.CloseNow()
	}

	// MCP
	for who, cl := range map[string]*Claims{"anonymous": anonymousClaims(""), "personal": p.effective(&Claims{Sub: "ed25519:p", Role: "personal"})} {
		cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, cl))
		var out mcpVessels
		var tr mcpTrack
		for name, msg := range map[string]string{
			"get_vessels":            mcpCall(t, cs, "get_vessels", map[string]any{"imo": []uint32{9241061}}, &out),
			"search_vessels_by_name": mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "IMO 9241061"}, &out),
			"get_vessel_track":       mcpCall(t, cs, "get_vessel_track", map[string]any{"imo": 9241061}, &tr),
		} {
			if msg != errIMOTier.Error() {
				t.Errorf("%s %s: %q", who, name, msg)
			}
		}
	}
	cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, p.effective(&Claims{Sub: "ed25519:f", Role: "personal", MMSIs: personalMMSIs})))
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"imo": []uint32{9241061}}, &out); msg != "" || len(out.Vessels) != 2 {
		t.Errorf("earned feeder get_vessels: %q %+v", msg, out)
	}
}

func TestIMOResolution(t *testing.T) {
	p := imoScene(t)

	// a single answer serves the one MMSI heard in the last 30 days, and says which it is
	w := get(t, p, "/v1/vessels/IMO9241061")
	var f struct {
		ID uint32 `json:"id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &f) != nil || f.ID != 538000001 || w.Header().Get("Content-Location") != "/v1/vessels/538000001" ||
		w.Header().Get("Access-Control-Expose-Headers") != "Content-Location" {
		t.Errorf("reflagged: %d %s %v", w.Code, w.Body, w.Header())
	}
	if w := get(t, p, "/v1/vessels/imo9241061"); w.Code != 200 {
		t.Errorf("the prefix is case-insensitive: %d", w.Code)
	}
	// a list answers with both
	if fc := getFC(t, p, "/v1/vessels?imo=9241061"); !sameIDs(ids(fc), 538000001, 257000001) || fc.Truncated { // a list has no order
		t.Errorf("reflagged list: %v", ids(fc))
	}

	// a placeholder IMO names no one vessel: 300 with the ten most recently heard as candidates
	w = get(t, p, "/v1/vessels/IMO1234567")
	var c struct {
		Features []struct {
			ID   uint32 `json:"id"`
			Href string `json:"href"`
		} `json:"features"`
		Attribution map[string]string `json:"attribution"`
		Truncated   bool              `json:"truncated"`
	}
	if w.Code != http.StatusMultipleChoices || json.Unmarshal(w.Body.Bytes(), &c) != nil || len(c.Features) != maxMMSIsPerIMO ||
		c.Features[0].ID != 230000111 || c.Features[0].Href != "/v1/vessels/230000111" || c.Attribution["kystverket"] == "" || !c.Truncated ||
		w.Header().Get("Content-Location") != "" {
		t.Errorf("placeholder: %d %s", w.Code, w.Body)
	}
	// a candidate's link keeps the request's parameters but not its token
	w = get(t, p, "/v1/vessels/IMO1234567/track?format=gpx&key=junk")
	if w.Code != http.StatusMultipleChoices || json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Features[0].Href != "/v1/vessels/230000111/track?format=gpx" {
		t.Errorf("placeholder track: %d %s", w.Code, w.Body)
	}
	fc := getFC(t, p, "/v1/vessels?imo=1234567")
	if len(fc.Features) != maxMMSIsPerIMO || !slices.Contains(ids(fc), 230000111) || slices.Contains(ids(fc), 230000100) || !fc.Truncated {
		t.Errorf("placeholder list: %v %v", ids(fc), fc.Truncated)
	}
	if fc := getFC(t, p, "/v1/vessels?q=IMO1234567"); len(fc.Features) != maxMMSIsPerIMO || !fc.Truncated {
		t.Errorf("placeholder search: %v %v", ids(fc), fc.Truncated)
	}
	if fc := getFC(t, p, "/v1/vessels?q=PLACEHOLDER&imo=1234567"); len(fc.Features) != maxMMSIsPerIMO || !fc.Truncated {
		t.Errorf("search narrowed by a placeholder: %v %v", ids(fc), fc.Truncated)
	}
	// every imo value counts, as one list
	if fc := getFC(t, p, "/v1/vessels?imo=9241061&imo=1234567"); len(fc.Features) != 2+maxMMSIsPerIMO {
		t.Errorf("repeated imo parameter: %v", ids(fc))
	}

	// an IMO no vessel reports
	for _, target := range []string{"/v1/vessels/IMO7654321", "/v1/vessels/IMO7654321/track"} {
		if w := get(t, p, target); w.Code != 404 || !strings.Contains(w.Body.String(), "unknown vessel") || w.Header().Get("Content-Location") != "" {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	if fc := getFC(t, p, "/v1/vessels?imo=7654321"); len(fc.Features) != 0 {
		t.Errorf("unknown imo matched everything: %v", ids(fc))
	}

	// 0 is "not available" on the wire, and an IMO has at most seven digits
	for _, target := range []string{"/v1/vessels/IMO0", "/v1/vessels/IMO12345678", "/v1/vessels/IMO", "/v1/vessels?imo=0", "/v1/vessels?imo=9241061,x", "/v1/vessels/tiles/0/0/0?imo=0",
		"/v1/vessels/IMO00009241061", "/v1/vessels?imo=00001234567", "/v1/vessels?imo=&imo=9241061", "/v1/vessels/tiles/0/0/0?imo=&imo=9241061"} {
		if w := get(t, p, target); w.Code != 400 {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}

	// text that is no IMO number stays a name search
	if fc := getFC(t, p, "/v1/vessels?q=IMO0"); len(fc.Features) != 0 {
		t.Errorf("IMO0 as a name: %v", ids(fc))
	}

	// a request that resolves and then fails says nothing about where the answer lives
	if w := get(t, p, "/v1/vessels/IMO9241061/track?from=garbage"); w.Code != 400 || w.Header().Get("Content-Location") != "" {
		t.Errorf("bad track request by imo: %d %v", w.Code, w.Header())
	}

	// the track follows the one resolved MMSI
	sail(t, p, 538000001, 2*time.Hour, time.Hour)
	w = get(t, p, "/v1/vessels/IMO9241061/track?interval=0")
	var tr struct {
		ID         uint32 `json:"id"`
		Properties struct{ Points int }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tr) != nil || tr.ID != 538000001 || tr.Properties.Points == 0 || w.Header().Get("Content-Location") != "/v1/vessels/538000001/track?interval=0" {
		t.Errorf("track by imo: %d %s", w.Code, w.Body)
	}

	// a tile found by IMO depends on the caller's tier, so no shared cache may keep it
	if w := get(t, p, "/v1/vessels/tiles/0/0/0?imo=9241061"); w.Code != 200 || w.Header().Get("Cache-Control") != "private, max-age=10" {
		t.Errorf("imo tile cache: %d %v", w.Code, w.Header())
	}
	if w := get(t, p, "/v1/vessels/tiles/0/0/0?mmsi=538000001"); w.Header().Get("Cache-Control") != "public, max-age=10" {
		t.Errorf("mmsi tile cache: %v", w.Header())
	}

	// tiles.json keeps its own route beside /v1/vessels/IMO<n>
	if w := get(t, p, "/v1/vessels/tiles.json?imo=9241061"); w.Code != 200 || !strings.Contains(w.Body.String(), `"tilejson"`) || w.Header().Get("Cache-Control") != "private, max-age=300" {
		t.Errorf("tiles.json: %d %s", w.Code, w.Body)
	}
}

func TestIMOCountsAgainstTheMMSICap(t *testing.T) {
	p := imoScene(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	_, _, partner := imoTokens(t, p)
	tok := partner(3)
	if w := get(t, p, withKey("/v1/vessels?mmsi=1,2&imo=9241061", tok)); w.Code != 200 {
		t.Errorf("at the cap: %d %s", w.Code, w.Body)
	}
	// one IMO counts once however many vessels report it, and a repeated one counts once
	if w := get(t, p, withKey("/v1/vessels?mmsi=1,2&imo=1234567,1234567", tok)); w.Code != 200 {
		t.Errorf("placeholder at the cap: %d %s", w.Code, w.Body)
	}
	if w := get(t, p, withKey("/v1/vessels?mmsi=1,2,3&imo=9241061", tok)); w.Code != 400 || !strings.Contains(w.Body.String(), "too many mmsi and imo") {
		t.Errorf("past the cap: %d %s", w.Code, w.Body)
	}
	if w := get(t, p, withKey("/v1/vessels/tiles/0/0/0?mmsi=1,2&imo=9241061,1234567", tok)); w.Code != 400 {
		t.Errorf("tiles past the cap: %d %s", w.Code, w.Body)
	}
	// over the socket too, a repeated IMO counts once
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream?key="+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Read(ctx) // welcome
	wsWriteJSON(ctx, c, map[string]any{"type": "subscribe", "mmsi": []uint32{538000001, 2}, "imo": []uint32{9241061, 9241061}, "snapshot": true})
	if _, msg, err := c.Read(ctx); err != nil || strings.Contains(string(msg), "error") {
		t.Errorf("repeated imo on the socket: %s %v", msg, err)
	}
	// past what one record query can hold is a bad request, not an outage
	many := make([]string, maxParams+1)
	for i := range many {
		many[i] = fmt.Sprint(i + 1)
	}
	if _, status, msg := p.parseSub(map[string][]string{"imo": {strings.Join(many, ",")}}, &Claims{Sub: "fleet", Role: "partner"}, true); status != 400 || msg != errTooManyTerms.Error() {
		t.Errorf("too many imo: %d %q", status, msg)
	}

	cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, &Claims{Sub: "fleet", Role: "partner", MMSIs: 3}))
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{1, 2, 3}, "imo": []uint32{9241061}}, &out); !strings.Contains(msg, "this key allows 3") {
		t.Errorf("mcp past the cap: %q", msg)
	}
	// a list caps each IMO at ten
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"imo": []uint32{1234567}}, &out); msg != "" || len(out.Vessels) != maxMMSIsPerIMO || out.Vessels[0].MMSI != 230000111 || !out.Truncated {
		t.Errorf("mcp placeholder: %q %+v", msg, out)
	}
	// a key that follows vessels only by list may name them by IMO, even an IMO that matches nothing
	kid, priv := testIssuer(t, p) // new keys: the tokens above stop verifying
	fleet, _ := signToken(priv, Claims{Kid: kid, Sub: "fleet2", Role: "partner", Area: -1, Exp: time.Now().Add(time.Hour).Unix()})
	for _, imo := range []string{"9241061", "7654321"} {
		if w := get(t, p, withKey("/v1/vessels/tiles/0/0/0?imo="+imo, fleet)); w.Code != 200 {
			t.Errorf("list-only key, tile by imo %s: %d %s", imo, w.Code, w.Body)
		}
	}
}

func TestIMOSearch(t *testing.T) {
	p := imoScene(t)
	heardIMO(p, 924106123, 0, "PREFIX MATCH", 0)
	// a bare seven-digit number: the IMO's vessels, then its MMSI prefix matches
	if got := ids(getFC(t, p, "/v1/vessels?q=9241061")); !slices.Equal(got, []uint32{538000001, 257000001, 924106123}) {
		t.Errorf("bare number: %v", got)
	}
	// the IMO form finds only the IMO
	if got := ids(getFC(t, p, "/v1/vessels?q=IMO%209241061")); !slices.Equal(got, []uint32{538000001, 257000001}) {
		t.Errorf("imo form: %v", got)
	}
	if got := ids(getFC(t, p, "/v1/vessels?q=IMO9241061&max_age=1h")); !slices.Equal(got, []uint32{538000001}) {
		t.Errorf("max_age narrows it: %v", got)
	}

	cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, &Claims{Sub: "f", Role: "feeder"}))
	var out mcpVessels
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "IMO9241061"}, &out); msg != "" || len(out.Vessels) != 2 || out.Vessels[0].MMSI != 538000001 || out.Total != 2 {
		t.Errorf("mcp imo form: %q %+v", msg, out)
	}
	heardIMO(p, 257000009, 0, "FLAG 9241061", 0)
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "9241061"}, &out); msg != "" || len(out.Vessels) != 3 ||
		out.Vessels[0].MMSI != 538000001 || out.Vessels[2].MMSI != 257000009 || out.Total != 3 {
		t.Errorf("mcp bare number: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "9241061", "limit": 1}, &out); msg != "" || len(out.Vessels) != 1 || out.Total != 3 || !out.Truncated {
		t.Errorf("mcp bare number, limited: %q %+v", msg, out)
	}
}

func TestIMOTrackTool(t *testing.T) {
	p := imoScene(t)
	sail(t, p, 538000001, 2*time.Hour, time.Hour)
	cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, &Claims{Sub: "f", Role: "feeder"}))
	var tr mcpTrack
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"imo": 9241061}, &tr); msg != "" || tr.MMSI != 538000001 || len(tr.Positions) == 0 {
		t.Errorf("by imo: %q %+v", msg, tr)
	}
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"imo": 1234567}, &tr); !strings.Contains(msg, "names no one vessel") || !strings.Contains(msg, "MMSI 230000111 (PLACEHOLDER 11)") {
		t.Errorf("placeholder: %q", msg)
	}
	if msg := mcpCall(t, cs, "get_vessel_track", map[string]any{"imo": 7654321}, &tr); !strings.Contains(msg, "no vessel reporting IMO 7654321") {
		t.Errorf("unknown: %q", msg)
	}
	for _, args := range []map[string]any{{}, {"mmsi": 538000001, "imo": 9241061}} {
		if msg := mcpCall(t, cs, "get_vessel_track", args, &tr); msg != "give exactly one of mmsi or imo" {
			t.Errorf("%v: %q", args, msg)
		}
	}
}

// A stream following an IMO follows a vessel that first reports it after the subscription is made.
func TestIMOStreamFollowsNewVessels(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7))
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read := func() map[string]any {
		t.Helper()
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(msg, &m)
		return m
	}
	read() // welcome
	wsWriteJSON(ctx, c, map[string]any{"type": "subscribe", "mmsi": []uint32{257000001}, "imo": []uint32{9241061}, "snapshot": true})
	if m := read(); m["mmsi"] != float64(257000001) { // the snapshot: the subscription is in place
		t.Fatalf("snapshot: %v", m)
	}
	at := time.Now()
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(538000001, 10, 10)) // not yet known to report the IMO
	p.ingestPacket("kystverket", "kystverket", at, at, staticIMO(538000001, 9241061, "NEW FLAG"))
	p.ingestPacket("kystverket", "kystverket", at.Add(10*time.Second), at.Add(10*time.Second), posReport(538000001, 10, 10))
	if m := read(); m["mmsi"] != float64(538000001) || m["msg_type"] != "ShipStaticData" {
		t.Fatalf("first static report: %v", m)
	}
	if m := read(); m["mmsi"] != float64(538000001) || m["msg_type"] != "PositionReport" {
		t.Fatalf("followed after it: %v", m)
	}
}

func TestIMOJoinsAtMostTenPerIMO(t *testing.T) {
	s := &v1Sub{mmsi: map[uint32]bool{}, imo: map[uint32]int{1234567: maxMMSIsPerIMO - 1}}
	static := func(mmsi, imo uint32) *Event {
		return &Event{MMSI: mmsi, Packet: staticIMO(mmsi, imo, "")}
	}
	if s.match(static(1, 7654321)) {
		t.Error("an IMO not followed joined")
	}
	if !s.match(static(2, 1234567)) || !s.match(&Event{MMSI: 2}) {
		t.Error("the tenth vessel for the IMO did not join")
	}
	if s.match(static(3, 1234567)) {
		t.Error("an eleventh vessel joined")
	}
}

// soleVessel's 30-day rule: one MMSI ever is the vessel however quiet; among several, the one heard recently.
func TestIMOSoleVessel(t *testing.T) {
	now := time.Now()
	heard := func(mmsi uint32, age time.Duration) record {
		v := newVessel()
		v.Seen = now.Add(-age)
		return record{mmsi: mmsi, v: v}
	}
	day := 24 * time.Hour
	for _, c := range []struct {
		recs []record
		want uint32
	}{
		{[]record{heard(1, 400*day)}, 1},
		{[]record{heard(1, 29*day), heard(2, 31*day)}, 1},
		{[]record{heard(1, time.Hour), heard(2, 29*day)}, 0},
		{[]record{heard(1, 31*day), heard(2, 40*day)}, 0},
	} {
		if r, ok := soleVessel(c.recs, now); (ok && r.mmsi != c.want) || ok != (c.want != 0) {
			t.Errorf("%v: %d %v, want %d", c.recs, r.mmsi, ok, c.want)
		}
	}
}

// Every way the resolver reads gives one answer: the mirror, SQLite when the mirror is not loaded, and the cache
// without a store.
func TestIMOResolverPaths(t *testing.T) {
	want := []uint32{538000001, 257000001}
	check := func(what string, p *Pipeline) {
		t.Helper()
		byIMO, cut, err := p.resolveIMOs([]uint32{9241061, 1234567, 7654321})
		var got []uint32
		for _, r := range byIMO[9241061] {
			got = append(got, r.mmsi)
		}
		if err != nil || !slices.Equal(got, want) || len(byIMO[1234567]) != maxMMSIsPerIMO || byIMO[1234567][0].mmsi != 230000111 || !cut || byIMO[7654321] != nil {
			t.Errorf("%s: %v %v %v %d", what, got, cut, err, len(byIMO[1234567]))
		}
	}
	p := imoScene(t)
	check("mirror", p)
	p.store.mirror = nil // no writes follow, which would refresh it
	check("sqlite", p)

	p = testPipeline(t) // the cache holds 30 minutes, so the quiet MMSI is heard recently here
	for i := range uint32(12) {
		heardIMO(p, 230000100+i, 1234567, "", time.Duration(12-i)*time.Minute)
	}
	heardIMO(p, 257000001, 9241061, "OLD FLAG", 20*time.Minute)
	heardIMO(p, 538000001, 9241061, "NEW FLAG", time.Minute)
	check("cache", p)
	if fc := getFC(t, p, "/v1/vessels?imo=9241061"); !sameIDs(ids(fc), want...) {
		t.Errorf("cache list: %v", ids(fc))
	}
}

// The record runs a flush behind the cache, and the vessels folded since answer from the cache.
func TestIMOBetweenFlushes(t *testing.T) {
	p := imoScene(t)
	at := time.Now()
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(538000009, 59.9, 10.7))
	p.ingestPacket("kystverket", "kystverket", at, at, staticIMO(538000009, 7777777, "FIRST HEARD"))
	p.ingestPacket("kystverket", "kystverket", at, at, staticIMO(230000100, 9241061, "NOW REFLAGGED")) // was 1234567
	byIMO, _, err := p.resolveIMOs([]uint32{7777777, 9241061, 1234567})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIMO[7777777]) != 1 || byIMO[7777777][0].mmsi != 538000009 {
		t.Errorf("first static report: %v", byIMO[7777777])
	}
	if len(byIMO[9241061]) != 3 || slices.ContainsFunc(byIMO[1234567], func(r record) bool { return r.mmsi == 230000100 }) {
		t.Errorf("changed imo: %d for 9241061, %d for 1234567", len(byIMO[9241061]), len(byIMO[1234567]))
	}
	if w := get(t, p, "/v1/vessels/IMO7777777"); w.Code != 200 {
		t.Errorf("by path before the flush: %d %s", w.Code, w.Body)
	}
	cs := mcpClientCtx(t, p, context.WithValue(context.Background(), mcpClaimsKey{}, &Claims{Sub: "f", Role: "feeder"}))
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"imo": []uint32{7777777}}, &out); msg != "" || len(out.Vessels) != 1 || len(out.UnknownIMO) != 0 {
		t.Errorf("get_vessels before the flush: %q %+v", msg, out)
	}

	// a vessel back from the sweep has not resent its IMO, and the record still answers for it
	mustFlush(t, p)
	forget(p)
	at = time.Now()
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(538000001, 59.9, 10.7))
	if byIMO, _, _ := p.resolveIMOs([]uint32{9241061}); !slices.ContainsFunc(byIMO[9241061], func(r record) bool { return r.mmsi == 538000001 }) {
		t.Errorf("returning vessel lost its imo: %v", byIMO[9241061])
	}
}

func sameIDs(got []uint32, want ...uint32) bool {
	return slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}
