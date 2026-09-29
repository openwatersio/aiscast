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
	positions []map[string]any // day, ts, lat6, lon6, sog10, cog10, heading, navstat, source
	vessels   []map[string]any // ais.vessels rows, in MMSI order
	cap       int              // rows per response, like an engine with a row limit; 0 = none
	empty     bool             // the catalog has no tables yet
	queries   atomic.Int64
}

func (f *fakeLake) query(_ context.Context, q string) ([]map[string]json.RawMessage, error) {
	f.queries.Add(1)
	if f.empty {
		return nil, errLakeEmpty
	}
	rows := f.positions
	if strings.Contains(q, "ais.vessels") {
		var after, limit int
		fmt.Sscanf(q[strings.Index(q, "WHERE mmsi > "):], "WHERE mmsi > %d ORDER BY mmsi LIMIT %d", &after, &limit)
		rows = nil
		for _, r := range f.vessels {
			if r["mmsi"].(int) > after && len(rows) < limit && (f.cap == 0 || len(rows) < f.cap) {
				rows = append(rows, r)
			}
		}
	}
	var out []map[string]json.RawMessage
	for _, r := range rows {
		m := map[string]json.RawMessage{}
		for k, v := range r {
			m[k], _ = json.Marshal(v)
		}
		out = append(out, m)
	}
	return out, nil
}

// lakePosition is a lake row for mmsi 257000001 at t, in the lake's encodings.
func lakePosition(t time.Time, lat float64) map[string]any {
	return map[string]any{"day": t.UTC().Format("2006-01-02"), "ts": t.UTC().Format("2006-01-02T15:04:05.000000"),
		"lat6": int(lat * 600000), "lon6": int(10.7 * 600000), "sog10": 112, "cog10": 1234, "heading": 124, "navstat": -1, "source": "digitraffic"}
}

func lakePipeline(t *testing.T, f *fakeLake) *Pipeline {
	p, _ := trackPipeline(t)
	p.lake = &lake{client: f, cache: p.tracks}
	return p
}

func TestTrackStitchesTheLake(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour).Truncate(time.Hour)
	unsourced := lakePosition(old.Add(20*time.Minute), 59.2)
	unsourced["source"] = nil // packaged before positions carried their source
	f := &fakeLake{positions: []map[string]any{lakePosition(old, 59.0), lakePosition(old.Add(10*time.Minute), 59.1), unsourced}}
	p := lakePipeline(t, f)
	sail(t, p, 257000001, 2*time.Hour, time.Hour)

	from := now.Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from)
	if tr.Properties.Points != 5 || tr.Attribution["digitraffic"] == "" || tr.Attribution["kystverket"] == "" {
		t.Fatalf("lake and track store together: %+v %v", tr.Properties, tr.Attribution)
	}
	if first, _ := time.Parse(time.RFC3339, tr.Properties.Times[0]); !first.Equal(old) {
		t.Errorf("oldest first, from the lake: %v", tr.Properties.Times)
	}
	if tr.Properties.Sog[0] == nil || *tr.Properties.Sog[0] != 11.2 {
		t.Errorf("lake encodings decode: %v", tr.Properties.Sog[0])
	}

	// Every vessel-day is read once.
	n := f.queries.Load()
	getTrack(t, p, "/v1/vessels/257000001/track?from="+from)
	if f.queries.Load() != n {
		t.Errorf("a cached day was read again: %d queries, then %d", n, f.queries.Load())
	}

	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from+"&interval=15m"); tr.Properties.Points != 4 {
		t.Errorf("thinned across both: %d", tr.Properties.Points)
	}
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from+"&limit=3&interval=0"); tr.Properties.Points != 3 || !tr.Properties.Truncated {
		t.Errorf("the newest positions win the limit: %+v", tr.Properties)
	} else if first, _ := time.Parse(time.RFC3339, tr.Properties.Times[0]); !first.Equal(old.Add(20 * time.Minute)) {
		t.Errorf("limit kept the wrong lake positions: %v", tr.Properties.Times)
	} else if tr.Attribution["digitraffic"] != "" || tr.Attribution["kystverket"] == "" {
		t.Errorf("credits only the sources of the positions returned, from the cache: %v", tr.Attribution)
	}
}

func TestTrackArchiveIsATier(t *testing.T) {
	p := lakePipeline(t, &fakeLake{})
	sail(t, p, 257000001, time.Hour)
	allowAnon = false
	t.Cleanup(func() { allowAnon = true })
	from := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-2*24*time.Hour - time.Hour).UTC().Format(time.RFC3339)
	if w := get(t, p, "/v1/vessels/257000001/track?from="+from+"&to="+old); w.Code != 403 || !strings.Contains(w.Body.String(), "feeder") {
		t.Errorf("anonymous past the window: %d %s", w.Code, w.Body)
	}
	if w := get(t, p, "/v1/vessels/257000001/track"); w.Code != 200 {
		t.Errorf("anonymous inside the window: %d", w.Code)
	}
	// A range that overlaps the window is clamped to it, as a client asking for 48 hours a moment early does.
	early := time.Now().Add(-trackWindow - time.Second).UTC().Format(time.RFC3339)
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+early); tr.Properties.Points == 0 {
		t.Errorf("anonymous overlapping the window: %+v", tr.Properties)
	}
	var out mcpTrack
	if msg := mcpCall(t, mcpClient(t, p), "get_vessel_track", map[string]any{"mmsi": 257000001, "from": from, "to": old}, &out); !strings.Contains(msg, "feeder") {
		t.Errorf("MCP past the window: %q", msg)
	}
	allowAnon = true
	long := time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if w := get(t, p, "/v1/vessels/257000001/track?from="+long); w.Code != 400 {
		t.Errorf("more than 7 days: %d", w.Code)
	}
}

func TestLakeEmptyAndStaleDays(t *testing.T) {
	f := &fakeLake{empty: true}
	p := lakePipeline(t, f)
	sail(t, p, 257000001, time.Hour)
	from := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if tr := getTrack(t, p, "/v1/vessels/257000001/track?from="+from); tr.Properties.Points != 1 {
		t.Errorf("an empty lake is no history, not an error: %+v", tr.Properties)
	}

	// A day inside the repackaging week, and an empty day of any age, is read again once its cache entry is
	// older than lakeRecentTTL; an old day with positions never is.
	f.empty = false
	now := time.Now()
	recent, ancient, bare := now.Add(-3*24*time.Hour), now.Add(-20*24*time.Hour), now.Add(-40*24*time.Hour)
	for _, d := range []time.Time{recent, ancient} { // positions in each day and in the partition after it
		f.positions = append(f.positions, lakePosition(d, 59), lakePosition(d.Add(24*time.Hour), 59))
	}
	ctx := context.Background()
	for _, d := range []time.Time{recent, ancient, bare} {
		p.lake.days(ctx, 257000001, d, d, now)
	}
	n := f.queries.Load()
	p.lake.days(ctx, 257000001, ancient, ancient, now.Add(30*24*time.Hour))
	if f.queries.Load() != n {
		t.Error("an old day with positions was read again")
	}
	for name, d := range map[string]time.Time{"recent": recent, "empty": bare} {
		n := f.queries.Load()
		p.lake.days(ctx, 257000001, d, d, now.Add(lakeRecentTTL+time.Minute))
		if f.queries.Load() == n {
			t.Errorf("a %s day was not read again after its TTL", name)
		}
	}
}

// The lake partitions by the day a position arrived. A satellite relay arrives hours late, so a position sent
// before midnight can sit in the next day's partition, and a null speed is not a speed of zero.
func TestLakeLateReportsAndNulls(t *testing.T) {
	now := time.Now()
	day := now.Add(-5 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	late := lakePosition(day.Add(23*time.Hour), 59.3)
	late["day"] = day.Add(24 * time.Hour).Format("2006-01-02") // received after midnight
	early := lakePosition(day.Add(22*time.Hour), 59.2)
	early["sog10"] = nil
	f := &fakeLake{positions: []map[string]any{lakePosition(day.Add(time.Hour), 59.1), late, early}}
	p := lakePipeline(t, f)
	sail(t, p, 257000001, time.Hour)
	q := fmt.Sprintf("/v1/vessels/257000001/track?from=%s&to=%s", day.Format(time.RFC3339), day.Add(24*time.Hour-time.Second).Format(time.RFC3339))
	tr := getTrack(t, p, q)
	if tr.Properties.Points != 3 || tr.Properties.Times[2] != day.Add(23*time.Hour).Format(time.RFC3339) {
		t.Fatalf("the late report is in the track, in order: %v", tr.Properties.Times)
	}
	if tr.Properties.Sog[1] != nil {
		t.Errorf("a null speed reads as %v", *tr.Properties.Sog[1])
	}
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
	for raw, want := range map[string]string{`"2026-09-01"`: "2026-09-01", `20697`: "2026-09-01"} {
		if got, err := lakeDate(json.RawMessage(raw)); err != nil || got != want {
			t.Errorf("day %s: %q %v", raw, got, err)
		}
	}
	pts := []trackPoint{{mmsi: 1, ts: time.UnixMilli(1788264000500).UTC(), lat6: -35000000, lon6: 108000000, sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "digitraffic"},
		{mmsi: 1, ts: time.UnixMilli(1788264001500).UTC(), navStatus: 15},
		{mmsi: 1, ts: time.UnixMilli(1788264002500).UTC(), navStatus: 15, source: "aishub"}}
	if b, sources := encodeLakePoints(pts); fmt.Sprint(decodeLakePoints(1, b, sources)) != fmt.Sprint(pts) {
		t.Errorf("round trip: %+v", decodeLakePoints(1, b, sources))
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

func TestLakeCacheTrimsByBytes(t *testing.T) {
	p, _ := trackPipeline(t)
	now := time.Now()
	points := make([]trackPoint, 10) // 230 bytes
	for i, day := range []string{"2026-01-01", "2026-01-02", "2026-01-03"} {
		if err := p.tracks.lakeStore(257000001, day, lakeDay{points: points}, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.tracks.lakeTrim(500); err != nil {
		t.Fatal(err)
	}
	var kept []string
	rows, err := p.tracks.db.Query(`SELECT day FROM lake_days ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		rows.Scan(&d)
		kept = append(kept, d)
	}
	if strings.Join(kept, ",") != "2026-01-02,2026-01-03" {
		t.Errorf("the least recently fetched day goes first: %v", kept)
	}
}
