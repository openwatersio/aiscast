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
	feederConns, feederRate                   = 5, 200       // earned: a personal token whose station is feeding, or a connection from a feeding UDP network; area unlimited
	feederMinEvents24h                        = 1000         // events credited to the token's stations, or the network's UDP stations, in the last 24 h
	anonMMSIs, personalMMSIs, feederMMSIs     = 10, 50, 200  // vessels that may be followed by MMSI or IMO per subscription
	addrMaxStreams                            = 32           // concurrent streams per address across all tokens; roomy for a shared egress (CGNAT, marina wifi)
	httpPerMinute, keysPerMinute              = 120, 10      // per address
	tilesPerMinute                            = 600          // per address: a map view is ~20 tiles, refreshed every 10–30 s
	udpLinesPerMinute                         = 30000        // ≈500 sentences/s per source address
	corroborationWindow                       = time.Hour    // a low-trust position counts as corroborated this long after a trusted source heard the vessel
	implausibleKnots                          = 120.0        // positions implying faster than this are dropped
	implausibleJumpNM                         = 10.0         // ...but only once the jump is this far; nearer than that, source jitter dominates
)

func anonymousClaims(ip string) *Claims {
	return &Claims{Sub: "anon:" + ip, Role: "anonymous", Conns: anonConns, Rate: anonRate, Area: anonArea, MMSIs: anonMMSIs}
}

func personalClaims(kid, sub string, now time.Time) Claims {
	return Claims{Kid: kid, Sub: sub, Role: "personal", Iat: now.Unix(), Conns: personalConns, Rate: personalRate, Area: personalArea, MMSIs: personalMMSIs}
}

// effective applies the earned feeder tier to a connection from ip: feeder limits and the raw feed when the
// token's own stations (personal only), or the UDP stations sending from ip's network (anonymous or personal),
// delivered feederMinEvents24h between them in the last 24 h. The network's stations are summed, so a receiver
// whose IPv6 address rotates keeps its count. Minted tokens are returned unchanged. A UDP station that sends
// !AIVDO has its events relabelled mmsi:<n> (see ownOf) and earns nothing here: that label is self-reported, so
// counting it would let one spoofed VDO borrow another station's count. Such a station feeds with a token instead.
func (p *Pipeline) effective(c *Claims, ip string) *Claims {
	if c == nil || c.Feeder || (c.Role != "personal" && c.Role != "anonymous") {
		return c
	}
	if (c.Role != "personal" || p.contributed24h(c) < feederMinEvents24h) &&
		p.stations.events24h(p.stations.netIDs(netKey(ip), udpStation(ip)), time.Now()) < feederMinEvents24h {
		return c
	}
	e := *c
	e.Feeder = true
	e.Conns, e.Rate, e.Area, e.MMSIs = feederConns, feederRate, 0, feederMMSIs
	return &e
}

// contributed24h sums the last 24 h of events over the stations a token owns: its own station, and the UDP
// stations of the addresses in its udp claim and of any single addresses in its cidr claim.
func (p *Pipeline) contributed24h(c *Claims) int64 {
	ids := []string{stationSource(c.Sub)}
	for _, s := range append(c.UDP[:len(c.UDP):len(c.UDP)], c.CIDR...) {
		if ip := net.ParseIP(strings.TrimSuffix(strings.TrimSuffix(s, "/32"), "/128")); ip != nil {
			ids = append(ids, udpStation(ip.String()))
		}
	}
	return p.stations.events24h(ids, time.Now())
}

// netKey is the network a reader shares with the receiver it feeds from: an IPv4 address, or an IPv6 /64, so
// rotating privacy addresses still match. "" for anything unparseable.
func netKey(ip string) string {
	a := net.ParseIP(ip)
	if a == nil {
		return ""
	}
	if v4 := a.To4(); v4 != nil {
		return v4.String()
	}
	return a.Mask(net.CIDRMask(64, 128)).String() + "/64"
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

// acquireStreamSlots takes both the token's and the address's slot; either cap refuses the stream with a
// message naming the cap. Anonymous claims are keyed by address, so their per-key cap is an address cap too.
// The slots come back separately so an in-place tier upgrade can re-key the sub slot alone.
func acquireStreamSlots(c *Claims, ip string) (relSub, relAddr func(), err error) {
	relAddr, ok := addrConns.acquire(ip, addrMaxStreams)
	if !ok {
		return nil, nil, errors.New("concurrent streams per address exceeded")
	}
	relSub, ok = conns.acquire(c.Sub, c.Conns)
	if !ok {
		relAddr()
		if c.Role == "anonymous" {
			return nil, nil, errors.New("concurrent streams per address exceeded")
		}
		return nil, nil, errors.New("concurrent connections per key exceeded")
	}
	return relSub, relAddr, nil
}

func acquireStream(c *Claims, ip string) (func(), error) {
	relSub, relAddr, err := acquireStreamSlots(c, ip)
	if err != nil {
		return nil, err
	}
	return func() { relSub(); relAddr() }, nil
}
