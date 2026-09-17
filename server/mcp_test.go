package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSeed fills a pipeline with a small Oslo fjord and Helsinki scene: two Norwegian ships, an aid to
// navigation, a Finnish tug, and a vessel heard by name only.
func mcpSeed(t *testing.T) *Pipeline {
	t.Helper()
	p := testPipeline(t)
	now := time.Now().Truncate(time.Second)
	pos := func(mmsi uint32, lat, lon, sog, cog float64, nav uint8) ais.Packet {
		return ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true, NavigationalStatus: nav,
			Latitude: ais.FieldLatLonFine(lat), Longitude: ais.FieldLatLonFine(lon), Sog: ais.Field10(sog), Cog: ais.Field10(cog), TrueHeading: 511}
	}
	static := func(mmsi uint32, name string, typ uint8) ais.Packet {
		return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: name, Type: typ}
	}
	p.ingestPacket("kystverket", "kystverket", now.Add(-20*time.Second), pos(257000001, 59.9, 10.7, 12, 180, 0))
	p.ingestPacket("kystverket", "kystverket", now.Add(-19*time.Second), static(257000001, "NORDIC STAR", 70))
	p.ingestPacket("kystverket", "kystverket", now.Add(-10*time.Second), pos(257000002, 59.5, 10.6, 18, 10, 0))
	p.ingestPacket("kystverket", "kystverket", now.Add(-9*time.Second), static(257000002, "OSLO FERRY", 60))
	p.ingestPacket("kystverket", "kystverket", now.Add(-5*time.Second), ais.AidsToNavigationReport{Header: ais.Header{MessageID: 21, UserID: 992571234}, Valid: true,
		Type: 25, Name: "DYNA GRUNNE", Latitude: ais.FieldLatLonFine(59.95), Longitude: ais.FieldLatLonFine(10.75)})
	p.ingestPacket("digitraffic", "digitraffic", now.Add(-30*time.Second), pos(230000001, 60.1, 25.0, 4, 90, 0))
	p.ingestPacket("digitraffic", "digitraffic", now.Add(-29*time.Second), static(230000001, "HELSINKI TUG", 52))
	p.ingestPacket("kystverket", "kystverket", now.Add(-3*time.Second), static(257000003, "GHOST", 37))
	return p
}

// mcpClient connects an in-process client; no HTTP request, so tools see the anonymous tier.
func mcpClient(t *testing.T, p *Pipeline) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := p.mcp.srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and decodes its structured result into out; an error result is returned as text.
func mcpCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return strings.Join(msgs, "\n")
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	// Decoding into a struct a previous call filled keeps every field the new JSON omits, so clear it first.
	reflect.ValueOf(out).Elem().SetZero()
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
	return ""
}

func TestMCPToolList(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"get_vessels": true, "find_vessels_in_area": true, "find_vessels_near": true, "search_vessels_by_name": true, "get_coverage": true}
	for _, tool := range res.Tools {
		if !want[tool.Name] {
			t.Errorf("unexpected tool %q", tool.Name)
		}
		delete(want, tool.Name)
		// The directory review criteria: a title, a read-only hint, a name under 64 characters, a description.
		if tool.Title == "" || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || len(tool.Name) > 64 || tool.Description == "" {
			t.Errorf("%s: missing title, read-only hint, or description: %+v", tool.Name, tool)
		}
		if tool.Name == "find_vessels_in_area" {
			schema, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(schema), `"required":["bbox"]`) {
				t.Errorf("find_vessels_in_area schema should require only bbox: %s", schema)
			}
		}
	}
	for name := range want {
		t.Errorf("tool %q not listed", name)
	}
	if res.TTLMs <= 0 || res.CacheScope != "public" {
		t.Errorf("tool list should be cacheable: ttl=%d scope=%q", res.TTLMs, res.CacheScope)
	}
}

func TestMCPGetVessels(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	var out mcpVessels
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{257000001, 1, 257000003}}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 || out.Total != 2 || out.Truncated {
		t.Fatalf("rows: %+v", out)
	}
	v := out.Vessels[0]
	if v.MMSI != 257000001 || v.Name != "NORDIC STAR" || v.TypeName != "Cargo" || v.NavStatusName != "Under way using engine" ||
		v.Lat == nil || *v.Lat != 59.9 || v.Sog == nil || *v.Sog != 12 || v.Heading != nil || v.AgeS < 19 || v.Source != "kystverket" {
		t.Errorf("row: %+v", v)
	}
	if g := out.Vessels[1]; g.Name != "GHOST" || g.Lat != nil || g.TypeName != "Pleasure craft" {
		t.Errorf("name-only vessel: %+v", g)
	}
	if len(out.Unknown) != 1 || out.Unknown[0] != 1 {
		t.Errorf("unknown: %v", out.Unknown)
	}
	if !strings.Contains(out.Attribution["kystverket"], "Norwegian Coastal Administration") {
		t.Errorf("attribution: %v", out.Attribution)
	}
	// anonymous: 10 per call, and the refusal says how to get more
	var many []uint32
	for i := range 11 {
		many = append(many, uint32(i+1))
	}
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": many}, &out); !strings.Contains(msg, "allows 10") || !strings.Contains(msg, mcpTokenURL) {
		t.Errorf("cap message: %q", msg)
	}
	if msg := mcpCall(t, cs, "get_vessels", map[string]any{"mmsi": []uint32{}}, &out); !strings.Contains(msg, "empty") {
		t.Errorf("empty list: %q", msg)
	}
}

func TestMCPFindInArea(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	oslo := map[string]any{"min_lat": 60, "min_lon": 11, "max_lat": 59, "max_lon": 10} // inverted corners on purpose
	var out mcpVessels
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": oslo}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 3 || out.Vessels[0].Kind != "aton" || out.Vessels[0].TypeName != "Starboard hand mark" || out.Vessels[2].Name != "NORDIC STAR" {
		t.Errorf("newest first: %+v", out.Vessels)
	}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": oslo, "types": []int{70}}, &out); msg != "" || len(out.Vessels) != 1 || out.Vessels[0].Name != "NORDIC STAR" {
		t.Errorf("type filter: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": oslo, "kind": "aton"}, &out); msg != "" || len(out.Vessels) != 1 {
		t.Errorf("kind filter: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": oslo, "limit": 1}, &out); msg != "" || len(out.Vessels) != 1 || out.Total != 3 || !out.Truncated {
		t.Errorf("limit: %q %+v", msg, out)
	}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": oslo, "kind": "boat"}, &out); !strings.Contains(msg, "kind") {
		t.Errorf("bad kind: %q", msg)
	}
	big := map[string]any{"min_lat": 50, "min_lon": 0, "max_lat": 70, "max_lon": 20}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": big}, &out); !strings.Contains(msg, "400 square degrees") || !strings.Contains(msg, "allows 100") {
		t.Errorf("area cap: %q", msg)
	}
	if msg := mcpCall(t, cs, "find_vessels_in_area", map[string]any{"bbox": map[string]any{"min_lat": 0, "min_lon": 0, "max_lat": 91, "max_lon": 1}}, &out); !strings.Contains(msg, "out of range") {
		t.Errorf("range: %q", msg)
	}
}

func TestMCPFindNear(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	var out mcpVessels
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"lat": 59.9, "lon": 10.7, "radius_nm": 5}, &out); msg != "" {
		t.Fatal(msg)
	}
	if len(out.Vessels) != 2 || out.Vessels[0].Name != "NORDIC STAR" || *out.Vessels[0].DistanceNM != 0 || out.Vessels[1].Kind != "aton" || *out.Vessels[1].DistanceNM != 3.4 || *out.Vessels[1].Bearing != 27 {
		t.Errorf("nearest first with distance and bearing: %+v", out.Vessels)
	}
	// centred on a vessel: the vessel itself is left out, the ferry 25 NM south is inside 30 NM
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"mmsi": 257000001, "radius_nm": 30}, &out); msg != "" {
		t.Fatal(msg)
	}
	for _, v := range out.Vessels {
		if v.MMSI == 257000001 {
			t.Errorf("centre vessel in its own results")
		}
	}
	if len(out.Vessels) != 2 || out.Vessels[1].Name != "OSLO FERRY" || *out.Vessels[1].Bearing != 187 {
		t.Errorf("vessel centre: %+v", out.Vessels)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"lat": 59.9, "lon": 10.7, "radius_nm": 60}, &out); !strings.Contains(msg, "maximum of 50") {
		t.Errorf("radius cap: %q", msg)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"mmsi": 42}, &out); !strings.Contains(msg, "not been heard") {
		t.Errorf("unknown centre: %q", msg)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"mmsi": 257000003}, &out); !strings.Contains(msg, "no position") {
		t.Errorf("positionless centre: %q", msg)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"radius_nm": 5}, &out); !strings.Contains(msg, "lat and lon, or mmsi") {
		t.Errorf("no centre: %q", msg)
	}
	if msg := mcpCall(t, cs, "find_vessels_near", map[string]any{"lat": 59.9, "lon": 10.7, "mmsi": 257000001}, &out); !strings.Contains(msg, "not both") {
		t.Errorf("both centres: %q", msg)
	}
}

func TestMCPSearchByName(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	var out mcpVessels
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "oslo"}, &out); msg != "" || len(out.Vessels) != 1 || out.Vessels[0].MMSI != 257000002 {
		t.Errorf("substring, case-insensitive: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "ghost"}, &out); msg != "" || len(out.Vessels) != 1 || out.Vessels[0].Lat != nil {
		t.Errorf("name-only vessel: %q %+v", msg, out.Vessels)
	}
	oslo := map[string]any{"min_lat": 59, "min_lon": 10, "max_lat": 60, "max_lon": 11}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "ghost", "bbox": oslo}, &out); msg != "" || len(out.Vessels) != 0 {
		t.Errorf("bbox excludes positionless: %q %+v", msg, out.Vessels)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "s", "bbox": oslo}, &out); !strings.Contains(msg, "2 characters") {
		t.Errorf("short name: %q", msg)
	}
	if msg := mcpCall(t, cs, "search_vessels_by_name", map[string]any{"name": "zz"}, &out); msg != "" || len(out.Vessels) != 0 || out.Total != 0 || len(out.Attribution) != 0 {
		t.Errorf("no match: %q %+v", msg, out)
	}
}

func TestMCPCoverage(t *testing.T) {
	cs := mcpClient(t, mcpSeed(t))
	var out mcpCoverage
	if msg := mcpCall(t, cs, "get_coverage", map[string]any{}, &out); msg != "" {
		t.Fatal(msg)
	}
	if out.Stations.Total != 2 || out.Stations.Active != 2 || out.Vessels.Total != 5 || out.Vessels.WithPosition != 4 || len(out.Sources) != 2 {
		t.Errorf("counts: %+v", out)
	}
	if s := out.Sources[0]; s.Kind != "kystverket" || s.Vessels != 4 || s.VesselsExclusive != 4 || s.Events24h != 6 || s.License != "NLOD-2.0" || !strings.Contains(s.Description, "Norwegian") {
		t.Errorf("largest source first: %+v", s)
	}
	if !strings.Contains(out.Summary, "4 vessels with a position") {
		t.Errorf("summary: %q", out.Summary)
	}
	oslo := map[string]any{"min_lat": 59, "min_lon": 10, "max_lat": 60, "max_lon": 11}
	if msg := mcpCall(t, cs, "get_coverage", map[string]any{"bbox": oslo, "station": "digitraffic"}, &out); msg != "" {
		t.Fatal(msg)
	}
	if out.Area == nil || out.Area.Vessels != 3 || len(out.Area.Stations) != 1 || out.Area.Stations[0].Station != "kystverket" {
		t.Errorf("area: %+v", out.Area)
	}
	if out.Station == nil || out.Station.Station != "digitraffic" || out.Station.Vessels != 1 || out.Station.BBox == nil {
		t.Errorf("station: %+v", out.Station)
	}
	// a whole-world box is a coverage question, not a position query, so no tier check applies
	world := map[string]any{"min_lat": -90, "min_lon": -180, "max_lat": 90, "max_lon": 180}
	if msg := mcpCall(t, cs, "get_coverage", map[string]any{"bbox": world, "station": "nope"}, &out); msg != "" || out.Area.Vessels != 4 || out.Station != nil || !strings.Contains(out.Note, "nope") {
		t.Errorf("world box and unknown station: %q %+v", msg, out)
	}
}

// bearerTransport sends one token on every request, as an MCP client configured with a header does.
type bearerTransport string

func (tok bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+string(tok))
	return http.DefaultTransport.RoundTrip(r)
}

// Over HTTP the tier follows the token exactly as on /v1/vessels, and the endpoint answers plain JSON.
func TestMCPHTTP(t *testing.T) {
	p := mcpSeed(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	personal, err := signToken(priv, Claims{Kid: kid, Sub: "ed25519:abc", Role: "personal", Exp: time.Now().Add(time.Hour).Unix(), Area: personalArea, MMSIs: personalMMSIs})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()

	connect := func(client *http.Client) *mcp.ClientSession {
		t.Helper()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(),
			&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: client, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
	big := map[string]any{"bbox": map[string]any{"min_lat": 50, "min_lon": 0, "max_lat": 70, "max_lon": 20}} // 400 square degrees
	var out mcpVessels
	if msg := mcpCall(t, connect(http.DefaultClient), "find_vessels_in_area", big, &out); !strings.Contains(msg, "allows 100") {
		t.Errorf("anonymous over HTTP: %q", msg)
	}
	if msg := mcpCall(t, connect(&http.Client{Transport: bearerTransport(personal)}), "find_vessels_in_area", big, &out); msg != "" || out.Total != 3 { // the Helsinki tug is east of the box
		t.Errorf("personal token over HTTP: %q %+v", msg, out)
	}

	// a bad token is refused before any JSON-RPC, like every other endpoint
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer nope")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("bad token: %d", res.StatusCode)
	}

	// stateless: one POST with no session answers tools/list as plain JSON, with CORS open
	req, _ = http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var listed struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil || res.StatusCode != 200 || len(listed.Result.Tools) != 5 ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("raw tools/list: %d %s %v %+v", res.StatusCode, res.Header.Get("Content-Type"), err, listed)
	}

	// preflight for browser-based clients
	req, _ = http.NewRequest("OPTIONS", srv.URL+"/mcp", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("preflight: %d %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Headers"))
	}
}

// server.json at the repo root is what the MCP registry publishes; its version and URL must match the binary.
func TestMCPServerJSON(t *testing.T) {
	b, err := os.ReadFile("../server.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name    string
		Version string
		Remotes []struct{ Type, URL string }
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != mcpVersion {
		t.Errorf("server.json version %q, binary %q", doc.Version, mcpVersion)
	}
	if doc.Name != "io.openwaters/aiscast" || len(doc.Remotes) != 1 || doc.Remotes[0].Type != "streamable-http" || doc.Remotes[0].URL != "https://ais.openwaters.io/mcp" {
		t.Errorf("server.json: %+v", doc)
	}
}

func TestLabels(t *testing.T) {
	for code, want := range map[uint8]string{0: "Reserved", 30: "Fishing", 36: "Sailing", 37: "Pleasure craft", 52: "Tug", 60: "Passenger", 70: "Cargo",
		71: "Cargo, hazardous category A", 84: "Tanker, hazardous category D", 89: "Tanker, no additional information", 99: "Other, no additional information", 150: "Reserved"} {
		if got := shipTypeName(code); got != want {
			t.Errorf("type %d: %q, want %q", code, got, want)
		}
	}
	if navStatusName(1) != "At anchor" || navStatusName(15) != "" || navStatusName(200) != "" || atonTypeName(9) != "Beacon, cardinal N" || atonTypeName(40) != "" {
		t.Error("nav status or aton labels")
	}
}
