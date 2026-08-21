package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// /v1/nmea: the deduplicated stream as NMEA text, for feeders who want raw sentences back (AIS-catcher,
// OpenCPN, a Signal K input). One WebSocket text frame per message, sentences joined by "\r\n", each prefixed
// with a TAG block carrying the source (`s:`), the canonical time (`c:`), and the license tag (`t:`).
// Token required (feeder, peer, partner, admin); bbox via ?bbox=minLat,minLon,maxLat,maxLon (repeatable).

// nmeaRoles: reciprocity. Feeders get the raw feed back; personal and anonymous do not (see PLAN, Stage 1).
var nmeaRoles = map[string]bool{"feeder": true, "peer": true, "partner": true, "admin": true}

func (p *Pipeline) serveNMEA(w http.ResponseWriter, r *http.Request) {
	if p.limited(w, wsConnectLimit, clientIP(r)) {
		return
	}
	cl, err := p.socketClaims(r)
	if err == nil && (cl == nil || !nmeaRoles[cl.Role]) {
		err = fmt.Errorf("a feeder, peer, partner, or admin token is required")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var boxes []bbox
	for _, q := range r.URL.Query()["bbox"] {
		var b bbox
		if n, _ := fmt.Sscanf(q, "%f,%f,%f,%f", &b[0], &b[1], &b[2], &b[3]); n != 4 || !cl.allowsBox(b) {
			http.Error(w, "bbox=minLat,minLon,maxLat,maxLon (inside your token's bbox)", http.StatusBadRequest)
			return
		}
		boxes = append(boxes, b)
	}
	if !cl.allowsArea(boxes) || (len(boxes) == 0 && cl.Area > 0) {
		http.Error(w, "bbox area exceeds your token", http.StatusBadRequest)
		return
	}
	release, ok := conns.acquire(cl.Sub, cl.Conns)
	if !ok {
		http.Error(w, "concurrent connections per key exceeded", http.StatusTooManyRequests)
		return
	}
	defer release()

	c, err := websocket.Accept(w, r, wsOpts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // drain client frames (pings, closes); nothing is expected
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()
	sub := p.subscribe()
	defer p.unsubscribe(sub)
	pace := pacer{n: cl.Rate}
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
			if len(boxes) > 0 {
				hit := false
				for _, b := range boxes {
					if ev.HasPos && b.contains(ev.Lat, ev.Lon) {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
			}
			if !pace.allow(time.Now()) {
				p.stats.thinned.Add(1)
				continue
			}
			wctx, wc := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, []byte(ev.nmeaText()))
			wc()
			if err != nil {
				return
			}
		}
	}
}

// nmeaText renders the event's sentences with our TAG block; an incoming TAG block is replaced, the sentence kept.
func (ev *Event) nmeaText() string {
	lic := licenses[ev.Source]
	if lic == "" {
		if i := strings.IndexByte(ev.Source, ':'); i > 0 {
			lic = licenses[ev.Source[:i]]
		}
	}
	if lic == "" {
		lic = "feeder"
	}
	var sb strings.Builder
	for _, s := range ev.Sentences {
		if i := strings.LastIndexAny(s, "!$"); i > 0 {
			s = s[i:]
		}
		sb.WriteString(tagBlock(map[byte]string{'s': tagSafe(ev.Station), 'c': fmt.Sprint(ev.Time.Unix()), 't': lic}))
		sb.WriteString(s)
		sb.WriteString("\r\n")
	}
	return sb.String()
}

var tagSeq atomic.Int64

// tagBlock builds a NMEA 4.10 TAG block with its checksum: \s:...,c:...,t:...*hh\
func tagBlock(fields map[byte]string) string {
	parts := []string{}
	for _, k := range []byte{'s', 'c', 't'} {
		if v := fields[k]; v != "" {
			parts = append(parts, string(k)+":"+v)
		}
	}
	body := strings.Join(parts, ",")
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return fmt.Sprintf("\\%s*%02X\\", body, cs)
}

// tagSafe keeps a station id inside TAG-block rules: no commas, backslashes, asterisks; at most 15 chars.
func tagSafe(s string) string {
	r := strings.NewReplacer(",", "_", "\\", "_", "*", "_", ":", "_").Replace(s)
	if len(r) > 15 {
		r = r[len(r)-15:]
	}
	return r
}
