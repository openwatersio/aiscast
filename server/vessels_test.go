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
