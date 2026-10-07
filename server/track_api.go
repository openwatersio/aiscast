package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	trackDefaultSpan  = 24 * time.Hour
	trackDefaultLimit = 1000
)

// trackLimit is the most positions one track request returns for a tier. Every tier reaches the same history;
// the cap bounds the answer, and trackStep the step anonymous and personal requests read it at.
func trackLimit(cl *Claims) int {
	switch {
	case cl.Role == "anonymous":
		return 1000
	case cl.Role == "personal" && !cl.Feeder:
		return 1000
	default:
		return 5000
	}
}

// trackStep is the step a track is read at. An anonymous or personal range that reaches past the last 48 hours
// is read in whole minutes, interval rounded up, so it reads positions_1m rather than group every copy in the
// range through the positions view, which the limit does not bound. Feeder and above keep interval. A minute of
// slack keeps "the last 48 hours" from a client whose clock runs behind out of the rounding.
func trackStep(cl *Claims, from time.Time, interval time.Duration, now time.Time) time.Duration {
	const w = time.Minute
	if cl.Feeder || (cl.Role != "anonymous" && cl.Role != "personal") || !from.Before(now.Add(-trackWindow-time.Minute)) {
		return interval
	}
	interval = min(interval, trackMaxSpan) // a step longer than any range keeps one position, and cannot overflow below
	return max((interval+w-1)/w*w, w)
}

// trackRequest is a track query after defaults, clamping, and validation.
type trackRequest struct {
	mmsi     uint32
	from, to time.Time
	interval time.Duration // the step asked for, or with shape the resolution the positions were read at
	shape    bool          // no interval asked: simplify by shape
	limit    int
}

// trackMaxSpan is the longest range one request may ask for; a longer history pages back by moving to.
const trackMaxSpan = 366 * 24 * time.Hour

// trackReader is ClickHouse's reader, nil while CLICKHOUSE_URL is unset or it has not answered.
func (p *Pipeline) trackReader() chReader {
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	if p.ch == nil {
		return nil
	}
	return p.ch.r
}

// parseTrackRange reads from and to, RFC 3339 times. to defaults to now and from to a day before to. A range
// covers at most trackMaxSpan. A range after now clamps to an empty one there, so the answer is an empty track
// that says where it looked.
func parseTrackRange(fromS, toS string, now time.Time) (from, to time.Time, status int, msg string) {
	to = now
	if toS != "" {
		t, err := time.Parse(time.RFC3339, toS)
		if err != nil {
			return from, to, http.StatusBadRequest, "to must be an RFC 3339 time, such as 2026-09-28T12:00:00Z"
		}
		to = t
	}
	from = to.Add(-trackDefaultSpan)
	if fromS != "" {
		t, err := time.Parse(time.RFC3339, fromS)
		if err != nil {
			return from, to, http.StatusBadRequest, "from must be an RFC 3339 time, such as 2026-09-27T12:00:00Z"
		}
		from = t
	}
	if !from.Before(to) {
		return from, to, http.StatusBadRequest, "from must be before to"
	}
	to = clampTime(to, time.Time{}, now)
	if to.Sub(from) > trackMaxSpan {
		return from, to, http.StatusBadRequest, fmt.Sprintf("a track covers at most %d days per request; page back by moving to", trackMaxSpan/(24*time.Hour))
	}
	return clampTime(from, time.Time{}, now).UTC(), to.UTC(), 0, ""
}

func clampTime(t, lo, hi time.Time) time.Time {
	if t.Before(lo) {
		return lo
	}
	if t.After(hi) {
		return hi
	}
	return t
}

func (p *Pipeline) parseTrackRequest(r *http.Request, cl *Claims, now time.Time) (trackRequest, int, string) {
	var q trackRequest
	n, err := strconv.ParseUint(r.PathValue("mmsi"), 10, 32)
	if err != nil {
		return q, http.StatusBadRequest, "mmsi must be a number"
	}
	q.mmsi = uint32(n)
	vals := r.URL.Query()
	var status int
	var msg string
	if q.from, q.to, status, msg = parseTrackRange(vals.Get("from"), vals.Get("to"), now); msg != "" {
		return q, status, msg
	}
	cap := trackLimit(cl)
	q.limit = min(trackDefaultLimit, cap)
	if s := vals.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return q, http.StatusBadRequest, "limit must be a positive number"
		}
		q.limit = min(n, cap)
	}
	s := vals.Get("interval")
	if s == "" {
		q.shape = true
		return q, 0, ""
	}
	if secs, err := strconv.ParseUint(s, 10, 32); err == nil {
		q.interval = time.Duration(secs) * time.Second
	} else if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		q.interval = d
	} else {
		return q, http.StatusBadRequest, "interval=<seconds> or a duration such as 5m"
	}
	q.interval = p.answeredStep(q.from, q.to, trackStep(cl, q.from, q.interval, now), now)
	return q, 0, ""
}

// shapeRows bounds the positions a simplified track reads, which bounds the time simplifying them takes: a
// busy vessel's 48 hours grouped to 4 seconds, a month of positions_1m as it is, or a year grouped to 11 minutes.
const shapeRows = 50_000

// shapeRead is the step a simplified track reads its range at, and the silence that breaks it. A range that
// starts in the last 48 hours reads every position, and a silence over 30 minutes means the vessel went unheard.
// Further back it reads positions_1m, a minute while moving and a heartbeat per 30-minute window while still, so
// two heartbeats can stand almost an hour apart; a long range groups those minutes to keep shapeRows.
func shapeRead(from, to, now time.Time) (step, silence time.Duration) {
	if now.Sub(from) <= trackWindow {
		// A vessel reporting every 2 seconds sends 86,400 positions in 48 hours; past shapeRows, group them
		// to whole seconds, as finely as fits.
		if step = to.Sub(from) / shapeRows; step < time.Second {
			step = 0
		} else {
			step = (step + time.Second - 1) / time.Second * time.Second
		}
		return step, 30*time.Minute + step
	}
	step = max(time.Minute, to.Sub(from)/shapeRows)
	step = (step + time.Minute - 1) / time.Minute * time.Minute
	return step, time.Hour + step
}

// trackShape reads a vessel's positions between from and to at full detail, drops those implying an impossible
// speed, and simplifies the rest by shape to fit limit. step is the resolution it read them at.
func (p *Pipeline) trackShape(ctx context.Context, mmsi uint32, from, to time.Time, limit int, now time.Time) (s shape, step time.Duration, err error) {
	r := p.trackReader()
	if r == nil {
		return shape{}, 0, errNoTracks
	}
	step, silence := shapeRead(from, to, now)
	if !validMMSI(mmsi) {
		return shape{}, step, nil // see trackPoints
	}
	points, err := r.history(ctx, mmsi, from, to, step, shapeRows, now)
	if err != nil {
		return shape{}, 0, err
	}
	// history returns one row past shapeRows, the oldest, when more match; a range that dense keeps its newest.
	cut := len(points) > shapeRows
	if cut {
		points = points[1:]
	}
	s = simplify(despike(points), silence, limit)
	s.more = s.more || cut
	return s, step, nil
}

// trackPoints reads a vessel's positions between from and to from ClickHouse, which thins them by interval
// and returns the newest limit, and one more that says older positions were left out. Positions implying an
// impossible speed are dropped, judged against that extra row too, and the limit then trims it.
func (p *Pipeline) trackPoints(ctx context.Context, mmsi uint32, from, to time.Time, interval time.Duration, limit int, now time.Time) (shape, error) {
	r := p.trackReader()
	if r == nil {
		return shape{}, errNoTracks
	}
	// ClickHouse can hold rows under an MMSI the fold keeps out, several boats' positions as one track.
	if !validMMSI(mmsi) {
		return shape{}, nil
	}
	points, err := r.history(ctx, mmsi, from, to, interval, limit, now)
	if err != nil {
		return shape{}, err
	}
	capped := len(points) > limit
	// A rollup whose windows do not divide the step returns a position per window; each goes in the step
	// bucket of its own time.
	if ms := interval.Milliseconds(); ms > 0 {
		kept, last := points[:0], int64(-1)
		for _, pt := range points {
			if b := pt.ts.UnixMilli() / ms; b != last {
				kept, last = append(kept, pt), b
			}
		}
		points = kept
	}
	points = despike(points)
	more := false
	if len(points) > limit || capped {
		points, more = points[max(len(points)-limit, 0):], true
	}
	return shape{points: points, more: more}, nil
}

// errNoTracks is a track asked of a server without ClickHouse.
var errNoTracks = errors.New("tracks are not available on this server")

// answeredStep is the step an answer from from to to can keep: interval, or the window of the rollup ClickHouse
// reads a range from when the range is too long for every position, as for interval 0 over more than
// chFineSpan. The whole answer is thinned to it and reports it, so no part claims more positions than it holds.
func (p *Pipeline) answeredStep(from, to time.Time, interval time.Duration, now time.Time) time.Duration {
	if p.trackReader() == nil {
		return interval
	}
	_, window := chTable(from, to, interval, now)
	return max(interval, window)
}

// pointSources is the source kinds that delivered points, the ones a track credits.
func pointSources(points []trackPoint) []string {
	var sources []string
	for _, pt := range points {
		if pt.source != "" && !slices.Contains(sources, pt.source) {
			sources = append(sources, pt.source)
		}
	}
	return sources
}

// vesselName is the vessel's name from the cache or the record, and whether either knows the vessel.
func (p *Pipeline) vesselName(mmsi uint32) (name string, known bool, err error) {
	p.vmu.RLock()
	if v := p.vessels[mmsi]; v != nil {
		name, known = v.Name, true
	}
	p.vmu.RUnlock()
	if name == "" && p.store != nil {
		rec, ok, err := p.store.get(mmsi)
		if err != nil {
			return "", known, err
		}
		if ok {
			name, known = rec.v.Name, true
		}
	}
	return strings.TrimSpace(name), known, nil
}

// serveTrack: GET /v1/vessels/{mmsi}/track?from&to&interval&limit&format → the positions the network
// heard from one vessel over the range, as a GeoJSON Feature or, with format=gpx, a GPX track.
func (p *Pipeline) serveTrack(w http.ResponseWriter, r *http.Request) {
	cl, err := p.requestClaims(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "geojson" && format != "gpx" {
		http.Error(w, "format=geojson or gpx", http.StatusBadRequest)
		return
	}
	now := time.Now()
	q, status, msg := p.parseTrackRequest(r, p.effective(cl), now)
	if msg != "" {
		http.Error(w, msg, status)
		return
	}
	if p.trackReader() == nil {
		http.Error(w, errNoTracks.Error(), http.StatusServiceUnavailable)
		return
	}
	var s shape
	if q.shape {
		s, q.interval, err = p.trackShape(r.Context(), q.mmsi, q.from, q.to, q.limit, now)
	} else {
		s, err = p.trackPoints(r.Context(), q.mmsi, q.from, q.to, q.interval, q.limit, now)
	}
	var name string
	var known bool
	if err == nil {
		name, known, err = p.vesselName(q.mmsi)
	}
	if err != nil {
		log.Printf("tracks: %v", err)
		http.Error(w, "track unavailable", http.StatusInternalServerError)
		return
	}
	if len(s.points) == 0 && !known {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown vessel"})
		return
	}
	attribution := map[string]string{}
	for _, src := range pointSources(s.points) {
		noteAttribution(attribution, src)
	}
	if format == "gpx" {
		w.Header().Set("Content-Type", "application/gpx+xml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d.gpx"`, q.mmsi))
		writeGPX(w, q.mmsi, name, s, attribution)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(trackGeoJSON(q, name, s, attribution))
}

type trackFeature struct {
	Attribution map[string]string `json:"attribution"`
	Geometry    *trackGeometry    `json:"geometry"`
	ID          uint32            `json:"id"`
	Properties  trackProps        `json:"properties"`
	Type        string            `json:"type"`
}

type trackGeometry struct {
	Coordinates any    `json:"coordinates"`
	Type        string `json:"type"`
}

// trackProps carries per-position values as arrays aligned with the geometry's coordinates. A value AIS
// marks not available is null.
type trackProps struct {
	Breaks     *[]int     `json:"breaks,omitempty"` // with simplified, always present: positions that start a segment after a silence
	Cog        []*float64 `json:"cog"`
	From       string     `json:"from"`
	Heading    []*uint16  `json:"heading"`
	Interval   int64      `json:"interval"` // seconds between positions at most; 0 is every position
	MMSI       uint32     `json:"mmsi"`
	Name       string     `json:"name,omitempty"`
	NavStatus  []*uint8   `json:"nav_status"`
	Points     int        `json:"points"`
	Simplified bool       `json:"simplified"`
	Sog        []*float64 `json:"sog"`
	Times      []string   `json:"times"`
	To         string     `json:"to"`
	ToleranceM *float64   `json:"tolerance_m,omitempty"`
	Truncated  bool       `json:"truncated"`
}

func (pt trackPoint) latLon() (float64, float64) {
	return float64(pt.lat6) / 600000, float64(pt.lon6) / 600000
}

func (pt trackPoint) motion() (sog, cog *float64, heading *uint16, nav *uint8) {
	if pt.sog10 < 1023 {
		sog = mcpPtr(float64(pt.sog10) / 10)
	}
	if pt.cog10 < 3600 {
		cog = mcpPtr(float64(pt.cog10) / 10)
	}
	if pt.heading < 511 {
		heading = mcpPtr(pt.heading)
	}
	if pt.navStatus != 15 {
		nav = mcpPtr(pt.navStatus)
	}
	return
}

// trackGeoJSON is the track as a Feature: a LineString for two or more positions, a Point for one, and no
// geometry for none.
func trackGeoJSON(q trackRequest, name string, s shape, attribution map[string]string) trackFeature {
	props := trackProps{MMSI: q.mmsi, Name: name, From: q.from.Format(time.RFC3339), To: q.to.Format(time.RFC3339),
		Interval: int64(q.interval / time.Second), Points: len(s.points), Truncated: s.more, Times: []string{}, Sog: []*float64{}, Cog: []*float64{},
		Heading: []*uint16{}, NavStatus: []*uint8{}, Simplified: s.simplified}
	if s.simplified {
		props.Breaks, props.ToleranceM = &s.breaks, mcpPtr(math.Round(s.tolerance*10)/10)
	}
	coords := make([][2]float64, 0, len(s.points))
	for _, pt := range s.points {
		lat, lon := pt.latLon()
		coords = append(coords, [2]float64{lon, lat})
		sog, cog, heading, nav := pt.motion()
		props.Times = append(props.Times, pt.ts.Format(time.RFC3339))
		props.Sog, props.Cog = append(props.Sog, sog), append(props.Cog, cog)
		props.Heading, props.NavStatus = append(props.Heading, heading), append(props.NavStatus, nav)
	}
	f := trackFeature{Attribution: attribution, ID: q.mmsi, Properties: props, Type: "Feature"}
	switch len(coords) {
	case 0:
	case 1:
		f.Geometry = &trackGeometry{Type: "Point", Coordinates: coords[0]}
	default:
		f.Geometry = &trackGeometry{Type: "LineString", Coordinates: coords}
	}
	return f
}

// writeGPX writes a GPX 1.1 track. The credit lines go in the metadata description, since a GPX file
// travels without the page it came from.
func writeGPX(w http.ResponseWriter, mmsi uint32, name string, s shape, attribution map[string]string) {
	title := strconv.FormatUint(uint64(mmsi), 10)
	if name != "" {
		title = name + " (" + title + ")"
	}
	credits := make([]string, 0, len(attribution))
	for _, c := range attribution {
		credits = append(credits, c)
	}
	sort.Strings(credits)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<gpx version="1.1" creator="Open Waters AIS (https://openwaters.io/ais/)" xmlns="http://www.topografix.com/GPX/1/1">` + "\n")
	fmt.Fprintf(&b, "<metadata><name>%s</name><desc>%s</desc></metadata>\n", xmlEscape(title), xmlEscape(strings.Join(credits, " ")))
	fmt.Fprintf(&b, "<trk><name>%s</name><trkseg>\n", xmlEscape(title))
	for i, pt := range s.points {
		if slices.Contains(s.breaks, i) { // a segment per stretch the vessel was heard
			b.WriteString("</trkseg><trkseg>\n")
		}
		lat, lon := pt.latLon()
		fmt.Fprintf(&b, `<trkpt lat="%.6f" lon="%.6f"><time>%s</time></trkpt>`+"\n", lat, lon, pt.ts.Format(time.RFC3339))
	}
	b.WriteString("</trkseg></trk>\n</gpx>\n")
	w.Write([]byte(b.String()))
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- MCP ----

const mcpTrackDefaultLimit = 50

type mcpTrackIn struct {
	MMSI            uint32 `json:"mmsi" jsonschema:"the vessel's MMSI; use search_vessels_by_name first when you only have a name"`
	From            string `json:"from,omitempty" jsonschema:"start, RFC 3339 UTC; default 24 hours before to. A call covers up to 366 days"`
	To              string `json:"to,omitempty" jsonschema:"end, RFC 3339 UTC; default now"`
	IntervalMinutes int    `json:"interval_minutes,omitempty" jsonschema:"at most one position per this many minutes, evenly spaced; by default the track is simplified by shape instead, keeping the positions that hold its path. Anonymous and personal calls reaching past 48 hours are rounded up to whole minutes"`
	Limit           int    `json:"limit,omitempty" jsonschema:"positions to return: default 50, maximum 200; when more match, the newest are kept"`
}

type mcpTrackPoint struct {
	Seen          string   `json:"seen" jsonschema:"time of the report, RFC 3339 UTC"`
	Lat           float64  `json:"lat"`
	Lon           float64  `json:"lon"`
	Sog           *float64 `json:"sog,omitempty" jsonschema:"speed over ground, knots"`
	Cog           *float64 `json:"cog,omitempty" jsonschema:"course over ground, degrees true"`
	Heading       *uint16  `json:"heading,omitempty" jsonschema:"true heading, degrees"`
	NavStatus     *uint8   `json:"nav_status,omitempty" jsonschema:"AIS navigational status code"`
	NavStatusName string   `json:"nav_status_name,omitempty"`
}

type mcpTrack struct {
	MMSI        uint32            `json:"mmsi"`
	Name        string            `json:"name,omitempty"`
	From        string            `json:"from" jsonschema:"start of the range covered"`
	To          string            `json:"to"`
	IntervalS   int64             `json:"interval_s" jsonschema:"at most one position per this many seconds; 0 is every position heard. Simplified, the resolution the positions were read at"`
	Simplified  bool              `json:"simplified" jsonschema:"true when the track was simplified by shape: the positions that hold its path, not evenly spaced"`
	ToleranceM  *float64          `json:"tolerance_m,omitempty" jsonschema:"simplified: every position left out lay within this many meters of the line through the ones kept"`
	Breaks      *[]int            `json:"breaks,omitempty" jsonschema:"simplified, always present and empty when the vessel was heard throughout: indexes of positions that start a stretch after it went unheard; draw no line into them"`
	Positions   []mcpTrackPoint   `json:"positions" jsonschema:"oldest first"`
	Truncated   bool              `json:"truncated" jsonschema:"true when positions were left out: simplified, the tolerance rose past 15 m to fit the limit, or the range held more than 50,000 positions and the oldest were not read; by interval, the newest were kept, so set from later or raise interval_minutes"`
	Attribution map[string]string `json:"attribution" jsonschema:"credit line per source kind in the positions, to show with the data"`
}

func (p *Pipeline) mcpGetVesselTrack(ctx context.Context, _ *mcp.CallToolRequest, in mcpTrackIn) (*mcp.CallToolResult, mcpTrack, error) {
	cl := p.effective(mcpClaims(ctx))
	limit := mcpTrackDefaultLimit
	if in.Limit != 0 {
		n, err := mcpLimit(in.Limit)
		if err != nil {
			return nil, mcpTrack{}, err
		}
		limit = n
	}
	limit = min(limit, trackLimit(cl))
	now := time.Now()
	from, to, _, msg := parseTrackRange(in.From, in.To, now)
	if msg != "" {
		return nil, mcpTrack{}, errors.New(msg)
	}
	if in.IntervalMinutes < 0 {
		return nil, mcpTrack{}, errors.New("interval_minutes cannot be negative")
	}
	r := p.trackReader()
	if r == nil {
		return nil, mcpTrack{}, errNoTracks
	}
	// An assistant asking for a day wants the shape of the vessel's day, not its last few minutes at full rate,
	// so without an interval the track is simplified by shape to fit the limit.
	var s shape
	var interval time.Duration
	var err error
	if in.IntervalMinutes == 0 {
		s, interval, err = p.trackShape(ctx, in.MMSI, from, to, limit, now)
	} else {
		interval = p.answeredStep(from, to, trackStep(cl, from, time.Duration(in.IntervalMinutes)*time.Minute, now), now)
		s, err = p.trackPoints(ctx, in.MMSI, from, to, interval, limit, now)
	}
	var name string
	var known bool
	if err == nil {
		name, known, err = p.vesselName(in.MMSI)
	}
	if err != nil {
		log.Printf("tracks: %v", err)
		return nil, mcpTrack{}, errors.New("tracks are unavailable; try again shortly")
	}
	if len(s.points) == 0 && !known {
		return nil, mcpTrack{}, fmt.Errorf("no vessel with MMSI %d has been heard", in.MMSI)
	}
	out := mcpTrack{MMSI: in.MMSI, Name: name, From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
		IntervalS: int64(interval / time.Second), Positions: []mcpTrackPoint{}, Truncated: s.more, Attribution: map[string]string{},
		Simplified: s.simplified}
	if s.simplified {
		out.Breaks, out.ToleranceM = &s.breaks, mcpPtr(math.Round(s.tolerance*10)/10)
	}
	for _, pt := range s.points {
		lat, lon := pt.latLon()
		sog, cog, heading, nav := pt.motion()
		row := mcpTrackPoint{Seen: pt.ts.Format(time.RFC3339), Lat: lat, Lon: lon, Sog: sog, Cog: cog, Heading: heading, NavStatus: nav}
		if nav != nil {
			row.NavStatusName = navStatusName(*nav)
		}
		out.Positions = append(out.Positions, row)
	}
	for _, src := range pointSources(s.points) {
		noteAttribution(out.Attribution, src)
	}
	return nil, out, nil
}
