package main

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// Tiers, as documented in docs/limits.md. Tokens exist to bound load and recognise contribution, not to
// protect data: every limit here is a cost axis (streams, rate, area), never a secret.

const (
	anonConns, anonRate, anonArea             = 2, 20, 100.0 // per address, subscribe only
	personalConns, personalRate, personalArea = 2, 50, 400.0 // self-minted, no expiry
	feederConns, feederRate                   = 5, 200       // earned: personal token whose station is feeding; area unlimited
	feederMinEvents24h                        = 1000         // events credited to the token's stations in the last 24 h
	anonMMSIs, personalMMSIs, feederMMSIs     = 10, 50, 200  // vessels that may be followed by MMSI per subscription
	addrMaxStreams                            = 32           // concurrent streams per address across all tokens; roomy for a shared egress (CGNAT, marina wifi)
	httpPerMinute, keysPerMinute              = 120, 10      // per address
	udpLinesPerMinute                         = 30000        // ≈500 sentences/s per source address
	corroborationWindow                       = time.Hour    // a low-trust position counts as corroborated this long after a trusted source heard the vessel
	implausibleKnots                          = 120.0        // low-trust positions implying faster than this are dropped
)

func anonymousClaims(ip string) *Claims {
	return &Claims{Sub: "anon:" + ip, Role: "anonymous", Conns: anonConns, Rate: anonRate, Area: anonArea, MMSIs: anonMMSIs}
}

func personalClaims(kid, sub string, now time.Time) Claims {
	return Claims{Kid: kid, Sub: sub, Role: "personal", Iat: now.Unix(), Conns: personalConns, Rate: personalRate, Area: personalArea, MMSIs: personalMMSIs}
}

// effective applies the earned feeder tier: a personal token whose stations delivered feederMinEvents24h in the
// last 24 h gets feeder limits and the raw feed for this connection. Minted tokens are returned unchanged.
func (p *Pipeline) effective(c *Claims) *Claims {
	if c == nil || c.Role != "personal" || p.contributed24h(c) < feederMinEvents24h {
		return c
	}
	e := *c
	e.Feeder = true
	e.Conns, e.Rate, e.Area, e.MMSIs = feederConns, feederRate, 0, feederMMSIs
	return &e
}

// contributed24h sums the last 24 h of events over the stations a token can own: its /v1 and HTTP stations,
// and the UDP stations of any single addresses bound in its cidr claim.
func (p *Pipeline) contributed24h(c *Claims) int64 {
	ids := []string{"v1:" + c.Sub, "http:" + c.Sub}
	for _, s := range c.CIDR {
		if ip := net.ParseIP(strings.TrimSuffix(strings.TrimSuffix(s, "/32"), "/128")); ip != nil {
			ids = append(ids, udpStation(ip.String()))
		}
	}
	return p.stations.events24h(ids, time.Now())
}

// lowTrust marks sources that cannot be authenticated: UDP senders, named by hash or by their own AIVDO.
func lowTrust(source string) bool {
	return strings.HasPrefix(source, "udp:") || strings.HasPrefix(source, "mmsi:")
}

// ---- concurrent stream accounting: per token sub and per address ----

type connCounter struct {
	mu sync.Mutex
	n  map[string]int
}

var (
	conns     = &connCounter{n: map[string]int{}} // per token sub
	addrConns = &connCounter{n: map[string]int{}} // per address, all tokens
)

// acquire returns false when key already has max live entries (max <= 0 = unlimited).
func (cc *connCounter) acquire(key string, max int) (func(), bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if max > 0 && cc.n[key] >= max {
		return nil, false
	}
	cc.n[key]++
	return func() {
		cc.mu.Lock()
		cc.n[key]--
		if cc.n[key] == 0 {
			delete(cc.n, key)
		}
		cc.mu.Unlock()
	}, true
}

// acquireStream takes both the token's and the address's slot; either cap refuses the stream with a message
// naming the cap. Anonymous claims are keyed by address, so their per-key cap is an address cap too.
func acquireStream(c *Claims, ip string) (func(), error) {
	relAddr, ok := addrConns.acquire(ip, addrMaxStreams)
	if !ok {
		return nil, errors.New("concurrent streams per address exceeded")
	}
	relSub, ok := conns.acquire(c.Sub, c.Conns)
	if !ok {
		relAddr()
		if c.Role == "anonymous" {
			return nil, errors.New("concurrent streams per address exceeded")
		}
		return nil, errors.New("concurrent connections per key exceeded")
	}
	return func() { relSub(); relAddr() }, nil
}
