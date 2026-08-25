package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testPipeline(t *testing.T) *Pipeline {
	allowAnon = true
	return newPipeline(newArchive("", nil))
}

func TestParseV0Sub(t *testing.T) {
	cases := map[string]string{
		`{"APIKey":"k","BoundingBoxes":[[[-90,-180],[90,180]]]}`:                            "",
		`{"apikey":"k","boundingboxes":[[[60,11],[58,9]]]}`:                                 "", // any key casing, any corner order
		`{"APIKey":"k","BoundingBoxes":[[[60,11],[58,9]]],"FiltersShipMMSI":["227006760"]}`: "",
		`{"BoundingBoxes":[[[-90,-180],[90,180]]]}`:                                         errBadKey,
		`{"APIKey":"","BoundingBoxes":[[[-90,-180],[90,180]]]}`:                             errBadKey,
		`not json`:       errMalformed,
		`{"APIKey":"k"}`: errMalformed,
		`{"APIKey":"k","BoundingBoxes":[[[60,11]]]}`:                                                                 errMalformed,
		`{"APIKey":"k","BoundingBoxes":[[[95,11],[58,9]]]}`:                                                          errMalformed,
		`{"APIKey":"k","BoundingBoxes":[[[60,11],[58,9]]],"FiltersShipMMSI":["12"]}`:                                 errMalformed,
		`{"APIKey":"k","BoundingBoxes":[[[60,11],[58,9]]],"FilterMessageTypes":["PositionReport","PositionReport"]}`: errMalformed,
	}
	p := testPipeline(t)
	for in, want := range cases {
		if _, _, got := p.parseV0Sub([]byte(in), "127.0.0.1"); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
	f, _, _ := p.parseV0Sub([]byte(`{"APIKey":"k","BoundingBoxes":[[[60,11],[58,9]]]}`), "127.0.0.1")
	if f.boxes[0] != (bbox{58, 9, 60, 11}) {
		t.Errorf("corner normalization: %v", f.boxes[0])
	}
}

// Golden aisstream envelope for the pyais README sentence (MMSI 227006760, 49.4755767N 0.13138E, COG 36.7).
const goldenV0 = `{"Message":{"PositionReport":{"Cog":36.7,"CommunicationState":22136,"Latitude":49.47557666666667,"Longitude":0.13138,"MessageID":1,"NavigationalStatus":0,"PositionAccuracy":false,"Raim":false,"RateOfTurn":-128,"RepeatIndicator":0,"Sog":0,"Spare":0,"SpecialManoeuvreIndicator":0,"Timestamp":14,"TrueHeading":511,"UserID":227006760,"Valid":true}},"MessageType":"PositionReport","MetaData":{"MMSI":227006760,"MMSI_String":227006760,"ShipName":"","latitude":49.47557666666667,"longitude":0.13138,"time_utc":"2023-10-22 22:47:36.94034384 +0000 UTC"}}`

func TestV0Golden(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	at := time.Date(2023, 10, 22, 22, 47, 36, 940343840, time.UTC)
	p.Ingest(Reception{Source: "test", Station: "test", RecvTime: at, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	ev := <-sub.ch
	if got := string(ev.renderV0()); got != goldenV0 {
		t.Errorf("v0 envelope\n got %s\nwant %s", got, goldenV0)
	}
}

func TestEventIDDerivation(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.Ingest(Reception{Source: "test", Station: "test", RecvTime: time.Unix(1787234990, 0), Body: "!AIVDM,1,1,,A,H42O55lti4hhhilD3nink000?050,0*40"})
	select {
	case ev := <-sub.ch:
		// Documented in docs/API.md; changing the hash, truncation, or inputs breaks every archived id.
		if want := "381c250991f87733bb5080209c16904d"; ev.ID != want {
			t.Errorf("id=%s want %s", ev.ID, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("no event (parse_err=%d decode_fail=%d)", p.stats.parseErr.Load(), p.stats.decodeFail.Load())
	}
}

func TestPipelineAssemblyAndDedupe(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	f, err := os.Open("testdata/kystverket.nmea")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, frags := 0, 0
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	ingest := func() {
		for _, l := range lines {
			if strings.Contains(l, "VDM,2,2,") {
				frags++
			}
			p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: time.Unix(1787234990, 0), Body: l})
		}
	}
	ingest()
	multipart, tagged := false, 0
	for len(sub.ch) > 0 {
		ev := <-sub.ch
		n++
		if len(ev.Sentences) == 2 && ev.Type == "ShipStaticData" {
			multipart = true
		}
		if strings.HasPrefix(ev.Station, "kystverket/") {
			tagged++
		}
	}
	if !multipart {
		t.Error("no reassembled two-sentence ShipStaticData event")
	}
	if tagged < n-1 { // one fixture line has no TAG block
		t.Errorf("station not taken from TAG block: %d of %d", tagged, n)
	}
	if n == 0 || n > len(lines)-frags/2 {
		t.Errorf("events=%d lines=%d", n, len(lines))
	}
	ingest() // same payloads within the window: all duplicates
	if d := len(sub.ch); d != 0 {
		t.Errorf("second pass produced %d events, want 0 (dedupe)", d)
	}
	if p.stats.parseErr.Load() != 0 {
		t.Errorf("parse errors: %d", p.stats.parseErr.Load())
	}
}

func TestReceiveHTTP(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	body := `{"protocol":"jsonaiscatcher","msgs":[{"class":"AIS","channel":"A","rxtime":"20260820111900","nmea":["!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"]}]}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/receive", strings.NewReader(body))
	req.SetBasicAuth("st1", "secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("post: %v %v", err, res)
	}
	select {
	case ev := <-sub.ch:
		if ev.Source != "http:anon" || ev.MMSI != 227006760 { // ALLOW_ANON identity
			t.Errorf("unexpected event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("no event from HTTP post (parse_err=%d decode_fail=%d)", p.stats.parseErr.Load(), p.stats.decodeFail.Load())
	}
}

func TestV0WebSocket(t *testing.T) {
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v0/stream"

	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"BoundingBoxes":[[[-90,-180],[90,180]]]}`))
	_, msg, err := c.Read(ctx)
	if err != nil || string(msg) != `{"error":"Api Key Is Not Valid"}` {
		t.Fatalf("bad key: %s %v", msg, err)
	}
	c.CloseNow()

	c, _, err = websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageText, []byte(`{"APIKey":"k","BoundingBoxes":[[[49,0],[50,1]]],"FilterMessageTypes":["PositionReport"]}`))
	time.Sleep(50 * time.Millisecond) // let the server register the subscriber
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	_, msg, err = c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		MessageType string
		MetaData    map[string]any
	}
	json.Unmarshal(msg, &env)
	if env.MessageType != "PositionReport" || env.MetaData["MMSI"] != float64(227006760) {
		t.Errorf("unexpected frame: %s", msg)
	}
}

func TestVesselsSnapshot(t *testing.T) {
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // 49.4756N 0.1314E, COG 36.7
	get := func(q string) (int, map[string]any) {
		res, err := http.Get(srv.URL + "/v1/vessels" + q)
		if err != nil {
			t.Fatal(err)
		}
		var fc map[string]any
		json.NewDecoder(res.Body).Decode(&fc)
		feats, _ := fc["features"].([]any)
		if len(feats) == 0 {
			return 0, nil
		}
		return len(feats), feats[0].(map[string]any)["properties"].(map[string]any)
	}
	if n, props := get("?bbox=49,0,50,1"); n != 1 || props["mmsi"] != float64(227006760) || props["cog"] != 36.7 || props["heading"] != nil {
		t.Errorf("in-bbox: n=%d props=%v", n, props)
	}
	if n, _ := get("?bbox=60,10,61,11"); n != 0 {
		t.Errorf("out-of-bbox: n=%d", n)
	}
	if n, _ := get(""); n != 1 {
		t.Errorf("all: n=%d", n)
	}
	if res, _ := http.Get(srv.URL + "/v1/vessels?bbox=junk"); res.StatusCode != 400 {
		t.Errorf("bad bbox: %d", res.StatusCode)
	}
	if n := p.sweepVessels(time.Now().Add(time.Minute)); n != 0 {
		t.Errorf("sweep left %d", n)
	}
}

func TestUDPStationHidesIP(t *testing.T) {
	a, b := udpStation("203.0.113.5"), udpStation("203.0.113.6")
	if a == b || strings.Contains(a, "203") || len(a) != len("udp:")+12 || a != udpStation("203.0.113.5") {
		t.Errorf("udp station ids: %s %s", a, b)
	}
}

func TestUDPSenderKeyedByOwnMMSI(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	src := udpStation("198.51.100.7")
	now := time.Now()
	p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})                            // a target, before we know who the sender is
	p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: "!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21"})                            // own ship 227006760
	p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: `\s:2573010,c:1787234980*03\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43`}) // another target, after
	got := []string{}
	for len(sub.ch) > 0 {
		got = append(got, (<-sub.ch).Source)
	}
	want := []string{src, "mmsi:227006760", "mmsi:227006760"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sources %v, want %v", got, want)
	}
}

// /v1/stream publish: ack per frame, stale buffered sentences archived but not emitted, unsubscribe silences.
func TestV1PublishAckAndReplay(t *testing.T) {
	p := testPipeline(t)
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
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(msg, &m)
		return m
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","bbox":[]}`))
	time.Sleep(50 * time.Millisecond)

	// live sentence: ack, then the event
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"publish","nmea":["!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"]}`))
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		got[read()["type"].(string)] = true
	}
	if !got["ack"] || !got["event"] {
		t.Fatalf("want ack and event, got %v", got)
	}

	// stale replayed sentence (TAG c: an hour old): ack, counted as replayed, no event
	tag := fmt.Sprintf("c:%d", time.Now().Add(-time.Hour).Unix())
	var sum byte
	for i := 0; i < len(tag); i++ {
		sum ^= tag[i]
	}
	stale := fmt.Sprintf(`\%s*%02X\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`, tag, sum)
	body, _ := json.Marshal(map[string]any{"type": "publish", "replay": true, "nmea": []string{stale}})
	c.Write(ctx, websocket.MessageText, body)
	if m := read(); m["type"] != "ack" {
		t.Fatalf("want ack, got %v", m)
	}
	if p.stats.replayed.Load() != 1 {
		t.Errorf("replayed=%d want 1", p.stats.replayed.Load())
	}

	// unsubscribe: a fresh sentence acks but no event follows
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"unsubscribe"}`))
	time.Sleep(50 * time.Millisecond)
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"publish","nmea":["!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"]}`))
	if m := read(); m["type"] != "ack" {
		t.Fatalf("want ack, got %v", m)
	}
	rctx, rcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer rcancel()
	if _, msg, err := c.Read(rctx); err == nil {
		t.Errorf("unexpected frame after unsubscribe: %s", msg)
	}
}

// /v1/stream subscribe with snapshot:true replays cached vessels: retained originals when held,
// synthesized reconstructions after a restore from disk, nothing outside the subscription.
func TestV1SnapshotSubscribe(t *testing.T) {
	p := testPipeline(t)
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // 227006760 at 49.4756N 0.1314E
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
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(msg, &m)
		return m
	}

	// retained original: real NMEA, not synthesized
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","bbox":[[49,0,50,1]],"snapshot":true}`))
	if m := read(); m["type"] != "event" || m["mmsi"] != float64(227006760) || m["synthesized"] == true || m["nmea"] == nil {
		t.Fatalf("want retained event, got %v", m)
	}

	// restored-from-disk vessel (no retained events): synthesized position and static
	p.vmu.Lock()
	v := p.vessels[227006760]
	v.lastPos, v.lastStatic = nil, nil
	v.Name, v.ShipType = "TEST", 70
	p.vmu.Unlock()
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","mmsi":[227006760],"snapshot":true}`))
	types := map[string]bool{}
	for i := 0; i < 2; i++ {
		m := read()
		if m["synthesized"] != true {
			t.Fatalf("want synthesized, got %v", m)
		}
		if _, ok := m["id"]; ok {
			t.Fatalf("id present on reconstruction: %v", m)
		}
		if _, ok := m["nmea"]; ok {
			t.Fatalf("nmea present on reconstruction: %v", m)
		}
		types[m["msg_type"].(string)] = true
	}
	if !types["PositionReport"] || !types["ShipStaticData"] {
		t.Fatalf("want position and static, got %v", types)
	}

	// bbox elsewhere: nothing replayed (reading past the timeout closes the socket, so this check is last)
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","bbox":[[60,10,61,11]],"snapshot":true}`))
	rctx, rcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer rcancel()
	if _, msg, err := c.Read(rctx); err == nil {
		t.Fatalf("unexpected frame: %s", msg)
	}
}

func TestStationStats(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.Ingest(Reception{Source: "s1", Station: "s1", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	p.Ingest(Reception{Source: "s2", Station: "s2", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // same message, heard second
	rows := p.stations.rows(now)
	if len(rows) != 2 || rows[0].Station != "s1" || rows[0].Events["last_24h"] != 1 || rows[0].Events["last_7d"] != 1 || rows[0].Vessels != 1 || rows[0].Positions != 1 || rows[0].BBox == nil || rows[1].Dups != 1 || rows[1].Events["last_24h"] != 0 {
		t.Errorf("rows: %+v", rows)
	}
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	res, _ := http.Get(srv.URL + "/v1/stations/s1")
	var out struct {
		Station stationRow
		Vessels struct{ Features []any }
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Station.Station != "s1" || len(out.Vessels.Features) != 1 {
		t.Errorf("station detail: %+v", out)
	}
	if res, _ := http.Get(srv.URL + "/v1/stations/nope"); res.StatusCode != 404 {
		t.Errorf("unknown station: %d", res.StatusCode)
	}
}

func TestNMEAFeed(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	feeder, _ := signToken(priv, Claims{Kid: kid, Sub: "st1", Role: "feeder", Exp: time.Now().Add(time.Hour).Unix()})
	personal, _ := signToken(priv, Claims{Kid: kid, Sub: "p", Role: "personal", Exp: time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/nmea"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, res, err := websocket.Dial(ctx, base+"?key="+personal, nil); err == nil || res == nil || res.StatusCode != 401 {
		t.Errorf("personal token accepted on /v1/nmea: %v", err)
	}
	c, _, err := websocket.Dial(ctx, base+"?key="+feeder+"&bbox=49,0,50,1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	time.Sleep(50 * time.Millisecond)
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket/2573010", RecvTime: time.Unix(1787234980, 0), Body: `\s:2573010,c:1787234980*03\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43`}) // outside bbox
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Unix(1787234990, 0), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})                                                      // inside
	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := string(msg)
	if !strings.HasPrefix(got, `\s:t,c:1787234990,t:feeder*`) || !strings.Contains(got, `\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`+"\r\n") {
		t.Errorf("nmea frame: %q", got)
	}
	if tb := tagBlock(map[byte]string{'s': "2573010", 'c': "1787234980"}); tb != `\s:2573010,c:1787234980*03\` {
		t.Errorf("tag block checksum: %s", tb)
	}
}

func TestPingTimeoutReleasesSlots(t *testing.T) {
	pingEvery, pingTimeout = 50*time.Millisecond, 50*time.Millisecond
	defer func() { pingEvery, pingTimeout = 30*time.Second, 10*time.Second }()
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	// The client never calls Read, so it never answers the server's pings: a link that went dead without a FIN.
	held := func() string {
		conns.mu.Lock()
		defer conns.mu.Unlock()
		addrConns.mu.Lock()
		defer addrConns.mu.Unlock()
		if len(conns.n)+len(addrConns.n) == 0 {
			return ""
		}
		return fmt.Sprintf("conns=%v addr=%v", conns.n, addrConns.n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if held() == "" && p.stats.pingTimeouts.Load() == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("slots still held after ping timeout: %s timeouts=%d", held(), p.stats.pingTimeouts.Load())
}
