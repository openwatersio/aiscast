package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
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

// feederKeys maps station id → secret from FEEDER_KEYS="id:secret,id:secret". Empty = accept any (dev).
var feederKeys = func() map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("FEEDER_KEYS"), ",") {
		if id, sec, ok := strings.Cut(kv, ":"); ok {
			m[id] = sec
		}
	}
	if len(m) == 0 {
		log.Printf("FEEDER_KEYS unset: accepting any feeder credentials")
	}
	return m
}()

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// feederAuth returns the station id from Basic auth (AIS-catcher USERPWD id:key) or ?key=id:secret.
func feederAuth(r *http.Request) (string, bool) {
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec, ok = strings.Cut(r.URL.Query().Get("key"), ":")
	}
	if !safeID.MatchString(id) { // id becomes an archive path component
		return "", false
	}
	if len(feederKeys) == 0 {
		return id, true
	}
	return id, feederKeys[id] == sec
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
		rd = gz
	}
	body, err := io.ReadAll(rd)
	if err != nil {
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
