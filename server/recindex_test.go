package main

import (
	"testing"
	"time"
)

// The index files what the record holds after each write, by the record's merge rules: a vessel that moves
// changes cell, and one back from the sweep without a position keeps its stored one.
func TestRecordIndexFollowsTheRecord(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Millisecond)
	at := func(lat, lon float64, seen time.Time) record {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen, v.PosAt, v.Sog, v.Kind = lat, lon, true, seen, seen, 0, "aton"
		return record{mmsi: 992570001, v: v, firstSeen: seen}
	}
	where := func() (float64, float64, string, bool) {
		var lat, lon float64
		var kind string
		found := false
		p.store.idx.each(bbox{-90, -180, 90, 180}, recordQuery{}, func(mmsi uint32, e *recEntry) {
			if mmsi == 992570001 {
				lat, lon, kind, found = e.lat, e.lon, recKinds[e.kind], true
			}
		})
		return lat, lon, kind, found
	}
	if err := p.store.upsert([]record{at(59.5, 10.6, now.Add(-time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if lat, lon, kind, ok := where(); !ok || lat != 59.5 || lon != 10.6 || kind != "aton" {
		t.Fatalf("after the first write: %v %v %q %v", lat, lon, kind, ok)
	}
	if err := p.store.upsert([]record{at(10.5, -61.5, now.Add(-30*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if lat, _, _, _ := where(); lat != 10.5 || p.store.idx.cells[cellOf(59.5, 10.6)] != nil {
		t.Fatalf("a move left the vessel at %v or its old cell filed", lat)
	}
	blank := newVessel() // back from the sweep: no position, the default kind
	blank.Seen = now
	if err := p.store.upsert([]record{{mmsi: 992570001, v: blank, firstSeen: now}}); err != nil {
		t.Fatal(err)
	}
	if lat, _, kind, ok := where(); !ok || lat != 10.5 || kind != "aton" {
		t.Fatalf("a blank return: %v %q %v; want the stored position and kind", lat, kind, ok)
	}
	if err := p.store.importRows([]historyRow{{mmsi: 992570002, hasPos: true, lat: 1, lon: 2, last: now, first: now}}); err != nil {
		t.Fatal(err)
	}
	if p.store.idx.len() != 2 {
		t.Fatalf("an imported vessel did not reach the index: %d", p.store.idx.len())
	}
}
