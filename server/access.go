package main

// The access log: one JSON line per HTTP request, written when the response finishes, in hourly gzip files
// uploaded under access/v1/ to ACCESS_BUCKET. It answers who asked for what and what it cost, after the
// fact: the load behind a latency spike, and how clients use the API. It has a bucket of its own, never the
// archive's: the raw archive is meant to become publicly mirrorable, and R2 opens a whole bucket at once.
//
// A line never holds a token, a full address, or a precise location. It names a verified token by its
// subject, and a client by its network (the /24 or /48) and a keyed hash of the address, so one client's
// requests group across days without the address being written. The key is stable so abuse can be followed
// over weeks, and whoever holds it can recover an address from its /24, so the lines are personal data and
// kept 90 days (accessRetention). Coordinates in the query (around, a search ranked from
// the visitor's own position, and bbox, the view) are rounded to 0.1°, and a tile deeper than z12 is logged
// as the z12 tile holding it: about 10 km either way, enough to see where load falls and too coarse to
// place a home or a berth. Caddy keeps the full address in its own short log on the box, and the request id
// joins a line here to its line there.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// accessLine is one request. Stream routes log once, when the stream closes, so ms is how long it stayed open.
type accessLine struct {
	T       string  `json:"t"`            // when the response finished, UTC
	ID      string  `json:"id,omitempty"` // Caddy's request id, also in its access log
	Method  string  `json:"method"`
	Route   string  `json:"route"`           // the mux pattern; "other" when none matched
	Path    string  `json:"path"`            // canonical: the route with numeric path values, a deep tile as its z12 ancestor
	Z       int     `json:"z,omitempty"`     // the zoom of a tile request
	Query   string  `json:"query,omitempty"` // without key, coordinates rounded to 0.1°
	Status  int     `json:"status"`
	Ms      float64 `json:"ms"`
	Bytes   int64   `json:"bytes"`             // body bytes written; a WebSocket's frames bypass the writer
	Net     string  `json:"net,omitempty"`     // the client's /24 or /48
	Client  string  `json:"client,omitempty"`  // keyed hash of the client's address
	Sub     string  `json:"sub,omitempty"`     // the verified token's subject
	Role    string  `json:"role,omitempty"`    // and its role
	UA      string  `json:"ua,omitempty"`      // User-Agent
	Origin  string  `json:"origin,omitempty"`  // Origin, for browser clients
	Referer string  `json:"referer,omitempty"` // Referer without its query, which can carry a token
}

// accessNote collects what the handler learns about a request's caller, for its access line.
type accessNote struct{ sub, role string }

type accessNoteKey struct{}

// noteClaims records a verified token's subject and role for the request's access line. Anonymous claims are
// never noted: their subject holds the address.
func noteClaims(r *http.Request, c *Claims) {
	if c == nil || c.Role == "anonymous" {
		return
	}
	if n, ok := r.Context().Value(accessNoteKey{}).(*accessNote); ok {
		n.sub, n.role = c.Sub, c.Role
	}
}

// withAccessNote gives a request somewhere for its handler to note the caller.
func withAccessNote(r *http.Request) (*http.Request, *accessNote) {
	n := &accessNote{}
	return r.WithContext(context.WithValue(r.Context(), accessNoteKey{}, n)), n
}

// accessLineFor builds a finished request's line.
func accessLineFor(r *http.Request, n *accessNote, route string, status int, bytes int64, start, end time.Time) []byte {
	// Only the parameters the server reads are kept: any other may be a token under another name.
	vals := url.Values{}
	for k, vs := range r.URL.Query() {
		if accessParams[k] {
			vals[k] = vs
		}
	}
	for _, k := range []string{"around", "bbox"} {
		for i, v := range vals[k] {
			vals[k][i] = coarseCoords(v)
		}
	}
	l := accessLine{
		T: end.UTC().Format(time.RFC3339Nano), Method: r.Method, Route: route, Path: r.URL.Path, Query: vals.Encode(),
		Status: status, Ms: float64(end.Sub(start).Microseconds()) / 1000, Bytes: bytes, Sub: n.sub, Role: n.role,
		UA: r.Header.Get("User-Agent"), Origin: r.Header.Get("Origin"), Referer: stripQuery(r.Header.Get("Referer")),
	}
	l.Path, l.Z = canonicalPath(r, route)
	for _, s := range []*string{&l.Path, &l.Query, &l.UA, &l.Origin, &l.Referer} {
		*s = tokenPattern.ReplaceAllString(*s, tokenPrefix+"REDACTED")
	}
	if viaProxy(r) {
		l.ID = r.Header.Get("X-Request-Id")
	}
	l.Net, l.Client = clientNet(clientIP(r))
	b, _ := json.Marshal(l)
	return b
}

// clientNet is an address's /24 (IPv4) or /48 (IPv6), and its keyed hash. The key is STATION_SALT under a
// label of its own, so these hashes cannot be matched against UDP station ids, which hash addresses too.
func clientNet(ip string) (network, hash string) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", ""
	}
	a = a.Unmap()
	bits := 48
	if a.Is4() {
		bits = 24
	}
	pfx, _ := a.Prefix(bits)
	m := hmac.New(sha256.New, stationSalt)
	m.Write([]byte("access\x00" + a.String()))
	return pfx.String(), hex.EncodeToString(m.Sum(nil)[:8])
}

// accessParams are the query parameters the access log keeps: those the server reads, less key.
var accessParams = map[string]bool{"around": true, "bbox": true, "class": true, "format": true, "from": true,
	"interval": true, "kind": true, "limit": true, "max_age": true, "max_age_moving": true, "min_sog": true,
	"mmsi": true, "q": true, "snapshot": true, "to": true, "type": true}

// tokenPattern is an access token wherever it turns up: a path segment, a pasted URL, a user agent.
var tokenPattern = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(tokenPrefix) + `[^\s/?&#"]*`)

// knownSegments are the first path segments an unmatched path keeps; any other is logged as x.
var knownSegments = map[string]bool{"": true, "v0": true, "v1": true, "mcp": true, "health": true, "metrics": true,
	"openapi.json": true, "robots.txt": true, "favicon.ico": true}

// stationIDShape is what a station id looks like (udp:<hex>, station:ed25519:<key>[/n2k], mmsi:<n>), so a
// path such as a tile's numbers under /v1/stations/ is logged as x.
var stationIDShape = regexp.MustCompile(`^(?:station|udp|mmsi):[A-Za-z0-9:_./-]{1,120}$`)

// accessTileZoom is the deepest tile the access log names: a z12 tile is about 10 km across.
const accessTileZoom = 12

// canonicalPath is the path a line keeps: the matched route with its path values filled in only where they
// are plain numbers (an MMSI, a tile's coordinates, coarsened), and x for anything else, so coordinates or
// a token put where a number belongs never reach the log. A path no route matched keeps its first segment.
func canonicalPath(r *http.Request, route string) (string, int) {
	switch {
	case route == "other":
		first, _, _ := strings.Cut(strings.TrimLeft(r.URL.Path, "/"), "/")
		if !knownSegments[first] {
			first = "x"
		}
		return "/" + first, 0
	case route == "/{$}":
		return "/", 0
	}
	if route == "/v1/vessels/tiles/{z}/{x}/{y}" {
		return coarseTilePath(r.PathValue("z"), r.PathValue("x"), r.PathValue("y"))
	}
	if strings.HasSuffix(route, "/") { // a subtree route: the rest of the path is its argument, a public station id
		if rest := strings.TrimPrefix(r.URL.Path, route); !stationIDShape.MatchString(rest) {
			return route + "x", 0
		}
		return r.URL.Path, 0
	}
	parts := strings.Split(route, "/")
	for i, p := range parts {
		if name, ok := strings.CutPrefix(p, "{"); ok {
			v := r.PathValue(strings.TrimSuffix(name, "}"))
			if v == "" || strings.Trim(v, "0123456789") != "" {
				v = "x"
			}
			parts[i] = v
		}
	}
	return strings.Join(parts, "/"), 0
}

// coarseTilePath names a tile no deeper than accessTileZoom, with the requested zoom. Each coordinate is
// read from its leading digits, so a tile URL with an extension (.pbf, .mvt) is still named coarse; one
// whose numbers do not parse is named invalid.
func coarseTilePath(zs, xs, ys string) (string, int) {
	const prefix = "/v1/vessels/tiles/"
	var nums [3]int
	for i, p := range []string{zs, xs, ys} {
		n, err := strconv.Atoi(p[:len(p)-len(strings.TrimLeft(p, "0123456789"))])
		if err != nil {
			return prefix + "invalid", 0
		}
		nums[i] = n
	}
	z, x, y := nums[0], nums[1], nums[2]
	if z > 30 {
		return prefix + "invalid", 0
	}
	if z > accessTileZoom {
		x, y = x>>(z-accessTileZoom), y>>(z-accessTileZoom)
	}
	return fmt.Sprintf("%s%d/%d/%d", prefix, min(z, accessTileZoom), x, y), z
}

// coarseCoords rounds each number in a comma-separated coordinate list to 0.1°. A part that is not a
// number is dropped, so nothing precise survives a malformed value.
func coarseCoords(s string) string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if f, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			out = append(out, strconv.FormatFloat(math.Round(f*10)/10, 'f', 1, 64))
		}
	}
	return strings.Join(out, ",")
}

func stripQuery(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// viaProxy reports a request Caddy forwarded: only Caddy and a local browser connect from loopback, and only
// then are its headers about the client to be believed.
func viaProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// accessDir is ACCESS_DIR, local staging that mirrors the bucket's layout; "off" turns the log off.
func accessDir() string {
	if d := env("ACCESS_DIR", "access"); d != "off" {
		return d
	}
	return ""
}

// accessStoreFromEnv is ACCESS_BUCKET, in the raw archive's account and with its keys: never R2_BUCKET or
// NORMALIZED_BUCKET, even when ACCESS_BUCKET names one of them.
func accessStoreFromEnv() *s3Client {
	b := os.Getenv("ACCESS_BUCKET")
	if b != "" && (b == os.Getenv("R2_BUCKET") || b == os.Getenv("NORMALIZED_BUCKET")) {
		log.Printf("ACCESS_BUCKET is the archive's bucket; the access log stays on disk")
		return nil
	}
	return s3BucketFromEnv(b)
}

// accessPrefix is the access log's top-level directory. Its hour keys have the shape of raw ones, so replay
// and the replay job's sync skip it by name, as they skip normPrefix, should a copy ever share a tree with
// the raw archive.
const accessPrefix = "access"

// accessRetention is how long access lines are kept: long enough to follow abuse across weeks. The bucket
// enforces it with a lifecycle rule; without a bucket, the sweep deletes older hours on disk.
const accessRetention = 90 * 24 * time.Hour

// newAccessArchive is the archive writer configured for the access log: one file per hour, a line per request.
func newAccessArchive(dir string, s3 *s3Client) *archive {
	a := newArchive(dir, s3)
	a.bare, a.keepFor, a.keepUnder = true, accessRetention, accessPrefix
	a.keyFn = func(_ string, hour time.Time) string {
		return filepath.Join(accessPrefix, "v1", hour.Format("2006/01/02/15")+".gz")
	}
	return a
}

// logAccess queues a finished request's line, dropping it when the writer has fallen behind: a line is worth
// less than the request it describes, which must not wait on the disk.
func (p *Pipeline) logAccess(r *http.Request, n *accessNote, route string, status int, bytes int64, start, end time.Time) {
	if p.access.dir == "" {
		return
	}
	line := accessLineFor(r, n, route, status, bytes, start, end)
	if !p.access.offer(Reception{RecvTime: end, Body: string(line)}) {
		p.accessDropped.Add(1)
	}
}
