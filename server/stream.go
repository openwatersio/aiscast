package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
)

const maxPublishFrame = 1000 // sentences per publish frame; the rest are dropped and counted

var wsOpts = &websocket.AcceptOptions{OriginPatterns: []string{"*"}, CompressionMode: websocket.CompressionContextTakeover}

// Vars so tests can shrink them; atomic because a handler's pingLoop can outlive its test and read while
// the next test writes.
var pingEvery, pingTimeout atomic.Int64

func init() { pingEvery.Store(int64(30 * time.Second)); pingTimeout.Store(int64(10 * time.Second)) }

// pingLoop ends the handler when the peer stops answering pings, so a dropped link (NAT/VPN timeout, half-open
// socket behind a proxy) releases its stream slots instead of holding them until TCP gives up. Ping only
// completes while the handler's Read loop is running; every stream handler has one.
func (p *Pipeline) pingLoop(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	defer cancel()
	t := time.NewTicker(time.Duration(pingEvery.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pc := context.WithTimeout(ctx, time.Duration(pingTimeout.Load()))
			err := c.Ping(pctx)
			pc()
			if err != nil {
				if ctx.Err() == nil { // peer is gone; skip the close handshake it would never answer
					p.stats.pingTimeouts.Add(1)
					c.CloseNow()
				}
				return
			}
		}
	}
}

// ---- /v0/stream: aisstream.io wire protocol. Frozen; nothing here may deviate. ----

// aisstream's subscription object; encoding/json matches keys case-insensitively, as aisstream does.
type v0Sub struct {
	APIKey             string
	BoundingBoxes      [][][]float64
	FiltersShipMMSI    []string
	FilterMessageTypes []string
}

type bbox [4]float64 // minLat, minLon, maxLat, maxLon

func (b bbox) contains(lat, lon float64) bool {
	return lat >= b[0] && lat <= b[2] && lon >= b[1] && lon <= b[3]
}

type v0Filter struct {
	boxes []bbox
	mmsi  map[uint32]bool
	types map[string]bool
}

const (
	errBadKey    = "Api Key Is Not Valid"
	errMalformed = "Subscription Object Is Malformed"
)

const (
	errBoxDenied   = "Bounding Box Not Allowed For This Key"
	errMMSIsDenied = "Too Many MMSI Filters For This Key"
)

// parseV0Sub validates an aisstream subscription against the token in APIKey. The claims come back so the
// caller can enforce connection caps; with ALLOW_ANON any non-empty key is an anonymous admin.
func (p *Pipeline) parseV0Sub(data []byte, ip string) (*v0Filter, *Claims, string) {
	var s v0Sub
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, errMalformed
	}
	if s.APIKey == "" {
		return nil, nil, errBadKey
	}
	c, err := p.auth.verify(s.APIKey, time.Now())
	if err != nil {
		if !allowAnon {
			return nil, nil, errBadKey
		}
		c = &Claims{Sub: "anon", Role: "admin"}
	}
	c = p.effective(c) // earned feeder tier applies on /v0 too
	if !c.may("subscribe") || !c.allowsIP(ip) {
		return nil, nil, errBadKey
	}
	if len(s.BoundingBoxes) == 0 || (len(s.FiltersShipMMSI) > 50 && c.MMSIs <= 50) { // aisstream caps the list at 50 unless the token's mmsis claim raises it
		return nil, nil, errMalformed
	}
	f := &v0Filter{}
	for _, box := range s.BoundingBoxes {
		if len(box) != 2 || len(box[0]) != 2 || len(box[1]) != 2 {
			return nil, nil, errMalformed
		}
		b := bbox{min(box[0][0], box[1][0]), min(box[0][1], box[1][1]), max(box[0][0], box[1][0]), max(box[0][1], box[1][1])}
		if b[0] < -90 || b[2] > 90 || b[1] < -180 || b[3] > 180 {
			return nil, nil, errMalformed
		}
		if !c.allowsBox(b) {
			return nil, nil, errBoxDenied
		}
		f.boxes = append(f.boxes, b)
	}
	// an MMSI filter bounds the traffic by the list, not the box, so the area cap does not apply to it
	if len(s.FiltersShipMMSI) == 0 && !c.allowsArea(f.boxes) {
		return nil, nil, errBoxDenied
	}
	if !c.allowsMMSIs(len(s.FiltersShipMMSI)) {
		return nil, nil, errMMSIsDenied
	}
	if len(s.FiltersShipMMSI) > 0 {
		f.mmsi = map[uint32]bool{}
		for _, m := range s.FiltersShipMMSI {
			n, err := strconv.ParseUint(m, 10, 32)
			if err != nil || len(m) != 9 {
				return nil, nil, errMalformed
			}
			f.mmsi[uint32(n)] = true
		}
	}
	if len(s.FilterMessageTypes) > 0 {
		f.types = map[string]bool{}
		for _, t := range s.FilterMessageTypes {
			if f.types[t] {
				return nil, nil, errMalformed // duplicates are rejected upstream
			}
			f.types[t] = true
		}
	}
	return f, c, ""
}

func (f *v0Filter) match(ev *Event) bool {
	if f.mmsi != nil && !f.mmsi[ev.MMSI] || f.types != nil && !f.types[ev.Type] || !ev.HasPos {
		return false
	}
	for _, b := range f.boxes {
		if b.contains(ev.Lat, ev.Lon) {
			return true
		}
	}
	return false
}

// renderV0 produces the aisstream envelope. The packet is round-tripped through a map so keys come out
// sorted, matching aisstream's output; MetaData is a map for the same reason.
func (ev *Event) renderV0() []byte {
	ev.v0Once.Do(func() {
		b, _ := json.Marshal(ev.Packet)
		var m map[string]any
		json.Unmarshal(b, &m)
		meta := map[string]any{
			"MMSI": ev.MMSI, "MMSI_String": ev.MMSI, "ShipName": ev.Name,
			"latitude": ev.Lat, "longitude": ev.Lon, "time_utc": ev.Time.UTC().String(),
		}
		ev.v0, _ = json.Marshal(map[string]any{"Message": map[string]any{ev.Type: m}, "MessageType": ev.Type, "MetaData": meta})
	})
	return ev.v0
}

func wsWriteJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

func (p *Pipeline) serveV0(w http.ResponseWriter, r *http.Request) {
	if p.limited(w, wsConnectLimit, clientIP(r)) {
		return
	}
	c, err := websocket.Accept(w, r, wsOpts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go p.pingLoop(ctx, c, cancel)

	var filter atomic.Pointer[v0Filter]
	var pace pacer
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	ip := clientIP(r)
	readSub := func(timeout time.Duration) bool {
		rctx := ctx
		if timeout > 0 {
			var tc context.CancelFunc
			rctx, tc = context.WithTimeout(ctx, timeout)
			defer tc()
		}
		_, data, err := c.Read(rctx)
		if err != nil {
			return false
		}
		f, cl, msg := p.parseV0Sub(data, ip)
		if msg == "" && release == nil {
			var err error
			if release, err = acquireStream(cl, ip); err != nil {
				msg = "concurrent connections per user exceeded" // aisstream's wording
			}
			pace.n = cl.Rate
		}
		if msg != "" {
			wsWriteJSON(ctx, c, map[string]string{"error": msg})
			c.Close(websocket.StatusPolicyViolation, msg)
			return false
		}
		filter.Store(f) // resend replaces the subscription
		return true
	}
	if !readSub(3 * time.Second) { // aisstream's subscribe deadline
		return
	}
	go func() {
		defer cancel()
		for readSub(0) {
		}
	}()

	sub := p.subscribe()
	defer p.unsubscribe(sub)
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case ev := <-sub.ch:
			if sub.overflow.Load() {
				c.Close(websocket.StatusPolicyViolation, "client too slow")
				return
			}
			if !filter.Load().match(ev) {
				continue
			}
			if !pace.allow(time.Now()) {
				p.stats.thinned.Add(1)
				continue
			}
			wctx, wc := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, ev.renderV0())
			wc()
			if err != nil {
				return
			}
		}
	}
}

// ---- /v1/stream: one socket, both directions. Peers, feeders, and chart clients all speak this. ----
// ponytail: minimal envelope of our own; revisit against NIP-01 before any second implementation exists.

type v1Frame struct {
	Type     string   `json:"type"`               // "subscribe" | "unsubscribe" | "publish" | "register"
	BBox     []bbox   `json:"bbox,omitempty"`     // subscribe: [minLat,minLon,maxLat,maxLon]...; empty = everything
	MMSI     []uint32 `json:"mmsi,omitempty"`     // subscribe: vessels to follow wherever they are (ORed with bbox)
	NMEA     []string `json:"nmea,omitempty"`     // publish: tagged or bare sentences
	Replay   bool     `json:"replay,omitempty"`   // publish: an offline backlog; stale TAG times are archived, not emitted
	Snapshot bool     `json:"snapshot,omitempty"` // subscribe: first replay the last known events for vessels already tracked
	Pubkey   string   `json:"pubkey,omitempty"`   // register: base64url ed25519 public key
	BindIP   bool     `json:"bind_ip,omitempty"`  // register: bind the token to this connection's address (as /v1/keys bind_ip)
}

// v1Welcome is the first frame on every /v1/stream socket: the tier in effect for this connection, so a
// client can size its bbox, pace itself, and back off reconnects without discovering the limits by error.
type v1Welcome struct {
	Type   string   `json:"type"` // "welcome"
	Sub    string   `json:"sub"`
	Role   string   `json:"role"`
	Feeder bool     `json:"feeder,omitempty"` // personal token currently earning the feeder tier
	Limits v1Limits `json:"limits"`
}

// v1Limits: absent/0 = unlimited; area < 0 = MMSI-only subscriptions. Same semantics as the token claims.
type v1Limits struct {
	Conns          int     `json:"conns,omitempty"`
	Rate           int     `json:"rate,omitempty"`
	Area           float64 `json:"area,omitempty"`
	MMSIs          int     `json:"mmsis,omitempty"`
	BBox           []bbox  `json:"bbox,omitempty"` // subscriptions must fit inside one of these
	Publish        bool    `json:"publish"`
	PublishPerMin  int     `json:"publish_per_min,omitempty"`
	PublishFrame   int     `json:"publish_frame,omitempty"` // sentences accepted per publish frame
	ConnectsPerMin int     `json:"connects_per_min"`
}

type v1Event struct {
	Type        string     `json:"type"`
	ID          string     `json:"id,omitempty"` // absent on synthesized snapshot reconstructions
	Time        time.Time  `json:"time"`
	Source      string     `json:"source"`
	Station     string     `json:"station"`
	Channel     string     `json:"channel"`
	NMEA        []string   `json:"nmea,omitempty"` // absent on synthesized snapshot reconstructions
	MMSI        uint32     `json:"mmsi"`
	MsgType     string     `json:"msg_type"`
	Lat         *float64   `json:"lat,omitempty"`
	Lon         *float64   `json:"lon,omitempty"`
	Message     ais.Packet `json:"message"`
	Synthesized bool       `json:"synthesized"`
}

func (p *Pipeline) serveV1(w http.ResponseWriter, r *http.Request) {
	// Anonymous sockets may subscribe (the viewer), never publish. A supplied token must verify, and its
	// claims (cidr, conns, bbox) bind the socket whatever its role; publishing needs a publish role.
	cl, claimsErr := p.socketClaims(r)
	if p.limited(w, wsConnectLimit, connectKey(cl, r)) {
		return
	}
	c, err := websocket.Accept(w, r, wsOpts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go p.pingLoop(ctx, c, cancel)

	if err := claimsErr; err != nil {
		wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": err.Error()})
		c.Close(websocket.StatusPolicyViolation, "invalid token")
		return
	}
	if cl == nil {
		if allowAnon {
			cl = &Claims{Sub: "anon", Role: "admin"}
		} else {
			cl = anonymousClaims(clientIP(r)) // personal-tier limits, keyed by address
		}
	}
	ip := clientIP(r)
	relSub, relAddr, err := acquireStreamSlots(cl, ip)
	if err != nil {
		wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": err.Error()})
		return
	}
	// The exit release runs before cancel()/CloseNow() (defer LIFO), so the reader can be mid-register;
	// the mutex + closed flag make release-old and release-at-exit exactly-once.
	var slotMu sync.Mutex
	closed := false
	defer func() { slotMu.Lock(); closed = true; relSub(); relAddr(); slotMu.Unlock() }()
	canPublish := cl.may("publish")
	sendWelcome := func() error {
		lim := v1Limits{Conns: cl.Conns, Rate: cl.Rate, Area: cl.Area, MMSIs: cl.MMSIs, BBox: cl.BBox,
			Publish: canPublish, ConnectsPerMin: wsConnectLimit.max}
		if canPublish {
			lim.PublishPerMin, lim.PublishFrame = publishLimit.max, maxPublishFrame
		}
		return wsWriteJSON(ctx, c, v1Welcome{Type: "welcome", Sub: cl.Sub, Role: cl.Role, Feeder: cl.Feeder, Limits: lim})
	}
	if sendWelcome() != nil {
		return
	}
	var pace atomic.Pointer[pacer] // pointer, not value: register swaps it from the reader while the writer paces
	pace.Store(&pacer{n: cl.Rate})
	var subscription atomic.Pointer[v1Sub] // nil = not subscribed
	// Register with the fan-out before frames are read: an event broadcast between a snapshot replay
	// and a later registration would be in neither.
	sub := p.subscribe()
	defer p.unsubscribe(sub)
	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var f v1Frame
			if err := json.Unmarshal(data, &f); err != nil {
				wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "bad frame"})
				continue
			}
			switch f.Type {
			case "subscribe":
				b := f.BBox
				everything := len(b) == 0 && len(f.MMSI) == 0
				denied := (everything && cl.Area != 0) || (len(b) > 0 && !cl.allowsArea(b))
				for _, x := range b {
					if !cl.allowsBox(x) {
						denied = true
					}
				}
				if denied {
					wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "bbox not allowed for this key"})
					continue
				}
				if !cl.allowsMMSIs(len(f.MMSI)) {
					wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "too many mmsi for this key"})
					continue
				}
				s := &v1Sub{boxes: b, everything: everything}
				if len(f.MMSI) > 0 {
					s.mmsi = map[uint32]bool{}
					for _, m := range f.MMSI {
						s.mmsi[m] = true
					}
				}
				subscription.Store(s)
				if f.Snapshot { // replay after storing the live sub: a duplicate is possible, a gap is not
					for _, ev := range p.snapshotEvents(s) { // unpaced; bounded by the area claim like /v1/vessels
						if wsWriteJSON(ctx, c, renderV1(ev)) != nil {
							return
						}
					}
				}
			case "unsubscribe":
				subscription.Store(nil)
			case "publish":
				if !canPublish {
					wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "publish requires a token"})
					continue
				}
				now := time.Now()
				src := "v1:" + cl.Sub
				n := 0
				for _, line := range f.NMEA {
					if n >= maxPublishFrame || !publishLimit.allow(cl.Sub) {
						p.stats.rateLimited.Add(1)
						break
					}
					p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: line, Buffered: f.Replay})
					n++
				}
				wsWriteJSON(ctx, c, map[string]any{"type": "ack", "n": n})
			case "register":
				// In-band twin of POST /v1/keys, for clients that can't POST (sandboxed plugins). Mints a
				// personal token and upgrades this connection in place, so no reconnect is needed.
				errf := func(msg string) { wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": msg}) }
				if cl.Role != "anonymous" && !(allowAnon && cl.Sub == "anon") { // the ALLOW_ANON admin socket may still register (local dev)
					errf("already registered")
					continue
				}
				if !keysLimit.allow(ip) { // before the mint, so spammed frames don't pay key derivation
					p.stats.rateLimited.Add(1)
					errf("rate limited")
					continue
				}
				tok, nc, msg := mintPersonal(ip, f.Pubkey, f.BindIP)
				if msg != "" {
					errf(msg)
					continue
				}
				ncl := p.effective(&nc) // an already-feeding bound station earns the feeder tier now, as a reconnect would
				// Only the sub slot is re-keyed; the addr slot is kept, since the upgrade adds no stream.
				rel, ok := conns.acquire(ncl.Sub, ncl.Conns)
				if !ok { // keep the anonymous claims and slot
					errf("concurrent connections per key exceeded")
					continue
				}
				slotMu.Lock()
				if closed { // handler exited and released while we were minting; give the new slot back
					slotMu.Unlock()
					rel()
					return
				}
				relSub()
				relSub = rel
				slotMu.Unlock()
				cl, canPublish = ncl, ncl.may("publish")
				pace.Store(&pacer{n: cl.Rate})
				wsWriteJSON(ctx, c, map[string]any{"type": "key", "token": tok, "claims": nc})
				if sendWelcome() != nil {
					return
				}
			default:
				wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "unknown type"})
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case ev := <-sub.ch:
			if sub.overflow.Load() {
				c.Close(websocket.StatusPolicyViolation, "client too slow")
				return
			}
			sp := subscription.Load()
			if sp == nil || !sp.match(ev) {
				continue
			}
			if !pace.Load().allow(time.Now()) {
				p.stats.thinned.Add(1)
				continue
			}
			if err := wsWriteJSON(ctx, c, renderV1(ev)); err != nil {
				return
			}
		}
	}
}

func init() { log.SetFlags(log.LstdFlags | log.Lmicroseconds) }

// v1Sub is one socket's subscription: everything, a set of boxes, a set of MMSIs, or boxes OR MMSIs.
type v1Sub struct {
	everything bool
	boxes      []bbox
	mmsi       map[uint32]bool
}

func (s *v1Sub) match(ev *Event) bool {
	if s.everything || s.mmsi[ev.MMSI] {
		return true
	}
	if ev.HasPos {
		for _, b := range s.boxes {
			if b.contains(ev.Lat, ev.Lon) {
				return true
			}
		}
	}
	return false
}

func renderV1(ev *Event) v1Event {
	out := v1Event{Type: "event", ID: ev.ID, Time: ev.Time.UTC(), Source: ev.Source, Station: ev.Station, Channel: channelString(ev.Channel),
		NMEA: ev.Sentences, MMSI: ev.MMSI, MsgType: ev.Type, Message: ev.Packet, Synthesized: ev.Synthesized}
	if ev.HasPos {
		out.Lat, out.Lon = &ev.Lat, &ev.Lon
	}
	return out
}

func channelString(c byte) string {
	if c == 0 {
		return ""
	}
	return string(c)
}
