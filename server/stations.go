package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
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

// stationKeep is how long a silent station stays listed. With the station series, a restart lists every station
// it heard in that time; without it, only those heard since the start. It matches the client's sitemap, so a
// station page it links still answers.
const stationKeep = 30 * 24 * time.Hour

type stationStat struct {
	Source    string
	First     time.Time
	Last      time.Time
	Dups      int64 // events dropped as duplicates that this station also heard (someone else was first)
	Positions int64
	MinLat    float64 // extent of the positions heard since the start
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
	byNet       map[string]map[string]bool  // netKey → UDP station ids sending from that network, for the feeder tier
	restored    map[string]ringState        // rings from the usage file, claimed when a station is heard again after a restart
	restoredOwn map[string]map[uint32]int64 // own-ship candidates read back from ClickHouse, claimed the same way
	ownPending  atomic.Bool                 // set while candidates are still to be read back, so no own vessel is decided on part of them
}

func newStationStats() *stationStats {
	return &stationStats{m: map[string]*stationStat{}, byNet: map[string]map[string]bool{}}
}

// noteNet records that UDP station id sends from network key. Only a station already in m (one that delivered
// an event, accepted or a duplicate) is noted, so datagrams the pipeline rejects add nothing; the tier itself
// counts only accepted events. ponytail: never pruned, like m, which it is bounded by.
func (s *stationStats) noteNet(key, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" || s.m[id] == nil || s.byNet[key][id] {
		return
	}
	if s.byNet[key] == nil {
		s.byNet[key] = map[string]bool{}
	}
	s.byNet[key][id] = true
}

// netIDs is the UDP stations sending from network key. own is the station at the reader's exact address:
// byNet is not saved, so own is what keeps the tier through a restart (for IPv4, the whole network). The rest
// of an IPv6 /64 comes back with each station's next accepted event.
func (s *stationStats) netIDs(key, own string) []string {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	ids := slices.Collect(maps.Keys(s.byNet[key]))
	s.mu.Unlock()
	if !slices.Contains(ids, own) {
		ids = append(ids, own)
	}
	return ids
}

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
	if ev.Time.After(st.Last) { // a late copy must not age out a station that is still reporting
		st.Last = ev.Time
	}
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
	if ev.Time.After(st.Last) { // still heard, just beaten to it
		st.Last = ev.Time
	}
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
	for id, own := range s.restoredOwn { // a station listed from the series decides on what it sent before the start
		out[id] = maps.Clone(own)
	}
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
	Station   string            `json:"station"`
	Source    string            `json:"source"`
	Events    map[string]int64  `json:"events"` // last_24h, last_7d
	Dups      int64             `json:"duplicates"`
	Vessels   int               `json:"vessels"`               // distinct MMSIs heard within the last 30 min, own ship aside
	Vessels24 int               `json:"vessels_24h"`           // distinct MMSIs heard within the last 24 h, own ship aside
	Exclusive int               `json:"vessels_exclusive_24h"` // of those, heard by no other station
	Uptime    *float64          `json:"uptime_7d"`             // share of hours with a reception over 7 days; null before the series has the station
	Positions int64             `json:"positions"`
	FirstSeen time.Time         `json:"first_seen"`
	LastSeen  time.Time         `json:"last_seen"`
	LastAgeS  int64             `json:"last_age_s"`
	BBox      *[4]float64       `json:"bbox,omitempty"`  // minLat, minLon, maxLat, maxLon of positions heard
	Hours     []uint64          `json:"hours,omitempty"` // the station's own page only: receptions each hour of 7 days, oldest first
	Types     map[string][2]int `json:"types,omitempty"` // the station's own page only: 24 h vessels by ITU ship type or kind, as [vessels, unique]

	Name     string `json:"name,omitempty"`      // see stationNames.decorate for the order names are chosen in
	NameFrom string `json:"name_from,omitempty"` // "operator" or "vessel"
	MMSI     uint32 `json:"mmsi,omitempty"`      // the station's own vessel, from its !AIVDO
	Near     string `json:"near,omitempty"`      // coverage label: the town or region nearest the traffic it hears
}

// rows is every station heard within stationKeep: those heard since the start, and from the series those heard
// only before it, a UDP station once established. The series' figures fill in vessels, uptime, the first hour,
// and the receptions in place of the counts since the start.
func (s *stationStats) rows(now time.Time, counts map[string]stationCount) []stationRow {
	keep := now.Add(-stationKeep)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]stationRow, 0, max(len(s.m), len(counts)))
	for id, st := range s.m {
		last := st.Last
		if c := counts[id].last; c.After(last) { // the first copies after a start can be a backlog, older than the series' last
			last = c
		}
		if last.Before(keep) {
			continue
		}
		r := stationRow{Station: id, Source: st.Source, Events: st.ring.windows(now), Dups: st.Dups, Positions: st.Positions,
			FirstSeen: st.First.UTC(), LastSeen: last.UTC(), LastAgeS: int64(now.Sub(last).Seconds())}
		if st.MinLat <= st.MaxLat { // a station that has sent no position has no extent
			r.BBox = &[4]float64{st.MinLat, st.MinLon, st.MaxLat, st.MaxLon}
		}
		rows = append(rows, withSeries(r, counts[id]))
	}
	for id, c := range counts {
		if s.m[id] != nil || c.last.Before(keep) {
			continue // listed live above, or silent too long
		}
		// A UDP station, udp: or mmsi:, is listed only while the process that heard it runs. Anyone can name one with a
		// line to the UDP port or claim any MMSI with !AIVDO, and a re-keyed sender leaves a udp: row at every restart
		// until its next own-ship sentence, so nothing in the series tells a real one from these.
		if c.source == "udp" || c.source == "mmsi" || retiredStation(c.source) {
			continue
		}
		src := seriesSource(id, c.source)
		r := stationRow{Station: id, Source: src, Events: (&hourRing{ringState: s.restored[id]}).windows(now),
			FirstSeen: c.first.UTC(), LastSeen: c.last.UTC(), LastAgeS: int64(now.Sub(c.last).Seconds())}
		rows = append(rows, withSeries(r, c))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Station < rows[j].Station })
	return rows
}

// withSeries is r with the series' figures for its station. A station new since the totals' last read, or while they
// are unavailable, keeps the counts since the start and has no uptime. positions counts first copies, as the live
// count does, and duplicates the rest.
func withSeries(r stationRow, c stationCount) stationRow {
	r.Vessels, r.Vessels24, r.Exclusive = c.live, c.day, c.unique
	if c.totaled {
		r.Uptime = &c.uptime
		r.Positions, r.Dups = int64(c.firsts), int64(c.receptions-c.firsts)
		if c.first.Before(r.FirstSeen) {
			r.FirstSeen = c.first.UTC()
		}
	}
	return r
}

// seriesPending is whether the series has yet to answer for the stations heard before the start, given the figures
// read: nil while the server runs with ClickHouse, before the first read and once reads have failed for a while.
func (p *Pipeline) seriesPending(counts map[string]stationCount) bool {
	return counts == nil && p.series.expected.Load()
}

// retiredStation is a kind the series can still hold that live ingest no longer names a station by: a token's
// volunteer under the v1: and http: kinds it had before station:.
func retiredStation(kind string) bool { return kind == "v1" || kind == "http" }

// seriesSource is a station's source from the kind the series keeps: a volunteer's station is named by its source,
// and a feed's receivers by the feed.
func seriesSource(id, kind string) string {
	if strings.HasPrefix(id, kind+":") {
		return id
	}
	return kind
}

// stationRows is every station's row with its name fields filled in.
func (p *Pipeline) stationRows(now time.Time) []stationRow {
	counts, _ := p.rollups(now)
	return p.stationRowsFrom(now, counts)
}

// stationRowsFrom is stationRows from series figures the caller already holds.
func (p *Pipeline) stationRowsFrom(now time.Time, counts map[string]stationCount) []stationRow {
	rows := p.stations.rows(now, counts)
	for i := range rows {
		p.names.decorate(&rows[i])
	}
	return rows
}

// serveStations: GET /v1/stations → every station heard within stationKeep; GET /v1/stations/{id} → that station with
// the vessels it last updated as GeoJSON. Volunteer UDP stations appear as keyed hashes, never addresses.
func (p *Pipeline) serveStations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	now := time.Now()
	id := strings.TrimPrefix(r.URL.Path, "/v1/stations")
	id = strings.TrimPrefix(id, "/")
	counts, _ := p.rollups(now) // read once, so the list and the answer for a missing station agree
	if id == "" {
		json.NewEncoder(w).Encode(p.stationRowsFrom(now, counts))
		return
	}
	for _, row := range p.stationRowsFrom(now, counts) {
		if row.Station != id {
			continue
		}
		d := p.stationDetail(id, now)
		row.Hours, row.Types = d.hours, d.types
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
		out := map[string]any{"station": row, "vessels": json.RawMessage(featureCollection(features, attribution, false))}
		if len(d.credits) > 0 {
			out["attribution"] = d.credits // credits for the ship types in station.types, beside the vessels' own
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.seriesPending(counts) {
		// The series lists the stations heard before the start; until it answers, an absent station may be one of them.
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "station list not loaded yet"})
		return
	}
	w.WriteHeader(http.StatusNotFound) // not http.Error: that would override the JSON Content-Type set above
	json.NewEncoder(w).Encode(map[string]string{"error": "unknown station"})
}
