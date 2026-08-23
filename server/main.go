// aiscast: AIS ingest → dedupe → decode → bbox fan-out, aisstream.io-compatible at /v0/stream.
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	arch := newArchive(env("ARCHIVE_DIR", "archive"), s3FromEnv())
	p := newPipeline(arch)

	snapshot := env("SNAPSHOT", "vessels.json")
	if n, err := p.loadSnapshot(snapshot); err == nil {
		log.Printf("restored %d vessels from %s", n, snapshot)
	}
	usage := usagePath(snapshot)
	if err := p.loadUsage(usage); err == nil {
		log.Printf("restored usage counters from %s", usage)
	}
	if env("KYSTVERKET", "1") == "1" {
		p.upstreams = append(p.upstreams, "kystverket")
		go runTCPSource(p, "kystverket", env("KYSTVERKET_ADDR", "153.44.253.27:5631"))
	}
	if env("DIGITRAFFIC", "1") == "1" {
		p.upstreams = append(p.upstreams, "digitraffic")
		go runDigitraffic(p, env("DIGITRAFFIC_URL", "wss://meri.digitraffic.fi:443/mqtt"))
	}
	if key := os.Getenv("AISSTREAM_API_KEY"); key != "" {
		// best effort: not in p.upstreams, so aisstream being down never fails our /health
		go runAisstream(p, env("AISSTREAM_URL", "wss://stream.aisstream.io/v0/stream"), key, env("AISSTREAM_BBOX", "[[[-90,-180],[90,180]]]"))
	}
	if addr := os.Getenv("AISHUB_FEED"); addr != "" { // reciprocity: our received stream to AISHub's assigned UDP port
		f, err := newUDPFeeder(addr)
		if err != nil {
			log.Fatalf("AISHUB_FEED: %v", err)
		}
		p.feeder = f
	}
	if u := os.Getenv("AISHUB_USERNAME"); u != "" {
		iv, err := time.ParseDuration(env("AISHUB_INTERVAL", "65s"))
		if err != nil || iv < 60*time.Second {
			iv = 65 * time.Second
		}
		go runAishub(p, u, iv) // best effort, outside the health gate like aisstream
	}
	go runUDP(p, env("UDP_ADDR", ":10110"))
	go p.logStats()
	go func() {
		for range time.Tick(10 * time.Second) {
			if err := p.saveSnapshot(snapshot); err != nil {
				log.Printf("snapshot: %v", err)
			}
			if err := p.saveUsage(usage); err != nil {
				log.Printf("usage: %v", err)
			}
		}
	}()
	go func() { // SIGTERM/SIGINT: snapshot, flush and upload the open archive hours, exit
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Printf("shutting down")
		p.saveSnapshot(snapshot)
		p.saveUsage(usage)
		arch.shutdown()
		os.Exit(0)
	}()

	addr := env("ADDR", ":8080")
	log.Printf("listening on %s (udp %s)", addr, env("UDP_ADDR", ":10110"))
	go p.runProbe(probeURL(addr))
	log.Fatal(http.ListenAndServe(addr, httpHandler(p)))
}

func httpHandler(p *Pipeline) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/stream", p.serveV0)
	mux.HandleFunc("/v1/stream", p.serveV1)
	mux.HandleFunc("/v1/receive", p.serveReceive)
	mux.HandleFunc("/v1/keys", p.serveKeys)
	mux.HandleFunc("/v1/nmea", p.serveNMEA)
	mux.HandleFunc("/v1/stations", p.rateLimited(p.serveStations))
	mux.HandleFunc("/v1/stations/", p.rateLimited(p.serveStations))
	mux.HandleFunc("/v1/vessels", p.rateLimited(p.serveVessels))
	mux.HandleFunc("/v1/stats", p.rateLimited(p.serveStats))
	mux.HandleFunc("/health", p.serveHealth)
	mux.HandleFunc("/metrics", p.serveMetrics)
	mux.Handle("/{$}", http.RedirectHandler("https://openwaters.io/ais/", http.StatusFound))
	return p.countRequests(mux)
}
