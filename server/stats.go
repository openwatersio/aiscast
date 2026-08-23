package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// ringState is the persisted part of an hourRing: per-hour counts over the last 7 days.
type ringState struct {
	At int64         // unix hour of B[At%len(B)]
	B  [7 * 24]int64 // per-hour counts
}

// hourRing counts per clock hour over the last 7 days.
type hourRing struct {
	mu sync.Mutex
	ringState
}

func (r *hourRing) add(now time.Time) {
	h := now.Unix() / 3600
	r.mu.Lock()
	defer r.mu.Unlock()
	n := int64(len(r.B))
	if h > r.At {
		for i := r.At + 1; i <= h && i-r.At <= n; i++ {
			r.B[i%n] = 0
		}
		r.At = h
	}
	if r.At-h < n {
		r.B[h%n]++
	}
}

// sum returns the count over the `hours` clock hours ending now (the current partial hour included).
func (r *hourRing) sum(now time.Time, hours int) (total int64) {
	h := now.Unix() / 3600
	n := int64(len(r.B))
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < hours && int64(i) < n; i++ {
		if hh := h - int64(i); hh <= r.At && r.At-hh < n {
			total += r.B[hh%n]
		}
	}
	return total
}

// windows is the JSON shape of one rolling counter.
func (r *hourRing) windows(now time.Time) map[string]int64 {
	return map[string]int64{"last_24h": r.sum(now, 24), "last_7d": r.sum(now, 7*24)}
}

func (r *hourRing) state() ringState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ringState
}

func (r *hourRing) restore(s ringState) {
	r.mu.Lock()
	r.ringState = s
	r.mu.Unlock()
}

// usageCounters are the rolling counters behind /v1/stats. Totals since start are not kept: deploys restart the
// server, so they would measure time since the last deploy. The rings persist in a JSON file next to the vessel snapshot.
type usageCounters struct {
	events   hourRing // deduplicated events
	dups     hourRing // duplicates dropped
	streams  hourRing // WebSocket streams opened (/v0/stream, /v1/stream, /v1/nmea)
	requests hourRing // HTTP API requests (everything but streams, /health, /metrics)

	smu     sync.Mutex
	sources map[string]*hourRing // events per source
}

func (u *usageCounters) source(name string) *hourRing {
	u.smu.Lock()
	defer u.smu.Unlock()
	if u.sources == nil {
		u.sources = map[string]*hourRing{}
	}
	r := u.sources[name]
	if r == nil {
		r = &hourRing{}
		u.sources[name] = r
	}
	return r
}

// sourceNames lists sources with a ring, dropping ones silent for the whole window so UDP station churn (ids
// change per boot without STATION_SALT) cannot grow the map, the usage file, or the /v1/stats response forever.
func (u *usageCounters) sourceNames(now time.Time) []string {
	h := now.Unix() / 3600
	u.smu.Lock()
	defer u.smu.Unlock()
	names := make([]string, 0, len(u.sources))
	for k, r := range u.sources {
		r.mu.Lock()
		stale := h-r.At >= int64(len(r.B))
		r.mu.Unlock()
		if stale {
			delete(u.sources, k)
			continue
		}
		names = append(names, k)
	}
	return names
}

// usageFile is the on-disk form: plain state, no locks.
type usageFile struct {
	Events, Dups, Streams, Requests ringState
	Sources                         map[string]ringState
}

// usagePath derives the usage file from the vessel snapshot path: vessels.json → vessels-usage.json.
func usagePath(snapshot string) string {
	return strings.TrimSuffix(snapshot, filepath.Ext(snapshot)) + "-usage.json"
}

func (p *Pipeline) saveUsage(path string) error {
	u := &p.usage
	out := usageFile{Events: u.events.state(), Dups: u.dups.state(), Streams: u.streams.state(), Requests: u.requests.state(), Sources: map[string]ringState{}}
	for _, k := range u.sourceNames(time.Now()) {
		out.Sources[k] = u.source(k).state()
	}
	b, err := json.Marshal(&out)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Pipeline) loadUsage(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var in usageFile
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	u := &p.usage
	u.events.restore(in.Events)
	u.dups.restore(in.Dups)
	u.streams.restore(in.Streams)
	u.requests.restore(in.Requests)
	for k, s := range in.Sources {
		u.source(k).restore(s)
	}
	return nil
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
	clients := map[string]any{"streams": streams, "streams_opened": p.usage.streams.windows(now), "requests": p.usage.requests.windows(now)}

	sources := map[string]any{}
	vbs := p.stations.vesselsBySource()
	names := map[string]bool{}
	for _, k := range p.usage.sourceNames(now) {
		names[k] = true
	}
	for k := range vbs { // a source that only ever duplicated others still hears vessels
		names[k] = true
	}
	for k := range names {
		vs := vbs[k]
		sources[k] = map[string]any{"events": p.usage.source(k).windows(now), "last_age_s": int64(p.sourceAge(k).Seconds()),
			"vessels": vs[0], "vessels_exclusive": vs[1]} // distinct MMSIs heard in the last vesselTTL; exclusive = no other source heard them
	}

	p.rate.mu.Lock()
	perSec := p.rate.perSec
	p.rate.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]any{
		"time":     now.UTC(),
		"stations": stations,
		"vessels":  map[string]any{"total": nv, "with_position": withPos, "by_kind": byKind},
		"events":   map[string]any{"per_second": perSec, "last_24h": p.usage.events.sum(now, 24), "last_7d": p.usage.events.sum(now, 7*24), "duplicates": p.usage.dups.windows(now)},
		"clients":  clients,
		"sources":  sources,
	})
}
