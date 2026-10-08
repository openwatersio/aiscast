package main

import (
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"
)

func rowOf(t *testing.T, rows []stationRow, id string) stationRow {
	t.Helper()
	for _, r := range rows {
		if r.Station == id {
			return r
		}
	}
	t.Fatalf("no row %s in %+v", id, rows)
	return stationRow{}
}

func TestUniqueVessels(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("s1", "s1", now, now, posReport(366000001, 41.5, -70.6))
	p.ingestPacket("s1", "s1", now, now, posReport(366000002, 41.6, -70.7))
	p.ingestPacket("s2", "s2", now, now, posReport(366000003, 42.0, -70.0))
	p.ingestPacket("s2", "s2", now.Add(5*time.Second), now.Add(5*time.Second), posReport(366000002, 41.6, -70.7)) // s2 also hears s1's second vessel
	rows := p.stations.rows(now.Add(10 * time.Second))
	if r := rowOf(t, rows, "s1"); r.Vessels24 != 2 || r.Exclusive != 1 {
		t.Errorf("s1: %+v", r)
	}
	if r := rowOf(t, rows, "s2"); r.Vessels24 != 2 || r.Exclusive != 1 {
		t.Errorf("s2: %+v", r)
	}
}

func TestUniqueVesselsCountDuplicates(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	line := "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"
	p.Ingest(Reception{Source: "s1", Station: "s1", RecvTime: now, Body: line})
	p.Ingest(Reception{Source: "s2", Station: "s2", RecvTime: now, Body: line}) // beaten to it, but it heard the vessel
	rows := p.stations.rows(now)
	for _, id := range []string{"s1", "s2"} {
		if r := rowOf(t, rows, id); r.Vessels24 != 1 || r.Exclusive != 0 {
			t.Errorf("%s: %+v", id, r)
		}
	}
}

func TestUniqueVesselsGroupTagRows(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.event(&Event{Station: "station:ed25519:x", Source: "station:ed25519:x", Time: now, MMSI: 366000001})
	p.stations.event(&Event{Station: "station:ed25519:x/n2k", Source: "station:ed25519:x", Time: now, MMSI: 366000001})
	p.stations.event(&Event{Station: "station:ed25519:y", Source: "station:ed25519:y", Time: now, MMSI: 366000002})
	rows := p.stations.rows(now)
	if a, b := rowOf(t, rows, "station:ed25519:x"), rowOf(t, rows, "station:ed25519:x/n2k"); a.Exclusive != 1 || b.Exclusive != 1 {
		t.Errorf("rows of one receiver competed for a vessel: %+v %+v", a, b)
	}
}

func TestUniqueVesselsLeaveOutOwnShip(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.Ingest(Reception{Source: "station:ed25519:k", Station: "station:ed25519:k", RecvTime: now, Body: `\s:self*55\!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21`})
	p.ingestPacket("station:ed25519:k", "station:ed25519:k", now, now, posReport(366000009, 41.5, -70.6))
	if r := rowOf(t, p.stations.rows(now), "station:ed25519:k"); r.Vessels24 != 2 || r.Exclusive != 1 {
		t.Errorf("own ship counted as unique: %+v", r)
	}
}

// One volunteer receiver is one station, whatever paths its TAG s: names. A feed's s: still names separate
// receivers, and its stations stay apart.
func TestTagSplitsFeedStationsOnly(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.Ingest(Reception{Source: "station:mmsi:368168720", Station: "station:mmsi:368168720", RecvTime: now, Body: `\s:n2k*7E\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`})
	p.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: `\s:n2k*7E\!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E`})
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: `\s:2573010*7B\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43`})
	var ids []string
	for _, r := range p.stations.rows(now) {
		ids = append(ids, r.Station)
	}
	if want := []string{"kystverket/2573010", "station:mmsi:368168720", "udp:abc"}; !slices.Equal(ids, want) {
		t.Errorf("stations %v, want %v", ids, want)
	}
}

// The server feeds volunteer receptions to AISHub and polls AISHub back, so a volunteer's vessel returns as
// an AISHub row. That row is never newer than the volunteer's own copy, so it is stale and must not take
// the vessel's uniqueness away, even when it carries the same second.
func TestUniqueVesselsIgnoreAishubEcho(t *testing.T) {
	p := testPipeline(t)
	now := time.Now().Truncate(time.Second)
	p.ingestPacket("udp:abc", "udp:abc", now, now, posReport(366000001, 41.5, -70.6))
	p.ingestPacket("aishub", "aishub", now, now.Add(time.Minute), posReport(366000001, 41.50001, -70.6))                // same second
	p.ingestPacket("aishub", "aishub", now.Add(-time.Minute), now.Add(time.Minute), posReport(366000001, 41.49, -70.6)) // older
	rows := p.stations.rows(now.Add(2 * time.Minute))
	if r := rowOf(t, rows, "udp:abc"); r.Exclusive != 1 {
		t.Errorf("echo took uniqueness: %+v (all %+v)", r, rows)
	}
}

func TestStationVesselWindows(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now.Add(-2 * time.Hour), MMSI: 366000001})
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: 366000002})
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now.Add(-25 * time.Hour), MMSI: 366000003})
	r := rowOf(t, p.stations.rows(now), "s1")
	if r.Vessels != 1 || r.Vessels24 != 2 || r.Exclusive != 2 {
		t.Errorf("windows: %+v", r)
	}
	if vs := p.stations.vesselsBySource(now)["s1"]; vs[0] != 1 {
		t.Errorf("per-source vessels left the 30-minute window: %v", vs)
	}
	p.stations.sweep(now.Add(-stationVesselTTL))
	if n := len(p.stations.m["s1"].vessels); n != 2 {
		t.Errorf("sweep kept %d vessels", n)
	}
}

func TestStationVesselsSurviveRestart(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("s1", "s1", now, now, posReport(366000001, 41.5, -70.6))
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: 366000002, Own: true})
	path := t.TempDir() + "/station-vessels.json"
	if err := p.stations.saveVessels(path, now); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.stations.loadVessels(path); err != nil {
		t.Fatal(err)
	}
	if rows := q.stations.rows(now); len(rows) != 0 {
		t.Errorf("restored station listed before it reports: %+v", rows)
	}
	q.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: 366000003})
	r := rowOf(t, q.stations.rows(now), "s1")
	if r.Vessels24 != 3 || r.Exclusive != 2 {
		t.Errorf("after restore: %+v", r)
	}
	if h := q.stations.m["s1"].vessels[366000001]; !h.pos || h.lat < 41.49 || h.lat > 41.51 {
		t.Errorf("position not restored: %+v", h)
	}
}

// A station that never reports again after a restart is saved again at the next shutdown, but only with
// what is still inside the window, so its vessels cannot ride from deploy to deploy forever.
func TestUnclaimedStationVesselsAgeOut(t *testing.T) {
	p := testPipeline(t)
	then := time.Now().Add(-20 * time.Hour)
	p.stations.event(&Event{Station: "gone", Source: "gone", Time: then, MMSI: 366000001})
	path := t.TempDir() + "/station-vessels.json"
	if err := p.stations.saveVessels(path, then); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.stations.loadVessels(path); err != nil {
		t.Fatal(err)
	}
	if err := q.stations.saveVessels(path, then.Add(25*time.Hour)); err != nil { // past the window
		t.Fatal(err)
	}
	r := testPipeline(t)
	if err := r.stations.loadVessels(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.stations.restoredV["gone"]; ok {
		t.Errorf("stale station kept: %+v", r.stations.restoredV)
	}
}

// Production-like: two aggregates hearing tens of thousands of vessels a day, a few feeds, and volunteers
// hearing a few hundred each, about 150,000 entries in all.
func benchStations(now time.Time) *stationStats {
	s := newStationStats()
	sizes := []int{80000, 45000, 8000, 4000, 2000}
	for range 25 {
		sizes = append(sizes, 400)
	}
	for i, n := range sizes {
		id := fmt.Sprintf("s%d", i)
		for j := range n {
			mmsi := uint32(200000000 + (j*7+i*13)%120000)
			s.event(&Event{Station: id, Source: id, Time: now.Add(-time.Duration(j%86400) * time.Second), MMSI: mmsi, HasPos: true, Lat: 41, Lon: -70, Type: "PositionReport"})
		}
	}
	return s
}

func BenchmarkStationRows(b *testing.B) {
	now := time.Now()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	s := benchStations(now)
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := float64(after.HeapAlloc-before.HeapAlloc) / 1e6
	b.ResetTimer()
	for i := range b.N {
		s.mu.Lock()
		s.excl = nil // measure the uncached walk
		s.mu.Unlock()
		s.rows(now.Add(time.Duration(i)))
	}
	b.ReportMetric(held, "MB-held")
}

// A saved station file can hold MMSIs the fold keeps out; restored, they would count as vessels heard and stand as
// own-ship candidates for another day.
func TestRestoredStationVesselsLeaveOutInvalidMMSIs(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	for _, m := range []uint32{0, 123456789, 366000001} {
		p.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: m})
	}
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: 555555555, Own: true})
	path := t.TempDir() + "/station-vessels.json"
	if err := p.stations.saveVessels(path, now); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.stations.loadVessels(path); err != nil {
		t.Fatal(err)
	}
	m := q.stations.restoredV["s1"]
	if len(m.V) != 1 || uint32(m.V[0][0]) != 366000001 || len(m.Own) != 0 {
		t.Errorf("restored %+v, want only 366000001 and no own ship", m)
	}
}
