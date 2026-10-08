package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type windows struct {
	Last24h int64 `json:"last_24h"`
	Last7d  int64 `json:"last_7d"`
}

func TestStats(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.sampleRate(now.Add(-10 * time.Second))
	p.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})   // dup
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})   // only kystverket hears this one
	p.Ingest(Reception{Source: "digitraffic", Station: "digitraffic", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // only ever a duplicate
	// The station series' per-source figures, as ClickHouse would have them for what was heard above.
	series := &fakeSeries{sources: map[string][2]int{"udp": {1, 0}, "kystverket": {2, 1}, "digitraffic": {1, 0}}}
	p.attachClickHouse(&chStore{w: &fakeCH{}, series: series})
	p.refreshRollups(series, now)
	p.sampleRate(now)
	p.subscribe() // one open stream
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	http.Get(srv.URL + "/v1/vessels")
	http.Get(srv.URL + "/health") // not an API request
	res, _ := http.Get(srv.URL + "/v1/stats")
	var out struct {
		Stations struct {
			Total, Active int
			BySource      map[string]int `json:"by_source"`
		}
		Vessels struct {
			Total, Active int
			WithPosition  int  `json:"with_position"`
			Last24h       *int `json:"last_24h"`
		}
		Events struct {
			Last24h    int64   `json:"last_24h"`
			Last7d     int64   `json:"last_7d"`
			Duplicates windows `json:"duplicates"`
			PerSecond  float64 `json:"per_second"`
		}
		Clients struct {
			Streams       int
			StreamsOpened windows `json:"streams_opened"`
			Requests      windows
		}
		Sources map[string]struct {
			Events           windows
			Vessels          int
			VesselsExclusive int   `json:"vessels_exclusive"`
			LastAgeS         int64 `json:"last_age_s"`
		}
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Stations.Total != 3 || out.Stations.Active != 3 || out.Stations.BySource["udp"] != 1 || out.Stations.BySource["kystverket"] != 1 {
		t.Errorf("stations: %+v", out.Stations)
	}
	if out.Vessels.Active != 2 || out.Vessels.Last24h != nil { // without a record, total is the cache and there are no windows
		t.Errorf("vessels without a record: %+v", out.Vessels)
	}
	if out.Vessels.Total != 2 || out.Events.Last24h != 2 || out.Events.Last7d != 2 || out.Events.Duplicates.Last24h != 2 || out.Events.PerSecond != 0.2 {
		t.Errorf("vessels/events: %+v %+v", out.Vessels, out.Events)
	}
	if c := out.Clients; c.Streams != 1 || c.StreamsOpened.Last24h != 1 || c.StreamsOpened.Last7d != 1 || c.Requests.Last24h != 2 || c.Requests.Last7d != 2 {
		t.Errorf("clients: %+v", c)
	}
	if d, ok := out.Sources["digitraffic"]; !ok || d.Events.Last24h != 0 || d.Vessels != 1 || d.LastAgeS > 5 {
		t.Errorf("dup-only source should still list its vessels: %+v", out.Sources["digitraffic"])
	}
	// udp heard 1 vessel, shared; kystverket heard it too (as a dup) plus one of its own.
	if u := out.Sources["udp"]; u.LastAgeS > 5 {
		t.Errorf("udp kind last_age_s should be fresh, got %d", u.LastAgeS)
	}
	if u, k := out.Sources["udp"], out.Sources["kystverket"]; u.Events.Last24h != 1 || u.Vessels != 1 || u.VesselsExclusive != 0 || k.Events.Last7d != 1 || k.Vessels != 2 || k.VesselsExclusive != 1 {
		t.Errorf("sources: %+v", out.Sources)
	}
}

// recordSeed is a pipeline whose record holds vessels heard over the last 40 days, one of them still in the
// cache, with its counts refreshed.
func recordSeed(t *testing.T) *Pipeline {
	t.Helper()
	p := storePipeline(t)
	now := time.Now()
	put := func(mmsi uint32, seen time.Time, lat, lon float64) {
		v := newVessel()
		v.Seen, v.Source = seen, "aishub"
		if lat != 0 {
			v.HasPos, v.Lat, v.Lon, v.PosAt = true, lat, lon, seen
		}
		if err := p.store.upsert([]record{{mmsi: mmsi, v: v}}); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	put(257000001, now.Add(-40*day), 59.1, 10.1) // heard once, before every window
	put(257000002, now.Add(-20*day), 0, 0)       // first heard 20 days ago...
	put(257000002, now.Add(-time.Hour), 0, 0)    // ...and again today
	put(257000003, now.Add(-3*day), 59.5, 10.5)  // first and last heard 3 days ago
	put(257000004, now.Add(-time.Hour), 0, 0)    // first heard today
	heardAgo(p, 257000005, "LIVE", 59.9, 10.7, time.Minute)
	p.refreshRecordCounts(now)
	return p
}

func TestStatsCountsTheRecord(t *testing.T) {
	var out struct {
		Vessels struct {
			Total, Active int
			Last24h       int `json:"last_24h"`
			Last7d        int `json:"last_7d"`
			Last30d       int `json:"last_30d"`
			New           map[string]int
		}
	}
	json.Unmarshal(get(t, recordSeed(t), "/v1/stats").Body.Bytes(), &out)
	v := out.Vessels
	if v.Total != 5 || v.Active != 1 {
		t.Errorf("total is every vessel in the record, active the cache: %+v", v)
	}
	if v.Last24h != 3 || v.Last7d != 4 || v.Last30d != 4 {
		t.Errorf("heard per window: %+v", v)
	}
	if v.New["last_24h"] != 2 || v.New["last_7d"] != 3 || v.New["last_30d"] != 4 {
		t.Errorf("first heard per window: %+v", v.New)
	}
}

func TestRootRedirect(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testPipeline(t)))
	defer srv.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ := c.Get(srv.URL + "/")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://openwaters.io/ais/" {
		t.Fatalf("got %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ = c.Get(srv.URL + "/nope"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("/nope: got %d, want 404", res.StatusCode)
	}
}

func TestUsageSurvivesRestart(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	for range 3 {
		p.usage.requests.add(now)
	}
	p.usage.streams.add(now.Add(-30 * time.Hour))    // outside 24 h, inside 7 d
	p.usage.events.add(now.Add(-8 * 24 * time.Hour)) // outside both
	p.usage.source("kystverket").add(now)
	p.usage.source("udp:gone").add(now.Add(-8 * 24 * time.Hour)) // silent for the whole window: pruned, not saved
	path := t.TempDir() + "/vessels-usage.json"
	if err := p.saveUsage(path); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.loadUsage(path); err != nil {
		t.Fatal(err)
	}
	if d, w := q.usage.requests.sum(now, 24), q.usage.requests.sum(now, 7*24); d != 3 || w != 3 {
		t.Errorf("requests after restore: 24h %d 7d %d", d, w)
	}
	if d, w := q.usage.streams.sum(now, 24), q.usage.streams.sum(now, 7*24); d != 0 || w != 1 {
		t.Errorf("streams after restore: 24h %d 7d %d", d, w)
	}
	if w := q.usage.events.sum(now, 7*24); w != 0 {
		t.Errorf("events older than 7 d still counted: %d", w)
	}
	if d := q.usage.source("kystverket").sum(now, 24); d != 1 {
		t.Errorf("source ring after restore: %d", d)
	}
	if names := q.usage.sourceNames(now); len(names) != 1 || names[0] != "kystverket" {
		t.Errorf("stale source not pruned: %v", names)
	}
}

func TestStationRingSurvivesRestart(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	path := t.TempDir() + "/vessels-usage.json"
	if err := p.saveUsage(path); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.loadUsage(path); err != nil {
		t.Fatal(err)
	}
	if rows := q.stations.rows(now, nil); len(rows) != 0 { // not heard yet since restart: not listed
		t.Errorf("restored station listed before it reports: %+v", rows)
	}
	q.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})
	if rows := q.stations.rows(now, nil); len(rows) != 1 || rows[0].Events["last_24h"] != 2 {
		t.Errorf("station ring after restore: %+v", rows)
	}
}
