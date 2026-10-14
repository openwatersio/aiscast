package main

import (
	"slices"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
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

// One volunteer receiver is one station, whatever paths its TAG s: names. A feed's s: still names separate
// receivers, and its stations stay apart.
func TestTagSplitsFeedStationsOnly(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.Ingest(Reception{Source: "station:mmsi:368168720", Station: "station:mmsi:368168720", RecvTime: now, Body: `\s:n2k*7E\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`})
	p.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: `\s:n2k*7E\!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E`})
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: `\s:2573010*7B\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43`})
	var ids []string
	for _, r := range p.stations.rows(now, nil) {
		ids = append(ids, r.Station)
	}
	if want := []string{"kystverket/2573010", "station:mmsi:368168720", "udp:abc"}; !slices.Equal(ids, want) {
		t.Errorf("stations %v, want %v", ids, want)
	}
}

// A station's row takes the series' figures where the series has the station: vessels, unique vessels, uptime, its
// receptions and duplicates, and its first hour when earlier than the live row's. Without them it keeps the live
// counts and has no uptime.
func TestStationRowsTakeTheSeries(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("s1", "s1", now, now, posReport(366000001, 41.5, -70.6))
	p.ingestPacket("s2", "s2", now, now, posReport(366000002, 41.5, -70.6))
	first := now.Truncate(time.Hour).Add(-47 * time.Hour)
	// 35 of the 47 hours before this one heard, and this one: 36 of 48
	f := &fakeSeries{counts: map[string]stationCount{"s1": {first: first, past: 35, now: true, live: 3, day: 9, unique: 2}},
		totals: map[string]stationCount{"s1": {first: first, receptions: 500, firsts: 450}}}
	p.attachClickHouse(&chStore{w: &fakeCH{}, series: f})
	p.refreshRollups(f, now)
	rows := p.stationRows(now)
	r := rowOf(t, rows, "s1")
	if r.Vessels != 3 || r.Vessels24 != 9 || r.Exclusive != 2 || r.Uptime == nil || *r.Uptime != 0.75 || r.Positions != 450 || r.Dups != 50 || !r.FirstSeen.Equal(first) {
		t.Errorf("s1 from the series: %+v", r)
	}
	if r := rowOf(t, rows, "s2"); r.Uptime != nil || r.Vessels24 != 0 || r.Positions != 1 {
		t.Errorf("s2 without the series: %+v", r)
	}
	if f.reads != 1 {
		t.Errorf("serving the list read the series: %d reads", f.reads)
	}
	// A station new since the totals' last read keeps its own counts since first_seen, rather than reading 0, and has no
	// uptime until its first hour is known.
	f.counts["s2"] = stationCount{first: now.Truncate(time.Hour), now: true, day: 1}
	p.refreshRollups(f, now.Add(time.Minute))
	if r := rowOf(t, p.stationRows(now.Add(time.Minute)), "s2"); r.Positions != 1 || r.Uptime != nil {
		t.Errorf("s2 heard since the totals: %+v", r)
	}
}

// Own-ship candidates read back at start are claimed by their station when it is next heard, or at once when it
// already has been, so two vessels sent as own within the hour still mean no own vessel after a restart.
func TestOwnCandidatesSurviveRestart(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("heard", "heard", now, now, posReport(366000001, 41.5, -70.6))
	p.stations.restoreOwn(map[string]map[uint32]int64{"heard": {227006760: now.Unix()}, "later": {227006761: now.Unix()}})
	if own := p.stations.ownShips(); own["heard"][227006760] != now.Unix() || own["later"] != nil {
		t.Errorf("own ships before the second station reports: %v", own)
	}
	p.ingestPacket("later", "later", now, now, posReport(366000001, 41.5, -70.6))
	if own := p.stations.ownShips(); own["later"][227006761] != now.Unix() {
		t.Errorf("a restored candidate not claimed when its station reported: %v", own)
	}
}

// Until the candidates from before the start are back, no own vessel is decided: a station that sent two vessels
// as its own within the hour before the restart, and one of them since, still has none.
func TestOwnVesselWaitsForRestoredCandidates(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.ownPending.Store(true)
	p.Ingest(Reception{Source: "station:ed25519:k", Station: "station:ed25519:k", RecvTime: now, Body: `\s:n2k*7E\!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*21`})
	p.refreshOwn(now)
	if r := rowOf(t, p.stationRows(now), "station:ed25519:k"); r.MMSI != 0 {
		t.Fatalf("own vessel decided before the candidates were back: %+v", r)
	}
	// A vessel decided before the restart keeps its name meanwhile.
	p.ingestPacket("aishub", "aishub", now, now, ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 227006761}, Valid: true, Name: "TENDER"})
	p.stations.event(&Event{Station: "station:ed25519:z", Source: "station:ed25519:z", Time: now, MMSI: 1})
	p.names.mu.Lock()
	p.names.meta("station:ed25519:z").Own = 227006761
	p.names.mu.Unlock()
	p.refreshOwn(now)
	if r := rowOf(t, p.stationRows(now), "station:ed25519:z"); r.Name != "TENDER" {
		t.Errorf("a vessel decided before the restart lost its name while candidates were pending: %+v", r)
	}
	p.stations.restoreOwn(map[string]map[uint32]int64{"station:ed25519:k": {366000002: now.Add(-10 * time.Minute).Unix()}})
	p.refreshOwn(now)
	if r := rowOf(t, p.stationRows(now), "station:ed25519:k"); r.MMSI != 0 {
		t.Errorf("two own vessels within the hour across the restart, yet one decided: %+v", r)
	}
}

// A restart lists every station at once, as it stood at shutdown. Its extent is not saved, so it has no bbox until
// it hears a position again.
func TestStationsListedAfterRestart(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.event(&Event{Station: "s1", Source: "src", Time: now.Add(-3 * time.Hour), MMSI: 366000001, HasPos: true, Lat: 41.5, Lon: -70.6, Type: "PositionReport"})
	p.stations.event(&Event{Station: "s1", Source: "src", Time: now.Add(-time.Minute), MMSI: 366000002, HasPos: true, Lat: 41.6, Lon: -70.5, Type: "PositionReport"})
	p.stations.dup(&Event{Station: "s1", Source: "src", Time: now.Add(-time.Minute), Packet: posReport(366000003, 41.7, -70.4)})
	want := rowOf(t, p.stations.rows(now, nil), "s1")
	path := t.TempDir() + "/usage.json"
	if err := p.saveUsage(path); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.loadUsage(path); err != nil {
		t.Fatal(err)
	}
	got := rowOf(t, q.stations.rows(now, nil), "s1")
	if got.Source != "src" || !got.FirstSeen.Equal(want.FirstSeen) || !got.LastSeen.Equal(want.LastSeen) ||
		got.Dups != 1 || got.Positions != 2 || got.Events["last_24h"] != 2 || got.BBox != nil {
		t.Errorf("restored %+v, want %+v without a bbox", got, want)
	}
	q.stations.event(&Event{Station: "s1", Source: "src", Time: now, MMSI: 366000001, HasPos: true, Lat: 41.5, Lon: -70.6, Type: "PositionReport"})
	if got := rowOf(t, q.stations.rows(now, nil), "s1"); got.BBox == nil || *got.BBox != [4]float64{41.5, -70.6, 41.5, -70.6} || got.Positions != 3 {
		t.Errorf("after a position: %+v", got)
	}
}

// Own-ship candidates read back from ClickHouse reach a station restored from the usage file, whichever comes first.
func TestRestoredStationTakesOwnCandidates(t *testing.T) {
	now := time.Now()
	infos := map[string]stationInfo{"s1": {Source: "s1", First: now, Last: now}}
	cands := map[string]map[uint32]int64{"s1": {227006760: now.Unix()}}
	for _, usageFirst := range []bool{false, true} {
		p := testPipeline(t)
		if usageFirst {
			p.stations.restore(nil, infos, now)
			p.stations.restoreOwn(cands)
		} else {
			p.stations.restoreOwn(cands)
			p.stations.restore(nil, infos, now)
		}
		if own := p.stations.ownShips(); own["s1"][227006760] != now.Unix() {
			t.Errorf("usage first %v: own ships %v", usageFirst, own)
		}
	}
}

// A station silent for stationKeep is forgotten, by a running process and across a restart alike.
func TestSilentStationsForgotten(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.event(&Event{Station: "old", Source: "old", Time: now.Add(-stationKeep - time.Hour), MMSI: 366000001})
	p.stations.event(&Event{Station: "new", Source: "new", Time: now.Add(-stationKeep + time.Hour), MMSI: 366000002})
	if _, ok := p.stations.infos(now)["old"]; ok {
		t.Error("silent station saved")
	}
	q := testPipeline(t)
	q.stations.restore(nil, map[string]stationInfo{"old": {Source: "old", Last: now.Add(-stationKeep - time.Hour)}}, now)
	if len(q.stations.m) != 0 {
		t.Errorf("silent station restored: %+v", q.stations.m)
	}
	p.stations.sweep(now)
	if _, ok := p.stations.m["old"]; ok {
		t.Error("sweep kept a silent station")
	}
	if _, ok := p.stations.m["new"]; !ok {
		t.Error("sweep dropped a station inside the window")
	}
}

// A late copy leaves last_seen alone, so it cannot make sweep forget a station that is still reporting.
func TestLateEventKeepsStation(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now, MMSI: 366000001})
	p.stations.event(&Event{Station: "s1", Source: "s1", Time: now.Add(-stationKeep - time.Hour), MMSI: 366000002})
	p.stations.dup(&Event{Station: "s1", Source: "s1", Time: now.Add(-stationKeep - time.Hour), Packet: posReport(366000003, 41.7, -70.4)})
	p.stations.sweep(now)
	if st := p.stations.m["s1"]; st == nil || !st.Last.Equal(now) {
		t.Errorf("late copy moved the station: %+v", st)
	}
}

// A UDP station is kept across restarts only once it has delivered a message first in establishedHours, and stays kept after that
// however quiet it goes. A station with a token is kept from its first message.
func TestUDPStationsKeptOnceEstablished(t *testing.T) {
	p := testPipeline(t)
	now := time.Now().Truncate(time.Hour).Add(30 * time.Minute)
	p.stations.event(&Event{Station: "udp:once", Source: "udp:once", Time: now, MMSI: 366000001})
	p.stations.event(&Event{Station: "mmsi:366000009", Source: "mmsi:366000009", Time: now, MMSI: 366000001})
	p.stations.event(&Event{Station: "station:ed25519:k", Source: "station:ed25519:k", Time: now, MMSI: 366000001})
	for i := range establishedHours - 1 {
		p.stations.event(&Event{Station: "udp:fed", Source: "udp:fed", Time: now.Add(-time.Duration(i) * time.Hour), MMSI: 366000001})
	}
	infos := p.stations.infos(now)
	for id, want := range map[string]bool{"udp:once": false, "mmsi:366000009": false, "udp:fed": false, "station:ed25519:k": true} {
		if _, ok := infos[id]; ok != want {
			t.Errorf("%s saved %v, want %v", id, ok, want)
		}
	}
	p.stations.event(&Event{Station: "udp:fed", Source: "udp:fed", Time: now.Add(-time.Duration(establishedHours-1) * time.Hour), MMSI: 366000001})
	if in, ok := p.stations.infos(now)["udp:fed"]; !ok || !in.Established {
		t.Fatalf("udp:fed not saved after %d hours: %+v", establishedHours, in)
	}
	// A week later its ring is empty, but it was established, so it is still saved and restored.
	later := now.Add(8 * 24 * time.Hour)
	q := testPipeline(t)
	q.stations.restore(nil, p.stations.infos(later), later)
	if in, ok := q.stations.infos(later)["udp:fed"]; !ok || !in.Established {
		t.Errorf("established station dropped once its ring went quiet: %+v", in)
	}
}
