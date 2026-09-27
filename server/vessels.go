package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/BertoldVdb/go-ais"
)

// vesselTTL: a vessel unseen this long is dropped from the cache and the /v1/vessels snapshot.
const vesselTTL = 30 * time.Minute

// vessel is the per-MMSI state folded from every message seen. Fields keep AIS "not available" sentinels
// (COG 360, SOG 102.3, heading 511, nav status 15) so nothing here is ambiguous with a real zero.
type vessel struct {
	Name      string
	Lat, Lon  float64
	HasPos    bool
	Cog       float64
	Sog       float64
	Heading   uint16
	NavStatus uint8
	ShipType  uint8  // ITU ship/cargo type code; AtoN type for aids
	Kind      string // vessel | aton | base | sar
	Class     string // A or B from the position report types, the truthful class signal; empty until one is heard
	// Static particulars from type 5 and 24 messages, zero or empty until heard. Length and beam come
	// from the dimension fields (reference point to bow plus to stern, port plus starboard).
	IMO         uint32
	CallSign    string
	Destination string       // as typed by the crew: a port name, a UN/LOCODE, or nothing useful
	ETA         ais.FieldETA // month, day, hour, minute UTC as sent; AIS carries no year
	Draught     float64      // metres
	Length      uint16       // metres
	Beam        uint16       // metres
	Seen        time.Time
	Source      string
	Station     string
	MsgType     string
	TrustedAt   time.Time // last position from a source that is not low-trust
	PosAt       time.Time // time of the last position folded in (Seen also moves on static messages)
	StaticAt    time.Time // time of the last static folded in; gates rebuilt copies of the same broadcast

	// last events heard, replayed by snapshot subscriptions; unexported so the vessel snapshot file skips
	// them — nil after a restore, and then a reconstruction is synthesized instead.
	lastPos    *Event
	lastStatic *Event

	cell    cellKey                // index cell of the position, valid while indexed (index.go)
	indexed bool                   // filed in the spatial index; true exactly when HasPos
	feat    atomic.Pointer[[]byte] // encoded GeoJSON Feature, built on first read and cleared on every fold
}

func newVessel() *vessel {
	return &vessel{Cog: 360, Sog: 102.3, Heading: 511, NavStatus: 15, Kind: "vessel"}
}

// updateVessel folds the event into the per-MMSI cache and stamps the event with the cached name/position,
// so positionless messages (5, 24) can be bbox-routed and carry MetaData like aisstream.
func (p *Pipeline) updateVessel(ev *Event) {
	u := newVessel() // fields at sentinel = "not in this message"
	var hasPos, isStatic bool
	switch m := ev.Packet.(type) {
	case ais.PositionReport:
		u.Lat, u.Lon, hasPos, u.Class = float64(m.Latitude), float64(m.Longitude), true, "A"
		u.Cog, u.Sog, u.Heading, u.NavStatus = float64(m.Cog), float64(m.Sog), m.TrueHeading, m.NavigationalStatus
	case ais.StandardClassBPositionReport:
		u.Lat, u.Lon, hasPos, u.Class = float64(m.Latitude), float64(m.Longitude), true, "B"
		u.Cog, u.Sog, u.Heading = float64(m.Cog), float64(m.Sog), m.TrueHeading
	case ais.ExtendedClassBPositionReport:
		u.Lat, u.Lon, hasPos, u.Name, u.Class = float64(m.Latitude), float64(m.Longitude), true, m.Name, "B"
		u.Cog, u.Sog, u.Heading, u.ShipType = float64(m.Cog), float64(m.Sog), m.TrueHeading, m.Type
		u.Length, u.Beam = dimensions(m.Dimension)
	case ais.LongRangeAisBroadcastMessage:
		u.Lat, u.Lon, hasPos = float64(m.Latitude), float64(m.Longitude), true
		u.Cog, u.Sog, u.NavStatus = float64(m.Cog), float64(m.Sog), m.NavigationalStatus
		if m.Cog == 511 {
			u.Cog = 360
		}
		if m.Sog == 63 {
			u.Sog = 102.3
		}
	case ais.StandardSearchAndRescueAircraftReport:
		u.Lat, u.Lon, hasPos, u.Kind = float64(m.Latitude), float64(m.Longitude), true, "sar"
		u.Cog, u.Sog = float64(m.Cog), float64(m.Sog)
	case ais.BaseStationReport:
		u.Lat, u.Lon, hasPos, u.Kind = float64(m.Latitude), float64(m.Longitude), true, "base"
	case ais.AidsToNavigationReport:
		u.Lat, u.Lon, hasPos, u.Name, u.Kind, u.ShipType = float64(m.Latitude), float64(m.Longitude), true, m.Name, "aton", m.Type
	case ais.ShipStaticData:
		u.Name, u.ShipType, isStatic = m.Name, m.Type, true
		u.IMO, u.CallSign, u.Destination, u.Draught = m.ImoNumber, m.CallSign, m.Destination, float64(m.MaximumStaticDraught)
		u.Length, u.Beam = dimensions(m.Dimension)
		if m.Eta.Month >= 1 && m.Eta.Month <= 12 && m.Eta.Day >= 1 && m.Eta.Day <= 31 { // 0 is "not available"
			u.ETA = m.Eta
		}
	case ais.StaticDataReport:
		if m.ReportA.Valid {
			u.Name = m.ReportA.Name
		}
		if m.ReportB.Valid {
			u.ShipType, u.CallSign = m.ReportB.ShipType, m.ReportB.CallSign
			u.Length, u.Beam = dimensions(m.ReportB.Dimension)
		}
	}
	// 91/181 are the "not available" sentinels. (0,0) is a valid coordinate, so it passes the range test,
	// but it is a GPS default rather than a fix; it arrives steadily from the upstream aggregates. Treated
	// as "no position" rather than dropped, so the rest of the message still folds in.
	if hasPos && (math.Abs(u.Lat) > 90 || math.Abs(u.Lon) > 180 || (u.Lat == 0 && u.Lon == 0)) {
		hasPos = false
	}
	p.vmu.Lock()
	// The cache sweeps on the reception clock, not a wall-clock ticker. It decides which events are
	// stale and gives a static its vessel's position, and both reach the archive, so replay has to
	// sweep at the same points live did; recv is the clock replay reproduces. If ingest stops
	// entirely, nothing sweeps until it resumes, and /health is already failing by then.
	if !ev.RecvTime.IsZero() && !ev.RecvTime.Before(p.nextSweep) {
		p.sweepLocked(ev.RecvTime.Add(-vesselTTL))
		p.nextSweep = ev.RecvTime.Truncate(vesselSweep).Add(vesselSweep)
	}
	v := p.vessels[ev.MMSI]
	if v == nil {
		v = newVessel()
		p.vessels[ev.MMSI] = v
	}
	// Any event older than the vessel's newest (AISHub lags minutes behind VHF) must not drag the vessel
	// back along its track; only static fields fold in. Whole-second source stamps make ties and
	// sub-second skew meaningless.
	stale := v.HasPos && ev.Time.Before(v.Seen.Add(-time.Second))
	// Rebuilt events must moreover advance the vessel's clock, not merely match it: sources overlap
	// (BarentsWatch and aisstream re-serve what Kystverket already delivered raw), and a rebuilt copy of
	// the same transmission carries the same message time but never byte-matches the payload dedupe. AIS
	// transmits at 2 s minimum spacing, so "more than a second newer" separates copies from genuinely new
	// reports without any per-source rule, and a vessel every other source has gone silent on flows again
	// on its next transmission. Raw receptions keep the exact test instead — identical bytes — because an
	// equal-time raw event that survives dedupe is usually distinct data (1 Hz s:self reports, truncated
	// TAG stamps), and withholding a reception loses data where withholding a reconstruction loses nothing.
	if ev.rebuilt {
		if hasPos && v.HasPos && !ev.Time.After(v.PosAt.Add(time.Second)) {
			stale = true
		}
		if isStatic && !v.StaticAt.IsZero() && !ev.Time.After(v.StaticAt.Add(time.Second)) {
			stale = true
		}
	}
	ev.Stale = stale
	// A position implying an impossible speed from the vessel's last position is dropped whatever the
	// source: the aggregates carry bad positions of their own, and no vessel outruns implausibleKnots.
	// The jump must also clear implausibleJumpNM. Two sources reporting the same vessel a second apart
	// disagree by metres, and at that spacing 100 m alone implies 117 kn — so without a distance floor
	// the speed test flags ordinary cross-source jitter instead of teleports. The reception is archived
	// either way.
	if hasPos && !stale && v.HasPos {
		if dt := ev.Time.Sub(v.PosAt).Seconds(); dt >= 1 { // dt first: nm() is trig, and tied stamps are common
			if d := nm(v.Lat, v.Lon, u.Lat, u.Lon); d > implausibleJumpNM && d/(dt/3600) > implausibleKnots {
				ev.Implausible = true
				p.vmu.Unlock()
				return
			}
		}
	}
	v.feat.Store(nil) // everything below may change what the Feature shows
	if hasPos && !stale {
		v.Lat, v.Lon, v.HasPos, v.PosAt = u.Lat, u.Lon, true, ev.Time
		p.indexLocked(ev.MMSI, v)
		v.Cog, v.Sog, v.Heading = u.Cog, u.Sog, u.Heading // sentinels from a position report are real "unknown"s
		v.lastPos = ev
		if !ev.LowTrust {
			v.TrustedAt = ev.Time
		}
	}
	ev.Corroborated = !ev.LowTrust || ev.Time.Sub(v.TrustedAt) < corroborationWindow
	if u.NavStatus != 15 && !stale {
		v.NavStatus = u.NavStatus
	}
	if u.Name != "" {
		v.Name = u.Name
	}
	if u.ShipType != 0 {
		v.ShipType = u.ShipType
	}
	if u.Kind != "vessel" {
		v.Kind = u.Kind
	}
	if u.Class != "" {
		v.Class = u.Class
	}
	// Particulars are folded like the name: whenever present, stale or not, since they do not move.
	if u.IMO != 0 {
		v.IMO = u.IMO
	}
	if u.CallSign != "" {
		v.CallSign = u.CallSign
	}
	if u.Destination != "" {
		v.Destination = u.Destination
	}
	if u.ETA.Month != 0 {
		v.ETA = u.ETA
	}
	if u.Draught > 0 {
		v.Draught = u.Draught
	}
	if u.Length > 0 {
		v.Length = u.Length
	}
	if u.Beam > 0 {
		v.Beam = u.Beam
	}
	// Type 24 halves (name in A, ship type in B) are not retained: replaying only the latest half would
	// drop the other cached field, so those vessels get a synthesized type 5 carrying both instead.
	if isStatic { // names don't move, so a stale static is still worth keeping
		v.lastStatic = ev
		if !stale {
			v.StaticAt = ev.Time
		}
	}
	if !stale {
		v.Seen, v.Source, v.Station, v.MsgType = ev.Time, ev.Source, ev.Station, ev.Type
	}
	ev.Name, ev.Lat, ev.Lon, ev.HasPos = v.Name, v.Lat, v.Lon, v.HasPos
	if stale && hasPos { // the event still carries its own position; only the cache ignores it
		ev.Lat, ev.Lon = u.Lat, u.Lon
	}
	if p.dirty != nil {
		p.dirty[ev.MMSI] = struct{}{}
	}
	p.vmu.Unlock()
}

// markTrusted records that a trusted source heard the vessel's position at t (used when its copy was deduplicated).
func (p *Pipeline) markTrusted(mmsi uint32, t time.Time) {
	p.vmu.Lock()
	if v := p.vessels[mmsi]; v != nil && t.After(v.TrustedAt) {
		v.TrustedAt = t
	}
	p.vmu.Unlock()
}

// vesselSweep is how much reception time passes between sweeps of the vessel cache.
const vesselSweep = 30 * time.Second

// sweepLocked drops vessels unseen since cutoff; the caller holds vmu.
func (p *Pipeline) sweepLocked(cutoff time.Time) {
	for mmsi, v := range p.vessels {
		if v.Seen.Before(cutoff) {
			p.unindexLocked(mmsi, v)
			delete(p.vessels, mmsi)
		}
	}
}

func (p *Pipeline) vesselCount() int {
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	return len(p.vessels)
}

// vesselFeature is the GeoJSON Feature for one vessel. Fields are in alphabetical order so the keys come
// out sorted, as they do from a map. Pointers carry values whose zero is real (a heading of 0, nav
// status 0) and whose absence is the AIS "not available" sentinel.
type vesselFeature struct {
	Geometry   pointGeometry `json:"geometry"`
	ID         uint32        `json:"id"`
	Properties vesselProps   `json:"properties"`
	Type       string        `json:"type"`
}

type pointGeometry struct {
	Coordinates [2]float64 `json:"coordinates"`
	Type        string     `json:"type"`
}

type vesselProps struct {
	Beam        uint16   `json:"beam,omitempty"`
	CallSign    string   `json:"callsign,omitempty"`
	Cog         *float64 `json:"cog,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Draught     float64  `json:"draught,omitempty"`
	ETA         string   `json:"eta,omitempty"`
	FirstSeen   string   `json:"first_seen,omitempty"` // from the record, on /v1/vessels/{mmsi} only
	Flag        string   `json:"flag,omitempty"`
	Heading     *uint16  `json:"heading,omitempty"`
	IMO         uint32   `json:"imo,omitempty"`
	Kind        string   `json:"kind"`
	Length      uint16   `json:"length,omitempty"`
	MMSI        uint32   `json:"mmsi"`
	MsgType     string   `json:"msg_type"`
	Name        string   `json:"name,omitempty"`
	NavStatus   *uint8   `json:"nav_status,omitempty"`
	Seen        string   `json:"seen"`
	Sog         *float64 `json:"sog,omitempty"`
	Source      string   `json:"source"`
	Station     string   `json:"station"`
	Type        uint8    `json:"type,omitempty"`
}

func (v *vessel) feature(mmsi uint32) vesselFeature {
	props := vesselProps{
		MMSI: mmsi, Kind: v.Kind, Seen: v.Seen.UTC().Format(time.RFC3339),
		Source: v.Source, Station: v.Station, MsgType: v.MsgType,
		Name: v.Name, Type: v.ShipType, Flag: flagOf(mmsi), IMO: v.IMO, CallSign: v.CallSign,
		Destination: v.Destination, ETA: etaString(v.ETA), Draught: v.Draught, Length: v.Length, Beam: v.Beam,
	}
	if v.Cog < 360 {
		cog := v.Cog
		props.Cog = &cog
	}
	if v.Sog < 102.3 {
		sog := v.Sog
		props.Sog = &sog
	}
	if v.Heading < 511 {
		h := v.Heading
		props.Heading = &h
	}
	if v.NavStatus != 15 {
		ns := v.NavStatus
		props.NavStatus = &ns
	}
	return vesselFeature{
		Type: "Feature", ID: mmsi,
		Geometry:   pointGeometry{Type: "Point", Coordinates: [2]float64{v.Lon, v.Lat}},
		Properties: props,
	}
}

// featureJSON is the vessel's encoded Feature, built once per fold and shared by every request until the
// next one. Concurrent readers under the read lock may both build it; they store identical bytes. The
// returned slice is never modified.
func (v *vessel) featureJSON(mmsi uint32) []byte {
	if b := v.feat.Load(); b != nil {
		return *b
	}
	b, _ := json.Marshal(v.feature(mmsi))
	v.feat.Store(&b)
	return b
}

// recordLimit caps the vessels a /v1/vessels answer takes from the record for a box past the cache's 30
// minutes. The area cap bounds an answer from the cache, since a box holds only so many vessels at once.
// Over all time the same box holds far more.
var recordLimit = 500

// searchLimit caps a search answer. A person picks from a short list, and a short prefix matches thousands.
const searchLimit = 50

// serveVessels: GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon&mmsi=a,b&max_age= → GeoJSON of vessel
// positions. The filters, the token, and the area and MMSI caps are exactly those of /v1/stream. A box
// answers from the cache: vessels heard in the last 30 minutes, or further back when max_age asks. A
// followed MMSI answers with its last known position however old, unless max_age says otherwise. ?q=
// searches instead (serveVesselSearch).
func (p *Pipeline) serveVessels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	vals := r.URL.Query()
	if vals.Has("q") {
		p.serveVesselSearch(w, vals, cl)
		return
	}
	s, msg := parseSub(vals, cl, true)
	age, set, ageMsg := parseMaxAge(vals.Get("max_age"))
	if msg == "" {
		msg = ageMsg
	}
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	now := time.Now()
	var cutoff time.Time // zero: no age limit beyond what the cache holds
	if set {
		cutoff = ageCutoff(now, age)
	}
	deep := set && age > vesselTTL && (len(s.boxes) > 0 || s.everything)
	var emitted map[uint32]int // each vessel the cache answered, and its index in features
	if p.store != nil && (len(s.mmsi) > 0 || deep) {
		emitted = map[uint32]int{}
	}
	var features [][]byte
	attribution := map[string]string{}
	p.vmu.RLock()
	p.eachMatch(s, func(mmsi uint32, v *vessel) {
		if v.HasPos && !v.Seen.Before(cutoff) {
			features = append(features, v.featureJSON(mmsi))
			noteAttribution(attribution, v.Source)
			if emitted != nil {
				emitted[mmsi] = len(features) - 1
			}
		}
	})
	p.vmu.RUnlock()
	truncated := false
	if emitted != nil {
		recs, more, err := p.recordVessels(s, cutoff, deep, now)
		if err != nil {
			log.Printf("store: %v", err)
			http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
			return
		}
		taken := map[uint32]bool{}
		p.vmu.RLock()
		for _, rec := range recs {
			if taken[rec.mmsi] {
				continue
			}
			taken[rec.mmsi] = true
			f, source, ahead := p.newest(rec)
			if i, ok := emitted[rec.mmsi]; ok {
				if ahead {
					features[i] = f
					noteAttribution(attribution, source)
				}
				continue
			}
			// A vessel the cache holds with a position at least as new is the cache's to answer, and this
			// request did not ask about where it is or how old that position is.
			if ahead {
				features = append(features, f)
				noteAttribution(attribution, source)
			}
		}
		p.vmu.RUnlock()
		truncated = more
	}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Write(append(featureCollection(features, attribution, truncated), '\n'))
}

// recordVessels reads what the record holds for a request: every followed vessel, and, for a deep request,
// vessels last heard inside the boxes more than 30 minutes ago. more reports that the box query stopped at
// recordLimit.
func (p *Pipeline) recordVessels(s *v1Sub, cutoff time.Time, deep bool, now time.Time) (_ []record, more bool, _ error) {
	var recs []record
	if len(s.mmsi) > 0 {
		followed := make([]uint32, 0, len(s.mmsi))
		for m := range s.mmsi {
			followed = append(followed, m)
		}
		rs, err := p.store.find(recordQuery{mmsis: followed, since: cutoff, hasPos: true})
		if err != nil {
			return nil, false, err
		}
		recs = append(recs, rs...)
	}
	if deep {
		rs, err := p.store.find(recordQuery{boxes: s.boxes, since: cutoff, before: now.Add(-vesselTTL), hasPos: true, limit: recordLimit + 1})
		if err != nil {
			return nil, false, err
		}
		if len(rs) > recordLimit {
			rs, more = rs[:recordLimit], true
		}
		recs = append(recs, rs...)
	}
	return recs, more, nil
}

// newest is a vessel's newest state as a Feature, with the source it credits. The cache is usually ahead
// of the record by up to a second, and then its own encoded Feature is the answer. After a restart the
// record can be ahead instead, until the vessel next reports, because the snapshot is written every minute
// and the record every second. Then ahead is true and the Feature is the cache merged with the record.
// The caller holds vmu for reading.
func (p *Pipeline) newest(rec record) (feature []byte, source string, ahead bool) {
	v, cached := p.newestState(rec)
	if cached {
		return v.featureJSON(rec.mmsi), v.Source, false
	}
	b, _ := json.Marshal(v.feature(rec.mmsi))
	return b, v.Source, true
}

// newestState is newest's vessel state: the cached vessel itself when it is at least as new as the record,
// with cached true, else a merged copy. The caller holds vmu for reading and must not modify a cached one.
func (p *Pipeline) newestState(rec record) (v *vessel, cached bool) {
	c := p.vessels[rec.mmsi]
	if c != nil && !rec.v.Seen.After(c.Seen) && !rec.v.PosAt.After(c.PosAt) {
		return c, true
	}
	if c == nil {
		return rec.v, false
	}
	v = c.state()
	v.merge(rec.v)
	return v, false
}

// searchParams are the parameters a search accepts. Search refuses any other, so a filter added later
// never changes an answer an older client already received. The rest of /v1/vessels ignores unknown
// parameters, as it always has.
var searchParams = map[string]bool{"q": true, "bbox": true, "mmsi": true, "max_age": true, "key": true}

// serveVesselSearch: GET /v1/vessels?q= → vessels whose name starts with q, or whose MMSI does when q is
// digits, most recently heard first, from the record. bbox, mmsi, and max_age narrow it.
func (p *Pipeline) serveVesselSearch(w http.ResponseWriter, vals url.Values, cl *Claims) {
	for k := range vals {
		if !searchParams[k] {
			http.Error(w, "unknown parameter "+k, http.StatusBadRequest)
			return
		}
	}
	text := strings.TrimSpace(vals.Get("q"))
	if utf8.RuneCountInString(text) < 2 {
		http.Error(w, "q needs at least 2 characters", http.StatusBadRequest)
		return
	}
	s, msg := parseSub(vals, cl, false)
	age, set, ageMsg := parseMaxAge(vals.Get("max_age"))
	if msg == "" {
		msg = ageMsg
	}
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if p.store == nil {
		http.Error(w, errNoStore.Error(), http.StatusServiceUnavailable)
		return
	}
	// The record is ordered by the seen it stores, and the cache usually runs a second ahead of it, so read
	// past the cap and order by each vessel's newest seen before cutting.
	q := recordQuery{prefix: text, boxes: s.boxes, hasPos: true, limit: 2*searchLimit + 1}
	if len(s.mmsi) > 0 {
		q.mmsis = make([]uint32, 0, len(s.mmsi))
		for m := range s.mmsi {
			q.mmsis = append(q.mmsis, m)
		}
	}
	if set {
		q.since = ageCutoff(time.Now(), age)
	}
	recs, err := p.store.find(q)
	if err != nil {
		log.Printf("store: %v", err)
		http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
		return
	}
	type hit struct {
		feature []byte
		source  string
		seen    time.Time
	}
	hits := make([]hit, 0, len(recs))
	p.vmu.RLock()
	for _, rec := range recs {
		v, cached := p.newestState(rec)
		var f []byte
		if cached {
			f = v.featureJSON(rec.mmsi)
		} else {
			f, _ = json.Marshal(v.feature(rec.mmsi))
		}
		hits = append(hits, hit{f, v.Source, v.Seen})
	}
	p.vmu.RUnlock()
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].seen.After(hits[j].seen) })
	truncated := len(hits) > searchLimit
	if truncated {
		hits = hits[:searchLimit]
	}
	var features [][]byte
	attribution := map[string]string{}
	for _, h := range hits {
		features = append(features, h.feature)
		noteAttribution(attribution, h.source)
	}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Write(append(featureCollection(features, attribution, truncated), '\n'))
}

// serveVessel: GET /v1/vessels/{mmsi} → one vessel's last known state as a GeoJSON Feature, from the cache
// completed by the record, or from the record alone for a vessel the cache no longer holds. geometry is
// null for a vessel whose position was never heard. An unknown vessel is a 404.
func (p *Pipeline) serveVessel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if _, err := p.requestClaims(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	n, err := strconv.ParseUint(r.PathValue("mmsi"), 10, 32)
	if err != nil {
		http.Error(w, "mmsi must be a number", http.StatusBadRequest)
		return
	}
	mmsi := uint32(n)
	notFound := func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound) // not http.Error: that would override the JSON Content-Type
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown vessel"})
	}
	var cur *vessel
	p.vmu.RLock()
	if v := p.vessels[mmsi]; v != nil {
		cur = v.state()
	}
	p.vmu.RUnlock()
	var first time.Time
	if p.store != nil {
		rec, ok, err := p.store.get(mmsi)
		if err != nil {
			log.Printf("store: %v", err)
			http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
			return
		}
		if ok {
			first = rec.firstSeen
			if cur == nil {
				cur = rec.v
			} else {
				cur.merge(rec.v)
			}
		}
	}
	if cur == nil {
		notFound()
		return
	}
	// In the second before a vessel's first write the record has no row yet. That write will store the
	// vessel's seen as its first_seen, so answer with that now rather than leave the field out.
	if first.IsZero() && p.store != nil {
		first = cur.Seen
	}
	f := cur.feature(mmsi)
	if !first.IsZero() {
		f.Properties.FirstSeen = first.UTC().Format(time.RFC3339)
	}
	// The Feature with attribution beside it, and geometry null for a vessel whose position was never
	// heard. Its own type rather than a pointer in vesselFeature, which would cost every cached Feature an
	// allocation.
	out := struct {
		Attribution map[string]string `json:"attribution"`
		Geometry    *pointGeometry    `json:"geometry"`
		ID          uint32            `json:"id"`
		Properties  vesselProps       `json:"properties"`
		Type        string            `json:"type"`
	}{Attribution: map[string]string{}, ID: f.ID, Properties: f.Properties, Type: f.Type}
	if cur.HasPos {
		out.Geometry = &f.Geometry
	}
	noteAttribution(out.Attribution, cur.Source)
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(out)
}

// ageAll is max_age=all: no age limit at all.
const ageAll = time.Duration(math.MaxInt64)

// parseMaxAge reads max_age: whole seconds, a duration such as 90m or 24h, or all. set is false when the
// parameter is absent.
func parseMaxAge(s string) (age time.Duration, set bool, msg string) {
	if s == "" {
		return 0, false, ""
	}
	if s == "all" {
		return ageAll, true, ""
	}
	if n, err := strconv.ParseUint(s, 10, 32); err == nil && n > 0 {
		return time.Duration(n) * time.Second, true, ""
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, true, ""
	}
	return 0, false, "max_age=<seconds>, a duration such as 24h, or all"
}

// ageCutoff is the oldest seen time an age admits; zero, which admits everything, for ageAll.
func ageCutoff(now time.Time, age time.Duration) time.Time {
	if age == ageAll {
		return time.Time{}
	}
	return now.Add(-age)
}

// noteAttribution records the credit line for the source's kind (its name up to the first `:`).
func noteAttribution(m map[string]string, source string) {
	if k := sourceKind(source); m[k] == "" {
		m[k] = attributionOf(source)
	}
}

// featureCollection assembles a GeoJSON FeatureCollection from encoded Features. attribution is a foreign
// member (RFC 7946 §6.1): per source kind present in the features, the credit line the consumer must
// display. truncated, another, is present when a cap cut the list. Keys are in sorted order, as
// encoding/json writes a map's.
func featureCollection(features [][]byte, attribution map[string]string, truncated bool) []byte {
	a, _ := json.Marshal(attribution)
	n := len(a) + 48
	for _, f := range features {
		n += len(f) + 1
	}
	b := make([]byte, 0, n)
	b = append(b, `{"attribution":`...)
	b = append(b, a...)
	b = append(b, `,"features":[`...)
	for i, f := range features {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, f...)
	}
	b = append(b, ']')
	if truncated {
		b = append(b, `,"truncated":true`...)
	}
	return append(b, `,"type":"FeatureCollection"}`...)
}

// ---- snapshot subscriptions: replay the cache so a new client starts with the vessels already tracked ----

// snapshotEvents returns replayable events for every cached vessel matching s: the retained originals
// when available, else reconstructions from the folded state (vessels restored from disk).
func (p *Pipeline) snapshotEvents(s *v1Sub) []*Event {
	var out []*Event
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	p.eachMatch(s, func(mmsi uint32, v *vessel) {
		if v.lastPos != nil {
			out = append(out, v.lastPos)
		} else if v.HasPos {
			out = append(out, v.synthPos(mmsi))
		}
		if v.lastStatic != nil {
			out = append(out, v.lastStatic)
		} else if (v.Kind == "vessel" || v.Kind == "sar") && (v.Name != "" || v.ShipType != 0) {
			out = append(out, v.synthStatic(mmsi))
		}
	})
	return out
}

// synthPos reconstructs a position message from the folded state. The cache's n/a sentinels (COG 360,
// SOG 102.3, heading 511, nav status 15) are already the AIS encodings, so fields pass through.
func (v *vessel) synthPos(mmsi uint32) *Event {
	lat, lon := ais.FieldLatLonFine(v.Lat), ais.FieldLatLonFine(v.Lon)
	var pkt ais.Packet
	switch v.Kind {
	case "aton":
		pkt = ais.AidsToNavigationReport{Header: ais.Header{MessageID: 21, UserID: mmsi}, Valid: true,
			Type: v.ShipType, Name: v.Name, Latitude: lat, Longitude: lon, Timestamp: 60}
	case "base":
		t := v.PosAt.UTC()
		pkt = ais.BaseStationReport{Header: ais.Header{MessageID: 4, UserID: mmsi}, Valid: true,
			UtcYear: uint16(t.Year()), UtcMonth: uint8(t.Month()), UtcDay: uint8(t.Day()),
			UtcHour: uint8(t.Hour()), UtcMinute: uint8(t.Minute()), UtcSecond: uint8(t.Second()),
			Latitude: lat, Longitude: lon}
	case "sar":
		sog := uint16(1023) // type 9 SOG is whole knots, n/a 1023; the cache holds it unscaled
		if v.Sog != 102.3 && v.Sog < 1023 {
			sog = uint16(math.Round(v.Sog))
		}
		pkt = ais.StandardSearchAndRescueAircraftReport{Header: ais.Header{MessageID: 9, UserID: mmsi}, Valid: true,
			Altitude: 4095, Sog: sog, Latitude: lat, Longitude: lon, Cog: ais.Field10(v.Cog), Timestamp: uint8(v.PosAt.Second())}
	default:
		pkt = ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true,
			NavigationalStatus: v.NavStatus, RateOfTurn: -128, Sog: ais.Field10(v.Sog),
			Latitude: lat, Longitude: lon, Cog: ais.Field10(v.Cog), TrueHeading: v.Heading,
			Timestamp: uint8(v.PosAt.Second())}
	}
	return v.synthEvent(mmsi, pkt, v.PosAt)
}

func (v *vessel) synthStatic(mmsi uint32) *Event {
	return v.synthEvent(mmsi, ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true,
		Name: v.Name, Type: v.ShipType}, v.Seen)
}

func (v *vessel) synthEvent(mmsi uint32, pkt ais.Packet, t time.Time) *Event {
	return &Event{Time: t, Source: v.Source, Station: v.Station, Packet: pkt, Type: typeName(pkt),
		MMSI: mmsi, Name: v.Name, Lat: v.Lat, Lon: v.Lon, HasPos: v.HasPos, Synthesized: true}
}

// dimensions turns the AIS reference-point distances into overall length and beam, 0 when not sent.
func dimensions(d ais.FieldDimension) (length, beam uint16) {
	return d.A + d.B, uint16(d.C) + uint16(d.D)
}

// etaString renders an ETA as "MM-DD HH:MM" UTC, "MM-DD" when the time is not available, "" when unset.
// AIS carries no year; the reader takes the next occurrence.
func etaString(e ais.FieldETA) string {
	if e.Month < 1 || e.Month > 12 || e.Day < 1 || e.Day > 31 {
		return ""
	}
	if e.Hour > 23 || e.Minute > 59 {
		return fmt.Sprintf("%02d-%02d", e.Month, e.Day)
	}
	return fmt.Sprintf("%02d-%02d %02d:%02d", e.Month, e.Day, e.Hour, e.Minute)
}

// nm is the great-circle distance in nautical miles.
func nm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 3440.065 // earth radius, nm
	φ1, φ2 := lat1*math.Pi/180, lat2*math.Pi/180
	dφ, dλ := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dφ/2)*math.Sin(dφ/2) + math.Cos(φ1)*math.Cos(φ2)*math.Sin(dλ/2)*math.Sin(dλ/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
