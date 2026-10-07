package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeVesselHistory serves the import's pages from fixed rows in MMSI order.
type fakeVesselHistory struct {
	rows []historyRow
	cap  int // rows per page, like a source that returns fewer than asked; 0 = none
}

func (f *fakeVesselHistory) page(_ context.Context, after uint32, limit int) ([]historyRow, error) {
	var out []historyRow
	for _, r := range f.rows {
		if r.mmsi > after && len(out) < limit && (f.cap == 0 || len(out) < f.cap) {
			out = append(out, r)
		}
	}
	return out, nil
}

func historyPipeline(t *testing.T, f *fakeVesselHistory) *Pipeline {
	p, _ := trackPipeline(t)
	p.vesselHistory = f.page
	return p
}

func TestImportReadsPastAShortPage(t *testing.T) {
	f := &fakeVesselHistory{cap: 2}
	for i := range 5 {
		f.rows = append(f.rows, historyRow{mmsi: uint32(257000001 + i), name: fmt.Sprintf("V%d", i), first: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	}
	p := historyPipeline(t, f)
	if n, err := p.importVessels(context.Background()); err != nil || n != 5 {
		t.Errorf("a source returning fewer rows than asked must not end the import: %d %v", n, err)
	}
}

// History holds MMSIs the fold keeps out, and the import keeps them out of the record too, reading on past a page
// that holds nothing else.
func TestImportSkipsInvalidMMSIs(t *testing.T) {
	month := time.Now().UTC().AddDate(0, -1, 0).Truncate(time.Second)
	f := &fakeVesselHistory{}
	for _, m := range []uint32{1, 123456789, 257000001, 999999999} {
		f.rows = append(f.rows, historyRow{mmsi: m, name: "DATAHUB", first: month, updated: month})
	}
	p := historyPipeline(t, f)
	importPage = 2
	t.Cleanup(func() { importPage = 20_000 })
	if n, err := p.importVessels(context.Background()); err != nil || n != 1 {
		t.Errorf("merged %d: %v", n, err)
	}
	for _, m := range []uint32{1, 123456789, 999999999} {
		if _, ok, _ := p.store.get(m); ok {
			t.Errorf("record has a row for %d", m)
		}
	}
	if _, ok, _ := p.store.get(257000001); !ok {
		t.Error("the valid vessel after a page of invalid ones was not imported")
	}
}

// Opening the record removes rows it already holds under MMSIs the fold keeps out.
func TestOpenStoreRemovesInvalidMMSIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiscast.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// The SQL and validMMSI must agree on every edge of every range and on every default.
	all := []uint32{1234567, 2573104, 25700001, 257000001, 1 << 30}
	for _, r := range invalidMMSIRanges {
		all = append(all, r[0], r[1])
		if r[0] > 0 {
			all = append(all, r[0]-1)
		}
		if r[1] < math.MaxUint32 {
			all = append(all, r[1]+1)
		}
	}
	for m := range defaultMMSIs {
		all = append(all, m, m+1)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	for _, m := range all {
		if _, err := st.db.Exec(`INSERT INTO vessels (mmsi, name, seen, first_seen) VALUES (?, 'X', 1, 1)`, m); err != nil {
			t.Fatal(err)
		}
	}
	for id, own := range map[string]uint32{"a": 555555555, "b": 257000001, "c": 0} {
		if _, err := st.db.Exec(`INSERT INTO stations (id, own) VALUES (?, ?)`, id, own); err != nil {
			t.Fatal(err)
		}
	}
	st.close()
	if st, err = openStore(path); err != nil {
		t.Fatal(err)
	}
	defer st.close()
	for id, want := range map[string]uint32{"a": 0, "b": 257000001, "c": 0} {
		var own uint32
		if err := st.db.QueryRow(`SELECT own FROM stations WHERE id = ?`, id).Scan(&own); err != nil || own != want {
			t.Errorf("station %s own vessel %d (%v), want %d", id, own, err, want)
		}
	}
	for _, m := range all {
		if _, ok, _ := st.get(m); ok != validMMSI(m) {
			t.Errorf("row for %d kept=%v, want %v", m, ok, validMMSI(m))
		}
	}
}

func TestImportMergesHistoryIntoTheRecord(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	month := now.AddDate(0, -1, 0)
	f := &fakeVesselHistory{rows: []historyRow{
		// Heard live today, and in history a month ago under an older name.
		{mmsi: 257000001, name: "OLD NAME", callsign: "OLDCS", shipType: 70, draught: 5.2, first: month, last: month,
			lat: 58, lon: 9, hasPos: true, updated: month, source: "kystverket"},
		// Only in history, with a static update after its last position: seen stays with the position, the
		// message its source describes.
		{mmsi: 257000002, name: "GONE", imo: 9123456, length: 120, beam: 20, first: month, last: month.Add(time.Hour),
			lat: 59.5, lon: 10.5, hasPos: true, updated: month.Add(2 * time.Hour), source: "digitraffic"},
		// Only in history, never with a position.
		{mmsi: 257000003, name: "STATIC ONLY", shipType: 30, first: month, updated: month},
		// Only in history, with no first sighting.
		{mmsi: 257000004, name: "BUOY", last: month, lat: 58, lon: 9, hasPos: true, source: "barentswatch"},
	}}
	p := historyPipeline(t, f)
	heardAgo(p, 257000001, "NORDIC STAR", 59.9, 10.7, time.Minute)
	importPage = 2 // four rows read in two pages
	t.Cleanup(func() { importPage = 20_000 })

	n, err := p.importVessels(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("merged %d: %v", n, err)
	}
	rec, _, _ := p.store.get(257000001)
	if rec.v.Name != "NORDIC STAR" || rec.v.Lat != 59.9 || !rec.firstSeen.Equal(month) || rec.v.Source != "kystverket" {
		t.Errorf("a live vessel keeps its name and position and gains its first sighting: %+v first %v", rec.v, rec.firstSeen)
	}
	rec, ok, _ := p.store.get(257000002)
	if !ok || rec.v.Name != "GONE" || !rec.v.HasPos || rec.v.Lat != 59.5 || rec.v.Source != "digitraffic" || rec.v.IMO != 9123456 ||
		rec.v.Length != 120 || rec.v.Beam != 20 || !rec.v.Seen.Equal(month.Add(time.Hour)) || !rec.firstSeen.Equal(month) {
		t.Errorf("a vessel only in history gets a row with its last position and particulars: %v %+v", ok, rec.v)
	}
	if rec, ok, _ := p.store.get(257000004); !ok || !rec.firstSeen.Equal(month) {
		t.Errorf("no first sighting falls back to the vessel's seen: %v %v", ok, rec.firstSeen)
	}
	rec, ok, _ = p.store.get(257000003)
	if !ok || rec.v.HasPos || rec.v.ShipType != 30 || !rec.v.Seen.Equal(month) {
		t.Errorf("a vessel with no position: %v %+v", ok, rec.v)
	}
	if w := get(t, p, "/v1/vessels/257000002"); w.Code != 200 || !strings.Contains(w.Body.String(), "digitraffic") {
		t.Errorf("an imported vessel answers its lookup with its credit line: %d %s", w.Code, w.Body)
	}

	// Running again changes nothing; an older first sighting from a later archive load moves first_seen back, and
	// a newer static update without a newer position leaves seen with the position.
	f.rows[1].first = month.AddDate(0, -1, 0)
	f.rows[1].updated = month.Add(3 * time.Hour)
	if _, err := p.importVessels(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = p.store.get(257000002)
	if !rec.firstSeen.Equal(month.AddDate(0, -1, 0)) || rec.v.Lat != 59.5 || !rec.v.Seen.Equal(month.Add(time.Hour)) {
		t.Errorf("backdated first_seen: %+v %v", rec.v, rec.firstSeen)
	}

	// A newer position from history clears the motion the older one carried, navigational status included.
	if _, err := p.store.db.Exec(`UPDATE vessels SET nav_status = 5 WHERE mmsi = 257000002`); err != nil {
		t.Fatal(err)
	}
	f.rows[1].last, f.rows[1].lat = month.Add(4*time.Hour), 59.6
	if _, err := p.importVessels(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = p.store.get(257000002)
	if rec.v.Lat != 59.6 || rec.v.NavStatus != 15 || !rec.v.Seen.Equal(month.Add(4*time.Hour)) {
		t.Errorf("a newer position from history: %+v", rec.v)
	}
}

func TestImportSchedule(t *testing.T) {
	f := &fakeVesselHistory{}
	p := historyPipeline(t, f)
	day := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)
	if p.importIfDue(day) {
		t.Fatal("an empty history counted as an import")
	}
	if last, _ := p.store.meta("vessels_import"); last != "" {
		t.Errorf("an empty import recorded as done: %q", last)
	}

	// The first import runs at once; later ones wait a day.
	f.rows = []historyRow{{mmsi: 257000002, name: "GONE", first: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
	if !p.importIfDue(day) {
		t.Fatal("the first import did not run")
	}
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{day.Add(12 * time.Hour), false}, // within a day
		{day.Add(25 * time.Hour), true},  // a day later
	} {
		if got := p.importIfDue(c.at); got != c.want {
			t.Errorf("at %v: ran %v, want %v", c.at, got, c.want)
		}
	}

	// An archive load makes the import due at once, so the vessels it brought in need not wait a day.
	p.importSoon()
	if !p.importIfDue(day.Add(26 * time.Hour)) {
		t.Error("the import did not run after an archive load")
	}
}

// The import's ClickHouse read pages through every vessel once, in MMSI order: positions_1m gives the first and
// last position, vessel_statics an archive's particulars and an earlier first row, and a vessel only
// vessel_statics knows comes in the page its MMSI falls in, past the last page of positions too.
func TestImportReadsClickHouse(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })

	t0 := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	at := func(mmsi uint32, ts time.Time, lat float64) trackPoint {
		return trackPoint{mmsi: mmsi, ts: ts, lat6: int32(lat * 600000), lon6: int32(10.7 * 600000), sog10: 1023, cog10: 3600, heading: 511,
			navStatus: 15, source: "aiscatcher:oslo", station: "aiscatcher:oslo", txAt: ts, txDisc: 1, recv: ts}
	}
	if err := conn.insert(ctx, "live", []trackPoint{at(257000001, t0, 59.1), at(257000001, t0.Add(24*time.Hour), 59.2), at(257000002, t0, 58.5)}); err != nil {
		t.Fatal(err)
	}
	archive := t0.AddDate(0, -3, 0)
	statics := func(mmsi uint32, name string) {
		t.Helper()
		if err := conn.conn.Exec(ctx, `INSERT INTO `+db+`.vessel_statics SELECT ?, 'marinecadastre', min(ts), max(ts), argMaxState((toInt32(0), toInt32(0)), ts),
			argMaxState(?, ts), argMaxState('WDC1234', ts), argMaxState(toUInt32(9123456), ts), argMaxState(toUInt8(70), ts),
			argMaxState(toUInt16(120), ts), argMaxState(toUInt16(20), ts), argMaxState(toUInt16(52), ts), argMaxState('', ts)
			FROM (SELECT toDateTime64(?, 3, 'UTC') AS ts)`, mmsi, name, archive); err != nil {
			t.Fatal(err)
		}
	}
	statics(257000000, "BEFORE EVERY POSITION")
	statics(257000002, "ARCHIVED")
	statics(257000005, "AFTER EVERY POSITION")

	got := map[uint32]historyRow{}
	var order []uint32
	for after := uint32(0); ; {
		page, err := conn.vesselHistory(ctx, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, h := range page {
			if _, dup := got[h.mmsi]; dup || h.mmsi <= after {
				t.Fatalf("vessel %d read twice or out of order", h.mmsi)
			}
			got[h.mmsi], order = h, append(order, h.mmsi)
		}
		after = page[len(page)-1].mmsi
	}
	if len(got) != 4 || !slices.IsSorted(order) {
		t.Fatalf("every vessel once, in order: %v", order)
	}
	if h := got[257000001]; !h.hasPos || !h.first.Equal(t0) || !h.last.Equal(t0.Add(24*time.Hour)) || h.lat != 59.2 || h.source != "aiscatcher" || h.name != "" {
		t.Errorf("a live vessel's first and last position: %+v", h)
	}
	if h := got[257000002]; !h.first.Equal(archive) || h.lat != 58.5 || h.name != "ARCHIVED" || h.imo != 9123456 || h.length != 120 || h.beam != 20 ||
		h.shipType != 70 || h.draught != 5.2 || h.callsign != "WDC1234" {
		t.Errorf("an archive's particulars and earlier first row: %+v", h)
	}
	for _, m := range []uint32{257000000, 257000005} {
		if h := got[m]; h.hasPos || !h.first.Equal(archive) || h.name == "" {
			t.Errorf("a vessel only vessel_statics knows: %+v", h)
		}
	}
}
