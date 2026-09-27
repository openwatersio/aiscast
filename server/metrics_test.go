package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMetrics(t *testing.T) {
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	get := func(path string) string {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	// The fan-out counter moves after the write returns, so the client can read the event first.
	waitFor := func(lines ...string) {
		t.Helper()
		var m string
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			m = get("/metrics")
			missing := false
			for _, l := range lines {
				missing = missing || !strings.Contains(m, l+"\n")
			}
			if !missing {
				return
			}
		}
		for _, l := range lines {
			if !strings.Contains(m, l+"\n") {
				t.Errorf("/metrics lacks %q", l)
			}
		}
	}

	get("/v1/vessels?bbox=49,0,50,1")
	get("/nope")

	// The upgrade goes through the status-recording wrapper, so this also checks it still hijacks.
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if _, _, err := c.Read(ctx); err != nil { // welcome
		t.Fatal(err)
	}
	wsWriteJSON(ctx, c, v1Frame{Type: "subscribe"})
	time.Sleep(50 * time.Millisecond) // let the reader store the subscription
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(
		`aiscast_http_requests_total{route="/v1/vessels",status="200"} 1`,
		`aiscast_http_requests_total{route="other",status="404"} 1`,
		`aiscast_http_request_duration_seconds_count{route="/v1/vessels"} 1`,
		`aiscast_http_request_duration_seconds_bucket{route="/v1/vessels",le="+Inf"} 1`,
		`aiscast_http_request_duration_seconds_count{route="/mcp"} 0`,
		`aiscast_streams{protocol="v1",tier="admin"} 1`, // ALLOW_ANON in tests: a tokenless socket is admin
		`aiscast_streams{protocol="mqtt",tier="feeder"} 0`,
		`aiscast_fanout_sends_total{protocol="v1"} 1`,
		fmt.Sprintf(`aiscast_fanout_bytes_total{protocol="v1"} %d`, len(msg)),
		`aiscast_fanout_sends_total{protocol="v0"} 0`,
		fmt.Sprintf("process_start_time_seconds %d", bootTime.Unix()),
	)
	m := get("/metrics")
	for _, prefix := range []string{"process_cpu_seconds_total ", "go_goroutines ", `aiscast_source_delay_seconds{source="t",quantile="0.99"} `} {
		if !strings.Contains(m, "\n"+prefix) {
			t.Errorf("/metrics lacks %q", prefix)
		}
	}

	c.Close(websocket.StatusNormalClosure, "")
	waitFor(`aiscast_streams{protocol="v1",tier="admin"} 0`, `aiscast_http_requests_total{route="/v1/stream",status="101"} 1`)
}

func TestTierOf(t *testing.T) {
	cases := map[string]*Claims{
		"anonymous": anonymousClaims("192.0.2.1"),
		"personal":  {Role: "personal"},
		"feeder":    {Role: "personal", Feeder: true},
		"partner":   {Role: "partner"},
		"other":     {Role: "mystery"},
	}
	for want, c := range cases {
		if got := tierOf(c); got != want {
			t.Errorf("%+v: tier %q, want %q", c, got, want)
		}
	}
}
