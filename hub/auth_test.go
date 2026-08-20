package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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

	// /v1/receive: feeder may publish, personal may not, bad token rejected
	for _, c := range []struct {
		tok  string
		want int
	}{{feeder, 200}, {personal, 401}, {"nope", 401}, {"", 401}} {
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
	if _, _, msg := p.parseV0Sub([]byte(`{"APIKey":"`+res.Token+`","BoundingBoxes":[[[-90,-180],[90,180]]]}`), "9.9.9.9"); msg != "" {
		t.Errorf("personal token on /v0: %q", msg)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
