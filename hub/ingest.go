package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const maxBody = 1 << 20

// allowAnon (ALLOW_ANON=1) accepts any feeder id and any v0 API key. Local development only; never set it on a public host.
var allowAnon = os.Getenv("ALLOW_ANON") == "1"

// feederKeys maps station id → secret from FEEDER_KEYS="id:secret,id:secret".
var feederKeys = func() map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("FEEDER_KEYS"), ",") {
		if id, sec, ok := strings.Cut(kv, ":"); ok && validID(id) {
			m[id] = sec
		}
	}
	if allowAnon {
		log.Printf("ALLOW_ANON=1: accepting any feeder and any v0 API key")
	}
	return m
}()

// validID keeps ids safe for log lines and archive path components.
var validID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`).MatchString

// feederAuth returns the station id from Basic auth (AIS-catcher USERPWD id:key) or ?key=id:secret.
func feederAuth(r *http.Request) (string, bool) {
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec, _ = strings.Cut(r.URL.Query().Get("key"), ":")
	}
	if !validID(id) {
		return "", false
	}
	if allowAnon {
		return id, true
	}
	want, known := feederKeys[id]
	return id, known && subtle.ConstantTimeCompare([]byte(want), []byte(sec)) == 1
}

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
	id, ok := feederAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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

// runUDP accepts raw NMEA datagrams. ponytail: station = sender IP; per-station ports/keys when real feeders arrive.
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
		src := "udp:" + ip
		now := time.Now()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: line})
		}
	}
}
