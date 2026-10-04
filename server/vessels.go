package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
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
	// from the dimension fields (reference point to bow plus to stern, port plus starboard), and Dim keeps
	// those four distances, where the reference point is the AIS antenna.
	IMO         uint32
	CallSign    string
	Destination string             // as typed by the crew: a port name, a UN/LOCODE, or nothing useful
	ETA         ais.FieldETA       // month, day, hour, minute UTC as sent; AIS carries no year
	Draught     float64            // metres
	Length      uint16             // metres
	Beam        uint16             // metres
	Dim         ais.FieldDimension // metres from the antenna to bow (A), stern (B), port (C), starboard (D)
	Seen        time.Time
	Source      string
	Station     string
	MsgType     string
	TrustedAt   time.Time // last position from a source that is not low-trust
	PosAt       time.Time // time of the last position folded in (Seen also moves on static messages)
	StaticAt    time.Time // time of the last static folded in; gates rebuilt copies of the same broadcast

	// last events heard, replayed by snapshot subscriptions. The record does not keep them, so they are nil
	// for a vessel restored from it, and a reconstruction is synthesized instead.
	lastPos    *Event
	lastStatic *Event
	recent     []recentPos // accepted positions of the last few minutes, for matching rebuilt copies (tracks.go)
	moved      anchor      // where it was last moving, for whether a report is moving (tracks.go)

	cell    cellKey                // index cell of the position, valid while indexed (index.go)
	indexed bool                   // filed in the spatial index; true exactly when HasPos
	feat    atomic.Pointer[[]byte] // encoded GeoJSON Feature, built on first read and cleared on every fold
}

func newVessel() *vessel {
	return &vessel{Cog: 360, Sog: 102.3, Heading: 511, NavStatus: 15, Kind: "vessel"}
}

// foldOf extracts what a message contributes to a vessel: fields at their sentinel mean "not in this
// message". updateVessel folds it into the cache, and a source can use it to ask whether a message
// would change what the cache already holds.
func foldOf(pkt ais.Packet) (u *vessel, hasPos, isStatic bool) {
	u = newVessel()
	switch m := pkt.(type) {
	case ais.PositionReport:
		u.Lat, u.Lon, hasPos, u.Class = float64(m.Latitude), float64(m.Longitude), true, "A"
		u.Cog, u.Sog, u.Heading, u.NavStatus = float64(m.Cog), float64(m.Sog), m.TrueHeading, m.NavigationalStatus
	case ais.StandardClassBPositionReport:
		u.Lat, u.Lon, hasPos, u.Class = float64(m.Latitude), float64(m.Longitude), true, "B"
		u.Cog, u.Sog, u.Heading = float64(m.Cog), float64(m.Sog), m.TrueHeading
	case ais.ExtendedClassBPositionReport:
		u.Lat, u.Lon, hasPos, u.Name, u.Class = float64(m.Latitude), float64(m.Longitude), true, m.Name, "B"
		u.Cog, u.Sog, u.Heading, u.ShipType = float64(m.Cog), float64(m.Sog), m.TrueHeading, m.Type
		u.Length, u.Beam, u.Dim = dimensions(m.Dimension)
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
		u.Length, u.Beam, u.Dim = dimensions(m.Dimension)
		if m.Eta.Month >= 1 && m.Eta.Month <= 12 && m.Eta.Day >= 1 && m.Eta.Day <= 31 { // 0 is "not available"
			u.ETA = m.Eta
		}
	case ais.StaticDataReport:
		if m.ReportA.Valid {
			u.Name = m.ReportA.Name
		}
		if m.ReportB.Valid {
			u.ShipType, u.CallSign = m.ReportB.ShipType, m.ReportB.CallSign
			u.Length, u.Beam, u.Dim = dimensions(m.ReportB.Dimension)
		}
	}
	// 91/181 are the "not available" sentinels. (0,0) is a valid coordinate, so it passes the range test,
	// but it is a GPS default rather than a fix; it arrives steadily from the upstream aggregates. Treated
	// as "no position" rather than dropped, so the rest of the message still folds in.
	if hasPos && (math.Abs(u.Lat) > 90 || math.Abs(u.Lon) > 180 || (u.Lat == 0 && u.Lon == 0)) {
		hasPos = false
	}
	return u, hasPos, isStatic
}

// staleFor reports whether an event at t would be stale for v: folded for its static fields, withheld
// from the stream. updateVessel decides with it, and a source that repeats itself asks it before
// sending, so the two cannot disagree.
func (v *vessel) staleFor(t time.Time, hasPos, isStatic, rebuilt bool) bool {
	// Any event older than the vessel's newest (AISHub lags minutes behind VHF) must not drag the vessel
	// back along its track; only static fields fold in. Whole-second source stamps make ties and
	// sub-second skew meaningless.
	stale := v.HasPos && t.Before(v.Seen.Add(-time.Second))
	// Rebuilt events must moreover advance the vessel's clock, not merely match it: sources overlap
	// (BarentsWatch and aisstream re-serve what Kystverket already delivered raw), and a rebuilt copy of
	// the same transmission carries the same message time but never byte-matches the payload dedupe. AIS
	// transmits at 2 s minimum spacing, so "more than a second newer" separates copies from genuinely new
	// reports without any per-source rule, and a vessel every other source has gone silent on flows again
	// on its next transmission. Raw receptions keep the exact test instead — identical bytes — because an
	// equal-time raw event that survives dedupe is usually distinct data (1 Hz s:self reports, truncated
	// TAG stamps), and withholding a reception loses data where withholding a reconstruction loses nothing.
	if rebuilt {
		if hasPos && v.HasPos && !t.After(v.PosAt.Add(time.Second)) {
			stale = true
		}
		if isStatic && !v.StaticAt.IsZero() && !t.After(v.StaticAt.Add(time.Second)) {
			stale = true
		}
	}
	return stale
}

// positionIsNew reports whether a position at t is news to the vessel cache: inside the cache's window
// and not stale by the rule updateVessel applies (staleFor), or for a vessel the cache does not hold. A
// source that repeats what it already sent (AISHub's snapshots) asks this instead of keeping its own
// state, and the cache survives a restart.
func (p *Pipeline) positionIsNew(mmsi uint32, t, now time.Time) bool {
	if t.Before(now.Add(-vesselTTL)) {
		return false // older than anything the cache would keep
	}
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	v := p.vessels[mmsi]
	return v == nil || !v.staleFor(t, true, false, true) // AISHub rows are rebuilt reports
}

// staticIsNew reports whether a static message at t is news to the vessel cache: inside the cache's
// window, more than a second newer than the last static folded in (staleFor's rule for rebuilt statics),
// and carrying a particular that the vessel does not already have. A static older than the vessel's last
// position still counts: it is withheld from the stream but folds its particulars into the cache. The
// time gate is what stops an aggregate that flips a vessel between two stations' versions (X, Y, X)
// under one timestamp from sending the same broadcast twice. pkt must be decoded as the pipeline decodes
// it, so its fields compare with what the cache stored.
func (p *Pipeline) staticIsNew(mmsi uint32, t, now time.Time, pkt ais.Packet) bool {
	if t.Before(now.Add(-vesselTTL)) {
		return false
	}
	u, _, _ := foldOf(pkt)
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	v := p.vessels[mmsi]
	if v == nil {
		return true
	}
	return (v.StaticAt.IsZero() || t.After(v.StaticAt.Add(time.Second))) && changesParticulars(v, u)
}

// changesParticulars reports whether folding u would change any of v's static particulars.
func changesParticulars(v, u *vessel) bool {
	return u.Name != "" && u.Name != v.Name ||
		u.ShipType != 0 && u.ShipType != v.ShipType ||
		u.IMO != 0 && u.IMO != v.IMO ||
		u.CallSign != "" && u.CallSign != v.CallSign ||
		u.Destination != "" && u.Destination != v.Destination ||
		u.ETA.Month != 0 && u.ETA != v.ETA ||
		u.Draught > 0 && u.Draught != v.Draught ||
		u.Length > 0 && (u.Dim.A != v.Dim.A || u.Dim.B != v.Dim.B) ||
		u.Beam > 0 && (u.Dim.C != v.Dim.C || u.Dim.D != v.Dim.D)
}

// asDecoded returns pkt as the pipeline sees it after re-encoding, which is what the cache folds in: the
// 6-bit text alphabet and the fixed-point fields can change a value on the way through.
func (p *Pipeline) asDecoded(pkt ais.Packet) ais.Packet {
	if payload := p.codec.EncodePacket(pkt); payload != nil {
		if d := p.codec.DecodePacket(payload); d != nil {
			return d
		}
	}
	return pkt
}

// updateVessel folds the event into the per-MMSI cache and stamps the event with the cached name/position,
// so positionless messages (5, 24) can be bbox-routed and carry MetaData like aisstream.
func (p *Pipeline) updateVessel(ev *Event) {
	u, hasPos, isStatic := foldOf(ev.Packet)
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
	hadPrev, prevLat, prevLon := v.HasPos, v.Lat, v.Lon // before this report moves it, for whether it is moving
	stale := v.staleFor(ev.Time, hasPos, isStatic, ev.rebuilt)
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
				p.noteFolded(ev, v, u, false, hadPrev, prevLat, prevLon)
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
		p.notePosition(ev.MMSI, ev.Time, u, ev.Source)
		if !ev.LowTrust {
			v.TrustedAt = ev.Time
		}
	}
	ev.Corroborated = !ev.LowTrust || ev.Time.Sub(v.TrustedAt) < corroborationWindow
	if hasPos {
		p.noteFolded(ev, v, u, stale, hadPrev, prevLat, prevLon)
	}
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
	// the offsets travel with the total they add up to, so a report with a length but no beam keeps the
	// port and starboard offsets as well as the beam
	if u.Length > 0 {
		v.Length, v.Dim.A, v.Dim.B = u.Length, u.Dim.A, u.Dim.B
	}
	if u.Beam > 0 {
		v.Beam, v.Dim.C, v.Dim.D = u.Beam, u.Dim.C, u.Dim.D
	}
	// Type 24 halves (name in A, ship type in B) are not retained: replaying only the latest half would
	// drop the other cached field, so those vessels get a synthesized type 5 carrying both instead.
	if isStatic { // names don't move, so a stale static is still worth keeping
		v.lastStatic = ev
		// A stale static folds in too, so it advances the gate: otherwise an aggregate flipping between
		// two versions under one old timestamp would pass the gate on every snapshot.
		if ev.Time.After(v.StaticAt) {
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

// noteFolded queues a position the fold judged, whatever it decided: an accepted position joins the vessel's
// recent ones, and a stale rebuilt copy at one of them is a copy of that transmission. A stale report that
// matches none is a real report that arrived late, which history keeps though the stream withholds it, but
// only as far as a live report would be believed. The fold never tests a stale report for an impossible jump,
// so it is tested here against the vessel's position nearest it in time. Late reports are believed only from
// the feeds the server pulls: a volunteer station's backlog never reaches the fold, so a station has no late
// reports to deliver, and anyone can run one, token or not, and stamp a report into any vessel's past. The
// caller holds vmu.
func (p *Pipeline) noteFolded(ev *Event, v *vessel, u *vessel, stale, hadPrev bool, prevLat, prevLon float64) {
	// The ring and the queue switch on together, so a position is remembered exactly when it is written, and a
	// stale copy only ever matches a transmission receptions holds. A copy of one folded before ClickHouse
	// connected, which was never written, matches nothing and is kept as the only copy, accepted, so
	// positions_1m has it; matching a transmission receptions lacks would leave it a copy of nothing.
	if !p.chOn.Load() {
		return
	}
	pt := newTrackPoint(ev.MMSI, ev.Time, u, ev.Source)
	pt.txAt, pt.txDisc, pt.recv, pt.station = ev.Time, discOf(ev.ID), ev.RecvTime, ev.Station
	var seed *[2]int32
	if hadPrev {
		seed = &[2]int32{int32(math.Round(prevLat * 600000)), int32(math.Round(prevLon * 600000))}
	}
	pt.clockBad = ev.RecvTime.Sub(ev.Time) >= clockBadAge
	// Only a report that enters positions_1m moves the anchor, or later reports would be judged against a place
	// positions_1m never holds.
	pt.still = v.moved.still(pt, seed, !stale && !ev.Implausible && !pt.clockBad)
	pt.uncorroborated = ev.LowTrust && !ev.Corroborated
	pt.implausible = ev.Implausible
	switch {
	case ev.Implausible, !stale:
	default:
		if ev.rebuilt {
			if r, ok := v.repeats(pt); ok {
				pt.txAt, pt.txDisc, pt.still, pt.dup = time.UnixMilli(r.ms), r.disc, r.still, true
				p.ch.rebuiltMatched.Add(1)
				break
			}
			p.ch.rebuiltLate.Add(1)
		}
		pt.implausible = volunteer(ev.Source) || v.jumps(pt)
		ev.unserved = pt.implausible // its dedupe copies inherit it; the stream's own flags stay as they were
	}
	// A transmission of its own takes a byte free in its millisecond and joins the recent ones its copies find it
	// among, an implausible one too, so a later report cannot take its byte and be hidden with it.
	if !pt.dup {
		v.freeDisc(&pt)
		v.remember(pt)
	}
	p.noteReception(pt)
}

// markTrusted records that a trusted source heard the vessel's position at t (used when its copy was deduplicated).
func (p *Pipeline) markTrusted(mmsi uint32, t time.Time) {
	p.vmu.Lock()
	if v := p.vessels[mmsi]; v != nil && t.After(v.TrustedAt) {
		v.TrustedAt = t
		if p.dirty != nil {
			p.dirty[mmsi] = struct{}{}
		}
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
	Near        string   `json:"near,omitempty"` // the place nearest the position, on searches only
	Seen        string   `json:"seen"`
	Sog         *float64 `json:"sog,omitempty"`
	Source      string   `json:"source"`
	Station     string   `json:"station"`
	ToBow       *uint16  `json:"to_bow,omitempty"`
	ToPort      *uint8   `json:"to_port,omitempty"`
	ToStarboard *uint8   `json:"to_starboard,omitempty"`
	ToStern     *uint16  `json:"to_stern,omitempty"`
	Type        uint8    `json:"type,omitempty"`
	// The merged enrichment document, its per-field provenance, and the contributing sources'
	// credits (particulars.go). On /v1/vessels/{mmsi} only.
	Particulars *particulars         `json:"particulars,omitempty"`
	Provenance  map[string]string    `json:"provenance,omitempty"`
	Sources     map[string]sourceRef `json:"sources,omitempty"`
}

func (v *vessel) feature(mmsi uint32) vesselFeature {
	props := vesselProps{
		MMSI: mmsi, Kind: v.Kind, Seen: v.Seen.UTC().Format(time.RFC3339),
		Source: v.Source, Station: v.Station, MsgType: v.MsgType,
		Name: v.Name, Type: v.ShipType, Flag: flagOf(mmsi), IMO: v.IMO, CallSign: v.CallSign,
		Destination: v.Destination, ETA: etaString(v.ETA), Draught: v.Draught, Length: v.Length, Beam: v.Beam,
	}
	if d := v.Dim; v.hasLengthOffsets() { // a copy, so the feature does not point into the live vessel
		props.ToBow, props.ToStern = &d.A, &d.B
	}
	if d := v.Dim; v.hasBeamOffsets() {
		props.ToPort, props.ToStarboard = &d.C, &d.D
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
// positions. The filters, the token, and the area and MMSI caps are exactly those of /v1/stream, and the
// age rules those of the tiles (ageRules). The cache answers for the last 30 minutes and the record for
// what it no longer holds. ?q= searches instead (serveVesselSearch).
func (p *Pipeline) serveVessels(w http.ResponseWriter, r *http.Request) {
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
	rules, rulesMsg := parseAgeRules(vals)
	if msg = cmp.Or(msg, rulesMsg); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	now := time.Now()
	deep := (rules.areaAge == 0 || rules.areaAge > vesselTTL) && (len(s.boxes) > 0 || s.everything)
	var emitted map[uint32]int // each vessel the cache answered, and its index in features
	if p.store != nil && (len(s.mmsi) > 0 || deep) {
		emitted = map[uint32]int{}
	}
	var features [][]byte
	attribution := map[string]string{}
	p.vmu.RLock()
	p.eachMatch(s, func(mmsi uint32, v *vessel) {
		if v.HasPos && rules.match(s.mmsi[mmsi], v, now) {
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
		recs, more, err := p.recordVessels(s, rules, deep, now)
		if errors.Is(err, errTooManyTerms) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
			i, ok := emitted[rec.mmsi]
			// A vessel the cache holds with a position the cache did not answer with is outside this request:
			// its newest position has left the boxes or the age rules leave it out.
			if c := p.vessels[rec.mmsi]; !ok && c != nil && c.HasPos {
				continue
			}
			f, source, merged := p.newest(rec)
			if ok && !merged {
				continue
			}
			if ok {
				features[i] = f
			} else {
				features = append(features, f)
			}
			noteAttribution(attribution, source)
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
func (p *Pipeline) recordVessels(s *v1Sub, rules *ageRules, deep bool, now time.Time) (_ []record, more bool, _ error) {
	var recs []record
	if len(s.mmsi) > 0 {
		followed := make([]uint32, 0, len(s.mmsi))
		for m := range s.mmsi {
			followed = append(followed, m)
		}
		age, vf := rules.rule(true)
		rs, err := p.store.find(recordQuery{mmsis: followed, since: since(now, age), hasPos: true, filter: vf, now: now})
		if err != nil {
			return nil, false, err
		}
		recs = append(recs, rs...)
	}
	if deep {
		age, vf := rules.rule(false)
		rs, err := p.store.find(recordQuery{boxes: s.boxes, since: since(now, age), before: now.Add(-vesselTTL), hasPos: true, limit: recordLimit + 1, filter: vf, now: now})
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

// newest is a vessel's newest state as a Feature, with the source it credits. When the record adds nothing
// to the cache, the cache's own encoded Feature is the answer. Otherwise merged is true and the Feature is
// the cache completed by the record. The caller holds vmu for reading.
func (p *Pipeline) newest(rec record) (feature []byte, source string, merged bool) {
	v, cached := p.newestState(rec)
	if cached {
		return v.featureJSON(rec.mmsi), v.Source, false
	}
	b, _ := json.Marshal(v.feature(rec.mmsi))
	return b, v.Source, true
}

// newestState is newest's vessel state: the cached vessel itself when the record adds nothing to it, with
// cached true, else the cache merged with the record, or the record alone for a vessel the cache does not
// hold. The caller holds vmu for reading and must not modify a cached one.
func (p *Pipeline) newestState(rec record) (v *vessel, cached bool) {
	c := p.vessels[rec.mmsi]
	if c == nil {
		return rec.v, false
	}
	v = c.state()
	if !v.merge(rec.v) {
		return c, true
	}
	return v, false
}

// searchParams are the parameters a search accepts. Search refuses any other, so a filter added later
// never changes an answer an older client already received. The rest of /v1/vessels ignores unknown
// parameters, as it always has.
var searchParams = map[string]bool{"q": true, "bbox": true, "mmsi": true, "max_age": true, "around": true, "key": true}

func init() {
	for k := range vesselFilterParams {
		searchParams[k] = true
	}
}

// serveVesselSearch: GET /v1/vessels?q= → vessels whose name starts with q, or whose MMSI does when q is
// digits, most recently heard first, from the record, each labeled with the place nearest it. bbox, mmsi, and
// max_age narrow it, and around=lat,lon orders it nearest first.
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
	vf, filterMsg := parseVesselFilter(vals, 0)
	around, aroundMsg := parseAround(vals.Get("around"))
	msg = cmp.Or(msg, ageMsg, filterMsg, aroundMsg)
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
	now := time.Now()
	q := recordQuery{prefix: text, boxes: s.boxes, hasPos: true, limit: 2*searchLimit + 1, filter: vf, now: now}
	if len(s.mmsi) > 0 {
		q.mmsis = make([]uint32, 0, len(s.mmsi))
		for m := range s.mmsi {
			q.mmsis = append(q.mmsis, m)
		}
	}
	if set {
		q.since = ageCutoff(now, age)
	}
	var recs []record
	var err error
	if around != nil {
		recs, err = p.nearestRecords(q, *around)
	} else {
		recs, err = p.store.find(q)
	}
	if errors.Is(err, errTooManyTerms) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("store: %v", err)
		http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
		return
	}
	type hit struct {
		feature vesselFeature
		source  string
		seen    time.Time
		dist    float64
	}
	hits := make([]hit, 0, len(recs))
	p.vmu.RLock()
	for _, rec := range recs {
		v, _ := p.newestState(rec)
		if !vf.match(v, now) { // the cache can be a report ahead of the row the filter passed
			continue
		}
		h := hit{feature: v.feature(rec.mmsi), source: v.Source, seen: v.Seen}
		if around != nil {
			h.dist = nm(around[0], around[1], v.Lat, v.Lon)
		}
		hits = append(hits, h)
	}
	p.vmu.RUnlock()
	// Ordered again here because the cache's position and seen can be a report ahead of the record's.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].seen.After(hits[j].seen)
	})
	truncated := len(hits) > searchLimit
	if truncated {
		hits = hits[:searchLimit]
	}
	var features [][]byte
	attribution := map[string]string{}
	for _, h := range hits {
		c := h.feature.Geometry.Coordinates
		h.feature.Properties.Near = nearLabel(c[1], c[0])
		f, _ := json.Marshal(h.feature)
		features = append(features, f)
		noteAttribution(attribution, h.source)
	}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Write(append(featureCollection(features, attribution, truncated), '\n'))
}

// serveVessel: GET /v1/vessels/{mmsi} → one vessel's last known state as a GeoJSON Feature, from the cache
// completed by the record, or from the record alone for a vessel the cache no longer holds, with its
// particulars from Wikidata when its IMO has an item and from the Coast Guard when it is a documented US
// vessel. geometry is null for a vessel whose position was never
// heard. An unknown vessel is a 404.
func (p *Pipeline) serveVessel(w http.ResponseWriter, r *http.Request) {
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
	fc := p.fccOf(mmsi)[mmsi]
	key := uscgKey{mmsi: mmsi, callsign: cur.CallSign, name: cur.Name}
	if fc != nil {
		key.official = fc.Official
	}
	e := enrichment{wd: p.wikidataOf(cur.IMO)[cur.IMO], cg: p.uscgOf(key)[mmsi],
		fd: p.fiskeridirOf(fdirKey{mmsi, cur.CallSign, cur.Name})[mmsi], fc: fc}
	// The Canadian register speaks for CA-flag vessels; an IMO another flag carries may have left it,
	// and the names must agree, as the other registries require, so a mistyped or copied IMO in AIS
	// static data never serves another registered ship's facts.
	if flagOf(mmsi) == "CA" {
		if tcv := p.tcOf(cur.IMO)[cur.IMO]; tcv != nil && namesAgree(cur.Name, tcv.Name) {
			e.tc = tcv
		}
		e.is = p.isedOf(mmsi)[mmsi]
	}
	f.Properties.Particulars, f.Properties.Provenance, f.Properties.Sources = mergeParticulars(e)
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

// vesselFilterParams are the filters /v1/vessels, its search, and the tiles share.
var vesselFilterParams = map[string]bool{"kind": true, "class": true, "type": true, "min_sog": true, "max_age_moving": true}

var vesselKinds = map[string]bool{"vessel": true, "aton": true, "base": true, "sar": true}

// vesselFilter narrows vessels by what they are and how they last reported. The zero value matches all.
type vesselFilter struct {
	kinds     map[string]bool
	class     string
	types     [][2]uint8
	minSog    float64
	hasMinSog bool          // min_sog=0 still asks for a known speed
	movingAge time.Duration // a vessel last heard under way longer ago than this is left out; 0: no limit
}

// parseVesselFilter reads the shared filters. movingAge is max_age_moving's default.
func parseVesselFilter(vals url.Values, movingAge time.Duration) (*vesselFilter, string) {
	f := &vesselFilter{movingAge: movingAge}
	if q := vals.Get("kind"); q != "" {
		f.kinds = map[string]bool{}
		for _, k := range strings.Split(q, ",") {
			if !vesselKinds[k] {
				return nil, "kind=vessel,aton,base,sar"
			}
			f.kinds[k] = true
		}
	}
	if f.class = vals.Get("class"); f.class != "" && f.class != "A" && f.class != "B" {
		return nil, "class=A or class=B"
	}
	if q := vals.Get("type"); q != "" {
		for _, r := range strings.Split(q, ",") {
			lo, hi, found := strings.Cut(r, "-")
			if !found {
				hi = lo
			}
			a, err1 := strconv.ParseUint(lo, 10, 8)
			b, err2 := strconv.ParseUint(hi, 10, 8)
			if err1 != nil || err2 != nil || a > b {
				return nil, "type=<code> or <from>-<to>, comma-separated, 0-255"
			}
			f.types = append(f.types, [2]uint8{uint8(a), uint8(b)})
		}
	}
	if q := vals.Get("min_sog"); q != "" {
		v, err := strconv.ParseFloat(q, 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, "min_sog=<knots>"
		}
		f.minSog, f.hasMinSog = v, true
	}
	if q := vals.Get("max_age_moving"); q != "" {
		age, _, msg := parseMaxAge(q)
		if msg != "" {
			return nil, "max_age_moving=<seconds>, a duration such as 2h, or all"
		}
		f.movingAge = age
		if age == ageAll {
			f.movingAge = 0
		}
	}
	return f, ""
}

// stationary: the vessel's last report says it was not going anywhere. Aids to navigation and base stations
// never move; a vessel counts when it was under a knot or reported itself moored, at anchor, or aground.
// A vessel with neither speed nor status known counts as moving.
func stationary(v *vessel) bool {
	return v.Kind == "aton" || v.Kind == "base" || v.Sog < 1 || v.NavStatus == 1 || v.NavStatus == 5 || v.NavStatus == 6
}

func (f *vesselFilter) match(v *vessel, now time.Time) bool {
	switch {
	case f.kinds != nil && !f.kinds[v.Kind],
		f.class != "" && v.Class != f.class,
		f.hasMinSog && !(v.Sog < 102.3 && v.Sog >= f.minSog),
		f.movingAge > 0 && now.Sub(v.Seen) > f.movingAge && !stationary(v):
		return false
	}
	if f.types == nil {
		return true
	}
	for _, t := range f.types {
		if v.ShipType >= t[0] && v.ShipType <= t[1] {
			return true
		}
	}
	return false
}

// where is match as SQL over the vessel record, so a limited query fills its limit with matching rows.
func (f *vesselFilter) where(now time.Time) (where []string, args []any) {
	if f.kinds != nil {
		ph := make([]string, 0, len(f.kinds))
		for k := range f.kinds {
			ph, args = append(ph, "?"), append(args, k)
		}
		where = append(where, "kind IN ("+strings.Join(ph, ",")+")")
	}
	if f.class != "" {
		where, args = append(where, "class = ?"), append(args, f.class)
	}
	if f.types != nil {
		ors := make([]string, len(f.types))
		for i, t := range f.types {
			ors[i], args = "ship_type BETWEEN ? AND ?", append(args, t[0], t[1])
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if f.hasMinSog {
		where, args = append(where, "sog >= ? AND sog < 102.3"), append(args, f.minSog)
	}
	if f.movingAge > 0 {
		where = append(where, "(seen >= ? OR kind IN ('aton', 'base') OR sog < 1 OR nav_status IN (1, 5, 6))")
		args = append(args, unixMs(now.Add(-f.movingAge)))
	}
	return where, args
}

// areaWindow is how far back an area answers. A vessel is usually still there when its receiver goes
// offline or it switches AIS off at its mooring, so an area shows its last known position, with its age.
const areaWindow = 7 * 24 * time.Hour

// ageRules are the age limits /v1/vessels and the tiles share. An area answers with a week of last known
// positions, and leaves out a vessel last heard under way more than 30 minutes ago, since it has moved on.
// A named vessel, one given in mmsi, answers with its last known position however old. max_age and
// max_age_moving set both.
type ageRules struct {
	areaAge, namedAge time.Duration // 0: no limit
	area, named       vesselFilter
}

func parseAgeRules(vals url.Values) (*ageRules, string) {
	area, msg := parseVesselFilter(vals, vesselTTL)
	if msg != "" {
		return nil, msg
	}
	named, _ := parseVesselFilter(vals, 0)
	r := &ageRules{areaAge: areaWindow, area: *area, named: *named}
	age, set, msg := parseMaxAge(vals.Get("max_age"))
	if msg != "" {
		return nil, msg
	}
	if set {
		if age == ageAll {
			age = 0
		}
		r.areaAge, r.namedAge = age, age
	}
	return r, ""
}

// rule is the age limit and filter for a named vessel or one found by area.
func (r *ageRules) rule(named bool) (time.Duration, *vesselFilter) {
	if named {
		return r.namedAge, &r.named
	}
	return r.areaAge, &r.area
}

func (r *ageRules) match(named bool, v *vessel, now time.Time) bool {
	age, f := r.rule(named)
	return (age == 0 || now.Sub(v.Seen) <= age) && f.match(v, now)
}

// since is the oldest seen an age admits; zero, admitting everything, for no limit.
func since(now time.Time, age time.Duration) time.Time {
	if age == 0 {
		return time.Time{}
	}
	return now.Add(-age)
}

// ageAll is max_age=all: no age limit at all.
const ageAll = time.Duration(math.MaxInt64)

// nearestRecords is the records of the vessels q matches nearest the point, as many as q's limit. Every match
// is ranked, by the cache's position where it is newer than the record's: the cache runs up to a flush ahead,
// and a report from the last second can bring a vessel into the nearest from anywhere.
func (p *Pipeline) nearestRecords(q recordQuery, around [2]float64) ([]record, error) {
	limit := q.limit
	q.limit = 0
	pos, err := p.store.positions(q)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		mmsi uint32
		dist float64
	}
	all := make([]ranked, len(pos))
	p.vmu.RLock()
	for i, sp := range pos {
		lat, lon := sp.lat, sp.lon
		if c := p.vessels[sp.mmsi]; c != nil && c.HasPos && c.PosAt.After(sp.posAt) {
			lat, lon = c.Lat, c.Lon
		}
		all[i] = ranked{sp.mmsi, nm(around[0], around[1], lat, lon)}
	}
	p.vmu.RUnlock()
	slices.SortFunc(all, func(a, b ranked) int { return cmp.Compare(a.dist, b.dist) })
	if len(all) > limit {
		all = all[:limit]
	}
	q.mmsis = make([]uint32, len(all))
	for i, r := range all {
		q.mmsis[i] = r.mmsi
	}
	return p.store.find(q)
}

// parseAround reads around=lat,lon, the point a search is ordered from.
func parseAround(s string) (*[2]float64, string) {
	if s == "" {
		return nil, ""
	}
	f := strings.Split(s, ",")
	if len(f) != 2 {
		return nil, "around=lat,lon"
	}
	lat, latErr := strconv.ParseFloat(f[0], 64)
	lon, lonErr := strconv.ParseFloat(f[1], 64)
	if latErr != nil || lonErr != nil || !(lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180) { // NaN fails every comparison
		return nil, "around=lat,lon"
	}
	return &[2]float64{lat, lon}, ""
}

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

// hasLengthOffsets and hasBeamOffsets report whether a pair of antenna offsets is known: it adds up to its
// known total. A zero offset is then information (a reference point on the bow, or AIS's "reference point
// unknown" of 0 to bow and the length to stern), so the pair is served whole. A record row from before the
// offsets were kept holds a total with 0 and 0, which adds up to nothing, until the next static fills it.
func (v *vessel) hasLengthOffsets() bool { return v.Length > 0 && v.Dim.A+v.Dim.B == v.Length }
func (v *vessel) hasBeamOffsets() bool {
	return v.Beam > 0 && uint16(v.Dim.C)+uint16(v.Dim.D) == v.Beam
}

// dimensions turns the AIS reference-point distances into overall length and beam, 0 when not sent, and
// returns the distances themselves.
func dimensions(d ais.FieldDimension) (length, beam uint16, dim ais.FieldDimension) {
	return d.A + d.B, uint16(d.C) + uint16(d.D), d
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
