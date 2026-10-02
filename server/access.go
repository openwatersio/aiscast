package main

// The access log: one JSON line per HTTP request, written when the response finishes, in hourly gzip files
// uploaded beside the archive under access/v1/. It answers who asked for what and what it cost, after the
// fact: the load behind a latency spike, and how clients use the API.
//
// A line never holds a token or a full address. It names a verified token by its subject, and a client by
// its network (the /24 or /48) and a keyed hash of the address, so one client's requests group without the
// address being stored. Caddy keeps the full address in its own short log on the box, and the request id
// joins a line here to its line there.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"time"
)

// accessLine is one request. Stream routes log once, when the stream closes, so ms is how long it stayed open.
type accessLine struct {
	T       string  `json:"t"`            // when the response finished, UTC
	ID      string  `json:"id,omitempty"` // Caddy's request id, also in its access log
	Method  string  `json:"method"`
	Route   string  `json:"route"` // the mux pattern; "other" when none matched
	Path    string  `json:"path"`
	Query   string  `json:"query,omitempty"` // without key
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
	vals := r.URL.Query()
	vals.Del("key")
	l := accessLine{
		T: end.UTC().Format(time.RFC3339Nano), Method: r.Method, Route: route, Path: r.URL.Path, Query: vals.Encode(),
		Status: status, Ms: float64(end.Sub(start).Microseconds()) / 1000, Bytes: bytes, Sub: n.sub, Role: n.role,
		UA: r.Header.Get("User-Agent"), Origin: r.Header.Get("Origin"), Referer: stripQuery(r.Header.Get("Referer")),
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

// accessPrefix is the access log's top-level directory in the archive bucket. Its hour keys have the shape
// of raw ones, so replay and the replay job's sync skip it by name, as they skip normPrefix.
const accessPrefix = "access"

// newAccessArchive is the archive writer configured for the access log: one file per hour, a line per request.
func newAccessArchive(dir string, s3 *s3Client) *archive {
	a := newArchive(dir, s3)
	a.bare = true
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
