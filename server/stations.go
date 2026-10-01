package main

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// Per-station statistics: what a receiver operator wants to know about their own station (is it arriving,
// how many vessels, how far it hears), keyed by the event's station id. Bounded by the number of stations
// and the vessels each heard within stationVesselTTL.

// stationVesselTTL is how long a station remembers a vessel it heard. Unique vessels are counted over it:
// a 30-minute snapshot swings with the time of day, too much for a number read once a month.
const stationVesselTTL = 24 * time.Hour

// heard is a station's memory of one vessel: when it last heard it, and the last position it heard.
type heard struct {
	t        int64 // unix seconds
	lat, lon float32
	pos      bool
}

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
	vessels   map[uint32]heard
	own       map[uint32]int64 // MMSIs this station sent as own ship (!AIVDO), unix seconds of the last
	ring      hourRing         // events per clock hour over 7 days; feeds /v1/stations and the earned feeder tier
}

// note records that the station heard mmsi at t, at lat/lon when pos.
func (st *stationStat) note(mmsi uint32, t time.Time, lat, lon float64, pos, own bool) {
	h := st.vessels[mmsi]
	h.t = t.Unix()
	if pos {
		h.lat, h.lon, h.pos = float32(lat), float32(lon), true
	}
	st.vessels[mmsi] = h
	if own {
		st.own[mmsi] = h.t
	}
}

// events24h sums events over the given station ids in the 24 clock hours ending now.
func (s *stationStats) events24h(ids []string, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, id := range ids {
		if st := s.m[id]; st != nil {
			n += st.ring.sum(now, 24)
		}
	}
	return n
}

type stationStats struct {
	mu        sync.Mutex
	m         map[string]*stationStat
	restored  map[string]ringState     // rings from the usage file, claimed when a station is heard again after a restart
	restoredV map[string]stationMemory // vessel maps saved at shutdown, claimed the same way

	excl   map[string]int // unique vessels per station, cached: computing it walks every station's vessels
	exclAt time.Time
}

func newStationStats() *stationStats { return &stationStats{m: map[string]*stationStat{}} }

func (s *stationStats) get(station, source string, now time.Time) *stationStat {
	st := s.m[station]
	if st == nil {
		st = &stationStat{Source: source, First: now, MinLat: 91, MinLon: 181, MaxLat: -91, MaxLon: -181, vessels: map[uint32]heard{}, own: map[uint32]int64{}}
		if r, ok := s.restored[station]; ok {
			st.ring.restore(r)
			delete(s.restored, station)
		}
		if m, ok := s.restoredV[station]; ok {
			m.restoreInto(st)
			delete(s.restoredV, station)
		}
		s.m[station] = st
		s.excl = nil // a cached count would leave the new station out until it expired
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
	pos := ev.HasPos && isPositionType(ev.Type)
	st.note(ev.MMSI, ev.Time, ev.Lat, ev.Lon, pos, ev.Own)
	if pos {
		st.Positions++
		st.MinLat, st.MaxLat = min(st.MinLat, ev.Lat), max(st.MaxLat, ev.Lat)
		st.MinLon, st.MaxLon = min(st.MinLon, ev.Lon), max(st.MaxLon, ev.Lon)
	}
}

// dup records an event another station delivered first. The station did hear the vessel, so it counts
// toward the station's vessels: per-station and per-source vessel counts must not depend on who was first.
func (s *stationStats) dup(ev *Event) {
	lat, lon, pos := packetPos(ev.Packet)
	s.mu.Lock()
	st := s.get(ev.Station, ev.Source, ev.Time)
	st.Last = ev.Time // still heard, just beaten to it
	st.Dups++
	st.note(ev.Packet.GetHeader().UserID, ev.Time, lat, lon, pos, ev.Own)
	s.mu.Unlock()
}

// packetPos is a position report's own position, for a duplicate that never reaches the vessel fold.
func packetPos(pkt ais.Packet) (lat, lon float64, ok bool) {
	switch m := pkt.(type) {
	case ais.PositionReport:
		lat, lon = float64(m.Latitude), float64(m.Longitude)
	case ais.StandardClassBPositionReport:
		lat, lon = float64(m.Latitude), float64(m.Longitude)
	case ais.ExtendedClassBPositionReport:
		lat, lon = float64(m.Latitude), float64(m.Longitude)
	case ais.LongRangeAisBroadcastMessage:
		lat, lon = float64(m.Latitude), float64(m.Longitude)
	case ais.StandardSearchAndRescueAircraftReport:
		lat, lon = float64(m.Latitude), float64(m.Longitude)
	default:
		return 0, 0, false
	}
	if math.Abs(lat) > 90 || math.Abs(lon) > 180 || lat == 0 && lon == 0 { // the "not available" sentinels and the GPS default, as foldOf
		return 0, 0, false
	}
	return lat, lon, true
}

// sourceKind groups sources for public stats: every API client, UDP sender or HTTP feeder is one kind
// (v1, udp, http, mmsi); upstreams keep their name.
func sourceKind(source string) string {
	kind, _, _ := strings.Cut(source, ":")
	return kind
}

// vesselsBySource returns, per source kind, how many distinct vessels its stations heard within vesselTTL and
// how many of those no other kind heard.
func (s *stationStats) vesselsBySource(now time.Time) map[string][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-vesselTTL).Unix()
	sets := map[string]map[uint32]struct{}{}
	heardBy := map[uint32]int{}
	for _, st := range s.m {
		kind := sourceKind(st.Source)
		set := sets[kind]
		if set == nil {
			set = map[uint32]struct{}{}
			sets[kind] = set
		}
		for m, h := range st.vessels {
			if h.t < cutoff {
				continue
			}
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

// baseStation is the station a TAG s: row belongs to: station:ed25519:X/n2k and station:ed25519:X/self are
// one receiver with station:ed25519:X, so they do not compete with it for unique vessels.
func baseStation(id string) string {
	b, _, _ := strings.Cut(id, "/")
	return b
}

// exclusive counts, per station, the vessels it heard in the last stationVesselTTL that no other station
// heard. Rows sharing a base station count as one station. Own-ship MMSIs are left out on both sides: a
// boat reporting itself is not reception, and would otherwise always add one unique vessel. Called with
// s.mu held; cached for a minute.
func (s *stationStats) exclusive(now time.Time) map[string]int {
	if s.excl != nil && now.Sub(s.exclAt) < time.Minute {
		return s.excl
	}
	cutoff := now.Add(-stationVesselTTL).Unix()
	own := map[string]map[uint32]bool{} // per base station
	for id, st := range s.m {
		b := baseStation(id)
		for m := range st.own {
			if own[b] == nil {
				own[b] = map[uint32]bool{}
			}
			own[b][m] = true
		}
	}
	type hearers struct{ last, n int32 } // the last base counted for the vessel, so two rows of one base count once
	heardBy := make(map[uint32]hearers, 1<<17)
	ids := make([]string, 0, len(s.m))
	for id := range s.m {
		ids = append(ids, id)
	}
	sort.Strings(ids) // rows of one base sort together, which the last-base check relies on
	bases := map[string]int32{}
	for _, id := range ids {
		b := baseStation(id)
		bi, ok := bases[b]
		if !ok {
			bi = int32(len(bases)) + 1 // 0 is "no base yet"
			bases[b] = bi
		}
		for m, h := range s.m[id].vessels {
			if h.t < cutoff || own[b][m] {
				continue
			}
			if hb := heardBy[m]; hb.last != bi {
				heardBy[m] = hearers{bi, hb.n + 1}
			}
		}
	}
	out := make(map[string]int, len(s.m))
	for id, st := range s.m {
		b := baseStation(id)
		n := 0
		for m, h := range st.vessels {
			if h.t >= cutoff && !own[b][m] && heardBy[m].n == 1 {
				n++
			}
		}
		out[id] = n
	}
	s.excl, s.exclAt = out, now
	return out
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

// ownShips returns, per base station, the MMSIs its rows sent as own ship and when each was last sent.
func (s *stationStats) ownShips() map[string]map[uint32]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[uint32]int64{}
	for id, st := range s.m {
		b := baseStation(id)
		for m, t := range st.own {
			if out[b] == nil {
				out[b] = map[uint32]int64{}
			}
			out[b][m] = max(out[b][m], t)
		}
	}
	return out
}

// coveragePoints returns, per volunteer base station, the median position of the vessels it heard in the last
// stationVesselTTL, its own ships aside, for those with at least labelMinPoints of them.
func (s *stationStats) coveragePoints(now time.Time) map[string][2]float64 {
	cutoff := now.Add(-stationVesselTTL).Unix()
	s.mu.Lock()
	own := map[string]map[uint32]bool{}
	for id, st := range s.m {
		b := baseStation(id)
		for m := range st.own {
			if own[b] == nil {
				own[b] = map[uint32]bool{}
			}
			own[b][m] = true
		}
	}
	by := map[string]map[uint32]heard{} // per base station, each vessel's newest position across its rows
	for id, st := range s.m {
		if !volunteer(st.Source) {
			continue
		}
		b := baseStation(id)
		latest := by[b]
		if latest == nil {
			latest = map[uint32]heard{}
			by[b] = latest
		}
		for m, h := range st.vessels {
			if h.pos && h.t >= cutoff && !own[b][m] && h.t > latest[m].t {
				latest[m] = h
			}
		}
	}
	s.mu.Unlock()
	out := map[string][2]float64{}
	for b, latest := range by {
		if len(latest) < labelMinPoints {
			continue
		}
		lats, lons := make([]float64, 0, len(latest)), make([]float64, 0, len(latest))
		for _, h := range latest {
			lats, lons = append(lats, float64(h.lat)), append(lons, float64(h.lon))
		}
		lat, lon := medianPoint(lats, lons)
		out[b] = [2]float64{lat, lon}
	}
	return out
}

// sweep forgets vessels a station has not heard since cutoff.
func (s *stationStats) sweep(cutoff time.Time) {
	c := cutoff.Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		for m, h := range st.vessels {
			if h.t < c {
				delete(st.vessels, m)
			}
		}
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
	Vessels   int              `json:"vessels"`               // distinct MMSIs heard within the last 30 min
	Vessels24 int              `json:"vessels_24h"`           // distinct MMSIs heard within the last 24 h
	Exclusive int              `json:"vessels_exclusive_24h"` // of those, heard by no other station (own ship aside)
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

func (s *stationStats) rows(now time.Time) []stationRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]stationRow, 0, len(s.m))
	excl := s.exclusive(now)
	live, day := now.Add(-vesselTTL).Unix(), now.Add(-stationVesselTTL).Unix()
	for id, st := range s.m {
		r := stationRow{Station: id, Source: st.Source, Events: st.ring.windows(now), Dups: st.Dups, Positions: st.Positions,
			Exclusive: excl[id], FirstSeen: st.First.UTC(), LastSeen: st.Last.UTC(), LastAgeS: int64(now.Sub(st.Last).Seconds())}
		for _, h := range st.vessels {
			if h.t >= day {
				r.Vessels24++
				if h.t >= live {
					r.Vessels++
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
	rows := p.stations.rows(now)
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

// stationMemory is one station's vessel map on disk: [mmsi, unix seconds] per vessel, with the last position
// heard appended as lat, lon when there is one, and the own-ship MMSIs as [mmsi, unix seconds].
type stationMemory struct {
	V   [][]float64 `json:"v"`
	Own [][2]int64  `json:"own,omitempty"`
}

func (m stationMemory) restoreInto(st *stationStat) {
	for _, v := range m.V {
		if len(v) < 2 {
			continue
		}
		h := heard{t: int64(v[1])}
		if len(v) == 4 {
			h.lat, h.lon, h.pos = float32(v[2]), float32(v[3]), true
		}
		st.vessels[uint32(v[0])] = h
	}
	for _, o := range m.Own {
		st.own[uint32(o[0])] = o[1]
	}
}

// saveVessels writes every station's vessel map, so a deploy does not empty the 24-hour counts. Written at
// shutdown only: at about 150,000 entries it is too big to rewrite every minute with the usage file, and a
// crash costs a day of rebuilding, not data.
func (s *stationStats) saveVessels(path string, now time.Time) error {
	cutoff := now.Add(-stationVesselTTL).Unix()
	round := func(x float32) float64 { return math.Round(float64(x)*1e5) / 1e5 }
	out := map[string]stationMemory{}
	s.mu.Lock()
	for id, m := range s.restoredV { // not heard again since the last restart: keep what is still inside the window
		var keep stationMemory
		for _, v := range m.V {
			if len(v) >= 2 && int64(v[1]) >= cutoff {
				keep.V = append(keep.V, v)
			}
		}
		for _, o := range m.Own {
			if o[1] >= cutoff {
				keep.Own = append(keep.Own, o)
			}
		}
		if len(keep.V) > 0 {
			out[id] = keep
		}
	}
	for id, st := range s.m {
		var m stationMemory
		for mmsi, h := range st.vessels {
			if h.t < cutoff {
				continue
			}
			if h.pos {
				m.V = append(m.V, []float64{float64(mmsi), float64(h.t), round(h.lat), round(h.lon)})
			} else {
				m.V = append(m.V, []float64{float64(mmsi), float64(h.t)})
			}
		}
		for mmsi, t := range st.own {
			m.Own = append(m.Own, [2]int64{int64(mmsi), t})
		}
		if len(m.V) > 0 {
			out[id] = m
		}
	}
	s.mu.Unlock()
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *stationStats) loadVessels(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var in map[string]stationMemory
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	s.mu.Lock()
	s.restoredV = in
	s.mu.Unlock()
	return nil
}
