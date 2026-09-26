package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed openapi.json
var openapiJSON []byte

// serveOpenAPI serves the hand-written OpenAPI document; openapi_test.go keeps it in sync with the mux.
func serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Write(openapiJSON)
}

const upstreamSilence = 2 * time.Minute

// serveHealth: 503 when the loopback probe has received nothing for upstreamSilence, i.e. the stream itself
// delivers no events to subscribers (the aisstream failure mode was a healthy-looking empty service). A single
// silent source is not an outage while the others keep the stream flowing; per-source ages are in /metrics.
func (p *Pipeline) serveHealth(w http.ResponseWriter, r *http.Request) {
	if last := p.probeLast.Load(); last != 0 && time.Since(time.Unix(last, 0)) > upstreamSilence {
		http.Error(w, fmt.Sprintf("no events delivered to subscribers for %s", time.Since(time.Unix(last, 0)).Truncate(time.Second)), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// serveRobots keeps search engines off the API entirely: crawlers were finding /v1/vessels and
// /v1/receive in the code samples on openwaters.io/ais/ and reporting the 4xx answers as site errors.
func serveRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
}

// ---- rate limiting: fixed one-minute window per key. ponytail: token bucket if burst shape ever matters. ----

type limiter struct {
	mu   sync.Mutex
	max  int
	seen map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(perMinute int) *limiter { return &limiter{max: perMinute, seen: map[string]*window{}} }

func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 10000 { // bound memory under a flood of distinct keys
		for k, w := range l.seen {
			if now.Sub(w.start) > time.Minute {
				delete(l.seen, k)
			}
		}
	}
	w := l.seen[key]
	if w == nil || now.Sub(w.start) > time.Minute {
		l.seen[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.max
}

var (
	wsConnectLimit = newLimiter(envInt("WS_CONNECTS_PER_MIN", 20)) // per address; a working client connects once; raise for load tests
	httpLimit      = newLimiter(httpPerMinute)                     // per address, every HTTP GET endpoint
	udpLimit       = newLimiter(udpLinesPerMinute)                 // per source address
	publishLimit   = newLimiter(6000)                              // /v1/stream publish sentences per key per minute (a single receiver hears <75/s)
	receiveLimit   = newLimiter(600)                               // /v1/receive posts per feeder per minute (AIS-catcher posts ~4/min)
	mqttAdmitLimit = newLimiter(200)                               // MQTT upgrades per address per minute before CONNECT names the token: a flood ceiling, loose enough that one egress can carry many feeders
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// trustCFHeaders: TRUST_CF_HEADERS=1 when Cloudflare proxies the hostname; otherwise CF-Connecting-IP is
// client-controlled and must not drive rate limiting.
var trustCFHeaders = os.Getenv("TRUST_CF_HEADERS") == "1"

// clientIP is the rate-limit key: Cloudflare's header when trusted, else the last X-Forwarded-For hop when the
// peer is loopback (only Caddy and a local browser ever are), else the socket peer. Non-loopback peers never get
// to name their own address via the header.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); trustCFHeaders && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && net.ParseIP(host).IsLoopback() {
		hops := strings.Split(xff, ",")
		if last := strings.TrimSpace(hops[len(hops)-1]); net.ParseIP(last) != nil {
			return last
		}
	}
	return host
}

// connectKey keys the WebSocket connect limit by token sub when one verified, by address otherwise, so a
// neighbour behind the same egress cannot lock a token out of reconnecting.
func connectKey(c *Claims, r *http.Request) string {
	if c != nil {
		return c.Sub
	}
	return clientIP(r)
}

// rateLimited wraps a public HTTP handler with the per-address request limit. CORS stays open on the 429
// itself, so a cross-origin client sees the rate limit rather than an opaque CORS failure.
func (p *Pipeline) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if p.limited(w, httpLimit, clientIP(r)) {
			return
		}
		h(w, r)
	}
}

func (p *Pipeline) limited(w http.ResponseWriter, l *limiter, key string) bool {
	if l.allow(key) {
		return false
	}
	p.stats.rateLimited.Add(1)
	http.Error(w, "rate limited", http.StatusTooManyRequests)
	return true
}

// ---- vessel snapshot: restart without a blank map ----

func (p *Pipeline) saveSnapshot(path string) error {
	p.vmu.RLock()
	b, err := json.Marshal(p.vessels)
	p.vmu.RUnlock()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Pipeline) loadSnapshot(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var m map[uint32]*vessel
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-vesselTTL)
	p.vmu.Lock()
	for mmsi, v := range m {
		if v.Seen.After(cutoff) {
			p.vessels[mmsi] = v
		}
	}
	n := len(p.vessels)
	p.vmu.Unlock()
	return n, nil
}
