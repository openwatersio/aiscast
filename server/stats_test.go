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
			Total        int
			WithPosition int `json:"with_position"`
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
			VesselsExclusive int `json:"vessels_exclusive"`
		}
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Stations.Total != 3 || out.Stations.Active != 3 || out.Stations.BySource["udp"] != 1 || out.Stations.BySource["kystverket"] != 1 {
		t.Errorf("stations: %+v", out.Stations)
	}
	if out.Vessels.Total != 2 || out.Events.Last24h != 2 || out.Events.Last7d != 2 || out.Events.Duplicates.Last24h != 2 || out.Events.PerSecond != 0.2 {
		t.Errorf("vessels/events: %+v %+v", out.Vessels, out.Events)
	}
	if c := out.Clients; c.Streams != 1 || c.StreamsOpened.Last24h != 1 || c.StreamsOpened.Last7d != 1 || c.Requests.Last24h != 2 || c.Requests.Last7d != 2 {
		t.Errorf("clients: %+v", c)
	}
	if d, ok := out.Sources["digitraffic"]; !ok || d.Events.Last24h != 0 || d.Vessels != 1 {
		t.Errorf("dup-only source should still list its vessels: %+v", out.Sources["digitraffic"])
	}
	// udp heard 1 vessel, shared; kystverket heard it too (as a dup) plus one of its own.
	if u, k := out.Sources["udp"], out.Sources["kystverket"]; u.Events.Last24h != 1 || u.Vessels != 1 || u.VesselsExclusive != 0 || k.Events.Last7d != 1 || k.Vessels != 2 || k.VesselsExclusive != 1 {
		t.Errorf("sources: %+v", out.Sources)
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
	path := usagePath(t.TempDir() + "/vessels.json")
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
	path := usagePath(t.TempDir() + "/vessels.json")
	if err := p.saveUsage(path); err != nil {
		t.Fatal(err)
	}
	q := testPipeline(t)
	if err := q.loadUsage(path); err != nil {
		t.Fatal(err)
	}
	if rows := q.stations.rows(now); len(rows) != 0 { // not heard yet since restart: not listed
		t.Errorf("restored station listed before it reports: %+v", rows)
	}
	q.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"})
	if rows := q.stations.rows(now); len(rows) != 1 || rows[0].Events["last_24h"] != 2 {
		t.Errorf("station ring after restore: %+v", rows)
	}
}
