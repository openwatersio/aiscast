package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
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

// /v1/stream greets every socket with its effective limits before anything else.
func TestV1Welcome(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/stream"

	welcome := func(url string) v1Welcome {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var w v1Welcome
		if json.Unmarshal(msg, &w) != nil || w.Type != "welcome" {
			t.Fatalf("first frame not a welcome: %s", msg)
		}
		if w.Terms != termsURL {
			t.Errorf("welcome terms: %q want %q", w.Terms, termsURL)
		}
		return w
	}

	w := welcome(base) // no token: anonymous tier
	if w.Role != "anonymous" || w.Limits.Conns != anonConns || w.Limits.Rate != anonRate || w.Limits.Area != anonArea || w.Limits.MMSIs != anonMMSIs || w.Limits.Publish || w.Limits.PublishFrame != 0 || w.Limits.ConnectsPerMin <= 0 {
		t.Errorf("anonymous welcome: %+v", w)
	}

	tok, _ := signToken(priv, personalClaims(kid, "ed25519:abc", time.Now()))
	w = welcome(base + "?key=" + tok)
	if w.Role != "personal" || w.Sub != "ed25519:abc" || w.Feeder || w.Limits.Rate != personalRate || w.Limits.Area != personalArea || !w.Limits.Publish || w.Limits.PublishFrame != maxPublishFrame || w.Limits.PublishPerMin <= 0 {
		t.Errorf("personal welcome: %+v", w)
	}

	scoped, _ := signToken(priv, Claims{Kid: kid, Sub: "acme", Role: "partner", Area: -1, BBox: []bbox{{55, 5, 65, 15}}, Exp: time.Now().Add(time.Hour).Unix()})
	w = welcome(base + "?key=" + scoped)
	if w.Role != "partner" || w.Limits.Area != -1 || len(w.Limits.BBox) != 1 || w.Limits.Publish {
		t.Errorf("scoped welcome: %+v", w)
	}
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
		if link := w.Result().Header.Get("Link"); link != "<"+termsURL+`>; rel="terms-of-service"` {
			t.Errorf("receive Link header: %q", link)
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

// registerConn opens an anonymous /v1/stream socket against a pipeline with a personal issuer, consumes the
// anonymous welcome, and returns write/read helpers for in-band register tests. keysPerMin swaps in a fresh
// limiter before the server exists (a swap after dialing would race the handler's reads).
func registerConn(t *testing.T, keysPerMin int) (*Pipeline, func(string), func() map[string]any) {
	t.Helper()
	p := testPipeline(t)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	pub, seed := newIssuerKey()
	t.Setenv("PERSONAL_ISSUER_KEY", "p:"+seed)
	pb, _ := base64.RawURLEncoding.DecodeString(pub)
	p.auth = &verifier{keys: map[string]ed25519.PublicKey{"p": ed25519.PublicKey(pb)}, revoked: map[string]bool{}}
	old := keysLimit
	keysLimit = newLimiter(keysPerMin) // fresh window: the loopback address is shared across the package
	t.Cleanup(func() { keysLimit = old })
	srv := httptest.NewServer(httpHandler(p))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	write := func(frame string) {
		if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("bad frame %s: %v", msg, err)
		}
		return m
	}
	if m := read(); m["type"] != "welcome" || m["role"] != "anonymous" {
		t.Fatalf("first frame: %v", m)
	}
	return p, write, read
}

func TestV1Register(t *testing.T) {
	p, write, read := registerConn(t, keysPerMinute)
	devPub, _ := newIssuerKey()

	write(`{"type":"register","pubkey":"nope"}`)
	if m := read(); m["error"] != "pubkey must be a base64url ed25519 public key" {
		t.Fatalf("bad pubkey: %v", m)
	}
	write(`{"type":"publish","nmea":[]}`)
	if m := read(); m["error"] != "publish requires a token" {
		t.Fatalf("still anonymous after bad pubkey: %v", m)
	}

	write(`{"type":"register","pubkey":"` + devPub + `"}`)
	key := read()
	if key["type"] != "key" {
		t.Fatalf("key frame: %v", key)
	}
	cl, err := p.auth.verify(key["token"].(string), time.Now())
	if err != nil || cl.Role != "personal" || cl.Sub != "ed25519:"+devPub || cl.Conns != personalConns {
		t.Errorf("minted token: %+v %v", cl, err)
	}
	w := read()
	if w["type"] != "welcome" || w["role"] != "personal" || w["sub"] != "ed25519:"+devPub {
		t.Errorf("second welcome: %v", w)
	}
	if lim, _ := w["limits"].(map[string]any); lim["publish"] != true || lim["rate"] != float64(personalRate) {
		t.Errorf("second welcome limits: %v", w["limits"])
	}
	conns.mu.Lock()
	n, anon := conns.n["ed25519:"+devPub], conns.n["anon:127.0.0.1"]
	conns.mu.Unlock()
	if n != 1 || anon != 0 {
		t.Errorf("slot not re-keyed: key=%d anon=%d", n, anon)
	}
	write(`{"type":"publish","nmea":[]}`)
	if m := read(); m["type"] != "ack" {
		t.Errorf("publish after register: %v", m)
	}
	write(`{"type":"register","pubkey":"` + devPub + `"}`)
	if m := read(); m["error"] != "already registered" {
		t.Errorf("second register: %v", m)
	}
}

func TestV1RegisterDisabled(t *testing.T) {
	_, write, read := registerConn(t, keysPerMinute)
	t.Setenv("PERSONAL_ISSUER_KEY", "")
	write(`{"type":"register","pubkey":"x"}`)
	if m := read(); m["error"] != "personal tokens not enabled" {
		t.Fatalf("issuer unset: %v", m)
	}
	write(`{"type":"end"}`) // connection survives the error
	if m := read(); m["error"] != "unknown type" {
		t.Errorf("connection after disabled register: %v", m)
	}
}

func TestV1RegisterRateLimited(t *testing.T) {
	_, write, read := registerConn(t, 1)
	write(`{"type":"register","pubkey":"bad"}`) // limiter check precedes pubkey decode
	if m := read(); m["error"] != "pubkey must be a base64url ed25519 public key" {
		t.Fatalf("first register: %v", m)
	}
	write(`{"type":"register","pubkey":"bad"}`)
	if m := read(); m["error"] != "rate limited" {
		t.Errorf("second register: %v", m)
	}
}

func TestV1RegisterSlotFull(t *testing.T) {
	_, write, read := registerConn(t, keysPerMinute)
	devPub, _ := newIssuerKey()
	rel1, _ := conns.acquire("ed25519:"+devPub, 0)
	rel2, _ := conns.acquire("ed25519:"+devPub, 0) // personalConns live entries: the upgrade slot is refused
	write(`{"type":"register","pubkey":"` + devPub + `"}`)
	if m := read(); m["error"] != "concurrent connections per key exceeded" {
		t.Fatalf("register with full slots: %v", m)
	}
	write(`{"type":"publish","nmea":[]}`)
	if m := read(); m["error"] != "publish requires a token" {
		t.Errorf("connection should stay anonymous: %v", m)
	}
	rel1()
	rel2()
	write(`{"type":"register","pubkey":"` + devPub + `"}`)
	if m := read(); m["type"] != "key" {
		t.Errorf("register after slots freed: %v", m)
	}
	if m := read(); m["type"] != "welcome" || m["role"] != "personal" {
		t.Errorf("welcome after retry: %v", m)
	}
}

// The upgrade re-keys only the sub slot, so an address already at its stream cap can still register in place.
func TestV1RegisterAtAddressCap(t *testing.T) {
	_, write, read := registerConn(t, keysPerMinute)
	for i := 0; i < addrMaxStreams-1; i++ { // the open socket holds the address's first slot
		rel, ok := addrConns.acquire("127.0.0.1", addrMaxStreams)
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		t.Cleanup(rel)
	}
	devPub, _ := newIssuerKey()
	write(`{"type":"register","pubkey":"` + devPub + `"}`)
	if m := read(); m["type"] != "key" {
		t.Fatalf("register at address cap: %v", m)
	}
	if m := read(); m["type"] != "welcome" || m["role"] != "personal" {
		t.Errorf("welcome at address cap: %v", m)
	}
	addrConns.mu.Lock()
	n := addrConns.n["127.0.0.1"]
	addrConns.mu.Unlock()
	if n != addrMaxStreams {
		t.Errorf("address count %d after upgrade, want %d", n, addrMaxStreams)
	}
}

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
	// in the past so the trust timeline below (events up to now+10min) never trips the future-stamp cap
	now := time.Now().Add(-20 * time.Minute)

	// no exp = never expires; exp in the past still expires
	forever, _ := signToken(priv, Claims{Kid: kid, Sub: "forever", Role: "personal"})
	if c, err := p.auth.verify(forever, now.Add(10*365*24*time.Hour)); err != nil || c.Exp != 0 {
		t.Errorf("token without exp: %v", err)
	}
	tampered := []byte(forever)
	i := len(tampered) - 20 // inside the signature; the last char's low bits can be padding and survive a flip
	if tampered[i] == 'A' {
		tampered[i] = 'B'
	} else {
		tampered[i] = 'A'
	}
	if _, err := p.auth.verify(string(tampered), now); err == nil {
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

	// per-address ceiling across tokens, with the refusal naming the cap that was hit
	var rels []func()
	for i := 0; i < addrMaxStreams; i++ {
		rel, err := acquireStream(&Claims{Sub: fmt.Sprintf("k%d", i), Conns: 2}, "198.51.100.1")
		if err != nil {
			t.Fatalf("stream %d refused under the address ceiling: %v", i, err)
		}
		rels = append(rels, rel)
	}
	if _, err := acquireStream(&Claims{Sub: "k-extra", Conns: 2}, "198.51.100.1"); err == nil || err.Error() != "concurrent streams per address exceeded" {
		t.Errorf("stream %d from one address: %v", addrMaxStreams+1, err)
	}
	for _, r := range rels {
		r()
	}
	tok := &Claims{Sub: "k-one", Conns: 2}
	for i := 0; i < 2; i++ {
		if rel, err := acquireStream(tok, "198.51.100.1"); err != nil {
			t.Fatal(err)
		} else {
			defer rel()
		}
	}
	if _, err := acquireStream(tok, "198.51.100.2"); err == nil || err.Error() != "concurrent connections per key exceeded" {
		t.Errorf("third stream on one token: %v", err)
	}
	anon := anonymousClaims("198.51.100.3")
	for i := 0; i < anonConns; i++ {
		if rel, err := acquireStream(anon, "198.51.100.3"); err != nil {
			t.Fatal(err)
		} else {
			defer rel()
		}
	}
	if _, err := acquireStream(anon, "198.51.100.3"); err == nil || err.Error() != "concurrent streams per address exceeded" {
		t.Errorf("third anonymous stream from one address: %v", err)
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
	// a UDP report putting the same vessel 3,000 nm away three seconds later is implausible: dropped, not
	// emitted (three, not two: rebuilt events must advance the vessel's clock by more than a second first)
	before := p.stats.implausible.Load()
	p.ingestPacket(udp, udp, now.Add(3*time.Second), ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 227006760}, Valid: true, Latitude: 0, Longitude: 0, Cog: 360, Sog: 102.3, TrueHeading: 511})
	if p.stats.implausible.Load() != before+1 || len(sub.ch) != 0 {
		t.Errorf("implausible jump: count %d→%d, events %d", before, p.stats.implausible.Load(), len(sub.ch))
	}
	// a different vessel far away is not a jump
	p.Ingest(Reception{Source: udp, Station: udp, RecvTime: now.Add(2 * time.Second), Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})
	if p.stats.implausible.Load() != before+1 {
		t.Error("different vessel counted as implausible")
	}
	// the same vessel, a plausible distance later, is accepted from UDP and is now corroborated
	for len(sub.ch) > 0 {
		<-sub.ch
	}
	p.ingestPacket(udp, udp, now.Add(10*time.Minute), ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 227006760}, Valid: true, Latitude: 49.5, Longitude: 0.2, Cog: 360, Sog: 102.3, TrueHeading: 511})
	if len(sub.ch) != 1 {
		t.Fatalf("plausible UDP report not emitted (events=%d)", len(sub.ch))
	}
	if ev := <-sub.ch; !ev.LowTrust || !ev.Corroborated {
		t.Errorf("expected corroborated low-trust event, got low=%v corroborated=%v", ev.LowTrust, ev.Corroborated)
	}
}

// A trusted source repeating, within the dedupe window, a payload that UDP delivered first must still corroborate.
func TestDedupedTrustedCopyCorroborates(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Now()
	udp := udpStation("203.0.113.77")
	line := "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23" // 227006760
	p.Ingest(Reception{Source: udp, Station: udp, RecvTime: now, Body: line})
	if ev := <-sub.ch; ev.Corroborated {
		t.Fatal("first UDP report should be uncorroborated")
	}
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now.Add(time.Second), Body: line}) // identical payload: deduplicated
	if len(sub.ch) != 0 {
		t.Fatal("duplicate was emitted")
	}
	p.ingestPacket(udp, udp, now.Add(30*time.Second), ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 227006760}, Valid: true, Latitude: 49.476, Longitude: 0.132, Cog: 360, Sog: 102.3, TrueHeading: 511})
	if ev := <-sub.ch; !ev.Corroborated {
		t.Error("UDP report after a deduplicated trusted copy should be corroborated")
	}
}

func TestMMSISubscriptions(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	tok, _ := signToken(priv, personalClaims(kid, "ed25519:m", time.Now()))
	// /v0: a world bbox is fine when an MMSI filter bounds the traffic; too many MMSIs for the tier is refused
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,-180],[90,180]]],"FiltersShipMMSI":["227006760"]}`), "1.1.1.1"); msg != "" {
		t.Errorf("world bbox with mmsi filter: %q", msg)
	}
	many := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		many = append(many, fmt.Sprintf("%09d", 200000000+i))
	}
	b, _ := json.Marshal(many)
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,-180],[90,180]]],"FiltersShipMMSI":`+string(b)+`}`), "1.1.1.1"); msg != errMalformed {
		t.Errorf("51 mmsi on /v0 should be malformed (aisstream cap): %q", msg)
	}
	an := anonymousClaims("1.1.1.1")
	if an.allowsMMSIs(anonMMSIs+1) || !an.allowsMMSIs(anonMMSIs) {
		t.Error("anonymous mmsi cap")
	}
	// /v1 matching: mmsi alone, bbox alone, both
	ev := &Event{MMSI: 1, HasPos: true, Lat: 50, Lon: 5}
	if !(&v1Sub{mmsi: map[uint32]bool{1: true}}).match(ev) || (&v1Sub{mmsi: map[uint32]bool{2: true}}).match(ev) {
		t.Error("mmsi-only subscription")
	}
	if !(&v1Sub{mmsi: map[uint32]bool{2: true}, boxes: []bbox{{49, 4, 51, 6}}}).match(ev) || (&v1Sub{boxes: []bbox{{0, 0, 1, 1}}}).match(ev) {
		t.Error("mmsi OR bbox subscription")
	}
	if !(&v1Sub{mmsi: map[uint32]bool{1: true}}).match(&Event{MMSI: 1}) { // positionless static message still follows
		t.Error("mmsi subscription should not require a position")
	}
}

// negative area: the key may only follow vessels by MMSI; no bbox or whole-world subscription on any endpoint.
func TestMMSIOnlyKey(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	tok, _ := signToken(priv, Claims{Kid: kid, Sub: "fleet", Role: "partner", Area: -1, MMSIs: 230, Exp: time.Now().Add(time.Hour).Unix()})
	many := make([]string, 0, 230)
	for i := 0; i < 230; i++ {
		many = append(many, fmt.Sprintf("%09d", 200000000+i))
	}
	b, _ := json.Marshal(many)
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,-180],[90,180]]],"FiltersShipMMSI":`+string(b)+`}`), "1.1.1.1"); msg != "" {
		t.Errorf("/v0 230 mmsi with a 230 claim: %q", msg)
	}
	b, _ = json.Marshal(append(many, "200000230"))
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,-180],[90,180]]],"FiltersShipMMSI":`+string(b)+`}`), "1.1.1.1"); msg != errMMSIsDenied {
		t.Errorf("/v0 231 mmsi with a 230 claim should be over the key's cap, not malformed: %q", msg)
	}
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[49,0],[50,1]]]}`), "1.1.1.1"); msg != errBoxDenied {
		t.Errorf("/v0 bbox without mmsi on an mmsi-only key: %q", msg)
	}
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+tok+`","BoundingBoxes":[[[-90,0],[90,0]]]}`), "1.1.1.1"); msg != errBoxDenied {
		t.Errorf("/v0 zero-area line box on an mmsi-only key: %q", msg)
	}
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	for _, q := range []string{"", "&bbox=-90,0,90,0"} {
		if _, res, err := websocket.Dial(ctx, base+"/v1/nmea?key="+tok+q, nil); err == nil || res == nil || res.StatusCode != 400 {
			t.Errorf("/v1/nmea%s open to an mmsi-only key: %v", q, err)
		}
	}
	c, _, err := websocket.Dial(ctx, base+"/v1/stream?key="+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	reply := func(frame string) string { // everything the server says about frame; an unknown type marks the end
		c.Write(ctx, websocket.MessageText, []byte(frame))
		c.Write(ctx, websocket.MessageText, []byte(`{"type":"end"}`))
		var out string
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(msg), "unknown type") {
				return out
			}
			out += string(msg)
		}
	}
	for _, f := range []string{`{"type":"subscribe"}`, `{"type":"subscribe","bbox":[[49,0,50,1]]}`, `{"type":"subscribe","bbox":[[49,0,50,1]],"mmsi":[1]}`, `{"type":"subscribe","bbox":[[-90,0,90,0]]}`} {
		if r := reply(f); !strings.Contains(r, "bbox not allowed") {
			t.Errorf("%s on an mmsi-only key: %s", f, r)
		}
	}
	if r := reply(`{"type":"subscribe","mmsi":[1,2,3]}`); strings.Contains(r, "error") {
		t.Errorf("mmsi subscribe on an mmsi-only key: %s", r)
	}
}
