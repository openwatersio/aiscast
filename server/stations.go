package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Per-station statistics: what a receiver operator wants to know about their own station (is it arriving,
// how many vessels, how far it hears), keyed by the event's station id. Bounded by the number of stations
// and the vessels each heard within vesselTTL.

type stationStat struct {
	Source     string
	First      time.Time
	Last       time.Time
	Events     int64
	Dups       int64 // events dropped as duplicates that this station also heard (someone else was first)
	Positions  int64
	MinLat     float64
	MinLon     float64
	MaxLat     float64
	MaxLon     float64
	vessels    map[uint32]time.Time
	vesselsMax int
	hourly     [24]int64 // events per clock hour, rolling; feeds the earned feeder tier
	hourlyAt   int64     // unix hour of hourly[hourlyAt%24]
}

// bump counts an event in its clock-hour bucket; out-of-order times are fine, too-old ones are ignored.
func (st *stationStat) bump(t time.Time) {
	h := t.Unix() / 3600
	switch {
	case h > st.hourlyAt:
		for i := st.hourlyAt + 1; i <= h && i-st.hourlyAt <= 24; i++ {
			st.hourly[i%24] = 0
		}
		st.hourlyAt = h
	case st.hourlyAt-h >= 24:
		return
	}
	st.hourly[h%24]++
}

// events24h sums events over the given station ids in the 24 clock hours ending now.
func (s *stationStats) events24h(ids []string, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := now.Unix() / 3600
	var n int64
	for _, id := range ids {
		st := s.m[id]
		if st == nil || h-st.hourlyAt >= 24 {
			continue
		}
		for i := int64(0); i < 24; i++ {
			if hh := h - i; hh <= st.hourlyAt && st.hourlyAt-hh < 24 {
				n += st.hourly[hh%24]
			}
		}
	}
	return n
}

type stationStats struct {
	mu sync.Mutex
	m  map[string]*stationStat
}

func newStationStats() *stationStats { return &stationStats{m: map[string]*stationStat{}} }

func (s *stationStats) get(station, source string, now time.Time) *stationStat {
	st := s.m[station]
	if st == nil {
		st = &stationStat{Source: source, First: now, MinLat: 91, MinLon: 181, MaxLat: -91, MaxLon: -181, vessels: map[uint32]time.Time{}}
		s.m[station] = st
	}
	return st
}

func (s *stationStats) event(ev *Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.get(ev.Station, ev.Source, ev.Time)
	st.Last = ev.Time
	st.Events++
	st.bump(ev.Time)
	st.vessels[ev.MMSI] = ev.Time
	if len(st.vessels) > st.vesselsMax {
		st.vesselsMax = len(st.vessels)
	}
	if ev.HasPos && isPositionType(ev.Type) {
		st.Positions++
		st.MinLat, st.MaxLat = min(st.MinLat, ev.Lat), max(st.MaxLat, ev.Lat)
		st.MinLon, st.MaxLon = min(st.MinLon, ev.Lon), max(st.MaxLon, ev.Lon)
	}
}

func (s *stationStats) dup(station, source string, mmsi uint32, now time.Time) {
	s.mu.Lock()
	st := s.get(station, source, now)
	st.Last = now // still heard, just beaten to it
	st.Dups++
	st.vessels[mmsi] = now // the station did hear the vessel; per-source vessel counts must not depend on who was first
	s.mu.Unlock()
}

// vesselsBySource returns, per source, how many distinct vessels its stations heard within the window and how
// many of those no other source heard.
func (s *stationStats) vesselsBySource() map[string][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := map[string]map[uint32]struct{}{}
	heardBy := map[uint32]int{}
	for _, st := range s.m {
		set := sets[st.Source]
		if set == nil {
			set = map[uint32]struct{}{}
			sets[st.Source] = set
		}
		for m := range st.vessels {
			if _, ok := set[m]; !ok {
				set[m] = struct{}{}
				heardBy[m]++
			}
		}
	}
	out := map[string][2]int{}
	for src, set := range sets {
		excl := 0
		for m := range set {
			if heardBy[m] == 1 {
				excl++
			}
		}
		out[src] = [2]int{len(set), excl}
	}
	return out
}

// sweep forgets vessels a station has not heard since cutoff.
func (s *stationStats) sweep(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		for m, t := range st.vessels {
			if t.Before(cutoff) {
				delete(st.vessels, m)
			}
		}
	}
}

func isPositionType(t string) bool {
	switch t {
	case "PositionReport", "StandardClassBPositionReport", "ExtendedClassBPositionReport", "LongRangeAisBroadcastMessage", "StandardSearchAndRescueAircraftReport":
		return true
	}
	return false
}

type stationRow struct {
	Station   string      `json:"station"`
	Source    string      `json:"source"`
	Events    int64       `json:"events"`
	Dups      int64       `json:"duplicates"`
	Vessels   int         `json:"vessels"` // distinct MMSIs heard within the last 30 min
	Positions int64       `json:"positions"`
	FirstSeen time.Time   `json:"first_seen"`
	LastSeen  time.Time   `json:"last_seen"`
	LastAgeS  int64       `json:"last_age_s"`
	BBox      *[4]float64 `json:"bbox,omitempty"` // minLat, minLon, maxLat, maxLon of positions heard
}

func (s *stationStats) rows(now time.Time) []stationRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]stationRow, 0, len(s.m))
	for id, st := range s.m {
		r := stationRow{Station: id, Source: st.Source, Events: st.Events, Dups: st.Dups, Vessels: len(st.vessels), Positions: st.Positions,
			FirstSeen: st.First.UTC(), LastSeen: st.Last.UTC(), LastAgeS: int64(now.Sub(st.Last).Seconds())}
		if st.Positions > 0 {
			r.BBox = &[4]float64{st.MinLat, st.MinLon, st.MaxLat, st.MaxLon}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Station < rows[j].Station })
	return rows
}

// serveStations: GET /v1/stations → every station heard since boot; GET /v1/stations/{id} → that station with
// the vessels it last updated as GeoJSON. Volunteer UDP stations appear as keyed hashes, never addresses.
func (p *Pipeline) serveStations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	now := time.Now()
	id := strings.TrimPrefix(r.URL.Path, "/v1/stations")
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		json.NewEncoder(w).Encode(p.stations.rows(now))
		return
	}
	for _, row := range p.stations.rows(now) {
		if row.Station != id {
			continue
		}
		features := []map[string]any{}
		p.vmu.RLock()
		for mmsi, v := range p.vessels {
			if v.HasPos && v.Station == id {
				features = append(features, v.feature(mmsi))
			}
		}
		p.vmu.RUnlock()
		json.NewEncoder(w).Encode(map[string]any{"station": row, "vessels": map[string]any{"type": "FeatureCollection", "features": features}})
		return
	}
	http.Error(w, `{"error":"unknown station"}`, http.StatusNotFound)
}
