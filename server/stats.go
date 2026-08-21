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
	clients := len(p.subs)
	p.smu.RUnlock()

	sources := map[string]any{}
	p.stats.bySource.Range(func(k, v any) bool {
		sources[k.(string)] = map[string]any{"events": v.(*counterT).Load(), "last_age_s": int64(p.sourceAge(k.(string)).Seconds())}
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
