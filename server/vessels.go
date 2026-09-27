package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

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
		u.Lat, u.Lon, hasPos = float64(m.Latitude), float64(m.Longitude), true
		u.Cog, u.Sog, u.Heading, u.NavStatus = float64(m.Cog), float64(m.Sog), m.TrueHeading, m.NavigationalStatus
	case ais.StandardClassBPositionReport:
		u.Lat, u.Lon, hasPos = float64(m.Latitude), float64(m.Longitude), true
		u.Cog, u.Sog, u.Heading = float64(m.Cog), float64(m.Sog), m.TrueHeading
	case ais.ExtendedClassBPositionReport:
		u.Lat, u.Lon, hasPos, u.Name = float64(m.Latitude), float64(m.Longitude), true, m.Name
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
	if hasPos && !stale {
		v.Lat, v.Lon, v.HasPos, v.PosAt = u.Lat, u.Lon, true, ev.Time
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
			delete(p.vessels, mmsi)
		}
	}
}

func (p *Pipeline) vesselCount() int {
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	return len(p.vessels)
}

func (v *vessel) feature(mmsi uint32) map[string]any {
	props := map[string]any{
		"mmsi": mmsi, "kind": v.Kind, "seen": v.Seen.UTC().Format(time.RFC3339),
		"source": v.Source, "station": v.Station, "msg_type": v.MsgType,
	}
	if v.Name != "" {
		props["name"] = v.Name
	}
	if v.ShipType != 0 {
		props["type"] = v.ShipType
	}
	if v.Cog < 360 {
		props["cog"] = v.Cog
	}
	if v.Sog < 102.3 {
		props["sog"] = v.Sog
	}
	if v.Heading < 511 {
		props["heading"] = v.Heading
	}
	if v.NavStatus != 15 {
		props["nav_status"] = v.NavStatus
	}
	if f := flagOf(mmsi); f != "" {
		props["flag"] = f
	}
	if v.IMO != 0 {
		props["imo"] = v.IMO
	}
	if v.CallSign != "" {
		props["callsign"] = v.CallSign
	}
	if v.Destination != "" {
		props["destination"] = v.Destination
	}
	if eta := etaString(v.ETA); eta != "" {
		props["eta"] = eta
	}
	if v.Draught > 0 {
		props["draught"] = v.Draught
	}
	if v.Length > 0 {
		props["length"] = v.Length
	}
	if v.Beam > 0 {
		props["beam"] = v.Beam
	}
	return map[string]any{
		"type":       "Feature",
		"id":         mmsi,
		"geometry":   map[string]any{"type": "Point", "coordinates": [2]float64{v.Lon, v.Lat}},
		"properties": props,
	}
}

// serveVessels: GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon&mmsi=a,b → GeoJSON of current positions.
// The filters, the token, and the area and MMSI caps are exactly those of /v1/stream.
func (p *Pipeline) serveVessels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s, msg := parseSSESub(r, cl)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	features := []map[string]any{}
	attribution := map[string]string{}
	p.vmu.RLock()
	for mmsi, v := range p.vessels {
		if v.HasPos && s.match(&Event{MMSI: mmsi, Lat: v.Lat, Lon: v.Lon, HasPos: true}) {
			features = append(features, v.feature(mmsi))
			noteAttribution(attribution, v.Source)
		}
	}
	p.vmu.RUnlock()
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(featureCollection(features, attribution))
}

// noteAttribution records the credit line for the source's kind (its name up to the first `:`).
func noteAttribution(m map[string]string, source string) {
	if k := sourceKind(source); m[k] == "" {
		m[k] = attributionOf(source)
	}
}

// featureCollection assembles a GeoJSON response. attribution is a foreign member (RFC 7946 §6.1): per
// source kind present in the features, the credit line the consumer must display.
func featureCollection(features []map[string]any, attribution map[string]string) map[string]any {
	return map[string]any{"type": "FeatureCollection", "features": features, "attribution": attribution}
}

// ---- snapshot subscriptions: replay the cache so a new client starts with the vessels already tracked ----

// snapshotEvents returns replayable events for every cached vessel matching s: the retained originals
// when available, else reconstructions from the folded state (vessels restored from disk).
func (p *Pipeline) snapshotEvents(s *v1Sub) []*Event {
	var out []*Event
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	for mmsi, v := range p.vessels {
		if !s.match(&Event{MMSI: mmsi, Lat: v.Lat, Lon: v.Lon, HasPos: v.HasPos}) {
			continue
		}
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
	}
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
