package main

// MCP (Model Context Protocol) at /mcp: the vessel cache and station statistics as tools an AI assistant
// calls. Streamable HTTP, stateless, JSON responses, no sign-in; the same claims, tiers, and rate limit
// as /v1/vessels. The path sits outside /v1 because MCP carries its own protocol version in every
// request and every client and registry expects /mcp.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpVersion is the tool-set version clients see; server.json at the repo root carries the same number
// and the two are checked against each other in mcp_test.go. Bump on any change to a tool or its schema.
const mcpVersion = "0.2.0"

const (
	mcpDefaultLimit    = 50  // rows per call unless asked; ~120 B of JSON each keeps a page under 10k tokens
	mcpMaxLimit        = 200 // the most a call may ask for
	mcpDefaultRadiusNM = 10.0
	mcpMaxRadiusNM     = 50.0
	mcpTokenURL        = "https://openwaters.io/ais/token"
	mcpDocsURL         = "https://openwaters.io/api/ais/"
)

const mcpInstructions = `Open Waters AIS (https://openwaters.io/ais/) is the open AIS network: live vessel positions from government feeds, partner aggregates, and volunteer receivers, deduplicated into one picture.

- Live terrestrial coverage is strongest in the Nordics and wherever volunteer receivers are; elsewhere positions come from partner aggregates, mostly AISHub, and are typically one to six minutes old. Where no feed or receiver hears, there is nothing. Call get_coverage before saying a region has no traffic.
- A position is the last report heard, up to 30 minutes old. Every row carries seen and age_s. A vessel unheard for 30 minutes is dropped.
- Destination, ETA, draught, dimensions, call sign, and IMO come from a vessel's static data, which it sends every six minutes, so a vessel heard for the first time may lack them. Flag comes from the MMSI's maritime identification digits and is present when those are known. ETA has no year: read it as the next occurrence.
- Anonymous calls may cover 100 square degrees and look up 10 vessels by MMSI or IMO per call. A free personal token from ` + mcpTokenURL + `, sent as an Authorization: Bearer header, raises that to 400 square degrees and 50 vessels. A tool says so when a call exceeds its limit.
- Show the credit lines from each result's attribution field wherever the data is displayed.
- A supplement to onboard AIS, never a substitute, and not for safety of navigation.
- These tools answer one question at a time. For continuous updates use the WebSocket stream at wss://ais.openwaters.io/v1/stream, documented at ` + mcpDocsURL + `.
- The network holds no port registry, weather, ownership, or inspection data.`

// mcpService is the MCP server and its HTTP handler, built once per Pipeline.
type mcpService struct {
	srv  *mcp.Server
	http http.Handler
}

// mcpClaimsKey carries the caller's claims from serveMCP into tool handlers. The SDK derives the tool
// handler's context from the HTTP request, so a context value is the one channel that survives it.
type mcpClaimsKey struct{}

// mcpClaims: the request's claims, or the anonymous tier for callers that did not come through HTTP.
func mcpClaims(ctx context.Context) *Claims {
	if c, ok := ctx.Value(mcpClaimsKey{}).(*Claims); ok && c != nil {
		return c
	}
	return anonymousClaims("")
}

func mcpPtr[T any](v T) *T { return &v }

func newMCPService(p *Pipeline) *mcpService {
	// Read-only, idempotent, closed world: every tool reads the in-memory cache and nothing else. Claude
	// runs read-only tools without a per-call confirmation, so the hints matter.
	ro := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: mcpPtr(false)}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "aiscast", Title: "Open Waters AIS", Version: mcpVersion, WebsiteURL: "https://openwaters.io/ais/"},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Capabilities: &mcp.ServerCapabilities{}, // drops the default logging capability, deprecated in the protocol and unused here; tools is inferred from AddTool
			// The tool list changes only on deploy, so clients and shared caches may hold it for an hour.
			SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) { c.TTLMs, c.CacheScope = 3600_000, "public" },
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_vessels", Title: "Vessels by MMSI", Annotations: ro("Vessels by MMSI"),
		Description: "Current position and particulars of specific vessels by MMSI (Maritime Mobile Service Identity) or IMO number: the last report heard for each, with destination, ETA, draught, and dimensions once its static data has been heard, and which identifiers matched nothing in the last 30 minutes. Use search_vessels_by_name first when you only have a name."},
		p.mcpGetVessels)
	mcp.AddTool(s, &mcp.Tool{Name: "find_vessels_in_area", Title: "Vessels in an area", Annotations: ro("Vessels in an area"),
		Description: "Vessels currently inside a latitude/longitude bounding box, newest report first, with optional kind and ship-type filters. Use for what is in a port, a strait, or a stretch of coast. Anonymous calls may cover 100 square degrees per call."},
		p.mcpFindInArea)
	mcp.AddTool(s, &mcp.Tool{Name: "find_vessels_near", Title: "Vessels near a point or vessel", Annotations: ro("Vessels near a point or vessel"),
		Description: "Vessels within a radius (default 10 NM, maximum 50) of a point or of another vessel, nearest first, each with distance and bearing from the centre. Use for what is near this position or what is around vessel X."},
		p.mcpFindNear)
	mcp.AddTool(s, &mcp.Tool{Name: "search_vessels_by_name", Title: "Search vessels by name", Annotations: ro("Search vessels by name"),
		Description: "Vessels whose name contains the text, case-insensitive, among vessels heard in the last 30 minutes. Use to turn a name into an MMSI, then get_vessels or find_vessels_near for detail. An optional bounding box narrows the search."},
		p.mcpSearchByName)
	mcp.AddTool(s, &mcp.Tool{Name: "get_coverage", Title: "Coverage and sources", Annotations: ro("Coverage and sources"),
		Description: "Where Open Waters AIS is hearing AIS right now: sources, stations, freshness, and vessel counts. Pass a bounding box to learn which stations cover it and how many vessels are in it, or a station id for that station's numbers. Call this before saying a region has no traffic."},
		p.mcpGetCoverage)
	return &mcpService{srv: s, http: mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		Stateless:           true, // no session state, and the mode the 2026-07-28 protocol revision requires
		JSONResponse:        true, // nothing here streams, and plain JSON suits curl, proxies, and logs
		MaxRequestBodyBytes: 64 << 10,
		// The SDK's DNS-rebinding guard rejects a request that arrives on a loopback address with a
		// non-loopback Host header, which is every production request: Caddy connects to localhost:8080
		// and forwards Host: ais.openwaters.io. The guard is for servers on a developer's machine.
		DisableLocalhostProtection: true,
	})}
}

// serveMCP: POST /mcp. CORS is open like the rest of the API, with the headers MCP clients send, so
// browser-based agents and the MCP Inspector can call it. Claims resolve exactly as for /v1/vessels.
func (p *Pipeline) serveMCP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Mcp-Protocol-Version, Mcp-Method, Mcp-Name")
	h.Set("Access-Control-Max-Age", "86400")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	p.mcp.http.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpClaimsKey{}, cl)))
}

// ---- shapes ----

// mcpBox is a bounding box as tool input. Corners may come in any order.
type mcpBox struct {
	MinLat float64 `json:"min_lat" jsonschema:"south edge, degrees, -90 to 90"`
	MinLon float64 `json:"min_lon" jsonschema:"west edge, degrees, -180 to 180"`
	MaxLat float64 `json:"max_lat" jsonschema:"north edge, degrees, -90 to 90"`
	MaxLon float64 `json:"max_lon" jsonschema:"east edge, degrees, -180 to 180"`
}

func (b mcpBox) bbox() bbox {
	return bbox{min(b.MinLat, b.MaxLat), min(b.MinLon, b.MaxLon), max(b.MinLat, b.MaxLat), max(b.MinLon, b.MaxLon)}
}

// mcpVessel is one row of a vessel result. Sentinel "not available" values are omitted rather than sent
// as 360, 102.3, or 511, so a reader never has to know the AIS encodings.
type mcpVessel struct {
	MMSI          uint32   `json:"mmsi"`
	Name          string   `json:"name,omitempty" jsonschema:"name from the vessel's static data, when heard"`
	Kind          string   `json:"kind" jsonschema:"vessel, aton (aid to navigation), base (base station), or sar (search and rescue aircraft)"`
	Type          uint8    `json:"type,omitempty" jsonschema:"ITU ship and cargo type code, or the aid type for an aton"`
	TypeName      string   `json:"type_name,omitempty"`
	Lat           *float64 `json:"lat,omitempty" jsonschema:"latitude of the last position, degrees; absent when no position has been heard"`
	Lon           *float64 `json:"lon,omitempty" jsonschema:"longitude of the last position, degrees"`
	Cog           *float64 `json:"cog,omitempty" jsonschema:"course over ground, degrees true"`
	Sog           *float64 `json:"sog,omitempty" jsonschema:"speed over ground, knots"`
	Heading       *uint16  `json:"heading,omitempty" jsonschema:"true heading, degrees"`
	NavStatus     *uint8   `json:"nav_status,omitempty" jsonschema:"AIS navigational status code"`
	NavStatusName string   `json:"nav_status_name,omitempty"`
	Flag          string   `json:"flag,omitempty" jsonschema:"ISO 3166-1 alpha-2 code of the flag state, from the MMSI's maritime identification digits"`
	IMO           uint32   `json:"imo,omitempty" jsonschema:"IMO number, once the vessel's static data has been heard"`
	CallSign      string   `json:"callsign,omitempty"`
	Destination   string   `json:"destination,omitempty" jsonschema:"destination as typed by the crew: a port name, a UN/LOCODE, or nothing useful"`
	ETA           string   `json:"eta,omitempty" jsonschema:"estimated arrival as sent, MM-DD HH:MM UTC or MM-DD; AIS carries no year, so read it as the next occurrence"`
	DraughtM      *float64 `json:"draught_m,omitempty" jsonschema:"maximum static draught, metres"`
	LengthM       *uint16  `json:"length_m,omitempty" jsonschema:"overall length, metres"`
	BeamM         *uint16  `json:"beam_m,omitempty" jsonschema:"beam, metres"`
	Seen          string   `json:"seen" jsonschema:"time of the last message heard, RFC 3339 UTC"`
	AgeS          int64    `json:"age_s" jsonschema:"seconds since seen"`
	Source        string   `json:"source" jsonschema:"feed or station kind the last message came from"`
	Station       string   `json:"station"`
	DistanceNM    *float64 `json:"distance_nm,omitempty" jsonschema:"nautical miles from the search centre (find_vessels_near)"`
	Bearing       *float64 `json:"bearing,omitempty" jsonschema:"degrees true from the search centre to the vessel (find_vessels_near)"`
}

type mcpVessels struct {
	Vessels     []mcpVessel       `json:"vessels"`
	Total       int               `json:"total" jsonschema:"vessels matched before the limit was applied"`
	Truncated   bool              `json:"truncated" jsonschema:"true when total exceeds the rows returned; narrow the query or raise limit"`
	Unknown     []uint32          `json:"unknown_mmsi,omitempty" jsonschema:"requested MMSIs not heard in the last 30 minutes"`
	UnknownIMO  []uint32          `json:"unknown_imo,omitempty" jsonschema:"requested IMO numbers matching no vessel heard in the last 30 minutes"`
	Attribution map[string]string `json:"attribution" jsonschema:"credit line per source kind in the rows, to show with the data"`
}

func mcpRow(mmsi uint32, v *vessel, now time.Time) mcpVessel {
	r := mcpVessel{MMSI: mmsi, Name: v.Name, Kind: v.Kind, Type: v.ShipType, Seen: v.Seen.UTC().Format(time.RFC3339),
		AgeS: max(int64(now.Sub(v.Seen).Seconds()), 0), Source: v.Source, Station: v.Station}
	if v.ShipType != 0 {
		if v.Kind == "aton" {
			r.TypeName = atonTypeName(v.ShipType)
		} else {
			r.TypeName = shipTypeName(v.ShipType)
		}
	}
	if v.HasPos {
		r.Lat, r.Lon = mcpPtr(v.Lat), mcpPtr(v.Lon)
	}
	if v.Cog < 360 {
		r.Cog = mcpPtr(v.Cog)
	}
	if v.Sog < 102.3 {
		r.Sog = mcpPtr(v.Sog)
	}
	if v.Heading < 511 {
		r.Heading = mcpPtr(v.Heading)
	}
	if v.NavStatus != 15 {
		r.NavStatus, r.NavStatusName = mcpPtr(v.NavStatus), navStatusName(v.NavStatus)
	}
	r.Flag, r.IMO, r.CallSign, r.Destination, r.ETA = flagOf(mmsi), v.IMO, v.CallSign, v.Destination, etaString(v.ETA)
	if v.Draught > 0 {
		r.DraughtM = mcpPtr(v.Draught)
	}
	if v.Length > 0 {
		r.LengthM = mcpPtr(v.Length)
	}
	if v.Beam > 0 {
		r.BeamM = mcpPtr(v.Beam)
	}
	return r
}

// mcpCollect walks the cache under the read lock and returns a row for every vessel keep accepts.
func (p *Pipeline) mcpCollect(now time.Time, keep func(uint32, *vessel) bool) []mcpVessel {
	var rows []mcpVessel
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	for mmsi, v := range p.vessels {
		if keep(mmsi, v) {
			rows = append(rows, mcpRow(mmsi, v, now))
		}
	}
	return rows
}

// mcpPage sorts, cuts to limit, and credits the sources left on the page.
func mcpPage(rows []mcpVessel, less func(a, b *mcpVessel) bool, limit int) mcpVessels {
	sort.SliceStable(rows, func(i, j int) bool { return less(&rows[i], &rows[j]) })
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []mcpVessel{}
	}
	attribution := map[string]string{}
	for _, r := range rows {
		noteAttribution(attribution, r.Source)
	}
	return mcpVessels{Vessels: rows, Total: total, Truncated: total > len(rows), Attribution: attribution}
}

func newestFirst(a, b *mcpVessel) bool {
	return a.Seen > b.Seen || (a.Seen == b.Seen && a.MMSI < b.MMSI)
}

// ---- checks shared by the tools; every message names the limit and the way past it ----

func mcpLimit(n int) (int, error) {
	switch {
	case n < 0:
		return 0, errors.New("limit must be positive")
	case n == 0:
		return mcpDefaultLimit, nil
	case n > mcpMaxLimit:
		return 0, fmt.Errorf("limit %d exceeds the maximum of %d; narrow the query instead", n, mcpMaxLimit)
	}
	return n, nil
}

func mcpKind(kind string) error {
	switch kind {
	case "", "vessel", "aton", "base", "sar":
		return nil
	}
	return fmt.Errorf("kind %q is not one of vessel, aton, base, sar", kind)
}

// mcpFlag normalises a flag filter: empty, or two letters upper-cased.
func mcpFlag(f string) (string, error) {
	f = strings.ToUpper(strings.TrimSpace(f))
	if f != "" && (len(f) != 2 || f[0] < 'A' || f[0] > 'Z' || f[1] < 'A' || f[1] > 'Z') {
		return "", fmt.Errorf("flag %q is not a two-letter ISO 3166-1 code", f)
	}
	return f, nil
}

// mcpMatch applies the kind, ship-type, and flag filters. A type code ending in 0 stands for its decade,
// so 70 matches every cargo subtype.
func mcpMatch(kind string, types []uint8, flag string, mmsi uint32, v *vessel) bool {
	if kind != "" && v.Kind != kind {
		return false
	}
	if flag != "" && flagOf(mmsi) != flag {
		return false
	}
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t == v.ShipType || (t%10 == 0 && t/10 == v.ShipType/10) {
			return true
		}
	}
	return false
}

// mcpCheckBox: the same range and tier rules as /v1/vessels, with messages an assistant can act on.
func mcpCheckBox(cl *Claims, b bbox) error { return mcpCheckBoxes(cl, []bbox{b}) }

func mcpCheckBoxes(cl *Claims, boxes []bbox) error {
	area := 0.0
	for _, b := range boxes {
		if b[0] < -90 || b[2] > 90 || b[1] < -180 || b[3] > 180 {
			return errors.New("bbox out of range: latitudes -90 to 90, longitudes -180 to 180")
		}
		if !cl.allowsBox(b) {
			return errors.New("bbox lies outside the area this token is limited to")
		}
		area += (b[2] - b[0]) * (b[3] - b[1])
	}
	if cl.allowsArea(boxes) {
		return nil
	}
	if cl.Area < 0 {
		return errors.New("this token follows vessels by MMSI only; use get_vessels")
	}
	msg := fmt.Sprintf("bbox covers %.0f square degrees; this key allows %.0f per call. Split the area into smaller calls", area, cl.Area)
	if cl.Role == "anonymous" {
		msg += fmt.Sprintf(", or send a free token from %s as an Authorization: Bearer header for %.0f", mcpTokenURL, personalArea)
	}
	return errors.New(msg)
}

func mcpCheckMMSIs(cl *Claims, n int) error {
	if n == 0 {
		return errors.New("give at least one mmsi or imo")
	}
	if cl.allowsMMSIs(n) {
		return nil
	}
	msg := fmt.Sprintf("%d MMSIs requested; this key allows %d per call. Split the list", n, cl.MMSIs)
	if cl.Role == "anonymous" {
		msg += fmt.Sprintf(", or send a free token from %s as an Authorization: Bearer header for %d", mcpTokenURL, personalMMSIs)
	}
	return errors.New(msg)
}

// ---- tools ----

type mcpGetIn struct {
	MMSI []uint32 `json:"mmsi,omitempty" jsonschema:"MMSIs to look up; anonymous calls may pass 10 identifiers per call, a personal token 50"`
	IMO  []uint32 `json:"imo,omitempty" jsonschema:"IMO numbers to look up, counted with mmsi against the same cap; a vessel is found by IMO only once its static data has been heard"`
}

func (p *Pipeline) mcpGetVessels(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetIn) (*mcp.CallToolResult, mcpVessels, error) {
	cl := mcpClaims(ctx)
	// First position of each identifier, in request order with IMOs after MMSIs; a repeated identifier
	// counts once against the cap, as on /v1/vessels.
	want, wantIMO := map[uint32]int{}, map[uint32]int{}
	for i, m := range in.MMSI {
		if _, ok := want[m]; !ok {
			want[m] = i
		}
	}
	for i, n := range in.IMO {
		if _, ok := wantIMO[n]; !ok && n != 0 { // 0 is "not available" on the wire and matches no vessel
			wantIMO[n] = len(in.MMSI) + i
		}
	}
	if err := mcpCheckMMSIs(cl, len(want)+len(wantIMO)); err != nil {
		return nil, mcpVessels{}, err
	}
	order := func(r *mcpVessel) int {
		if i, ok := want[r.MMSI]; ok {
			return i
		}
		return wantIMO[r.IMO]
	}
	rows := p.mcpCollect(time.Now(), func(m uint32, v *vessel) bool {
		if _, ok := want[m]; ok {
			return true
		}
		_, ok := wantIMO[v.IMO]
		return ok && v.IMO != 0
	})
	known, knownIMO := map[uint32]bool{}, map[uint32]bool{} // from every match, not the page: a vessel cut by the row cap is still known
	for _, r := range rows {
		known[r.MMSI] = true
		if r.IMO != 0 {
			knownIMO[r.IMO] = true
		}
	}
	out := mcpPage(rows, func(a, b *mcpVessel) bool { return order(a) < order(b) }, mcpMaxLimit)
	for _, m := range in.MMSI {
		if !known[m] {
			out.Unknown = append(out.Unknown, m)
			known[m] = true // listed once
		}
	}
	for _, n := range in.IMO {
		if !knownIMO[n] {
			out.UnknownIMO = append(out.UnknownIMO, n)
			knownIMO[n] = true
		}
	}
	return nil, out, nil
}

type mcpAreaIn struct {
	BBox  mcpBox  `json:"bbox"`
	Kind  string  `json:"kind,omitempty" jsonschema:"only this kind: vessel, aton (aid to navigation), base (base station), or sar (search and rescue aircraft)"`
	Flag  string  `json:"flag,omitempty" jsonschema:"only vessels flying this flag: ISO 3166-1 alpha-2, e.g. NO, FI, MH"`
	Types []uint8 `json:"types,omitempty" jsonschema:"only these ITU ship type codes, e.g. 30 fishing, 36 sailing, 37 pleasure craft, 52 tug, 60 passenger, 70 cargo, 80 tanker; a code ending in 0 matches its decade, so 70 matches 70 to 79"`
	Limit int     `json:"limit,omitempty" jsonschema:"rows to return: default 50, maximum 200"`
}

func (p *Pipeline) mcpFindInArea(ctx context.Context, _ *mcp.CallToolRequest, in mcpAreaIn) (*mcp.CallToolResult, mcpVessels, error) {
	cl := mcpClaims(ctx)
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	if err := mcpKind(in.Kind); err != nil {
		return nil, mcpVessels{}, err
	}
	flag, err := mcpFlag(in.Flag)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	b := in.BBox.bbox()
	if err := mcpCheckBox(cl, b); err != nil {
		return nil, mcpVessels{}, err
	}
	rows := p.mcpCollect(time.Now(), func(m uint32, v *vessel) bool {
		return v.HasPos && b.contains(v.Lat, v.Lon) && mcpMatch(in.Kind, in.Types, flag, m, v)
	})
	return nil, mcpPage(rows, newestFirst, limit), nil
}

type mcpNearIn struct {
	Lat      *float64 `json:"lat,omitempty" jsonschema:"centre latitude, degrees; give lat and lon, or mmsi"`
	Lon      *float64 `json:"lon,omitempty" jsonschema:"centre longitude, degrees"`
	MMSI     uint32   `json:"mmsi,omitempty" jsonschema:"centre on this vessel's last position instead of lat and lon; the vessel itself is left out of the results"`
	RadiusNM float64  `json:"radius_nm,omitempty" jsonschema:"search radius in nautical miles: default 10, maximum 50"`
	Kind     string   `json:"kind,omitempty" jsonschema:"only this kind: vessel, aton, base, or sar"`
	Flag     string   `json:"flag,omitempty" jsonschema:"only vessels flying this flag: ISO 3166-1 alpha-2, e.g. NO, FI, MH"`
	Types    []uint8  `json:"types,omitempty" jsonschema:"only these ITU ship type codes; a code ending in 0 matches its decade"`
	Limit    int      `json:"limit,omitempty" jsonschema:"rows to return: default 50, maximum 200"`
}

func (p *Pipeline) mcpFindNear(ctx context.Context, _ *mcp.CallToolRequest, in mcpNearIn) (*mcp.CallToolResult, mcpVessels, error) {
	cl := mcpClaims(ctx)
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	if err := mcpKind(in.Kind); err != nil {
		return nil, mcpVessels{}, err
	}
	flag, err := mcpFlag(in.Flag)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	radius := in.RadiusNM
	switch {
	case radius == 0:
		radius = mcpDefaultRadiusNM
	case radius < 0:
		return nil, mcpVessels{}, errors.New("radius_nm must be positive")
	case radius > mcpMaxRadiusNM:
		return nil, mcpVessels{}, fmt.Errorf("radius_nm %g exceeds the maximum of %g; use find_vessels_in_area for a wider view", radius, mcpMaxRadiusNM)
	}
	var lat, lon float64
	switch {
	case in.MMSI != 0 && (in.Lat != nil || in.Lon != nil):
		return nil, mcpVessels{}, errors.New("give lat and lon, or mmsi, not both")
	case in.MMSI != 0:
		p.vmu.RLock()
		v := p.vessels[in.MMSI]
		known, hasPos := v != nil, v != nil && v.HasPos // copied under the lock; updateVessel mutates v after it
		if hasPos {
			lat, lon = v.Lat, v.Lon
		}
		p.vmu.RUnlock()
		if !known {
			return nil, mcpVessels{}, fmt.Errorf("vessel %d has not been heard in the last 30 minutes", in.MMSI)
		}
		if !hasPos {
			return nil, mcpVessels{}, fmt.Errorf("vessel %d has been heard but has sent no position", in.MMSI)
		}
	case in.Lat != nil && in.Lon != nil:
		lat, lon = *in.Lat, *in.Lon
		if math.Abs(lat) > 90 || math.Abs(lon) > 180 {
			return nil, mcpVessels{}, errors.New("lat must be -90 to 90 and lon -180 to 180")
		}
	default:
		return nil, mcpVessels{}, errors.New("give lat and lon, or mmsi")
	}
	boxes := radiusBoxes(lat, lon, radius)
	if err := mcpCheckBoxes(cl, boxes); err != nil {
		return nil, mcpVessels{}, err
	}
	rows := p.mcpCollect(time.Now(), func(m uint32, v *vessel) bool {
		return m != in.MMSI && v.HasPos && inAny(boxes, v.Lat, v.Lon) && mcpMatch(in.Kind, in.Types, flag, m, v) && nm(lat, lon, v.Lat, v.Lon) <= radius
	})
	for i := range rows {
		r := &rows[i]
		r.DistanceNM = mcpPtr(math.Round(nm(lat, lon, *r.Lat, *r.Lon)*10) / 10)
		r.Bearing = mcpPtr(math.Round(bearing(lat, lon, *r.Lat, *r.Lon)))
	}
	return nil, mcpPage(rows, func(a, b *mcpVessel) bool {
		return *a.DistanceNM < *b.DistanceNM || (*a.DistanceNM == *b.DistanceNM && a.MMSI < b.MMSI)
	}, limit), nil
}

// radiusBoxes bounds the circle so the cache walk and the tier check see boxes. A circle across the
// antimeridian is two boxes, one on each side, whose areas sum to the unwrapped box's. A 50 NM circle is
// under 3 square degrees at the equator and under 10 at 80° latitude, inside every tier.
func radiusBoxes(lat, lon, radiusNM float64) []bbox {
	dLat := radiusNM / 60
	south, north := max(lat-dLat, -90), min(lat+dLat, 90)
	dLon := 180.0
	if c := math.Cos(lat * math.Pi / 180); c > 1e-6 {
		dLon = min(radiusNM/(60*c), 180)
	}
	west, east := lon-dLon, lon+dLon
	switch {
	case dLon >= 180:
		return []bbox{{south, -180, north, 180}}
	case west < -180:
		return []bbox{{south, -180, north, east}, {south, west + 360, north, 180}}
	case east > 180:
		return []bbox{{south, west, north, 180}, {south, -180, north, east - 360}}
	}
	return []bbox{{south, west, north, east}}
}

func inAny(boxes []bbox, lat, lon float64) bool {
	for _, b := range boxes {
		if b.contains(lat, lon) {
			return true
		}
	}
	return false
}

// bearing is the initial great-circle bearing from point 1 to point 2, degrees true.
func bearing(lat1, lon1, lat2, lon2 float64) float64 {
	φ1, φ2 := lat1*math.Pi/180, lat2*math.Pi/180
	dλ := (lon2 - lon1) * math.Pi / 180
	y := math.Sin(dλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(dλ)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

type mcpNameIn struct {
	Name  string  `json:"name" jsonschema:"text to find in the vessel name, at least 2 characters, case-insensitive"`
	BBox  *mcpBox `json:"bbox,omitempty" jsonschema:"only vessels whose last position is inside this box"`
	Flag  string  `json:"flag,omitempty" jsonschema:"only vessels flying this flag: ISO 3166-1 alpha-2, e.g. NO, FI, MH"`
	Limit int     `json:"limit,omitempty" jsonschema:"rows to return: default 50, maximum 200"`
}

func (p *Pipeline) mcpSearchByName(ctx context.Context, _ *mcp.CallToolRequest, in mcpNameIn) (*mcp.CallToolResult, mcpVessels, error) {
	cl := mcpClaims(ctx)
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	q := strings.ToUpper(strings.TrimSpace(in.Name))
	if len([]rune(q)) < 2 {
		return nil, mcpVessels{}, errors.New("name needs at least 2 characters")
	}
	flag, err := mcpFlag(in.Flag)
	if err != nil {
		return nil, mcpVessels{}, err
	}
	var box *bbox
	if in.BBox != nil {
		b := in.BBox.bbox()
		if err := mcpCheckBox(cl, b); err != nil {
			return nil, mcpVessels{}, err
		}
		box = &b
	}
	rows := p.mcpCollect(time.Now(), func(m uint32, v *vessel) bool {
		if !strings.Contains(strings.ToUpper(v.Name), q) || (flag != "" && flagOf(m) != flag) {
			return false
		}
		return box == nil || (v.HasPos && box.contains(v.Lat, v.Lon))
	})
	return nil, mcpPage(rows, func(a, b *mcpVessel) bool {
		return a.Name < b.Name || (a.Name == b.Name && a.MMSI < b.MMSI)
	}, limit), nil
}

type mcpCoverageIn struct {
	BBox    *mcpBox `json:"bbox,omitempty" jsonschema:"report the stations covering this box and the vessels currently in it"`
	Station string  `json:"station,omitempty" jsonschema:"a station id from an earlier result, for that station's numbers"`
}

type mcpSource struct {
	Kind             string `json:"kind"`
	Description      string `json:"description,omitempty"`
	Vessels          int    `json:"vessels" jsonschema:"distinct vessels heard from this source in the last 30 minutes"`
	VesselsExclusive int    `json:"vessels_exclusive" jsonschema:"of those, heard by no other source"`
	Events24h        int64  `json:"events_24h"`
	LastAgeS         int64  `json:"last_age_s" jsonschema:"seconds since the source last delivered a message"`
	License          string `json:"license"`
	Attribution      string `json:"attribution"`
}

type mcpStation struct {
	Station   string  `json:"station"`
	Source    string  `json:"source"`
	Vessels   int     `json:"vessels" jsonschema:"distinct vessels heard in the last 30 minutes"`
	Events24h int64   `json:"events_24h"`
	LastAgeS  int64   `json:"last_age_s"`
	BBox      *mcpBox `json:"bbox,omitempty" jsonschema:"extent of the positions this station has heard"`
}

type mcpStationCounts struct {
	Total  int `json:"total"`
	Active int `json:"active" jsonschema:"heard within the last 5 minutes"`
}

type mcpVesselCounts struct {
	Total        int `json:"total" jsonschema:"vessels heard in the last 30 minutes"`
	WithPosition int `json:"with_position"`
}

type mcpArea struct {
	BBox     mcpBox       `json:"bbox"`
	Vessels  int          `json:"vessels" jsonschema:"vessels with a position inside the box"`
	Stations []mcpStation `json:"stations" jsonschema:"stations whose heard extent overlaps the box"`
}

type mcpCoverage struct {
	Time            string           `json:"time"`
	Summary         string           `json:"summary"`
	Sources         []mcpSource      `json:"sources"`
	Stations        mcpStationCounts `json:"stations"`
	Vessels         mcpVesselCounts  `json:"vessels"`
	EventsPerSecond float64          `json:"events_per_second"`
	Area            *mcpArea         `json:"area,omitempty"`
	Station         *mcpStation      `json:"station,omitempty"`
	Note            string           `json:"note,omitempty"`
}

// sourceDescriptions tells an assistant what each source kind is. Kinds without an entry still appear.
var sourceDescriptions = map[string]string{
	"kystverket":   "Norwegian Coastal Administration open feed: the Norwegian coast, raw NMEA",
	"barentswatch": "BarentsWatch: the Norwegian coast, offshore, and Svalbard, including satellite receivers",
	"digitraffic":  "Fintraffic Digitraffic: the Finnish coast and lakes",
	"aisstream":    "aisstream.io: worldwide aggregate, best effort",
	"aishub":       "AISHub: worldwide aggregate snapshot and the network's largest source; positions 1 to 6 minutes old",
	"udp":          "volunteer receivers sending raw NMEA over UDP, unauthenticated",
	"mmsi":         "volunteer receivers identified by their own vessel's MMSI, unauthenticated",
	"http":         "volunteer receivers posting AIS-catcher output with a token",
	"v1":           "volunteer receivers and peers publishing on /v1/stream with a token",
}

func mcpStationRow(r stationRow) mcpStation {
	s := mcpStation{Station: r.Station, Source: r.Source, Vessels: r.Vessels, Events24h: r.Events["last_24h"], LastAgeS: r.LastAgeS}
	if r.BBox != nil {
		s.BBox = &mcpBox{r.BBox[0], r.BBox[1], r.BBox[2], r.BBox[3]}
	}
	return s
}

func (p *Pipeline) mcpGetCoverage(_ context.Context, _ *mcp.CallToolRequest, in mcpCoverageIn) (*mcp.CallToolResult, mcpCoverage, error) {
	now := time.Now()
	rows := p.stations.rows(now)
	out := mcpCoverage{Time: now.UTC().Format(time.RFC3339), Stations: mcpStationCounts{Total: len(rows)}}

	age := map[string]int64{}
	for _, r := range rows {
		if r.LastAgeS < int64(stationActive.Seconds()) {
			out.Stations.Active++
		}
		k := sourceKind(r.Source)
		if a, ok := age[k]; !ok || r.LastAgeS < a {
			age[k] = r.LastAgeS
		}
	}
	vbs := p.stations.vesselsBySource()
	for _, k := range p.usage.sourceNames(now) {
		if _, ok := vbs[k]; !ok {
			vbs[k] = [2]int{}
		}
	}
	for k, vs := range vbs {
		a, ok := age[k]
		if !ok {
			a = int64(now.Sub(bootTime).Seconds())
		}
		out.Sources = append(out.Sources, mcpSource{Kind: k, Description: sourceDescriptions[k], Vessels: vs[0], VesselsExclusive: vs[1],
			Events24h: p.usage.source(k).sum(now, 24), LastAgeS: a, License: licenseOf(k), Attribution: attributionOf(k)})
	}
	sort.Slice(out.Sources, func(i, j int) bool {
		return out.Sources[i].Vessels > out.Sources[j].Vessels || (out.Sources[i].Vessels == out.Sources[j].Vessels && out.Sources[i].Kind < out.Sources[j].Kind)
	})
	if out.Sources == nil {
		out.Sources = []mcpSource{}
	}

	var box *bbox
	if in.BBox != nil {
		b := in.BBox.bbox()
		if b[0] < -90 || b[2] > 90 || b[1] < -180 || b[3] > 180 {
			return nil, mcpCoverage{}, errors.New("bbox out of range: latitudes -90 to 90, longitudes -180 to 180")
		}
		box = &b
		out.Area = &mcpArea{BBox: mcpBox{b[0], b[1], b[2], b[3]}, Stations: []mcpStation{}}
		for _, r := range rows {
			if r.BBox != nil && r.BBox[0] <= b[2] && r.BBox[2] >= b[0] && r.BBox[1] <= b[3] && r.BBox[3] >= b[1] {
				out.Area.Stations = append(out.Area.Stations, mcpStationRow(r))
			}
		}
	}
	p.vmu.RLock()
	out.Vessels.Total = len(p.vessels)
	for _, v := range p.vessels {
		if v.HasPos {
			out.Vessels.WithPosition++
			if box != nil && box.contains(v.Lat, v.Lon) {
				out.Area.Vessels++
			}
		}
	}
	p.vmu.RUnlock()

	if in.Station != "" {
		for _, r := range rows {
			if r.Station == in.Station {
				out.Station = mcpPtr(mcpStationRow(r))
			}
		}
		if out.Station == nil {
			out.Note = fmt.Sprintf("station %q has not been heard since the server started", in.Station)
		}
	}

	p.rate.mu.Lock()
	out.EventsPerSecond = math.Round(p.rate.perSec*10) / 10
	p.rate.mu.Unlock()
	out.Summary = fmt.Sprintf("%d vessels with a position, heard by %d active stations across %d sources, %.0f messages/s",
		out.Vessels.WithPosition, out.Stations.Active, len(out.Sources), out.EventsPerSecond)
	if out.Area != nil {
		out.Summary += fmt.Sprintf("; %d vessels and %d stations in the requested box", out.Area.Vessels, len(out.Area.Stations))
	}
	return nil, out, nil
}

// ---- labels, so a reader never has to decode ITU tables ----

var navStatusNames = [...]string{"Under way using engine", "At anchor", "Not under command", "Restricted manoeuvrability",
	"Constrained by her draught", "Moored", "Aground", "Engaged in fishing", "Under way sailing",
	"Reserved (HSC)", "Reserved (WIG)", "Power-driven vessel towing astern", "Power-driven vessel pushing ahead or towing alongside",
	"Reserved", "AIS-SART, MOB-AIS, or EPIRB-AIS", ""}

func navStatusName(s uint8) string {
	if int(s) < len(navStatusNames) {
		return navStatusNames[s]
	}
	return ""
}

// shipTypeName expands an ITU-R M.1371 ship and cargo type code. The tens digit is the class; for the
// hazard-carrying classes the units digit is the IMO hazard category.
func shipTypeName(t uint8) string {
	hazard := func(base string) string {
		switch t % 10 {
		case 1, 2, 3, 4:
			return fmt.Sprintf("%s, hazardous category %c", base, 'A'+rune(t%10-1))
		case 9:
			return base + ", no additional information"
		}
		return base
	}
	switch t / 10 {
	case 2:
		return hazard("Wing in ground")
	case 3:
		return [...]string{"Fishing", "Towing", "Towing, length over 200 m or breadth over 25 m", "Dredging or underwater operations",
			"Diving operations", "Military operations", "Sailing", "Pleasure craft", "Reserved", "Reserved"}[t%10]
	case 4:
		return hazard("High-speed craft")
	case 5:
		return [...]string{"Pilot vessel", "Search and rescue vessel", "Tug", "Port tender", "Anti-pollution vessel",
			"Law enforcement", "Spare", "Spare", "Medical transport", "Noncombatant ship"}[t%10]
	case 6:
		return hazard("Passenger")
	case 7:
		return hazard("Cargo")
	case 8:
		return hazard("Tanker")
	case 9:
		return hazard("Other")
	}
	return "Reserved"
}

var atonTypeNames = [...]string{"Not specified", "Reference point", "RACON", "Fixed offshore structure", "Spare",
	"Light, without sectors", "Light, with sectors", "Leading light, front", "Leading light, rear",
	"Beacon, cardinal N", "Beacon, cardinal E", "Beacon, cardinal S", "Beacon, cardinal W",
	"Beacon, port hand", "Beacon, starboard hand", "Beacon, preferred channel port hand", "Beacon, preferred channel starboard hand",
	"Beacon, isolated danger", "Beacon, safe water", "Beacon, special mark",
	"Cardinal mark N", "Cardinal mark E", "Cardinal mark S", "Cardinal mark W",
	"Port hand mark", "Starboard hand mark", "Preferred channel port hand", "Preferred channel starboard hand",
	"Isolated danger", "Safe water", "Special mark", "Light vessel, LANBY, or rig"}

func atonTypeName(t uint8) string {
	if int(t) < len(atonTypeNames) {
		return atonTypeNames[t]
	}
	return ""
}
