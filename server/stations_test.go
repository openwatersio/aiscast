package main

import (
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
	// A station new since the totals' last read keeps its counts since the start, rather than reading 0, and has no
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
	p.stations.restoreOwn(map[string]map[uint32]int64{"station:ed25519:k": {366000002: now.Add(-10 * time.Minute).Unix()}})
	p.refreshOwn(now)
	if r := rowOf(t, p.stationRows(now), "station:ed25519:k"); r.MMSI != 0 {
		t.Errorf("two own vessels within the hour across the restart, yet one decided: %+v", r)
	}
}
