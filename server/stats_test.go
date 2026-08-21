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
	p.sampleRate(now)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
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
		Sources map[string]struct{ Events int }
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Stations.Total != 2 || out.Stations.Active != 2 || out.Stations.BySource["udp"] != 1 || out.Stations.BySource["kystverket"] != 1 {
		t.Errorf("stations: %+v", out.Stations)
	}
	if out.Vessels.Total != 1 || out.Events.Total != 1 || out.Events.Duplicates != 1 || out.Events.PerSecond != 0.1 {
		t.Errorf("vessels/events: %+v %+v", out.Vessels, out.Events)
	}
	if out.Sources["udp:abc"].Events != 1 {
		t.Errorf("sources: %+v", out.Sources)
	}
}
