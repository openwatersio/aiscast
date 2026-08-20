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
	if len(silent) > 0 {
		http.Error(w, strings.Join(silent, "; "), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// serveMetrics writes Prometheus text format by hand; no client library needed for a dozen series.
func (p *Pipeline) serveMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	counter := func(name, help string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	counter("hub_events_total", "decoded, deduplicated AIS messages", p.stats.events.Load())
	counter("hub_duplicates_total", "messages dropped as duplicates", p.stats.dup.Load())
	counter("hub_parse_errors_total", "unparseable input lines", p.stats.parseErr.Load())
	counter("hub_decode_failures_total", "sentences that did not decode to an AIS message", p.stats.decodeFail.Load())
	counter("hub_client_drops_total", "events dropped because a client queue was full", p.stats.clientDrops.Load())
	counter("hub_archive_drops_total", "receptions dropped because the archive queue was full", p.arch.drops.Load())
	counter("hub_ratelimited_total", "requests rejected by rate limits", p.stats.rateLimited.Load())

	p.vmu.RLock()
	nv := len(p.vessels)
	p.vmu.RUnlock()
	p.smu.RLock()
	ns := len(p.subs)
	p.smu.RUnlock()
	fmt.Fprintf(w, "# TYPE hub_vessels gauge\nhub_vessels %d\n# TYPE hub_clients gauge\nhub_clients %d\n", nv, ns)

	fmt.Fprintf(w, "# HELP hub_source_last_age_seconds seconds since the last event from each source\n# TYPE hub_source_last_age_seconds gauge\n")
	var sources []string
	p.lastBySource.Range(func(k, _ any) bool { sources = append(sources, k.(string)); return true })
	sort.Strings(sources)
	for _, s := range sources {
		fmt.Fprintf(w, "hub_source_last_age_seconds{source=%q} %.0f\n", s, p.sourceAge(s).Seconds())
	}
	fmt.Fprintf(w, "# HELP hub_source_events_total events per source\n# TYPE hub_source_events_total counter\n")
	p.stats.bySource.Range(func(k, v any) bool {
		fmt.Fprintf(w, "hub_source_events_total{source=%q} %d\n", k.(string), v.(*counterT).Load())
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
	wsConnectLimit = newLimiter(envInt("WS_CONNECTS_PER_MIN", 60)) // per IP; raise for load tests from one host
	receiveLimit   = newLimiter(600)                               // /v1/receive posts per feeder per minute (AIS-catcher posts ~4/min)
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// clientIP trusts Cloudflare's header when present (the proxied path), else the socket peer.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
