package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testIssuer(t *testing.T, p *Pipeline) (string, ed25519.PrivateKey) {
	pub, seed := newIssuerKey()
	sb, _ := base64.RawURLEncoding.DecodeString(seed)
	pb, _ := base64.RawURLEncoding.DecodeString(pub)
	p.auth = &verifier{keys: map[string]ed25519.PublicKey{"t": ed25519.PublicKey(pb)}, revoked: map[string]bool{"banned": true}}
	return "t", ed25519.NewKeyFromSeed(sb)
}

func TestTokens(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	now := time.Now()
	mk := func(c Claims) string {
		c.Kid = kid
		if c.Exp == 0 {
			c.Exp = now.Add(time.Hour).Unix()
		}
		tok, err := signToken(priv, c)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	feeder := mk(Claims{Sub: "station-1", Role: "feeder"})
	personal := mk(Claims{Sub: "ed25519:abc", Role: "personal", BBox: []bbox{{55, 5, 65, 15}}, Conns: 1})
	expired := mk(Claims{Sub: "old", Role: "admin", Exp: now.Add(-time.Minute).Unix()})
	partner := mk(Claims{Sub: "acme", Role: "partner"})
	banned := mk(Claims{Sub: "banned", Role: "admin"})
	lan := mk(Claims{Sub: "lan", Role: "peer", CIDR: []string{"10.0.0.0/8"}})

	if _, err := p.auth.verify(feeder, now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.auth.verify(feeder[:len(feeder)-2]+"xx", now); err == nil {
		t.Error("tampered signature accepted")
	}
	if _, err := p.auth.verify(expired, now); err == nil {
		t.Error("expired accepted")
	}
	if _, err := p.auth.verify(banned, now); err == nil {
		t.Error("revoked accepted")
	}

	// /v1/receive: feeder and personal may publish, partner may not, bad token rejected
	for _, c := range []struct {
		tok  string
		want int
	}{{feeder, 200}, {personal, 200}, {partner, 401}, {"nope", 401}, {"", 401}} {
		r := httptest.NewRequest("POST", "/v1/receive", strings.NewReader("!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"))
		r.RemoteAddr = "203.0.113.5:1234"
		if c.tok != "" {
			r.SetBasicAuth("x", c.tok)
		}
		w := httptest.NewRecorder()
		p.serveReceive(w, r)
		if w.Code != c.want {
			t.Errorf("receive with %q: %d want %d (%s)", c.tok[:min(8, len(c.tok))], w.Code, c.want, w.Body.String())
		}
	}
	// cidr claim
	r := httptest.NewRequest("POST", "/v1/receive", strings.NewReader("x"))
	r.RemoteAddr = "203.0.113.5:1"
	r.Header.Set("Authorization", "Bearer "+lan)
	if _, err := p.authorize(r, "publish"); err == nil {
		t.Error("cidr-bound token accepted from outside")
	}
	r.RemoteAddr = "10.1.2.3:1"
	if _, err := p.authorize(r, "publish"); err != nil {
		t.Errorf("cidr-bound token rejected from inside: %v", err)
	}

	// /v0: personal token inside and outside its bbox; feeder token may not subscribe
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+personal+`","BoundingBoxes":[[[58,9],[60,11]]]}`), "1.2.3.4"); msg != "" {
		t.Errorf("personal in-bbox: %q", msg)
	}
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+personal+`","BoundingBoxes":[[[40,-80],[45,-70]]]}`), "1.2.3.4"); msg != errBoxDenied {
		t.Errorf("personal out-of-bbox: %q", msg)
	}
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+feeder+`","BoundingBoxes":[[[58,9],[60,11]]]}`), "1.2.3.4"); msg != errBadKey {
		t.Errorf("feeder subscribing: %q", msg)
	}
	// connection cap
	rel, ok := conns.acquire("ed25519:abc", 1)
	if !ok {
		t.Fatal("first connection refused")
	}
	if _, ok := conns.acquire("ed25519:abc", 1); ok {
		t.Error("second connection allowed past cap")
	}
	rel()
	if rel2, ok := conns.acquire("ed25519:abc", 1); !ok {
		t.Error("connection refused after release")
	} else {
		rel2()
	}
}

func TestPersonalKeys(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	pub, seed := newIssuerKey()
	t.Setenv("PERSONAL_ISSUER_KEY", "p:"+seed)
	pb, _ := base64.RawURLEncoding.DecodeString(pub)
	p.auth = &verifier{keys: map[string]ed25519.PublicKey{"p": ed25519.PublicKey(pb)}, revoked: map[string]bool{}}
	devPub, _ := newIssuerKey()
	r := httptest.NewRequest("POST", "/v1/keys", strings.NewReader(`{"pubkey":"`+devPub+`"}`))
	r.RemoteAddr = "9.9.9.9:1"
	w := httptest.NewRecorder()
	p.serveKeys(w, r)
	if w.Code != 200 {
		t.Fatalf("keys: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Token  string
		Claims Claims
	}
	if err := jsonUnmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	c, err := p.auth.verify(res.Token, time.Now())
	if err != nil || c.Role != "personal" || c.Sub != "ed25519:"+devPub || c.Conns != 2 {
		t.Errorf("personal token: %+v %v", c, err)
	}
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+res.Token+`","BoundingBoxes":[[[40,-75],[45,-65]]]}`), "9.9.9.9"); msg != "" {
		t.Errorf("personal token on /v0: %q", msg)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func TestV1SocketTokenScope(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	exp := time.Now().Add(time.Hour).Unix()
	personal, _ := signToken(priv, Claims{Kid: kid, Sub: "p1", Role: "personal", Exp: exp, BBox: []bbox{{55, 5, 65, 15}}})
	expired, _ := signToken(priv, Claims{Kid: kid, Sub: "old", Role: "admin", Exp: time.Now().Add(-time.Minute).Unix()})

	for _, c := range []struct {
		name, key string
		wantErr   bool
	}{{"anonymous", "", false}, {"personal", personal, false}, {"expired", expired, true}, {"garbage", "ak1.x.y", true}} {
		r := httptest.NewRequest("GET", "/v1/stream?key="+c.key, nil)
		r.RemoteAddr = "1.2.3.4:1"
		cl, err := p.socketClaims(r)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v", c.name, err)
		}
		if c.name == "personal" && (cl == nil || !cl.may("publish") || cl.allowsBox(bbox{40, -80, 45, -70}) || !cl.allowsBox(bbox{58, 9, 60, 11})) {
			t.Errorf("personal claims not enforced: %+v", cl)
		}
		if c.name == "anonymous" && cl != nil {
			t.Errorf("anonymous should have no claims")
		}
	}
}

func TestLimitsClaims(t *testing.T) {
	c := &Claims{Area: 400}
	if !c.allowsArea([]bbox{{50, 0, 60, 20}}) || c.allowsArea([]bbox{{-90, -180, 90, 180}}) || !c.allowsArea([]bbox{{0, 0, 10, 10}, {20, 20, 30, 30}}) {
		t.Error("area claim")
	}
	if !(&Claims{}).allowsArea([]bbox{{-90, -180, 90, 180}}) {
		t.Error("unlimited area")
	}
	p := pacer{n: 2}
	now := time.Unix(100, 0)
	if !p.allow(now) || !p.allow(now) || p.allow(now) || !p.allow(now.Add(time.Second)) {
		t.Error("pacer")
	}
	if !(&pacer{}).allow(now) {
		t.Error("unlimited pacer")
	}
	an := anonymousClaims("1.2.3.4")
	if an.Conns != anonConns || an.Rate != anonRate || an.Area != anonArea || an.may("publish") || !an.may("subscribe") || an.Role != "anonymous" {
		t.Errorf("anonymous claims: %+v", an)
	}
	// /v0: personal token with the default area cannot take the whole world
	pp := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, pp)
	tok, _ := signToken(priv, personalClaims(kid, "ed25519:x", time.Now()))
	if _, _, msg := pp.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,-180],[90,180]]]}`), "1.2.3.4"); msg != errBoxDenied {
		t.Errorf("world bbox on personal token: %q", msg)
	}
	if _, _, msg := pp.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[40,-75],[45,-65]]]}`), "1.2.3.4"); msg != "" {
		t.Errorf("regional bbox on personal token: %q", msg)
	}
}

func TestTiersAndTrust(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	now := time.Now()

	// no exp = never expires; exp in the past still expires
	forever, _ := signToken(priv, Claims{Kid: kid, Sub: "forever", Role: "personal"})
	if c, err := p.auth.verify(forever, now.Add(10*365*24*time.Hour)); err != nil || c.Exp != 0 {
		t.Errorf("token without exp: %v", err)
	}
	if _, err := p.auth.verify(forever[:len(forever)-1]+"A", now); err == nil {
		t.Error("tampered token accepted")
	}

	// personal token earns the feeder tier once its station has fed enough in the last 24 h
	personal := personalClaims(kid, "ed25519:dev1", now)
	if e := p.effective(&personal); e.Feeder || e.Conns != personalConns || e.mayRaw() {
		t.Errorf("personal before feeding: %+v", e)
	}
	for i := 0; i < feederMinEvents24h; i++ {
		p.stations.event(&Event{Station: "v1:ed25519:dev1", Source: "v1:ed25519:dev1", Time: now.Add(-time.Duration(i) * time.Minute), MMSI: 1})
	}
	if e := p.effective(&personal); !e.Feeder || e.Conns != feederConns || e.Rate != feederRate || e.Area != 0 || !e.mayRaw() {
		t.Errorf("personal after feeding: %+v", e)
	}
	// events older than 24 h do not count
	old := Claims{Sub: "ed25519:dev2", Role: "personal"}
	for i := 0; i < feederMinEvents24h; i++ {
		p.stations.event(&Event{Station: "v1:ed25519:dev2", Source: "v1:ed25519:dev2", Time: now.Add(-30 * time.Hour), MMSI: 1})
	}
	if e := p.effective(&old); e.Feeder {
		t.Error("stale contribution earned the tier")
	}
	// a UDP station bound by cidr counts
	bound := Claims{Sub: "ed25519:dev3", Role: "personal", CIDR: []string{"203.0.113.9"}}
	for i := 0; i < feederMinEvents24h; i++ {
		p.stations.event(&Event{Station: udpStation("203.0.113.9"), Source: udpStation("203.0.113.9"), Time: now, MMSI: 1})
	}
	if e := p.effective(&bound); !e.Feeder {
		t.Error("bound UDP station did not count")
	}

	// per-address ceiling across tokens
	var rels []func()
	for i := 0; i < addrMaxStreams; i++ {
		rel, ok := acquireStream(&Claims{Sub: fmt.Sprintf("k%d", i), Conns: 2}, "198.51.100.1")
		if !ok {
			t.Fatalf("stream %d refused under the address ceiling", i)
		}
		rels = append(rels, rel)
	}
	if _, ok := acquireStream(&Claims{Sub: "k-extra", Conns: 2}, "198.51.100.1"); ok {
		t.Error("ninth stream from one address allowed")
	}
	for _, r := range rels {
		r()
	}

	// UDP trust: uncorroborated positions stay out of the AISHub feed; an implausible jump is dropped
	sub := p.subscribe()
	udp := udpStation("203.0.113.50")
	p.Ingest(Reception{Source: udp, Station: udp, RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // 227006760 at 49.48N 0.13E
	ev := <-sub.ch
	if !ev.LowTrust || ev.Corroborated {
		t.Errorf("udp event trust flags: low=%v corroborated=%v", ev.LowTrust, ev.Corroborated)
	}
	// same vessel heard by Kystverket → subsequent UDP reports are corroborated
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now.Add(time.Second), Body: "!AIVDM,1,1,,B,13HOI:0P0000VOHLCnHQKwvL05Ip,0*20"})
	for len(sub.ch) > 0 {
		<-sub.ch
	}
	// a UDP report 3,000 nm away two seconds later is implausible and not emitted
	before := p.stats.implausible.Load()
	p.Ingest(Reception{Source: udp, Station: udp, RecvTime: now.Add(2 * time.Second), Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"}) // different MMSI, so not a jump
	if p.stats.implausible.Load() != before {
		t.Error("different vessel counted as implausible")
	}
}
