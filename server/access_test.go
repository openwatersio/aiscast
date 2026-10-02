package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readAccess shuts the access log down and reads back every line it wrote.
func readAccess(t *testing.T, p *Pipeline, dir string) (lines []accessLine, raw string) {
	t.Helper()
	p.access.shutdown()
	files, _ := filepath.Glob(filepath.Join(dir, "access", "v1", "*", "*", "*", "*.gz"))
	if len(files) == 0 {
		t.Fatal("no access log written")
	}
	var all strings.Builder
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(fh)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(zr)
		for sc.Scan() {
			all.WriteString(sc.Text() + "\n")
			var l accessLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				t.Fatalf("%v: %s", err, sc.Text())
			}
			lines = append(lines, l)
		}
		fh.Close()
	}
	return lines, all.String()
}

func TestAccessLog(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	dir := t.TempDir()
	p.access = newAccessArchive(dir, nil)
	kid, priv := testIssuer(t, p)
	tok, _ := signToken(priv, Claims{Kid: kid, Sub: "fleet-co", Role: "partner", Exp: time.Now().Add(time.Hour).Unix()})
	h := httpHandler(p)

	// Through Caddy, with a token in the query and a page URL that carries one too.
	r := httptest.NewRequest("GET", "/v1/vessels?bbox=59,10,60,11&key="+tok, nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.77")
	r.Header.Set("X-Request-Id", "req-1")
	r.Header.Set("User-Agent", "maplibre-test")
	r.Header.Set("Referer", "https://charts.example/map?key="+tok+"#z5")
	h.ServeHTTP(httptest.NewRecorder(), r)

	// Straight to the server, anonymous, naming its own request id; and a tile, by the route pattern.
	r = httptest.NewRequest("GET", "/v1/vessels/tiles/3/4/2", nil)
	r.RemoteAddr = "198.51.100.9:6000"
	r.Header.Set("X-Request-Id", "forged")
	h.ServeHTTP(httptest.NewRecorder(), r)

	lines, raw := readAccess(t, p, dir)
	if strings.Contains(raw, tok) || strings.Contains(raw, "203.0.113.77") || strings.Contains(raw, "198.51.100.9") || strings.Contains(raw, "anon:") {
		t.Fatalf("a token or a full address reached the log:\n%s", raw)
	}
	if len(lines) != 2 {
		t.Fatalf("%d lines:\n%s", len(lines), raw)
	}
	a, b := lines[0], lines[1]
	if a.Route != "/v1/vessels" || a.Query != "bbox=59%2C10%2C60%2C11" || a.Status != 200 || a.Bytes == 0 || a.Ms <= 0 {
		t.Errorf("request: %+v", a)
	}
	if a.ID != "req-1" || a.Sub != "fleet-co" || a.Role != "partner" || a.UA != "maplibre-test" || a.Referer != "https://charts.example/map" {
		t.Errorf("caller: %+v", a)
	}
	if a.Net != "203.0.113.0/24" || len(a.Client) != 16 {
		t.Errorf("client: %q %q", a.Net, a.Client)
	}
	if b.Route != "/v1/vessels/tiles/{z}/{x}/{y}" || b.Path != "/v1/vessels/tiles/3/4/2" || b.ID != "" || b.Sub != "" || b.Role != "" {
		t.Errorf("anonymous tile: %+v", b)
	}
	if b.Net != "198.51.100.0/24" || b.Client == a.Client {
		t.Errorf("anonymous client: %q %q", b.Net, b.Client)
	}
	if _, err := time.Parse(time.RFC3339Nano, a.T); err != nil {
		t.Errorf("t: %v", err)
	}
}

func TestClientNet(t *testing.T) {
	for ip, want := range map[string]string{
		"203.0.113.77":        "203.0.113.0/24",
		"::ffff:203.0.113.77": "203.0.113.0/24",
		"2001:db8:1:2::5":     "2001:db8:1::/48",
		"not an address":      "",
	} {
		if got, _ := clientNet(ip); got != want {
			t.Errorf("clientNet(%q) = %q, want %q", ip, got, want)
		}
	}
	_, v4 := clientNet("203.0.113.77")
	_, mapped := clientNet("::ffff:203.0.113.77")
	if v4 != mapped {
		t.Error("a mapped address hashes apart from its IPv4 form")
	}
	if v4[:12] == strings.TrimPrefix(udpStation("203.0.113.77"), "udp:") {
		t.Error("the access hash matches the UDP station id")
	}
}

// A full queue drops lines rather than holding up the request.
func TestAccessLogDropsWhenBehind(t *testing.T) {
	p := testPipeline(t)
	p.access = &archive{dir: "x", ch: make(chan Reception, 1)} // no writer draining it
	r, n := withAccessNote(httptest.NewRequest("GET", "/health", nil))
	p.logAccess(r, n, "/health", 200, 0, time.Now(), time.Now())
	p.logAccess(r, n, "/health", 200, 0, time.Now(), time.Now())
	if p.accessDropped.Load() != 1 {
		t.Fatalf("dropped %d", p.accessDropped.Load())
	}
}

// A request that finishes after the access log shut down is counted, not left in a queue nothing reads.
func TestAccessLogCountsLinesAfterShutdown(t *testing.T) {
	p := testPipeline(t)
	p.access = newAccessArchive(t.TempDir(), nil)
	p.access.shutdown()
	r, n := withAccessNote(httptest.NewRequest("GET", "/health", nil))
	p.logAccess(r, n, "/health", 200, 0, time.Now(), time.Now())
	if p.accessDropped.Load() != 1 {
		t.Fatalf("dropped %d after shutdown", p.accessDropped.Load())
	}
}
