package main

import (
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestUpdateVesselStalePosition(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	pos := func(at time.Time, lat float64) *Event {
		return &Event{Time: at, Source: "local", MMSI: 1, Type: "PositionReport", Packet: ais.PositionReport{
			Header: ais.Header{UserID: 1}, Latitude: ais.FieldLatLonFine(lat), Longitude: 10, Cog: 90, Sog: 5, TrueHeading: 511, NavigationalStatus: 0}}
	}
	p.updateVessel(pos(t0, 50))

	// late report from a slow source: older time, different position, and a name (ExtendedClassB carries one)
	late := &Event{Time: t0.Add(-90 * time.Second), Source: "aishub", MMSI: 1, Type: "ExtendedClassBPositionReport", Packet: ais.ExtendedClassBPositionReport{
		Header: ais.Header{UserID: 1}, Latitude: 49, Longitude: 10, Cog: 180, Sog: 1, TrueHeading: 511, Name: "LATE"}}
	p.updateVessel(late)

	v := p.vessels[1]
	if v.Lat != 50 || v.Cog != 90 || v.Sog != 5 || !v.Seen.Equal(t0) || v.Source != "local" {
		t.Fatalf("stale event overwrote cache: %+v", v)
	}
	if v.Name != "LATE" {
		t.Fatalf("static field from stale event not folded: %+v", v)
	}
	if late.Lat != 49 || late.Name != "LATE" {
		t.Fatalf("stale event not stamped with own position/name: lat=%v name=%q", late.Lat, late.Name)
	}

	// within 1 s counts as fresh (sources stamp whole seconds)
	p.updateVessel(pos(t0.Add(-500*time.Millisecond), 51))
	if p.vessels[1].Lat != 51 {
		t.Fatalf("sub-second-older event should apply: %+v", p.vessels[1])
	}
}

func TestStaleEventNotBroadcast(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	report := func(lat float64) ais.Packet {
		return ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 2}, Valid: true,
			Latitude: ais.FieldLatLonFine(lat), Longitude: 10, Cog: 360, Sog: 102.3, TrueHeading: 511, NavigationalStatus: 15}
	}
	p.ingestPacket("kystverket", "kystverket", t0, report(50))
	if len(sub.ch) != 1 {
		t.Fatalf("fresh event not broadcast: %d", len(sub.ch))
	}
	<-sub.ch

	// a slow source's copy of an already-superseded report: archived and folded, never streamed
	before := p.stats.stale.Load()
	p.ingestPacket("aishub", "aishub", t0.Add(-90*time.Second), report(49))
	if len(sub.ch) != 0 || p.stats.stale.Load() != before+1 {
		t.Fatalf("stale event broadcast: events=%d stale=%d→%d", len(sub.ch), before, p.stats.stale.Load())
	}
}

// posAt is the AIS-quantized comparison: lat/lon round-trip to within one 1/10000-minute step.
func posAt(t *testing.T, v *vessel, lat, lon float64) bool {
	t.Helper()
	return v.HasPos && absF(v.Lat-lat) < 1e-4 && absF(v.Lon-lon) < 1e-4
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func posReport(mmsi uint32, lat, lon float64) ais.Packet {
	return ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true,
		Latitude: ais.FieldLatLonFine(lat), Longitude: ais.FieldLatLonFine(lon),
		Cog: 360, Sog: 102.3, TrueHeading: 511, NavigationalStatus: 15}
}

// The implausibility check is not limited to UDP: the upstream aggregates carry bad positions too.
func TestImplausibleJumpFromAnySource(t *testing.T) {
	for i, src := range []string{"aishub", "aisstream", "kystverket"} {
		p := testPipeline(t)
		mmsi := uint32(3000 + i)
		t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
		p.ingestPacket(src, src, t0, posReport(mmsi, 49.48, 0.13))

		before := p.stats.implausible.Load()
		ev := &Event{Time: t0.Add(3 * time.Second), Source: src, MMSI: mmsi, Type: "PositionReport",
			Packet: posReport(mmsi, -0.52, 0.13)} // 3,002 nm away
		p.updateVessel(ev)
		if !ev.Implausible {
			t.Errorf("%s: 3,000 nm in 3 s not flagged implausible", src)
		}
		if !posAt(t, p.vessels[mmsi], 49.48, 0.13) {
			t.Errorf("%s: implausible position folded into the cache: %+v", src, p.vessels[mmsi])
		}
		p.emit(ev) // emit counts it and withholds it from subscribers
		if p.stats.implausible.Load() != before+1 {
			t.Errorf("%s: implausible not counted", src)
		}
	}
}

// A jump shorter than implausibleJumpNM is left alone however fast it implies: at second-level spacing two
// sources reporting the same vessel disagree by metres, and 100 m in 1 s already implies 117 kn.
func TestShortJumpNotImplausible(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	p.ingestPacket("kystverket", "kystverket", t0, posReport(4242, 69.9, 20.1))
	ev := &Event{Time: t0.Add(time.Second), Source: "barentswatch", MMSI: 4242, Type: "PositionReport",
		Packet: posReport(4242, 69.909472, 20.163188)}
	p.updateVessel(ev)
	if ev.Implausible {
		t.Errorf("cross-source jitter of %.2f nm flagged implausible", nm(69.9, 20.1, 69.909472, 20.163188))
	}
}

// (0,0) is a GPS default, not a fix. It is a valid coordinate, so the 91/181 range test passes it.
func TestNullIslandIsNotAPosition(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// a vessel already tracked keeps the position it had
	p.ingestPacket("aisstream", "aisstream", t0, posReport(1, 49.48, 0.13))
	p.ingestPacket("aisstream", "aisstream", t0.Add(time.Minute), posReport(1, 0, 0))
	if !posAt(t, p.vessels[1], 49.48, 0.13) {
		t.Errorf("null island folded into the cache: %+v", p.vessels[1])
	}
	// a vessel seen only at (0,0) has no position at all, so it never reaches /v1/vessels
	p.ingestPacket("aishub", "aishub", t0, posReport(2, 0, 0))
	if v := p.vessels[2]; v.HasPos {
		t.Errorf("vessel known only at (0,0) has a position: lat=%v lon=%v", v.Lat, v.Lon)
	}
	// the meridian and the equator on their own are ordinary water: Greenwich is on longitude 0
	p.ingestPacket("aishub", "aishub", t0, posReport(3, 51.5, 0))
	if !posAt(t, p.vessels[3], 51.5, 0) {
		t.Errorf("Greenwich position rejected: %+v", p.vessels[3])
	}
}
