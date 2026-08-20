package main

import (
	"bufio"
	"log"
	"net"
	"time"
)

// runTCPSource reads newline-delimited NMEA from an upstream TCP feed (e.g. Kystverket), reconnecting with backoff.
func runTCPSource(p *Pipeline, name, addr string) {
	backoff := time.Second
	for {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			log.Printf("%s: dial %s: %v (retry in %s)", name, addr, err, backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		log.Printf("%s: connected to %s", name, addr)
		backoff = time.Second
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 4096), 4096) // line limit; a hostile upstream can't grow memory
		for sc.Scan() {
			conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
			p.Ingest(Reception{Source: name, Station: name, RecvTime: time.Now(), Body: sc.Text()})
		}
		log.Printf("%s: disconnected: %v", name, sc.Err())
		conn.Close()
		time.Sleep(backoff)
	}
}
