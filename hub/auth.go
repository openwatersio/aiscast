package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Access tokens: Ed25519-signed claims, verified with the issuer's public key. The hub never holds a key that
// can mint broad tokens; the CLI (cmd/aiscast-key) does. Format: "ak1.<b64url claims JSON>.<b64url signature>".
//
//	{"kid":"2026-08","sub":"station-42","role":"feeder","exp":1787000000,"iat":...,
//	 "bbox":[[s,w,n,e]],"cidr":["203.0.113.0/24"],"conns":2}
//
// Roles: personal (subscribe, small limits), feeder (publish/receive), peer (publish+subscribe), partner
// (subscribe, negotiated limits), admin (everything). bbox limits what may be subscribed; cidr limits where
// from; conns caps concurrent WebSockets per sub.

const tokenPrefix = "ak1."

type Claims struct {
	Kid   string   `json:"kid"`
	Sub   string   `json:"sub"`
	Role  string   `json:"role"`
	Exp   int64    `json:"exp"`
	Iat   int64    `json:"iat,omitempty"`
	BBox  []bbox   `json:"bbox,omitempty"`
	CIDR  []string `json:"cidr,omitempty"`
	Conns int      `json:"conns,omitempty"`
}

func (c *Claims) may(action string) bool {
	switch c.Role {
	case "admin":
		return true
	case "feeder":
		return action == "publish"
	case "peer":
		return action == "publish" || action == "subscribe"
	case "personal", "partner":
		return action == "subscribe"
	}
	return false
}

// allowsBox: the requested box must lie inside one of the claim's boxes (no claim boxes = anywhere).
func (c *Claims) allowsBox(b bbox) bool {
	if len(c.BBox) == 0 {
		return true
	}
	for _, a := range c.BBox {
		if b[0] >= a[0] && b[1] >= a[1] && b[2] <= a[2] && b[3] <= a[3] {
			return true
		}
	}
	return false
}

func (c *Claims) allowsIP(ip string) bool {
	if len(c.CIDR) == 0 {
		return true
	}
	addr := net.ParseIP(ip)
	for _, s := range c.CIDR {
		if _, n, err := net.ParseCIDR(s); err == nil && n.Contains(addr) {
			continue
		} else if err == nil {
			continue
		}
	}
	for _, s := range c.CIDR {
		if _, n, err := net.ParseCIDR(s); err == nil && n.Contains(addr) {
			return true
		}
		if net.ParseIP(s) != nil && net.ParseIP(s).Equal(addr) {
			return true
		}
	}
	return false
}

var validSub = regexp.MustCompile(`^[A-Za-z0-9_.:=/+-]{1,128}$`).MatchString

func signToken(priv ed25519.PrivateKey, c Claims) (string, error) {
	if !validSub(c.Sub) || c.Role == "" || c.Exp == 0 {
		return "", errors.New("claims need sub, role, exp")
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	sig := ed25519.Sign(priv, []byte(tokenPrefix+b64))
	return tokenPrefix + b64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// verifier holds the trusted issuer public keys by kid and the revocation list.
type verifier struct {
	keys    map[string]ed25519.PublicKey
	revoked map[string]bool
}

// verifierFromEnv: ISSUER_PUBKEYS="kid:base64pub,kid:base64pub"; REVOKED_SUBS="sub,sub".
func verifierFromEnv() *verifier {
	v := &verifier{keys: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	for _, kv := range strings.Split(os.Getenv("ISSUER_PUBKEYS"), ",") {
		if kid, pub, ok := strings.Cut(strings.TrimSpace(kv), ":"); ok {
			if b, err := base64.RawURLEncoding.DecodeString(pub); err == nil && len(b) == ed25519.PublicKeySize {
				v.keys[kid] = ed25519.PublicKey(b)
			} else {
				log.Printf("ISSUER_PUBKEYS: bad key for kid %q", kid)
			}
		}
	}
	for _, s := range strings.Split(os.Getenv("REVOKED_SUBS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			v.revoked[s] = true
		}
	}
	if len(v.keys) == 0 && !allowAnon {
		log.Printf("ISSUER_PUBKEYS unset: no token can verify; only ALLOW_ANON=1 would admit anyone")
	}
	return v
}

var errBadToken = errors.New("invalid token")

func (v *verifier) verify(token string, now time.Time) (*Claims, error) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return nil, errBadToken
	}
	b64, sigB64, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, errBadToken
	}
	body, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, errBadToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, errBadToken
	}
	var c Claims
	if json.Unmarshal(body, &c) != nil || !validSub(c.Sub) {
		return nil, errBadToken
	}
	pub, ok := v.keys[c.Kid]
	if !ok || !ed25519.Verify(pub, []byte(tokenPrefix+b64), sig) {
		return nil, errBadToken
	}
	if now.Unix() > c.Exp {
		return nil, errors.New("token expired")
	}
	if v.revoked[c.Sub] {
		return nil, errors.New("token revoked")
	}
	return &c, nil
}

// requestToken finds a token in Authorization: Bearer, Basic auth (either field), or ?key=.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if u, pw, ok := r.BasicAuth(); ok { // AIS-catcher USERPWD "anything:token" or "token:"
		if strings.HasPrefix(pw, tokenPrefix) {
			return pw
		}
		if strings.HasPrefix(u, tokenPrefix) {
			return u
		}
	}
	return r.URL.Query().Get("key")
}

// authorize: token from the request, verified, role allows action, IP allowed. With ALLOW_ANON a missing or
// bad token yields an anonymous admin identity (local development only).
func (p *Pipeline) authorize(r *http.Request, action string) (*Claims, error) {
	tok := requestToken(r)
	c, err := p.auth.verify(tok, time.Now())
	if err != nil {
		if allowAnon {
			return &Claims{Sub: "anon", Role: "admin", Exp: 1 << 62}, nil
		}
		return nil, err
	}
	if !c.may(action) {
		return nil, fmt.Errorf("role %s may not %s", c.Role, action)
	}
	if !c.allowsIP(clientIP(r)) {
		return nil, errors.New("token not valid from this address")
	}
	return c, nil
}

// ---- concurrent connection caps per sub ----

type connCounter struct {
	mu sync.Mutex
	n  map[string]int
}

var conns = &connCounter{n: map[string]int{}}

// acquire returns false when sub already has its allowed number of connections; release with the returned func.
func (cc *connCounter) acquire(sub string, max int) (func(), bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if max > 0 && cc.n[sub] >= max {
		return nil, false
	}
	cc.n[sub]++
	return func() {
		cc.mu.Lock()
		cc.n[sub]--
		if cc.n[sub] == 0 {
			delete(cc.n, sub)
		}
		cc.mu.Unlock()
	}, true
}

// ---- personal tokens: POST /v1/keys {"pubkey": "<base64 ed25519 public key>"} ----
// The hub holds a separate personal-tier issuer key (PERSONAL_ISSUER_KEY, base64 seed); what it mints is
// bounded by personalClaims, so a compromised box can only hand out personal-tier access.

var personalIssuer = func() (string, ed25519.PrivateKey) {
	kid, seed, ok := strings.Cut(os.Getenv("PERSONAL_ISSUER_KEY"), ":")
	if !ok {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(seed)
	if err != nil || len(b) != ed25519.SeedSize {
		log.Printf("PERSONAL_ISSUER_KEY: bad seed")
		return "", nil
	}
	return kid, ed25519.NewKeyFromSeed(b)
}

var keysLimit = newLimiter(10) // personal-token requests per IP per minute

func personalClaims(kid, sub string, now time.Time) Claims {
	return Claims{Kid: kid, Sub: sub, Role: "personal", Iat: now.Unix(), Exp: now.Add(30 * 24 * time.Hour).Unix(), Conns: 2}
}

func (p *Pipeline) serveKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST {\"pubkey\": \"<base64url ed25519 public key>\"}", http.StatusMethodNotAllowed)
		return
	}
	kid, priv := personalIssuer()
	if priv == nil {
		http.Error(w, "personal tokens not enabled", http.StatusNotImplemented)
		return
	}
	if p.limited(w, keysLimit, clientIP(r)) {
		return
	}
	var req struct{ Pubkey string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	pk, err := base64.RawURLEncoding.DecodeString(req.Pubkey)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		http.Error(w, "pubkey must be a base64url ed25519 public key", http.StatusBadRequest)
		return
	}
	// ponytail: no proof of possession yet; the token is a bearer token whose sub names the device key
	c := personalClaims(kid, "ed25519:"+req.Pubkey, time.Now())
	tok, err := signToken(priv, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"token": tok, "claims": c})
}

// newIssuerKey makes an issuer keypair; used by the CLI and tests.
func newIssuerKey() (pub, seed string) {
	pk, sk, _ := ed25519.GenerateKey(rand.Reader)
	return base64.RawURLEncoding.EncodeToString(pk), base64.RawURLEncoding.EncodeToString(sk.Seed())
}
