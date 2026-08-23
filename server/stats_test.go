package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStats(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.sampleRate(now.Add(-10 * time.Second))
	p.Ingest(Reception{Source: "udp:abc", Station: "udp:abc", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"}) // dup
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: now, Body: "!AIVDM,1,1,,A,15NJ5cPP00o?8pHG8CpSWwvP2<1h,0*6E"}) // only kystverket hears this one
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
			Total, Duplicates int
			PerSecond         float64 `json:"per_second"`
		}
		Clients struct {
			Streams       int
			StreamsOpened struct {
				Total    int64
				LastHour int64 `json:"last_hour"`
			} `json:"streams_opened"`
			Requests struct {
				Total    int64
				LastHour int64 `json:"last_hour"`
			}
		}
		Sources map[string]struct {
			Events, Vessels  int
			VesselsExclusive int `json:"vessels_exclusive"`
		}
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Stations.Total != 2 || out.Stations.Active != 2 || out.Stations.BySource["udp"] != 1 || out.Stations.BySource["kystverket"] != 1 {
		t.Errorf("stations: %+v", out.Stations)
	}
	if out.Vessels.Total != 2 || out.Events.Total != 2 || out.Events.Duplicates != 1 || out.Events.PerSecond != 0.2 {
		t.Errorf("vessels/events: %+v %+v", out.Vessels, out.Events)
	}
	if c := out.Clients; c.Streams != 1 || c.StreamsOpened.Total != 1 || c.StreamsOpened.LastHour != 1 || c.Requests.Total != 2 || c.Requests.LastHour != 2 {
		t.Errorf("clients: %+v", c)
	}
	// udp heard 1 vessel, shared; kystverket heard it too (as a dup) plus one of its own.
	if u, k := out.Sources["udp:abc"], out.Sources["kystverket"]; u.Events != 1 || u.Vessels != 1 || u.VesselsExclusive != 0 || k.Vessels != 2 || k.VesselsExclusive != 1 {
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
