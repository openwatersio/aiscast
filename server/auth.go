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
	"time"
)

// Access tokens: Ed25519-signed claims, verified with the issuer's public key. The server never holds a key that
// can mint broad tokens; the CLI (cmd/aiscast-key) does. Format: "ak1.<b64url claims JSON>.<b64url signature>".
//
//	{"kid":"2026-08","sub":"station-42","role":"feeder","exp":1787000000,"iat":...,
//	 "bbox":[[s,w,n,e]],"cidr":["203.0.113.0/24"],"conns":2,"rate":50,"area":400}
//
// Roles: personal (subscribe, small limits), feeder (publish/receive), peer (publish+subscribe), partner
// (subscribe, negotiated limits), admin (everything). bbox limits what may be subscribed; cidr limits where
// from; conns caps concurrent WebSockets per sub; rate thins each connection to n msg/s; area caps the total
// subscribed bbox area in square degrees (negative = no bbox subscriptions at all, only MMSI lists). Unset claims
// are unlimited; personal tokens get 2/50/400.

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
	Rate  int      `json:"rate,omitempty"`  // messages per second per connection; excess is thinned, not disconnected
	Area  float64  `json:"area,omitempty"`  // max total subscribed bbox area, square degrees; 0 = unlimited, <0 = MMSI-only
	MMSIs int      `json:"mmsis,omitempty"` // max vessels followed by MMSI per subscription (0 = unlimited)

	Feeder bool `json:"-"` // earned for this connection: the token's station is feeding (see tiers.go)
}

// allowsMMSIs: a subscription may follow at most this many vessels by MMSI (0 = unlimited).
func (c *Claims) allowsMMSIs(n int) bool { return c.MMSIs <= 0 || n <= c.MMSIs }

// allowsArea: the total area of the requested boxes must not exceed the claim (0 = unlimited, <0 = no boxes).
func (c *Claims) allowsArea(boxes []bbox) bool {
	if c.Area == 0 {
		return true
	}
	if c.Area < 0 { // MMSI-only: no boxes at all, so a zero-area line box cannot slip under the cap
		return len(boxes) == 0
	}
	total := 0.0
	for _, b := range boxes {
		total += (b[2] - b[0]) * (b[3] - b[1])
	}
	return total <= c.Area
}

// pacer thins a connection to at most n events per second. ponytail: fixed one-second window.
type pacer struct {
	n    int
	sec  int64
	sent int
}

func (p *pacer) allow(now time.Time) bool {
	if p.n <= 0 {
		return true
	}
	if s := now.Unix(); s != p.sec {
		p.sec, p.sent = s, 0
	}
	p.sent++
	return p.sent <= p.n
}

func (c *Claims) may(action string) bool {
	switch c.Role {
	case "admin":
		return true
	case "feeder":
		return action == "publish"
	case "peer", "personal": // personal: self-minted for a device key; publishes as itself, never a trust upgrade
		return action == "publish" || action == "subscribe"
	case "partner", "anonymous":
		return action == "subscribe"
	}
	return false
}

// mayRaw: the deduplicated raw feed is for feeders (minted, or earned by contribution), peers, partners, admins.
func (c *Claims) mayRaw() bool {
	switch c.Role {
	case "feeder", "peer", "partner", "admin":
		return true
	}
	return c.Feeder
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
	if !validSub(c.Sub) || c.Role == "" {
		return "", errors.New("claims need sub and role")
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
	if c.Exp != 0 && now.Unix() > c.Exp { // no exp = never expires; misuse is handled by revocation
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
			return &Claims{Sub: "anon", Role: "admin"}, nil
		}
		return nil, err
	}
	c = p.effective(c)
	if !c.may(action) {
		return nil, fmt.Errorf("role %s may not %s", c.Role, action)
	}
	if !c.allowsIP(clientIP(r)) {
		return nil, errors.New("token not valid from this address")
	}
	return c, nil
}

// socketClaims verifies the token on a WebSocket request. No token → anonymous (nil, nil). A token that is
// present but invalid, or not valid from this address, is an error: it must never degrade to anonymous access.
func (p *Pipeline) socketClaims(r *http.Request) (*Claims, error) {
	tok := requestToken(r)
	if tok == "" {
		return nil, nil
	}
	c, err := p.auth.verify(tok, time.Now())
	if err != nil {
		if allowAnon {
			return &Claims{Sub: "anon", Role: "admin"}, nil
		}
		return nil, err
	}
	if !c.allowsIP(clientIP(r)) {
		return nil, errors.New("token not valid from this address")
	}
	return p.effective(c), nil
}

// requestClaims is socketClaims with the anonymous tier filled in for a tokenless request, so a plain GET
// (/v1/stream over SSE, /v1/vessels) is checked against the same caps as a socket.
func (p *Pipeline) requestClaims(r *http.Request) (*Claims, error) {
	cl, err := p.socketClaims(r)
	if err != nil || cl != nil {
		return cl, err
	}
	if allowAnon {
		return &Claims{Sub: "anon", Role: "admin"}, nil
	}
	return anonymousClaims(clientIP(r)), nil
}

// ---- personal tokens: POST /v1/keys {"pubkey": "<base64 ed25519 public key>"} ----
// The server holds a separate personal-tier issuer key (PERSONAL_ISSUER_KEY, base64 seed); what it mints is
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

var keysLimit = newLimiter(keysPerMinute) // personal-token requests per address per minute: one is all a client needs

// mintPersonal is the one mint path behind both POST /v1/keys and /v1/stream's register frame, so the two
// transports cannot drift (same claims, same bind_ip behaviour, same client-safe error strings).
func mintPersonal(ip, pubkey string, bindIP bool) (token string, c Claims, errMsg string) {
	kid, priv := personalIssuer()
	if priv == nil {
		return "", Claims{}, "personal tokens not enabled"
	}
	pk, err := base64.RawURLEncoding.DecodeString(pubkey)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return "", Claims{}, "pubkey must be a base64url ed25519 public key"
	}
	// ponytail: no proof of possession yet; the token is a bearer token whose sub names the device key
	c = personalClaims(kid, "ed25519:"+pubkey, time.Now())
	if bindIP {
		c.CIDR = []string{ip}
	}
	token, err = signToken(priv, c)
	if err != nil {
		return "", Claims{}, err.Error()
	}
	return token, c, ""
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
	if p.limited(w, keysLimit, clientIP(r)) {
		return
	}
	var req struct {
		Pubkey string
		BindIP bool `json:"bind_ip"` // also bind the requester's address, so its UDP station counts as this token's
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	tok, c, msg := mintPersonal(clientIP(r), req.Pubkey, req.BindIP)
	switch msg {
	case "":
	case "personal tokens not enabled":
		http.Error(w, msg, http.StatusNotImplemented)
		return
	case "pubkey must be a base64url ed25519 public key":
		http.Error(w, msg, http.StatusBadRequest)
		return
	default: // a signing failure is ours, not the client's
		http.Error(w, msg, http.StatusInternalServerError)
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
