package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Usage/growth summary for dashboards and status pages; everything here is already tracked for /metrics
// and /v1/stations, just rolled up. Public, CORS open.

const stationActive = 5 * time.Minute // a station heard within this is "active"

type rateSample struct {
	mu     sync.Mutex
	at     time.Time
	events int64
	perSec float64
}

// sampleRate is called on the logStats tick; perSec is the event rate over the interval since the last call.
func (p *Pipeline) sampleRate(now time.Time) {
	p.rate.mu.Lock()
	defer p.rate.mu.Unlock()
	n := p.stats.events.Load()
	if dt := now.Sub(p.rate.at).Seconds(); !p.rate.at.IsZero() && dt > 0 {
		p.rate.perSec = float64(n-p.rate.events) / dt
	}
	p.rate.at, p.rate.events = now, n
}

// minuteRing counts events per clock minute over the last hour, plus a running total.
type minuteRing struct {
	mu    sync.Mutex
	at    int64 // unix minute of buckets[at%60]
	b     [60]int64
	total int64
}

func (r *minuteRing) add(now time.Time) {
	m := now.Unix() / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total++
	if m > r.at {
		for i := r.at + 1; i <= m && i-r.at <= 60; i++ {
			r.b[i%60] = 0
		}
		r.at = m
	}
	if r.at-m < 60 {
		r.b[m%60]++
	}
}

// lastHour returns the total and the count over the 60 minutes ending now.
func (r *minuteRing) lastHour(now time.Time) (total, hour int64) {
	m := now.Unix() / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.b {
		if mm := r.at - int64(i); mm > m-60 && mm <= m {
			hour += r.b[mm%60]
		}
	}
	return r.total, hour
}

type usageCounters struct {
	streams  minuteRing // WebSocket streams opened (/v0/stream, /v1/stream, /v1/nmea)
	requests minuteRing // HTTP API requests (everything but streams, /health, /metrics)
}

// countRequests wraps the mux so API requests are counted once, whatever handler serves them.
func (p *Pipeline) countRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/stream", "/v1/stream", "/v1/nmea", "/health", "/metrics":
		default:
			p.usage.requests.add(time.Now())
		}
		h.ServeHTTP(w, r)
	})
}

func (p *Pipeline) serveStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	now := time.Now()

	stations := map[string]any{}
	var active int
	bySource := map[string]int{}
	rows := p.stations.rows(now)
	for _, s := range rows {
		if now.Sub(s.LastSeen) < stationActive {
			active++
		}
		kind, _, _ := strings.Cut(s.Source, ":") // udp:<hash>, http:<id>, v1:<id>, kystverket, ...
		bySource[kind]++
	}
	stations["total"], stations["active"], stations["by_source"] = len(rows), active, bySource

	var withPos int
	byKind := map[string]int{}
	p.vmu.RLock()
	nv := len(p.vessels)
	for _, v := range p.vessels {
		if v.HasPos {
			withPos++
		}
		byKind[v.Kind]++
	}
	p.vmu.RUnlock()

	p.smu.RLock()
	streams := len(p.subs)
	p.smu.RUnlock()
	st, sh := p.usage.streams.lastHour(now)
	rt, rh := p.usage.requests.lastHour(now)
	clients := map[string]any{"streams": streams, "streams_opened": map[string]int64{"total": st, "last_hour": sh}, "requests": map[string]int64{"total": rt, "last_hour": rh}}

	sources := map[string]any{}
	vbs := p.stations.vesselsBySource()
	p.stats.bySource.Range(func(k, v any) bool {
		vs := vbs[k.(string)]
		sources[k.(string)] = map[string]any{"events": v.(*counterT).Load(), "last_age_s": int64(p.sourceAge(k.(string)).Seconds()),
			"vessels": vs[0], "vessels_exclusive": vs[1]} // distinct MMSIs heard in the last vesselTTL; exclusive = no other source heard them
		return true
	})

	p.rate.mu.Lock()
	perSec := p.rate.perSec
	p.rate.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]any{
		"time":     now.UTC(),
		"uptime_s": int64(now.Sub(bootTime).Seconds()),
		"stations": stations,
		"vessels":  map[string]any{"total": nv, "with_position": withPos, "by_kind": byKind},
		"events":   map[string]any{"total": p.stats.events.Load(), "duplicates": p.stats.dup.Load(), "per_second": perSec},
		"clients":  clients,
		"sources":  sources,
	})
}
