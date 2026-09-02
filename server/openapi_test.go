package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wsOnly routes stream over WebSocket/SSE and are pointed at docs/API.md by info.description instead of a
// paths entry. /v1/nmea is also WebSocket but keeps a paths entry for its pre-upgrade HTTP refusals.
var wsOnly = map[string]bool{"/v0/stream": true, "/v1/stream": true}

func TestOpenAPIMatchesMux(t *testing.T) {
	var spec struct {
		OpenAPI string `json:"openapi"`
		Info    struct{ Description string }
		Paths   map[string]json.RawMessage
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Errorf("openapi = %q, want 3.x", spec.OpenAPI)
	}

	mux := map[string]bool{}
	for pat := range routes(newPipeline(newArchive("", nil))) {
		mux[pat] = true
	}

	for path := range spec.Paths {
		pat := path
		if i := strings.Index(pat, "{"); i >= 0 {
			pat = pat[:i] // /v1/stations/{id} is served by the /v1/stations/ subtree
		}
		if !mux[pat] {
			t.Errorf("openapi.json documents %s but the mux has no %s handler", path, pat)
		}
	}

	documented := map[string]bool{}
	for path := range spec.Paths {
		if i := strings.Index(path, "{"); i >= 0 {
			path = path[:i]
		}
		documented[path] = true
	}
	for pat := range mux {
		if !strings.HasPrefix(pat, "/v1/") && pat != "/health" {
			continue // /metrics and the root redirect are not API surface
		}
		if wsOnly[pat] {
			if !strings.Contains(spec.Info.Description, "`"+pat+"`") {
				t.Errorf("%s is WebSocket-only and must be named in info.description", pat)
			}
			continue
		}
		if !documented[pat] {
			t.Errorf("mux serves %s but openapi.json does not document it", pat)
		}
	}
}

func TestServeOpenAPI(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testPipeline(t)))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("status %d, ACAO %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
}
