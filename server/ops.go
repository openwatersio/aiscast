package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const upstreamSilence = 2 * time.Minute

// serveHealth: 503 when any configured upstream has been silent longer than upstreamSilence (the aisstream
// failure mode was a healthy-looking empty service).
func (p *Pipeline) serveHealth(w http.ResponseWriter, r *http.Request) {
	var silent []string
	for _, s := range p.upstreams {
		if p.sourceAge(s) > upstreamSilence {
			silent = append(silent, fmt.Sprintf("%s silent %s", s, p.sourceAge(s).Truncate(time.Second)))
		}
	}
	if last := p.probeLast.Load(); last != 0 && time.Since(time.Unix(last, 0)) > upstreamSilence {
		silent = append(silent, fmt.Sprintf("no events delivered to subscribers for %s", time.Since(time.Unix(last, 0)).Truncate(time.Second)))
	}
	if len(silent) > 0 {
		http.Error(w, strings.Join(silent, "; "), http.StatusServiceUnavailable)
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

// serveMetrics writes Prometheus text format by hand; no client library needed for a dozen series.
func (p *Pipeline) serveMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	counter := func(name, help string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	counter("aiscast_events_total", "decoded, deduplicated AIS messages", p.stats.events.Load())
	counter("aiscast_duplicates_total", "messages dropped as duplicates", p.stats.dup.Load())
	counter("aiscast_parse_errors_total", "unparseable input lines", p.stats.parseErr.Load())
	counter("aiscast_replayed_total", "buffered sentences archived without live emit (TAG time older than 60 s)", p.stats.replayed.Load())
	counter("aiscast_decode_failures_total", "sentences that did not decode to an AIS message", p.stats.decodeFail.Load())
	counter("aiscast_client_drops_total", "events dropped because a client queue was full", p.stats.clientDrops.Load())
	counter("aiscast_ping_timeouts_total", "stream connections closed because the client stopped answering pings", p.stats.pingTimeouts.Load())
	counter("aiscast_archive_drops_total", "receptions dropped because the archive queue was full", p.arch.drops.Load())
	counter("aiscast_ratelimited_total", "requests rejected by rate limits", p.stats.rateLimited.Load())
	counter("aiscast_thinned_total", "events withheld from connections over their per-second rate", p.stats.thinned.Load())
	counter("aiscast_implausible_total", "low-trust positions dropped for implying an impossible speed", p.stats.implausible.Load())
	counter("aiscast_stale_total", "events withheld from the stream for being older than the vessel's newest", p.stats.stale.Load())
	counter("aiscast_uncorroborated_total", "low-trust events kept local because no trusted source has heard the vessel", p.stats.uncorroborated.Load())

	p.vmu.RLock()
	nv := len(p.vessels)
	p.vmu.RUnlock()
	p.smu.RLock()
	ns := len(p.subs)
	p.smu.RUnlock()
	fmt.Fprintf(w, "# TYPE aiscast_vessels gauge\naiscast_vessels %d\n# TYPE aiscast_clients gauge\naiscast_clients %d\n", nv, ns)

	fmt.Fprintf(w, "# HELP aiscast_source_last_age_seconds seconds since the last event from each source\n# TYPE aiscast_source_last_age_seconds gauge\n")
	var sources []string
	p.lastBySource.Range(func(k, _ any) bool { sources = append(sources, k.(string)); return true })
	sort.Strings(sources)
	for _, s := range sources {
		fmt.Fprintf(w, "aiscast_source_last_age_seconds{source=%q} %.0f\n", s, p.sourceAge(s).Seconds())
	}
	fmt.Fprintf(w, "# HELP aiscast_source_events_total events per source\n# TYPE aiscast_source_events_total counter\n")
	p.stats.bySource.Range(func(k, v any) bool {
		fmt.Fprintf(w, "aiscast_source_events_total{source=%q} %d\n", k.(string), v.(*counterT).Load())
		return true
	})
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

// rateLimited wraps a public HTTP handler with the per-address request limit.
func (p *Pipeline) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
