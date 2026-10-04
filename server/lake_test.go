package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLake answers the lake's queries from fixed rows, the way R2 SQL encodes them.
type fakeLake struct {
	vessels []map[string]any // ais.vessels rows, in MMSI order
	cap     int              // rows per response, like an engine with a row limit; 0 = none
	empty   bool             // the catalog has no tables yet
	queries atomic.Int64
}

func (f *fakeLake) query(_ context.Context, q string, each func(map[string]json.RawMessage) error) error {
	f.queries.Add(1)
	if f.empty {
		return errLakeEmpty
	}
	var after, limit int
	fmt.Sscanf(q[strings.Index(q, "WHERE mmsi > "):], "WHERE mmsi > %d ORDER BY mmsi LIMIT %d", &after, &limit)
	var rows []map[string]any
	for _, r := range f.vessels {
		if r["mmsi"].(int) > after && len(rows) < limit && (f.cap == 0 || len(rows) < f.cap) {
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		m := map[string]json.RawMessage{}
		for k, v := range r {
			m[k], _ = json.Marshal(v)
		}
		if err := each(m); err != nil {
			return err
		}
	}
	return nil
}

func lakePipeline(t *testing.T, f *fakeLake) *Pipeline {
	p, _ := trackPipeline(t)
	p.lake = &lake{client: f}
	return p
}

func TestImportReadsPastAShortPage(t *testing.T) {
	f := &fakeLake{cap: 2}
	for i := range 5 {
		f.vessels = append(f.vessels, map[string]any{"mmsi": 257000001 + i, "name": fmt.Sprintf("V%d", i), "first_ts": "2026-09-01T00:00:00", "last_ts": nil})
	}
	p := lakePipeline(t, f)
	if n, err := p.importVessels(context.Background()); err != nil || n != 5 {
		t.Errorf("a query engine returning fewer rows than asked must not end the import: %d %v", n, err)
	}
}

func TestLakeEncodings(t *testing.T) {
	for raw, want := range map[string]string{`"2026-09-01T12:00:00.5"`: "2026-09-01T12:00:00.5Z", `"2026-09-01 12:00:00"`: "2026-09-01T12:00:00Z",
		`"2026-09-01T12:00:00Z"`: "2026-09-01T12:00:00Z", `1788264000500000`: "2026-09-01T12:00:00.5Z"} {
		got, err := lakeTime(json.RawMessage(raw))
		if err != nil || got.Format(time.RFC3339Nano) != want {
			t.Errorf("ts %s: %v %v", raw, got, err)
		}
	}
}

func TestImportMergesHistoryIntoTheRecord(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	month := now.AddDate(0, -1, 0)
	ts := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000000") }
	f := &fakeLake{vessels: []map[string]any{
		// Heard live today, and in history a month ago under an older name.
		{"mmsi": 257000001, "name": "OLD NAME", "callsign": "OLDCS", "ship_type": 70, "draught10": 52, "cls": "A",
			"first_ts": ts(month), "last_ts": ts(month), "last_lat6": 58 * 600000, "last_lon6": 9 * 600000, "updated_ts": ts(month), "last_source": "kystverket"},
		// Only in history, with a static update after its last position: seen stays with the position, the
		// message its source describes.
		{"mmsi": 257000002, "name": "GONE", "callsign": nil, "ship_type": nil, "draught10": nil, "cls": "B",
			"first_ts": ts(month), "last_ts": ts(month.Add(time.Hour)), "last_lat6": int(59.5 * 600000), "last_lon6": int(10.5 * 600000), "updated_ts": ts(month.Add(2 * time.Hour)), "last_source": "digitraffic"},
		// Only in history, never with a position.
		{"mmsi": 257000003, "name": "STATIC ONLY", "callsign": nil, "ship_type": 30, "draught10": nil, "cls": nil,
			"first_ts": ts(month), "last_ts": nil, "last_lat6": nil, "last_lon6": nil, "updated_ts": ts(month), "last_source": nil},
		// Only in history, with its first sighting cleared as a reset device clock's.
		{"mmsi": 257000004, "name": "BUOY", "callsign": nil, "ship_type": nil, "draught10": nil, "cls": nil,
			"first_ts": nil, "last_ts": ts(month), "last_lat6": 58 * 600000, "last_lon6": 9 * 600000, "updated_ts": nil, "last_source": "barentswatch"},
	}}
	p := lakePipeline(t, f)
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
	if !ok || rec.v.Name != "GONE" || !rec.v.HasPos || rec.v.Lat != 59.5 || rec.v.Class != "B" || rec.v.Source != "digitraffic" ||
		!rec.v.Seen.Equal(month.Add(time.Hour)) || !rec.firstSeen.Equal(month) {
		t.Errorf("a vessel only in history gets a row with its last position: %v %+v", ok, rec.v)
	}
	if rec, ok, _ := p.store.get(257000004); !ok || !rec.firstSeen.Equal(month) {
		t.Errorf("a null first_ts falls back to the vessel's seen: %v %v", ok, rec.firstSeen)
	}
	rec, ok, _ = p.store.get(257000003)
	if !ok || rec.v.HasPos || rec.v.ShipType != 30 || !rec.v.Seen.Equal(month) {
		t.Errorf("a vessel with no position: %v %+v", ok, rec.v)
	}
	if w := get(t, p, "/v1/vessels/257000002"); w.Code != 200 || !strings.Contains(w.Body.String(), "digitraffic") {
		t.Errorf("an imported vessel answers its lookup with its credit line: %d %s", w.Code, w.Body)
	}

	// Running again changes nothing; an older first sighting from a later backfill moves first_seen back, and a
	// newer static update without a newer position leaves seen with the position.
	f.vessels[1]["first_ts"] = ts(month.AddDate(0, -1, 0))
	f.vessels[1]["updated_ts"] = ts(month.Add(3 * time.Hour))
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
	f.vessels[1]["last_ts"], f.vessels[1]["last_lat6"] = ts(month.Add(4*time.Hour)), int(59.6*600000)
	if _, err := p.importVessels(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = p.store.get(257000002)
	if rec.v.Lat != 59.6 || rec.v.NavStatus != 15 || !rec.v.Seen.Equal(month.Add(4*time.Hour)) {
		t.Errorf("a newer position from history: %+v", rec.v)
	}
}

func TestImportSchedule(t *testing.T) {
	f := &fakeLake{empty: true}
	p := lakePipeline(t, f)
	day := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)
	if p.importIfDue(day) {
		t.Fatal("an empty lake counted as an import")
	}
	if last, _ := p.store.meta("vessels_import"); last != "" {
		t.Errorf("an empty import recorded as done: %q", last)
	}

	// The first import runs whatever the hour; later ones wait a day and for the packager's night to end.
	f.empty, f.vessels = false, []map[string]any{{"mmsi": 257000002, "name": "GONE", "first_ts": "2026-09-01T00:00:00", "last_ts": nil}}
	if !p.importIfDue(day) {
		t.Fatal("the first import did not run")
	}
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{day.Add(12 * time.Hour), false}, // within a day
		{day.Add(25 * time.Hour), false}, // a day later, but 02:00 UTC
		{day.Add(27 * time.Hour), true},  // a day later, after 03:00 UTC
	} {
		if got := p.importIfDue(c.at); got != c.want {
			t.Errorf("at %v: ran %v, want %v", c.at, got, c.want)
		}
	}
}
