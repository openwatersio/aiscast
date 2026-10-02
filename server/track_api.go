package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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

// trackLimit is the most positions one track request returns for a tier. The window is the same for every
// tier; the cap bounds the work a request can ask for.
func trackLimit(cl *Claims) int {
	switch {
	case cl.Role == "anonymous":
		return 200
	case cl.Role == "personal" && !cl.Feeder:
		return 1000
	default:
		return 5000
	}
}

// trackRequest is a track query after defaults, clamping, and validation.
type trackRequest struct {
	mmsi     uint32
	from, to time.Time
	interval time.Duration
	limit    int
}

// trackMaxSpan is the longest range one request may ask for when history comes from the lake, whose reads are
// billed by the bytes they scan; a longer history pages back by moving to. ClickHouse answers trackMaxSpanCH
// from local disk.
const (
	trackMaxSpan   = 7 * 24 * time.Hour
	trackMaxSpanCH = 366 * 24 * time.Hour
)

// historySpan is the longest range a request may ask for past the track store's window, 0 when nothing holds
// history there.
func (p *Pipeline) historySpan() time.Duration {
	p.vmu.RLock()
	ch := p.ch
	p.vmu.RUnlock()
	switch {
	case ch != nil && ch.r != nil:
		return trackMaxSpanCH
	case p.lake != nil:
		return trackMaxSpan
	}
	return 0
}

// canReachArchive reports whether a tier reads positions older than the track store's window. Those reads
// scan the lake and cost money per request, which is why history is a feeder and commercial capability.
func canReachArchive(cl *Claims) bool {
	return cl.Feeder || (cl.Role != "anonymous" && cl.Role != "personal")
}

const trackArchiveTierMsg = "positions older than 48 hours need a feeder or commercial token; see https://openwaters.io/ais/"

// parseTrackRange reads from and to, RFC 3339 times. to defaults to now and from to a day before to. With
// history (span above 0) and archive true, a range may reach before the track store's window, at most span of
// it. Otherwise the range is clamped to the window, and with history a range wholly before the window is
// refused with a 403 that names the tier which reaches it. A range after now clamps to an empty one there, so
// the answer is an empty track that says where it looked.
func parseTrackRange(fromS, toS string, now time.Time, archive bool, span time.Duration) (from, to time.Time, status int, msg string) {
	lakeOn := span > 0
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
	start := now.Add(-trackWindow)
	if !lakeOn || !archive {
		// A range that only overlaps the window is clamped to it, so "the last 48 hours" computed on a client
		// a moment ahead of the server still answers. One wholly before the window is the archive's to answer.
		if lakeOn && to.Before(start) {
			return from, to, http.StatusForbidden, trackArchiveTierMsg
		}
		return clampTime(from, start, now).UTC(), clampTime(to, start, now).UTC(), 0, ""
	}
	to = clampTime(to, time.Time{}, now)
	if to.Sub(from) > span {
		return from, to, http.StatusBadRequest, fmt.Sprintf("a track covers at most %d days per request; page back by moving to", span/(24*time.Hour))
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
	if q.from, q.to, status, msg = parseTrackRange(vals.Get("from"), vals.Get("to"), now, canReachArchive(cl), p.historySpan()); msg != "" {
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
	q.interval = defaultInterval(q.to.Sub(q.from), q.limit)
	if s := vals.Get("interval"); s != "" {
		if secs, err := strconv.ParseUint(s, 10, 32); err == nil {
			q.interval = time.Duration(secs) * time.Second
		} else if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			q.interval = d
		} else {
			return q, http.StatusBadRequest, "interval=<seconds> or a duration such as 5m"
		}
	}
	return q, 0, ""
}

// intervalSteps are the round spacings a default interval rounds up to.
var intervalSteps = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
	5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour,
	6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

// defaultInterval spreads limit positions over span, rounded up to a round step, so a request that names no
// interval covers its whole range. Without it a dense range would be cut to its newest positions: a vessel
// reporting every two seconds fills a thousand positions in about half an hour. A span short enough to fit
// at full rate, a position every few seconds, needs no thinning.
func defaultInterval(span time.Duration, limit int) time.Duration {
	raw := span / time.Duration(max(limit, 1))
	if raw < 2*time.Second {
		return 0
	}
	for _, step := range intervalSteps {
		if step >= raw {
			return step
		}
	}
	return intervalSteps[len(intervalSteps)-1]
}

// trackPoints reads a vessel's positions between from and to: the lake for the part before the track store's
// window, the track store for the rest. interval thins both with the same buckets, positions implying an
// impossible speed are dropped from both, and when more than limit match, the newest limit are kept. sources
// are the source kinds that delivered the positions returned.
func (p *Pipeline) trackPoints(ctx context.Context, mmsi uint32, from, to time.Time, interval time.Duration, limit int, now time.Time) (points []trackPoint, sources []string, more bool, err error) {
	start := now.Add(-trackWindow)
	hotFrom := from
	if hotFrom.Before(start) {
		hotFrom = start
	}
	var hot []trackPoint
	if !hotFrom.After(to) {
		if hot, more, err = p.tracks.track(mmsi, hotFrom, to, interval, limit); err != nil {
			return nil, nil, false, err
		}
	}
	if more || p.historySpan() == 0 || !from.Before(start) {
		return hot, pointSources(hot), more, nil
	}
	// Keep the positions before the window, one per interval bucket, and none in the bucket the track store's
	// first position already fills.
	lakeTo := to
	if !lakeTo.Before(start) {
		lakeTo = start.Add(-time.Millisecond)
	}
	inRange, capped, err := p.historyPoints(ctx, mmsi, from, lakeTo, interval, limit-len(hot), now)
	if err != nil {
		return nil, nil, false, err
	}
	bucket := func(t time.Time) int64 {
		if ms := interval.Milliseconds(); ms > 0 {
			return t.UnixMilli() / ms
		}
		return t.UnixMilli()
	}
	firstHot := int64(-1)
	if len(hot) > 0 && interval > 0 {
		firstHot = bucket(hot[0].ts)
	}
	var old []trackPoint
	last := int64(-1)
	for _, pt := range inRange {
		if b := bucket(pt.ts); interval > 0 && (b == last || b == firstHot) {
			continue
		} else {
			last = b
		}
		old = append(old, pt)
	}
	// Judged after thinning, as the track store judges its own, and before the limit, so the limit counts
	// only positions the answer keeps.
	old = despike(old)
	if room := limit - len(hot); len(old) > room || capped {
		old, more = old[max(len(old)-room, 0):], true
	}
	points = append(old, hot...)
	return points, pointSources(points), more, nil
}

// historyPoints is one vessel's positions between from and to, oldest first, from before the track store's
// window: ClickHouse's when it is attached, else the lake's. capped says ClickHouse stopped at limit with older
// positions left, which the caller must report whatever its own filtering drops. When ClickHouse fails, the
// lake answers a range it allows, trackMaxSpan; a longer one fails, since the lake's reads are billed by the
// bytes they scan.
func (p *Pipeline) historyPoints(ctx context.Context, mmsi uint32, from, to time.Time, step time.Duration, limit int, now time.Time) (points []trackPoint, capped bool, err error) {
	p.vmu.RLock()
	ch := p.ch
	p.vmu.RUnlock()
	if ch != nil && ch.r != nil {
		points, err := ch.r.history(ctx, mmsi, from, to, step, limit, now)
		if err == nil || p.lake == nil || to.Sub(from) > trackMaxSpan {
			if len(points) > limit {
				return points[1:], true, err
			}
			return points, false, err
		}
	}
	days, err := p.lake.days(ctx, mmsi, from, to, now)
	if err != nil {
		return nil, false, err
	}
	// A late report sits in the partition after its own day, so the positions are ordered after gathering.
	for _, d := range days {
		for _, pt := range d.points {
			if !pt.ts.Before(from) && !pt.ts.After(to) {
				points = append(points, pt)
			}
		}
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].ts.Before(points[j].ts) })
	return points, false, nil
}

// pointSources is the source kinds that delivered points, the ones a track credits.
func pointSources(points []trackPoint) []string {
	var sources []string
	for _, pt := range points {
		if pt.source != "" && !contains(sources, pt.source) {
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
// heard from one vessel in the last 48 hours, as a GeoJSON Feature or, with format=gpx, a GPX track.
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
	if p.tracks == nil {
		http.Error(w, "tracks are not available on this server", http.StatusServiceUnavailable)
		return
	}
	points, sources, more, err := p.trackPoints(r.Context(), q.mmsi, q.from, q.to, q.interval, q.limit, now)
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
	if len(points) == 0 && !known {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown vessel"})
		return
	}
	attribution := map[string]string{}
	for _, s := range sources {
		noteAttribution(attribution, s)
	}
	if format == "gpx" {
		w.Header().Set("Content-Type", "application/gpx+xml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d.gpx"`, q.mmsi))
		writeGPX(w, q.mmsi, name, points, attribution)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(trackGeoJSON(q, name, points, more, attribution))
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
	Cog       []*float64 `json:"cog"`
	From      string     `json:"from"`
	Heading   []*uint16  `json:"heading"`
	Interval  int64      `json:"interval"` // seconds between positions at most; 0 is every position
	MMSI      uint32     `json:"mmsi"`
	Name      string     `json:"name,omitempty"`
	NavStatus []*uint8   `json:"nav_status"`
	Points    int        `json:"points"`
	Sog       []*float64 `json:"sog"`
	Times     []string   `json:"times"`
	To        string     `json:"to"`
	Truncated bool       `json:"truncated"`
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
func trackGeoJSON(q trackRequest, name string, points []trackPoint, more bool, attribution map[string]string) trackFeature {
	props := trackProps{MMSI: q.mmsi, Name: name, From: q.from.Format(time.RFC3339), To: q.to.Format(time.RFC3339),
		Interval: int64(q.interval / time.Second), Points: len(points), Truncated: more, Times: []string{}, Sog: []*float64{}, Cog: []*float64{},
		Heading: []*uint16{}, NavStatus: []*uint8{}}
	coords := make([][2]float64, 0, len(points))
	for _, pt := range points {
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
func writeGPX(w http.ResponseWriter, mmsi uint32, name string, points []trackPoint, attribution map[string]string) {
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
	for _, pt := range points {
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
	From            string `json:"from,omitempty" jsonschema:"start, RFC 3339 UTC; default 24 hours before to. Anonymous and personal calls reach back 48 hours; feeder and commercial tokens reach the archive, up to 366 days per call"`
	To              string `json:"to,omitempty" jsonschema:"end, RFC 3339 UTC; default now"`
	IntervalMinutes int    `json:"interval_minutes,omitempty" jsonschema:"at most one position per this many minutes; by default the limit is spread over the range at a round spacing, reported as interval_s"`
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
	From        string            `json:"from" jsonschema:"start of the range covered, after clamping to what the caller can reach: the last 48 hours, or the archive for feeder and commercial tokens"`
	To          string            `json:"to"`
	IntervalS   int64             `json:"interval_s" jsonschema:"at most one position per this many seconds; 0 is every position heard"`
	Positions   []mcpTrackPoint   `json:"positions" jsonschema:"oldest first"`
	Truncated   bool              `json:"truncated" jsonschema:"true when more positions matched than were returned; the newest were kept, so set from later or raise interval_minutes"`
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
	from, to, _, msg := parseTrackRange(in.From, in.To, now, canReachArchive(cl), p.historySpan())
	if msg != "" {
		return nil, mcpTrack{}, errors.New(msg)
	}
	if in.IntervalMinutes < 0 {
		return nil, mcpTrack{}, errors.New("interval_minutes cannot be negative")
	}
	if p.tracks == nil {
		return nil, mcpTrack{}, errors.New("tracks are not available on this server")
	}
	// An assistant asking for a day wants the whole of the vessel's day, not its last few minutes at full
	// rate, so the limit is spread over the range: from the vessel's first position in it when the track
	// store holds the range, and over the whole range when it reaches into the lake.
	interval := time.Duration(in.IntervalMinutes) * time.Minute
	var err error
	if interval == 0 {
		span := to.Sub(from)
		if !from.Before(now.Add(-trackWindow)) {
			var start time.Time
			var ok bool
			if start, ok, err = p.tracks.first(in.MMSI, from, to); ok {
				span = to.Sub(start)
			}
		}
		interval = defaultInterval(span, limit)
	}
	var points []trackPoint
	var sources []string
	var more bool
	if err == nil {
		points, sources, more, err = p.trackPoints(ctx, in.MMSI, from, to, interval, limit, now)
	}
	var name string
	var known bool
	if err == nil {
		name, known, err = p.vesselName(in.MMSI)
	}
	if err != nil {
		log.Printf("tracks: %v", err)
		return nil, mcpTrack{}, errors.New("the track store is unavailable; try again shortly")
	}
	if len(points) == 0 && !known {
		return nil, mcpTrack{}, fmt.Errorf("no vessel with MMSI %d has been heard", in.MMSI)
	}
	out := mcpTrack{MMSI: in.MMSI, Name: name, From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
		IntervalS: int64(interval / time.Second), Positions: []mcpTrackPoint{}, Truncated: more, Attribution: map[string]string{}}
	for _, pt := range points {
		lat, lon := pt.latLon()
		sog, cog, heading, nav := pt.motion()
		row := mcpTrackPoint{Seen: pt.ts.Format(time.RFC3339), Lat: lat, Lon: lon, Sog: sog, Cog: cog, Heading: heading, NavStatus: nav}
		if nav != nil {
			row.NavStatusName = navStatusName(*nav)
		}
		out.Positions = append(out.Positions, row)
	}
	for _, s := range sources {
		noteAttribution(out.Attribution, s)
	}
	return nil, out, nil
}
