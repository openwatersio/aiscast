package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	cases := []struct{ peer, xff, want string }{
		{"127.0.0.1:1234", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:1234", "10.0.0.1, 203.0.113.9", "203.0.113.9"},
		{"[::1]:1234", "203.0.113.9", "203.0.113.9"},
		{"198.51.100.7:1234", "203.0.113.9", "198.51.100.7"}, // a public peer cannot name its own address
		{"127.0.0.1:1234", "", "127.0.0.1"},
		{"127.0.0.1:1234", "unknown", "127.0.0.1"}, // a hop that is not an address falls back to the peer
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("peer %s xff %q: got %s want %s", c.peer, c.xff, got, c.want)
		}
	}
}

func TestRobotsDisallowsEverything(t *testing.T) {
	w := httptest.NewRecorder()
	httpHandler(testPipeline(t)).ServeHTTP(w, httptest.NewRequest("GET", "/robots.txt", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Disallow: /") {
		t.Errorf("robots.txt: %d %q", w.Code, w.Body.String())
	}
}

// A browser holding a token sends it in Authorization, which makes the request preflighted, so every JSON
// endpoint answers OPTIONS allowing that header. The query string keeps working for clients that need it.
func TestPreflightAllowsAuthorization(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	tok, _ := signToken(priv, Claims{Kid: kid, Sub: "web", Role: "personal", Exp: time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()

	for _, path := range []string{"/v1/vessels", "/v1/vessels/441754000", "/v1/vessels/441754000/track", "/v1/stations", "/v1/stats", "/mcp", "/openapi.json", "/v1/receive", "/v1/keys"} {
		req, _ := http.NewRequest("OPTIONS", srv.URL+path, nil)
		req.Header.Set("Origin", "https://example.test")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		allow := res.Header.Get("Access-Control-Allow-Headers")
		if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(allow, "Authorization") || res.Header.Get("Access-Control-Max-Age") == "" {
			t.Errorf("%s preflight: %d origin %q allow %q max-age %q", path, res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"), allow, res.Header.Get("Access-Control-Max-Age"))
		}
	}

	get := func(path, auth string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	const box = "/v1/vessels?bbox=59,10,60,11"
	if s := get(box, "Bearer "+tok); s != 200 {
		t.Errorf("Authorization header: %d", s)
	}
	if s := get(box+"&key="+tok, ""); s != 200 {
		t.Errorf("?key=: %d", s)
	}
	if s := get(box, "Bearer nope"); s != 401 {
		t.Errorf("bad Authorization header: %d want 401", s)
	}
}
