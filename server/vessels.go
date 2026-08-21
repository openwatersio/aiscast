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
	Seen      time.Time
	Source    string
	Station   string
	MsgType   string
}

func newVessel() *vessel {
	return &vessel{Cog: 360, Sog: 102.3, Heading: 511, NavStatus: 15, Kind: "vessel"}
}

// updateVessel folds the event into the per-MMSI cache and stamps the event with the cached name/position,
// so positionless messages (5, 24) can be bbox-routed and carry MetaData like aisstream.
func (p *Pipeline) updateVessel(ev *Event) {
	u := newVessel() // fields at sentinel = "not in this message"
	var hasPos bool
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
		u.Name, u.ShipType = m.Name, m.Type
	case ais.StaticDataReport:
		if m.ReportA.Valid {
			u.Name = m.ReportA.Name
		}
		if m.ReportB.Valid {
			u.ShipType = m.ReportB.ShipType
		}
	}
	if hasPos && (math.Abs(u.Lat) > 90 || math.Abs(u.Lon) > 180) { // 91/181 = not available
		hasPos = false
	}
	p.vmu.Lock()
	v := p.vessels[ev.MMSI]
	if v == nil {
		v = newVessel()
		p.vessels[ev.MMSI] = v
	}
	// A late-arriving report (AISHub lags minutes behind VHF) must not drag the vessel back along its track;
	// only static fields fold in. Whole-second source stamps make ties and sub-second skew meaningless.
	stale := v.HasPos && ev.Time.Before(v.Seen.Add(-time.Second))
	if hasPos && !stale {
		v.Lat, v.Lon, v.HasPos = u.Lat, u.Lon, true
		v.Cog, v.Sog, v.Heading = u.Cog, u.Sog, u.Heading // sentinels from a position report are real "unknown"s
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
	if !stale {
		v.Seen, v.Source, v.Station, v.MsgType = ev.Time, ev.Source, ev.Station, ev.Type
	}
	ev.Name, ev.Lat, ev.Lon, ev.HasPos = v.Name, v.Lat, v.Lon, v.HasPos
	if stale && hasPos { // the event still carries its own position; only the cache ignores it
		ev.Lat, ev.Lon = u.Lat, u.Lon
	}
	p.vmu.Unlock()
}

// sweepVessels drops vessels unseen since cutoff and returns the remaining count.
func (p *Pipeline) sweepVessels(cutoff time.Time) int {
	p.vmu.Lock()
	defer p.vmu.Unlock()
	for mmsi, v := range p.vessels {
		if v.Seen.Before(cutoff) {
			delete(p.vessels, mmsi)
		}
	}
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
	return map[string]any{
		"type":       "Feature",
		"id":         mmsi,
		"geometry":   map[string]any{"type": "Point", "coordinates": [2]float64{v.Lon, v.Lat}},
		"properties": props,
	}
}

// serveVessels: GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon → GeoJSON of current positions (all if no bbox).
func (p *Pipeline) serveVessels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	var b bbox
	all := true
	if q := r.URL.Query().Get("bbox"); q != "" {
		if n, _ := fmt.Sscanf(q, "%f,%f,%f,%f", &b[0], &b[1], &b[2], &b[3]); n != 4 {
			http.Error(w, "bbox=minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
			return
		}
		all = false
	}
	features := []map[string]any{}
	p.vmu.RLock()
	for mmsi, v := range p.vessels {
		if v.HasPos && (all || b.contains(v.Lat, v.Lon)) {
			features = append(features, v.feature(mmsi))
		}
	}
	p.vmu.RUnlock()
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(map[string]any{"type": "FeatureCollection", "features": features})
}
