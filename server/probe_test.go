package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	if !anonymousClaims("x").allowsArea(probeBoxes) {
		t.Fatal("probe boxes exceed the anonymous area cap")
	}
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	p.probeLast.Store(time.Now().Add(-3 * time.Minute).Unix())
	if rr := httptest.NewRecorder(); true {
		p.serveHealth(rr, httptest.NewRequest("GET", "/health", nil))
		if rr.Code != 503 || !strings.Contains(rr.Body.String(), "subscribers") {
			t.Fatalf("stale probe: %d %s", rr.Code, rr.Body)
		}
	}
	saved := probeBoxes
	probeBoxes = []bbox{{-90, -180, 90, 180}}
	defer func() { probeBoxes = saved }()
	go p.probeOnce("ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/stream")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
		rr := httptest.NewRecorder()
		p.serveHealth(rr, httptest.NewRequest("GET", "/health", nil))
		if rr.Code == 200 {
			return
		}
	}
	t.Fatal("probe never received an event")
}
