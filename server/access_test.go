package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	if a.Route != "/v1/vessels" || a.Query != "bbox=59.0%2C10.0%2C60.0%2C11.0" || a.Status != 200 || a.Bytes == 0 || a.Ms <= 0 {
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

// A search from the visitor's position, the view, and a deep tile are all logged to about 10 km.
func TestAccessLogCoarsensLocations(t *testing.T) {
	p := testPipeline(t)
	dir := t.TempDir()
	p.access = newAccessArchive(dir, nil)
	h := httpHandler(p)
	for _, target := range []string{
		"/v1/vessels?q=nordic&around=10.73519,59.91272",
		"/v1/vessels?bbox=59.91234,10.71234,59.93456,10.75678",
		"/v1/vessels/tiles/15/17361/9530",
		"/v1/vessels/tiles/5/16/9",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
	}
	lines, raw := readAccess(t, p, dir)
	for _, precise := range []string{"10.735", "59.912", "59.934", "10.756", "17361", "9530"} {
		if strings.Contains(raw, precise) {
			t.Errorf("%s reached the log:\n%s", precise, raw)
		}
	}
	got := map[string]accessLine{}
	for _, l := range lines {
		got[l.Path+"?"+l.Query] = l
	}
	for _, want := range []string{
		"/v1/vessels?around=10.7%2C59.9&q=nordic",
		"/v1/vessels?bbox=59.9%2C10.7%2C59.9%2C10.8",
		"/v1/vessels/tiles/12/2170/1191?",
		"/v1/vessels/tiles/5/16/9?",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("no line %s in %v", want, slices.Collect(maps.Keys(got)))
		}
	}
	if l := got["/v1/vessels/tiles/12/2170/1191?"]; l.Z != 15 {
		t.Errorf("deep tile zoom %d, want 15", l.Z)
	}
}

// No token or precise tile reaches the log by another parameter name, a path, a header, or an unrouted tile URL.
func TestAccessLogLeaksNothing(t *testing.T) {
	p := testPipeline(t)
	dir := t.TempDir()
	p.access = newAccessArchive(dir, nil)
	h := httpHandler(p)
	const tok = "ak1.eyJzdWIiOiJsZWFrIn0.c2ln"
	for _, target := range []string{
		"/v1/vessels?bbox=59,10,60,11&token=" + tok + "&Key=" + tok + "&api_key=" + tok,
		"/v1/vessels/" + tok,
		"/v1/vessels/tiles/15/17361/9530.pbf",
		"/v1/vessels/tiles/15/17361/9530/",
		"/v1/vessels/tiles/fifteen/x/y",
		"//v1/vessels/tiles/15/17361/9530",
		"/ais/v1/vessels/tiles/15/17361/9530.pbf",
		"/v1/vessels/tiles/tiles.json15/17361/9530.pbf",
		"/v1/vessels/59.91234,10.73456",
		"/v1/stations/15/17361/9530",
		"/v1/stations/station:ed25519:abc/n2k",
		"/59.91234,10.73456/x",
	} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("User-Agent", "client/1 key="+tok)
		r.Header.Set("Origin", "https://"+tok+".example")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	lines, raw := readAccess(t, p, dir)
	for _, leak := range []string{"eyJzdWIiOiJsZWFrIn0", "17361", "9530", "token=", "Key=", "api_key=", "59.91", "10.73"} {
		if strings.Contains(raw, leak) {
			t.Errorf("%s reached the log:\n%s", leak, raw)
		}
	}
	paths := map[string]int{}
	for _, l := range lines {
		paths[l.Path] = l.Z
	}
	if z, ok := paths["/v1/vessels/tiles/12/2170/1191"]; !ok || z != 15 {
		t.Errorf("a .pbf tile was not logged coarse: %v", paths)
	}
	if _, ok := paths["/v1/vessels/tiles/invalid"]; !ok {
		t.Errorf("a junk tile path was not logged as invalid: %v", paths)
	}
	for _, want := range []string{"/v1/vessels/x", "/v1/stations/x", "/x", "/v1/stations/station:ed25519:abc/n2k"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("no line for %s: %v", want, paths)
		}
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

// The access log uploads to a bucket of its own, never the archive's, which is meant to become public.
func TestAccessLogNeverUsesTheArchiveBucket(t *testing.T) {
	t.Setenv("R2_ACCOUNT_ID", "acct")
	t.Setenv("R2_ACCESS_KEY_ID", "id")
	t.Setenv("R2_SECRET_ACCESS_KEY", "secret")
	t.Setenv("R2_BUCKET", "ais-archive")
	for bucket, want := range map[string]string{"": "", "ais-archive": "", "ais-access": "ais-access"} {
		t.Setenv("ACCESS_BUCKET", bucket)
		got := ""
		if c := accessStoreFromEnv(); c != nil {
			got = c.bucket
		}
		if got != want {
			t.Errorf("ACCESS_BUCKET=%q uploads to %q, want %q", bucket, got, want)
		}
	}
}

// Without a bucket the access log keeps its hours on disk only for accessRetention.
func TestAccessLogExpiresWithoutABucket(t *testing.T) {
	dir := t.TempDir()
	a := newAccessArchive(dir, nil)
	old, recent := filepath.Join(dir, "access", "v1", "old.gz"), filepath.Join(dir, "access", "v1", "recent.gz")
	os.MkdirAll(filepath.Dir(old), 0o755)
	for _, f := range []string{old, recent} {
		os.WriteFile(f, []byte("x"), 0o644)
	}
	stale := time.Now().Add(-accessRetention - time.Hour)
	os.Chtimes(old, stale, stale)
	other := filepath.Join(dir, "CC0-1.0", "station", "x", "old.gz") // another archive's hour, should ACCESS_DIR cover it
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, []byte("x"), 0o644)
	os.Chtimes(other, stale, stale)
	a.sweep()
	if _, err := os.Stat(other); err != nil {
		t.Errorf("expiry reached outside access/: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an hour past retention survived the sweep")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a recent hour was deleted: %v", err)
	}
	a.shutdown()
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
