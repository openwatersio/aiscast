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
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
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
	src := "http:" + id
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var env jsonaiscatcher
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		p.arch.write(Reception{Source: src, Station: src, RecvTime: now, Body: string(body)}) // whole envelope, source-native
		for _, m := range env.Msgs {
			st, _ := time.Parse("20060102150405", m.RxTime)
			for _, line := range m.NMEA {
				p.ingestLine(Reception{Source: src, Station: src, RecvTime: now, SourceTime: st, Body: line})
			}
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: sc.Text()})
		}
	}
	w.WriteHeader(http.StatusOK)
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

// runUDP accepts raw NMEA datagrams. ponytail: station = keyed hash of sender IP; per-station ports/keys in Stage 1.
func runUDP(p *Pipeline, addr string) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Printf("udp: %v", err)
		return
	}
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			log.Printf("udp: %v", err)
			return
		}
		ip, _, _ := net.SplitHostPort(from.String())
		src := udpStation(ip)
		now := time.Now()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: line})
		}
	}
}
