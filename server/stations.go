package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Per-station statistics: what a receiver operator wants to know about their own station (is it arriving,
// how many vessels, how far it hears), keyed by the event's station id. Live figures are kept here, bounded by
// the number of stations; vessel counts and uptime come from the station series in ClickHouse (stationseries.go).

// stationVesselTTL is the window unique vessels are counted over: a 30-minute snapshot swings with the time of
// day, too much for a number read once a month. Own-ship candidates are forgotten after it too.
const stationVesselTTL = 24 * time.Hour

type stationStat struct {
	Source    string
	First     time.Time
	Last      time.Time
	Events    int64
	Dups      int64 // events dropped as duplicates that this station also heard (someone else was first)
	Positions int64
	MinLat    float64
	MinLon    float64
	MaxLat    float64
	MaxLon    float64
	own       map[uint32]int64 // MMSIs this station sent as own ship (!AIVDO), unix seconds of the last
	ring      hourRing         // events per clock hour over 7 days; feeds /v1/stations and the earned feeder tier
}

// noteOwn records that the station sent mmsi as its own ship at t.
func (st *stationStat) noteOwn(mmsi uint32, t time.Time) {
	st.own[mmsi] = t.Unix()
}

// events24h sums events over the given station ids in the 24 clock hours ending now. A station not heard
// since a restart counts from its restored ring: its client reconnects before it publishes again, and the
// tier that connection gets holds until it closes.
func (s *stationStats) events24h(ids []string, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, id := range ids {
		if st := s.m[id]; st != nil {
			n += st.ring.sum(now, 24)
		} else if r, ok := s.restored[id]; ok {
			n += (&hourRing{ringState: r}).sum(now, 24)
		}
	}
	return n
}

type stationStats struct {
	mu          sync.Mutex
	m           map[string]*stationStat
	restored    map[string]ringState        // rings from the usage file, claimed when a station is heard again after a restart
	restoredOwn map[string]map[uint32]int64 // own-ship candidates read back from ClickHouse, claimed the same way
	ownPending  atomic.Bool                 // set while candidates are still to be read back, so no own vessel is decided on part of them
}

func newStationStats() *stationStats { return &stationStats{m: map[string]*stationStat{}} }

func (s *stationStats) get(station, source string, now time.Time) *stationStat {
	st := s.m[station]
	if st == nil {
		st = &stationStat{Source: source, First: now, MinLat: 91, MinLon: 181, MaxLat: -91, MaxLon: -181, own: map[uint32]int64{}}
		if r, ok := s.restored[station]; ok {
			st.ring.restore(r)
			delete(s.restored, station)
		}
		for m, at := range s.restoredOwn[station] {
			st.own[m] = max(st.own[m], at)
		}
		delete(s.restoredOwn, station)
		s.m[station] = st
	}
	return st
}

// rings returns every station's ring state (for the usage file), including restored ones not yet heard again.
func (s *stationStats) rings(now time.Time) map[string]ringState {
	h := now.Unix() / 3600
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]ringState{}
	for id, st := range s.m {
		if r := st.ring.state(); h-r.At < int64(len(r.B)) {
			out[id] = r
		}
	}
	for id, r := range s.restored {
		if h-r.At < int64(len(r.B)) {
			out[id] = r
		}
	}
	return out
}

// ownPendingMax is how long own vessels wait for their candidates from before the start: past it, ClickHouse is
// taken to be down, and they are decided on what has arrived since, as without it.
const ownPendingMax = 10 * time.Minute

// restoreOwn holds own-ship candidates read back at start, for stations to claim when next heard, so the rule that
// two candidates within ownSettle mean no own vessel holds across a restart. A station already heard takes its
// own at once.
func (s *stationStats) restoreOwn(cands map[string]map[uint32]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.ownPending.Store(false)
	s.restoredOwn = map[string]map[uint32]int64{}
	for id, own := range cands {
		if st := s.m[id]; st != nil {
			for m, at := range own {
				st.own[m] = max(st.own[m], at)
			}
			continue
		}
		s.restoredOwn[id] = own
	}
}

func (s *stationStats) restoreRings(m map[string]ringState) {
	s.mu.Lock()
	s.restored = m
	s.mu.Unlock()
}

func (s *stationStats) event(ev *Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.get(ev.Station, ev.Source, ev.Time)
	st.Last = ev.Time
	st.Events++
	st.ring.add(ev.Time)
	if ev.Own {
		st.noteOwn(ev.MMSI, ev.Time)
	}
	if ev.HasPos && isPositionType(ev.Type) {
		st.Positions++
		st.MinLat, st.MaxLat = min(st.MinLat, ev.Lat), max(st.MaxLat, ev.Lat)
		st.MinLon, st.MaxLon = min(st.MinLon, ev.Lon), max(st.MaxLon, ev.Lon)
	}
}

// dup records an event another station delivered first. The station did hear the vessel, as its rows in
// ClickHouse's series say.
func (s *stationStats) dup(ev *Event) {
	s.mu.Lock()
	st := s.get(ev.Station, ev.Source, ev.Time)
	st.Last = ev.Time // still heard, just beaten to it
	st.Dups++
	if ev.Own {
		st.noteOwn(ev.Packet.GetHeader().UserID, ev.Time)
	}
	s.mu.Unlock()
}

// sourceKind groups sources for public stats: every API client, UDP sender or HTTP feeder is one kind
// (v1, udp, http, mmsi); upstreams keep their name.
func sourceKind(source string) string {
	kind, _, _ := strings.Cut(source, ":")
	return kind
}

// volunteer: a station someone runs and feeds us directly, as opposed to a feed or an aggregate.
func volunteer(source string) bool {
	for _, pfx := range []string{"station:", "udp:", "mmsi:"} {
		if strings.HasPrefix(source, pfx) {
			return true
		}
	}
	return false
}

// ownShips returns, per station, the MMSIs it sent as own ship and when each was last sent.
func (s *stationStats) ownShips() map[string]map[uint32]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[uint32]int64{}
	for id, st := range s.m {
		if len(st.own) > 0 {
			out[id] = maps.Clone(st.own)
		}
	}
	return out
}

// sweep forgets own-ship candidates a station has not sent since cutoff.
func (s *stationStats) sweep(cutoff time.Time) {
	c := cutoff.Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		for m, t := range st.own {
			if t < c {
				delete(st.own, m)
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
	Station   string           `json:"station"`
	Source    string           `json:"source"`
	Events    map[string]int64 `json:"events"` // last_24h, last_7d
	Dups      int64            `json:"duplicates"`
	Vessels   int              `json:"vessels"`               // distinct MMSIs heard within the last 30 min, own ship aside
	Vessels24 int              `json:"vessels_24h"`           // distinct MMSIs heard within the last 24 h, own ship aside
	Exclusive int              `json:"vessels_exclusive_24h"` // of those, heard by no other station
	Uptime    *float64         `json:"uptime_7d"`             // share of hours with a reception over 7 days; null before the series has the station
	Positions int64            `json:"positions"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  time.Time        `json:"last_seen"`
	LastAgeS  int64            `json:"last_age_s"`
	BBox      *[4]float64      `json:"bbox,omitempty"` // minLat, minLon, maxLat, maxLon of positions heard

	Name     string `json:"name,omitempty"`      // see stationNames.decorate for the order names are chosen in
	NameFrom string `json:"name_from,omitempty"` // "operator" or "vessel"
	MMSI     uint32 `json:"mmsi,omitempty"`      // the station's own vessel, from its !AIVDO
	Near     string `json:"near,omitempty"`      // coverage label: the town or region nearest the traffic it hears
}

// rows is every station heard since the start, with the series' figures for those it has: vessels, uptime,
// its first hour, and its receptions in place of the counts since the start.
func (s *stationStats) rows(now time.Time, counts map[string]stationCount) []stationRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]stationRow, 0, len(s.m))
	for id, st := range s.m {
		r := stationRow{Station: id, Source: st.Source, Events: st.ring.windows(now), Dups: st.Dups, Positions: st.Positions,
			FirstSeen: st.First.UTC(), LastSeen: st.Last.UTC(), LastAgeS: int64(now.Sub(st.Last).Seconds())}
		if c, ok := counts[id]; ok {
			r.Vessels, r.Vessels24, r.Exclusive = c.live, c.day, c.unique
			// A station new since the totals' last read, or while they are unavailable, keeps the counts since the start
			// and has no uptime. positions counts first copies, as the live count does, and duplicates the rest.
			if c.totaled {
				r.Uptime = &c.uptime
				r.Positions, r.Dups = int64(c.firsts), int64(c.receptions-c.firsts)
				if c.first.Before(r.FirstSeen) {
					r.FirstSeen = c.first.UTC()
				}
			}
		}
		if st.Positions > 0 {
			r.BBox = &[4]float64{st.MinLat, st.MinLon, st.MaxLat, st.MaxLon}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Station < rows[j].Station })
	return rows
}

// stationRows is every station's row with its name fields filled in.
func (p *Pipeline) stationRows(now time.Time) []stationRow {
	counts, _ := p.rollups(now)
	rows := p.stations.rows(now, counts)
	for i := range rows {
		p.names.decorate(&rows[i])
	}
	return rows
}

// serveStations: GET /v1/stations → every station heard since boot; GET /v1/stations/{id} → that station with
// the vessels it last updated as GeoJSON. Volunteer UDP stations appear as keyed hashes, never addresses.
func (p *Pipeline) serveStations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	now := time.Now()
	id := strings.TrimPrefix(r.URL.Path, "/v1/stations")
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		json.NewEncoder(w).Encode(p.stationRows(now))
		return
	}
	for _, row := range p.stationRows(now) {
		if row.Station != id {
			continue
		}
		var features [][]byte
		attribution := map[string]string{}
		p.vmu.RLock()
		for mmsi, v := range p.vessels {
			if v.HasPos && v.Station == id {
				features = append(features, v.featureJSON(mmsi))
				noteAttribution(attribution, v.Source)
			}
		}
		p.vmu.RUnlock()
		json.NewEncoder(w).Encode(map[string]any{"station": row, "vessels": json.RawMessage(featureCollection(features, attribution, false))})
		return
	}
	w.WriteHeader(http.StatusNotFound) // not http.Error: that would override the JSON Content-Type set above
	json.NewEncoder(w).Encode(map[string]string{"error": "unknown station"})
}
