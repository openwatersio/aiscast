package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const maxBody = 1 << 20

// allowAnon (ALLOW_ANON=1) accepts any feeder id and any v0 API key. Local development only; never set it on a public host.
var allowAnon = os.Getenv("ALLOW_ANON") == "1"

// jsonaiscatcher is AIS-catcher's -H envelope; only the fields we use.
type jsonaiscatcher struct {
	Msgs []struct {
		NMEA   []string `json:"nmea"`
		RxTime string   `json:"rxtime"` // YYYYMMDDHHMMSS UTC
	} `json:"msgs"`
}

// serveReceive accepts AIS-catcher HTTP output (jsonaiscatcher JSON, or plain NMEA lines), optionally gzip'd.
func (p *Pipeline) serveReceive(w http.ResponseWriter, r *http.Request) {
	// Terms with every response: a repeat sender who keeps posting after receiving them accepts the agreement.
	w.Header().Set("Link", "<"+termsURL+`>; rel="terms-of-service"`)
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	c, err := p.authorize(r, "publish")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	id := c.Sub
	if p.limited(w, receiveLimit, id) {
		return
	}
	var rd io.Reader = http.MaxBytesReader(w, r.Body, maxBody)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(rd)
		if err != nil {
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		rd = io.LimitReader(gz, maxBody+1) // bound the decompressed size too
	}
	body, err := io.ReadAll(rd)
	if err != nil || len(body) > maxBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	now := time.Now()
	src := stationSource(id)
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		if !p.ingestCatcher(src, body, now) {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: sc.Text()})
		}
	}
	w.WriteHeader(http.StatusOK)
}

// stationSource names an authenticated contributor by its token's sub alone. HTTP, MQTT, and /v1/stream
// publishes from one token are one station: the transport is the feeder's business, not a fact about the data.
func stationSource(sub string) string { return "station:" + sub }

// ingestCatcher archives and ingests one jsonaiscatcher envelope; replay feeds archived envelopes back here.
func (p *Pipeline) ingestCatcher(src string, body []byte, now time.Time) bool {
	var env jsonaiscatcher
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	now, ok := p.admit(now)
	if !ok {
		return true // shutting down: dropped from both archives, never half recorded
	}
	defer p.release()
	if shadowSample("catcher") {
		shadowCheck("catcher", body, catcherKnown) // recursive: per-message fields ride the msgs subtree
	}
	p.arch.write(Reception{Source: src, Station: src, RecvTime: now, Body: string(body)}) // whole envelope, source-native
	for _, m := range env.Msgs {
		st, _ := time.Parse("20060102150405", m.RxTime)
		for _, line := range m.NMEA {
			p.ingestLine(Reception{Source: src, Station: src, RecvTime: now, SourceTime: st, Body: line})
		}
	}
	return true
}

// stationSalt keys the UDP station ids. STATION_SALT keeps them stable across restarts; unset = per-boot random.
var stationSalt = func() []byte {
	if s := os.Getenv("STATION_SALT"); s != "" {
		return []byte(s)
	}
	b := make([]byte, 16)
	rand.Read(b)
	log.Printf("STATION_SALT unset: UDP station ids change on restart")
	return b
}()

// udpStation names a UDP sender without exposing its address: station ids appear in public responses.
func udpStation(ip string) string {
	m := hmac.New(sha256.New, stationSalt)
	m.Write([]byte(ip))
	return "udp:" + hex.EncodeToString(m.Sum(nil)[:6])
}

// udpListener is one UDP ingest socket. A forwarder resolves its target name once and sends to that address
// until restarted, so a socket per address, counted apart, is the only way to see which name feeders use.
type udpListener struct {
	label, addr string
	datagrams   atomic.Int64
}

// parseUDPAddrs reads UDP_ADDR: comma-separated `[label=]host:port`. The label defaults to the address and
// is the metric series identity, so it must be unique. A malformed entry is an error rather than a skipped
// listener, because an empty address would bind an ephemeral port and the server would look healthy while
// nothing listens on 10110.
func parseUDPAddrs(s string) ([]*udpListener, error) {
	var ls []*udpListener
	labels := map[string]bool{}
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		l := &udpListener{label: e, addr: e}
		if i := strings.Index(e, "="); i >= 0 {
			l.label, l.addr = e[:i], e[i+1:]
		}
		if _, _, err := net.SplitHostPort(l.addr); err != nil {
			return nil, fmt.Errorf("UDP_ADDR entry %q: %w", e, err)
		}
		if l.label == "" || labels[l.label] {
			return nil, fmt.Errorf("UDP_ADDR entry %q: label must be non-empty and unique", e)
		}
		labels[l.label] = true
		ls = append(ls, l)
	}
	return ls, nil
}

// listenUDP binds one listener. An IPv6 literal binds v6 only, so a v4 listener can hold the same port.
func listenUDP(l *udpListener) (net.PacketConn, error) {
	network := "udp"
	if strings.HasPrefix(l.addr, "[") {
		network = "udp6"
	}
	return net.ListenPacket(network, l.addr)
}

// runUDP accepts raw NMEA datagrams. ponytail: station = keyed hash of sender IP; per-station ports/keys in Stage 1.
func runUDP(p *Pipeline, l *udpListener) {
	pc, err := listenUDP(l)
	if err != nil {
		log.Printf("udp %s: %v", l.label, err)
		return
	}
	serveUDP(p, l, pc)
}

func serveUDP(p *Pipeline, l *udpListener, pc net.PacketConn) {
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			log.Printf("udp %s: %v", l.label, err)
			return
		}
		l.datagrams.Add(1)
		ip, _, _ := net.SplitHostPort(from.String())
		src := udpStation(ip)
		now := time.Now()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !udpLimit.allow(ip) {
				p.stats.rateLimited.Add(1)
				break
			}
			p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: line})
		}
	}
}
