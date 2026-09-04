package main

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// Delivery delay per source kind for /v1/stats: broadcast to arrival in the pipeline. A position report
// carries only the UTC second it was fixed, so the event's canonical time (the source's stamp when it has
// one, else our receipt) picks the minute. Delays of 55 s or more alias back into the minute; the archive
// cross-check (analysis/archive-delay.py) shows that bites only on the slowest tail of the aggregators.
// Observed before dedupe, so a late copy of a message another source delivered first still counts
// against the slow source.

const delaySamples = 512 // ponytail: last N events per kind, not a time window; seconds for busy sources, minutes for a lone feeder

type delayRing struct {
	mu   sync.Mutex
	buf  [delaySamples]float32
	n, i int
}

func (r *delayRing) add(d float64) {
	r.mu.Lock()
	r.buf[r.i] = float32(d)
	r.i = (r.i + 1) % delaySamples
	if r.n < delaySamples {
		r.n++
	}
	r.mu.Unlock()
}

// percentiles returns p50 and p99 in seconds, rounded to 0.1, or nil with no samples.
func (r *delayRing) percentiles() map[string]any {
	r.mu.Lock()
	s := make([]float32, r.n)
	copy(s, r.buf[:r.n])
	r.mu.Unlock()
	if len(s) == 0 {
		return nil
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	round := func(v float32) float64 { return math.Round(float64(v)*10) / 10 }
	return map[string]any{"n": len(s), "p50": round(s[len(s)/2]), "p99": round(s[len(s)*99/100])}
}

type delayStats struct {
	mu sync.Mutex
	by map[string]*delayRing
}

func (d *delayStats) ring(kind string) *delayRing {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.by == nil {
		d.by = map[string]*delayRing{}
	}
	r := d.by[kind]
	if r == nil {
		r = &delayRing{}
		d.by[kind] = r
	}
	return r
}

func (d *delayStats) observe(ev *Event, now time.Time) {
	if sec, ok := txSecond(ev.Packet); ok {
		d.ring(sourceKind(ev.Source)).add(broadcastDelay(sec, ev.Time, now))
	}
}

func (d *delayStats) snapshot(kind string) map[string]any {
	d.mu.Lock()
	r := d.by[kind]
	d.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.percentiles()
}

// txSecond is the UTC second the position was fixed; 60 and above mean unavailable or a special condition.
func txSecond(pkt ais.Packet) (uint8, bool) {
	var s uint8 = 60
	switch m := pkt.(type) {
	case ais.PositionReport:
		s = m.Timestamp
	case ais.StandardClassBPositionReport:
		s = m.Timestamp
	case ais.ExtendedClassBPositionReport:
		s = m.Timestamp
	}
	return s, s < 60
}

// broadcastDelay is seconds from the broadcast to now, with t (the best known time near the broadcast)
// choosing the minute. A stamp up to 5 s ahead of t is clock skew, anything further is the previous minute.
func broadcastDelay(sec uint8, t, now time.Time) float64 {
	b := t.Truncate(time.Minute).Add(time.Duration(sec) * time.Second)
	if b.After(t.Add(5 * time.Second)) {
		b = b.Add(-time.Minute)
	}
	return now.Sub(b).Seconds()
}
