package main

import (
	"errors"
	"math/rand/v2"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// The mirror answers every query SQLite would, with the same rows in the same order.
func TestMirrorMatchesSQL(t *testing.T) {
	p := storePipeline(t)
	r := rand.New(rand.NewPCG(9, 10))
	now := time.Now().Truncate(time.Millisecond)
	kinds := []string{"vessel", "vessel", "vessel", "aton", "base", "sar"}
	var rows []record
	order := r.Perm(3000) // seen independent of MMSI, so an order by MMSI cannot pass for one by seen
	for i := range 3000 {
		v := newVessel()
		if i%7 != 0 {
			v.HasPos = true
			if i%2 == 0 { // half in one busy area, so small boxes find something
				v.Lat, v.Lon = 57+r.Float64()*5, 5+r.Float64()*7
			} else {
				v.Lat, v.Lon = r.Float64()*170-85, r.Float64()*360-180
			}
		}
		v.Seen = now.Add(-time.Duration(order[i]) * 7 * time.Minute) // distinct, so SQLite's order has no ties
		v.PosAt, v.Kind, v.ShipType, v.IMO = v.Seen, kinds[i%len(kinds)], uint8(i%100), uint32(9000000+i%500)
		v.Class = []string{"", "A", "B"}[i%3]
		v.Sog, v.NavStatus = []float64{0, 0.5, 4, 12, 102.3}[i%5], uint8([]int{0, 1, 5, 15}[i%4])
		v.ETA, v.Dim = ais.FieldETA{Month: uint8(1 + i%12), Day: uint8(1 + i%28), Hour: uint8(i % 24), Minute: uint8(i % 60)}, ais.FieldDimension{A: uint16(i % 90), B: 10, C: 4, D: 4}
		v.Length, v.Beam, v.Name = v.Dim.A+v.Dim.B, 8, []string{"", "NORDIC STAR"}[i%2]
		rows = append(rows, record{mmsi: uint32(200000000 + i), v: v, firstSeen: v.Seen.Add(-time.Hour)})
	}
	if err := p.store.upsert(rows); err != nil {
		t.Fatal(err)
	}
	rules := func(q string) *vesselFilter {
		vals, _ := url.ParseQuery(q)
		ar, msg := parseAgeRules(vals)
		if msg != "" {
			t.Fatal(msg)
		}
		_, f := ar.rule(false)
		return f
	}
	mmsis := []uint32{200000001, 200000002, 200000500, 200002999, 299999999}
	for name, q := range map[string]recordQuery{
		"everything":          {},
		"since":               {since: now.Add(-24 * time.Hour)},
		"since and before":    {since: now.Add(-7 * 24 * time.Hour), before: now.Add(-30 * time.Minute), hasPos: true},
		"small box":           {boxes: []bbox{{58, 6, 59, 8}}, hasPos: true},
		"small box, limited":  {boxes: []bbox{{57, 5, 62, 12}}, hasPos: true, limit: 50},
		"two boxes":           {boxes: []bbox{{57, 5, 59, 8}, {58, 7, 62, 12}}, hasPos: true},
		"world box":           {boxes: []bbox{{-90, -180, 90, 180}}, hasPos: true, since: now.Add(-72 * time.Hour)},
		"area rules":          {boxes: []bbox{{57, 5, 62, 12}}, since: now.Add(-areaWindow), before: now.Add(-vesselTTL), hasPos: true, filter: rules(""), now: now},
		"kind filter":         {boxes: []bbox{{57, 5, 62, 12}}, hasPos: true, filter: rules("kind=aton,base&max_age_moving=all"), now: now},
		"type and class":      {hasPos: true, filter: rules("type=10-40,70&class=B&max_age_moving=all"), now: now},
		"min_sog":             {hasPos: true, filter: rules("min_sog=1&max_age_moving=all"), now: now},
		"mmsis":               {mmsis: mmsis},
		"mmsis with position": {mmsis: mmsis, hasPos: true, since: now.Add(-time.Hour)},
		"imos":                {imos: []uint32{9000001, 9000002, 9000499}},
		"imos with position":  {imos: []uint32{9000001, 9000002, 9000499}, hasPos: true, since: now.Add(-48 * time.Hour)},
		"no mmsis":            {mmsis: []uint32{}},
	} {
		if !p.store.mirror.answers(q) {
			t.Fatalf("%s: the mirror does not answer", name)
		}
		want, err := p.store.findSQL(q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := p.store.mirror.find(q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ids := func(rs []record) (out []uint32) {
			for _, r := range rs {
				out = append(out, r.mmsi)
			}
			return out
		}
		if !slices.Equal(ids(got), ids(want)) {
			t.Errorf("%s: mirror %d rows, SQLite %d", name, len(got), len(want))
			continue
		}
		for i := range got {
			if g, w := got[i].v, want[i].v; *g.facts() != *w.facts() || !got[i].firstSeen.Equal(want[i].firstSeen) {
				t.Errorf("%s: %d differs: %+v, want %+v", name, got[i].mmsi, g, w)
				break
			}
		}
		q.limit = 0
		n, _ := p.store.mirror.count(q)
		sqlN, _ := p.store.findSQL(q)
		if n != len(sqlN) {
			t.Errorf("%s: count %d, want %d", name, n, len(sqlN))
		}
	}
	want, _ := p.store.countsSQL(now)
	if got := p.store.mirror.counts(now); got.Total != want.Total || !mapsEqual(got.Heard, want.Heard) || !mapsEqual(got.New, want.New) {
		t.Errorf("counts %+v, want %+v", got, want)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// mirrorFacts is what TestMirrorMatchesSQL compares of a vessel: every field the record stores.
type mirrorFacts struct {
	name, kind, class, callSign, destination, source, station, msgType string
	shipType, navStatus                                                uint8
	imo                                                                uint32
	lat, lon, cog, sog, draught                                        float64
	hasPos                                                             bool
	heading, length, beam                                              uint16
	seen, posAt, trustedAt, staticAt                                   int64
	eta                                                                ais.FieldETA
	dim                                                                ais.FieldDimension
}

func (v *vessel) facts() *mirrorFacts {
	return &mirrorFacts{v.Name, v.Kind, v.Class, v.CallSign, v.Destination, v.Source, v.Station, v.MsgType, v.ShipType, v.NavStatus,
		v.IMO, v.Lat, v.Lon, v.Cog, v.Sog, v.Draught, v.HasPos, v.Heading, v.Length, v.Beam,
		unixMs(v.Seen), unixMs(v.PosAt), unixMs(v.TrustedAt), unixMs(v.StaticAt), v.ETA, v.Dim}
}

// The mirror follows the record's merge rules: a move changes cell, and a blank return keeps the stored position.
func TestMirrorFollowsTheRecord(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Millisecond)
	at := func(lat, lon float64, seen time.Time) record {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.Seen, v.PosAt, v.Sog, v.Kind, v.Name = lat, lon, true, seen, seen, 0, "aton", "SKOMVÆR"
		return record{mmsi: 992570001, v: v, firstSeen: seen}
	}
	get := func() *vessel {
		rs, err := p.store.mirror.find(recordQuery{mmsis: []uint32{992570001}})
		if err != nil || len(rs) != 1 {
			t.Fatalf("%d rows, %v", len(rs), err)
		}
		return rs[0].v
	}
	if err := p.store.upsert([]record{at(59.5, 10.6, now.Add(-time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if v := get(); v.Lat != 59.5 || v.Kind != "aton" || v.Name != "SKOMVÆR" {
		t.Fatalf("after the first write: %+v", v)
	}
	if err := p.store.upsert([]record{at(10.5, -61.5, now.Add(-30*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if v := get(); v.Lat != 10.5 || p.store.mirror.cells[cellOf(59.5, 10.6)] != nil {
		t.Fatalf("a move left the vessel at %v or its old cell filed", v.Lat)
	}
	blank := newVessel() // back from the sweep: no position, no name, the default kind
	blank.Seen = now
	if err := p.store.upsert([]record{{mmsi: 992570001, v: blank, firstSeen: now}}); err != nil {
		t.Fatal(err)
	}
	if v := get(); v.Lat != 10.5 || v.Kind != "aton" || v.Name != "SKOMVÆR" || !v.Seen.Equal(now) {
		t.Fatalf("a blank return: %+v; want the stored position, kind, and name, seen now", v)
	}
	get().Name = "CHANGED" // a copy: the filed vessel is untouched
	if get().Name != "SKOMVÆR" {
		t.Fatal("a reader changed the filed vessel")
	}
	if err := p.store.importRows([]historyRow{{mmsi: 992570002, hasPos: true, lat: 1, lon: 2, last: now, first: now}}); err != nil {
		t.Fatal(err)
	}
	if p.store.mirror.len() != 2 {
		t.Fatalf("an imported vessel did not reach the mirror: %d", p.store.mirror.len())
	}
}

// Ties come back lower MMSI first, and too many terms are refused as SQLite refused them.
func TestMirrorTiesAndLimits(t *testing.T) {
	p := storePipeline(t)
	seen := time.Now().Truncate(time.Millisecond)
	var rows []record
	for _, mmsi := range []uint32{257000003, 257000001, 257000002} {
		v := newVessel()
		v.Seen = seen
		rows = append(rows, record{mmsi: mmsi, v: v, firstSeen: seen})
	}
	if err := p.store.upsert(rows); err != nil {
		t.Fatal(err)
	}
	got, err := p.store.find(recordQuery{since: seen.Add(-time.Second)})
	if err != nil || len(got) != 3 || got[0].mmsi != 257000001 || got[2].mmsi != 257000003 {
		t.Fatalf("tie order: %v %v", got, err)
	}
	if _, err := p.store.find(recordQuery{mmsis: make([]uint32, maxParams+1)}); !errors.Is(err, errTooManyTerms) {
		t.Errorf("past the term limit: %v", err)
	}
}

// A failed refresh is retried with the next one.
func TestMirrorRetriesAFailedRefresh(t *testing.T) {
	p := storePipeline(t)
	broken, err := openStore(t.TempDir() + "/broken.db")
	if err != nil {
		t.Fatal(err)
	}
	broken.close()
	m := p.store.mirror
	if err := m.refresh(broken, []uint32{257000009}); err == nil {
		t.Fatal("a refresh from a closed record succeeded")
	}
	now := unixMs(time.Now())
	// written behind the mirror's back, as the failed refresh left it
	if _, err := p.store.db.Exec(`INSERT INTO vessels (mmsi, name, seen, first_seen) VALUES (257000009, 'QUIET ONE', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := m.refresh(p.store, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.find(recordQuery{mmsis: []uint32{257000009}}); len(got) != 1 || got[0].v.Name != "QUIET ONE" {
		t.Fatalf("the failed vessel was not retried: %+v", got)
	}
}
