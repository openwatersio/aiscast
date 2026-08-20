// hub: AIS ingest → dedupe → decode → bbox fan-out, aisstream.io-compatible at /v0/stream.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	arch := newArchive(env("ARCHIVE_DIR", "archive"), os.Getenv("R2_BUCKET"))
	p := newPipeline(arch)

	if env("KYSTVERKET", "1") == "1" {
		go runTCPSource(p, "kystverket", env("KYSTVERKET_ADDR", "153.44.253.27:5631"))
	}
	if env("DIGITRAFFIC", "1") == "1" {
		go runDigitraffic(p, env("DIGITRAFFIC_URL", "wss://meri.digitraffic.fi:443/mqtt"))
	}
	go runUDP(p, env("UDP_ADDR", ":10110"))
	go p.logStats()

	addr := env("ADDR", ":8080")
	log.Printf("listening on %s (udp %s)", addr, env("UDP_ADDR", ":10110"))
	log.Fatal(http.ListenAndServe(addr, httpHandler(p)))
}

func httpHandler(p *Pipeline) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/stream", p.serveV0)
	mux.HandleFunc("/v1/stream", p.serveV1)
	mux.HandleFunc("/v1/receive", p.serveReceive)
	mux.HandleFunc("/v1/vessels", p.serveVessels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(p.lastEvent()) > 2*time.Minute {
			http.Error(w, "no events in 2 minutes", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	return mux
}
