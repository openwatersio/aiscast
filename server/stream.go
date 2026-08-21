package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
)

const maxPublishFrame = 1000 // sentences per publish frame; the rest are dropped and counted

var wsOpts = &websocket.AcceptOptions{OriginPatterns: []string{"*"}, CompressionMode: websocket.CompressionContextTakeover}

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

const errBoxDenied = "Bounding Box Not Allowed For This Key"

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
	if len(s.BoundingBoxes) == 0 || len(s.FiltersShipMMSI) > 50 {
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
	if !c.allowsArea(f.boxes) {
		return nil, nil, errBoxDenied
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
			var ok bool
			if release, ok = acquireStream(cl, ip); !ok {
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
	Type   string   `json:"type"`             // "subscribe" | "unsubscribe" | "publish"
	BBox   []bbox   `json:"bbox,omitempty"`   // subscribe: [minLat,minLon,maxLat,maxLon]...; empty = everything
	NMEA   []string `json:"nmea,omitempty"`   // publish: tagged or bare sentences
	Replay bool     `json:"replay,omitempty"` // publish: an offline backlog; stale TAG times are archived, not emitted
}

type v1Event struct {
	Type        string     `json:"type"`
	ID          string     `json:"id"`
	Time        time.Time  `json:"time"`
	Source      string     `json:"source"`
	Station     string     `json:"station"`
	Channel     string     `json:"channel"`
	NMEA        []string   `json:"nmea"`
	MMSI        uint32     `json:"mmsi"`
	MsgType     string     `json:"msg_type"`
	Lat         *float64   `json:"lat,omitempty"`
	Lon         *float64   `json:"lon,omitempty"`
	Message     ais.Packet `json:"message"`
	Synthesized bool       `json:"synthesized"`
}

func (p *Pipeline) serveV1(w http.ResponseWriter, r *http.Request) {
	if p.limited(w, wsConnectLimit, clientIP(r)) {
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

	// Anonymous sockets may subscribe (the viewer), never publish. A supplied token must verify, and its
	// claims (cidr, conns, bbox) bind the socket whatever its role; publishing needs a publish role.
	cl, err := p.socketClaims(r)
	if err != nil {
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
	if release, ok := acquireStream(cl, clientIP(r)); ok {
		defer release()
	} else {
		wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "concurrent connections per key exceeded"})
		return
	}
	canPublish := cl.may("publish")
	pace := pacer{n: cl.Rate}
	var boxes atomic.Pointer[[]bbox] // nil = not subscribed
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
				denied := !cl.allowsArea(b) || (len(b) == 0 && cl.Area > 0)
				for _, x := range b {
					if !cl.allowsBox(x) {
						denied = true
					}
				}
				if denied {
					wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "bbox not allowed for this key"})
					continue
				}
				boxes.Store(&b)
			case "unsubscribe":
				boxes.Store(nil)
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
			default:
				wsWriteJSON(ctx, c, map[string]string{"type": "error", "error": "unknown type"})
			}
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
			bp := boxes.Load()
			if bp == nil {
				continue
			}
			if !pace.allow(time.Now()) {
				p.stats.thinned.Add(1)
				continue
			}
			if len(*bp) > 0 {
				hit := false
				for _, b := range *bp {
					if ev.HasPos && b.contains(ev.Lat, ev.Lon) {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
			}
			out := v1Event{Type: "event", ID: ev.ID, Time: ev.Time.UTC(), Source: ev.Source, Station: ev.Station, Channel: channelString(ev.Channel),
				NMEA: ev.Sentences, MMSI: ev.MMSI, MsgType: ev.Type, Message: ev.Packet, Synthesized: ev.Synthesized}
			if ev.HasPos {
				out.Lat, out.Lon = &ev.Lat, &ev.Lon
			}
			if err := wsWriteJSON(ctx, c, out); err != nil {
				return
			}
		}
	}
}

func init() { log.SetFlags(log.LstdFlags | log.Lmicroseconds) }

func channelString(c byte) string {
	if c == 0 {
		return ""
	}
	return string(c)
}
