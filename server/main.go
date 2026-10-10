// aiscast: AIS ingest → dedupe → decode → bbox fan-out, aisstream.io-compatible at /v0/stream.
package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
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

// usageEvery spaces the usage counter writes. Shutdown writes the file too, so a deploy loses nothing, and
// a crash loses at most this much of the counters.
const usageEvery = time.Minute

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "replay":
			runReplay(os.Args[2:])
			return
		case "convert-receptions":
			runConvertReceptions(os.Args[2:])
			return
		case "clickhouse-cleanup":
			runClickHouseCleanup()
			return
		case "rebuild-positions-1m":
			runRebuildPositions1m(os.Args[2:])
			return
		case "sweep":
			runSweep()
			return
		}
	}
	arch := newArchive(env("ARCHIVE_DIR", "archive"), s3FromEnv())
	go arch.sweepLoop() // reclaim what the bucket already has; slow, so it must not hold up ingest
	p := newPipeline(arch)
	p.access = newAccessArchive(accessDir(), accessStoreFromEnv())
	go p.access.sweepLoop()

	// The record restores the vessel cache, so a restart resumes the map the last process left. A record
	// that will not open costs the restored map and the lookups it serves, never live ingest or the stream:
	// the server runs without it, the feeds refill the map within minutes, and aiscast_store_up says so.
	if path := env("STORE", "aiscast.db"); path != "off" {
		st, err := openStore(path)
		if err == nil {
			if err = p.attachStore(st); err != nil {
				st.close()
			}
		}
		if err != nil {
			log.Printf("store: %v; running without the vessel record", err)
		} else {
			log.Printf("restored %d vessels from %s", p.vesselCount(), path)
			if err := p.names.attach(st); err != nil {
				log.Printf("stations: %v; station names will not survive a restart", err)
			}
			if os.Getenv("CLICKHOUSE_URL") != "" {
				p.vesselHistory = p.vesselHistoryFromClickHouse
				go p.runImport()
			}
			go p.runStore()
			if err := p.loadWikidataStats(); err != nil {
				log.Printf("wikidata: %v", err)
			}
			if env("WIKIDATA", "1") == "1" {
				p.wikidata.enabled.Store(true)
				go p.runWikidata(env("WIKIDATA_URL", wikidataSPARQL))
			}
			if err := p.loadUSCGStats(); err != nil {
				log.Printf("uscg: %v", err)
			}
			if env("USCG", "1") == "1" {
				p.uscg.enabled.Store(true)
				go p.runUSCG(env("USCG_URL", psixEndpoint))
			}
			if err := p.loadFiskeridirStats(); err != nil {
				log.Printf("fiskeridir: %v", err)
			}
			if env("FISKERIDIR", "1") == "1" {
				p.fiskeridir.enabled.Store(true)
				go p.runFiskeridir(env("FISKERIDIR_URL", fdirEndpoint))
			}
			if err := p.loadFCCStats(); err != nil {
				log.Printf("fcc: %v", err)
			}
			if env("FCC", "1") == "1" {
				p.fcc.enabled.Store(true)
				go p.runFCC(env("FCC_URL", fccEndpoint))
			}
			if err := p.loadTCStats(); err != nil {
				log.Printf("tc: %v", err)
			}
			if env("TC", "1") == "1" {
				p.tc.enabled.Store(true)
				go p.runTC(env("TC_URL", tcEndpoint))
			}
			if err := p.loadAMSAStats(); err != nil {
				log.Printf("amsa: %v", err)
			}
			if env("AMSA", "1") == "1" {
				p.amsa.enabled.Store(true)
				go p.runAMSA(env("AMSA_URL", amsaPage))
			}
			if err := p.loadISEDStats(); err != nil {
				log.Printf("ised: %v", err)
			}
			if env("ISED", "1") == "1" {
				go p.runISED(env("ISED_URL", isedEndpoint))
			}
			go p.runRecordCounts()
		}
	}
	dedupe := env("DEDUPE", "dedupe.json")
	if n, err := p.loadDedupe(dedupe); err == nil {
		log.Printf("restored %d dedupe entries from %s", n, dedupe)
	}
	usage := env("USAGE", "vessels-usage.json")
	if err := p.loadUsage(usage); err == nil {
		log.Printf("restored usage counters from %s", usage)
	} else if !os.IsNotExist(err) {
		log.Printf("usage: %v (counters start empty)", err)
	}
	// Before any source starts, so every producer sees them: receive times taken at admission make
	// live processing order the raw archive's order, and AISHub snapshots are delivered paced.
	p.stampAtAdmission = true
	if os.Getenv("AISHUB_USERNAME") != "" {
		p.startAishubPacing(45 * time.Second)
	}
	if env("KYSTVERKET", "1") == "1" {
		go runTCPSource(p, "kystverket", env("KYSTVERKET_ADDR", "153.44.253.27:5631"))
	}
	if id := os.Getenv("BARENTSWATCH_CLIENT_ID"); id != "" {
		if secret := os.Getenv("BARENTSWATCH_CLIENT_SECRET"); secret == "" {
			log.Printf("BARENTSWATCH_CLIENT_SECRET unset: barentswatch upstream off")
		} else {
			go runBarentswatch(p, env("BARENTSWATCH_URL", "https://live.ais.barentswatch.no/v1/ais"), id, secret)
		}
	}
	if env("DIGITRAFFIC", "1") == "1" {
		go runDigitraffic(p, env("DIGITRAFFIC_URL", "wss://meri.digitraffic.fi:443/mqtt"))
	}
	if key := os.Getenv("AISSTREAM_API_KEY"); key != "" {
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
		iv, err := time.ParseDuration(env("AISHUB_INTERVAL", "20s"))
		if err != nil || iv < 20*time.Second {
			iv = 20 * time.Second
		}
		go runAishub(p, u, iv) // best effort, outside the health gate like aisstream
	}
	udp, err := parseUDPAddrs(env("UDP_ADDR", ":10110"))
	if err != nil {
		log.Fatal(err)
	}
	p.udp = udp
	for _, l := range p.udp {
		go runUDP(p, l)
	}
	if url := os.Getenv("CLICKHOUSE_URL"); url != "" {
		// Until runStationSeries reads the candidates back, or ownPendingMax passes without ClickHouse answering.
		p.stations.ownPending.Store(true)
		time.AfterFunc(ownPendingMax, func() { p.stations.ownPending.Store(false) })
		go p.runClickHouse(url)
		var sources []historySource
		if os.Getenv("MARINECADASTRE") != "" {
			sources = append(sources, marineCadastre(historyFrom("MARINECADASTRE_FROM")))
		}
		if os.Getenv("DMA") != "" {
			sources = append(sources, dma(historyFrom("DMA_FROM")))
		}
		if len(sources) > 0 {
			if loader, err := historyLoaderURL(url, os.Getenv("CLICKHOUSE_LOADER_URL")); err != nil {
				log.Printf("history: %v; archives do not load", err)
			} else {
				p.history = newHistoryStats(sources)
				go p.runHistory(loader, sources)
			}
		}
		p.coverage = newCoverageMap()
		go p.runCoverage()
		go p.runStationSeries()
		go p.runStatics()
	}
	go p.logStats()
	go p.runStationNames()
	go func() {
		for range time.Tick(usageEvery) {
			if err := p.saveUsage(usage); err != nil {
				log.Printf("usage: %v", err)
			}
		}
	}()
	go func() { // SIGTERM/SIGINT: close the open archive hours, save state, exit; the next process uploads the hours
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Printf("shutting down")
		p.closeArchives() // state saves follow, so they see everything the archives saw
		if err := p.saveUsage(usage); err != nil {
			log.Printf("usage: %v", err)
		}
		if err := p.saveDedupe(dedupe); err != nil {
			log.Printf("dedupe: %v (the next process may re-accept copies inside the window)", err)
		}
		if err := p.names.flush(); err != nil {
			log.Printf("station names: %v", err)
		}
		if err := p.closeStore(); err != nil {
			log.Printf("store: %v", err)
		}
		if err := p.drainClickHouse(); err != nil {
			log.Printf("clickhouse: %v", err)
		}
		os.Exit(0)
	}()

	// net/http/pprof registers on the default mux, which only this listener serves; the public mux never has it.
	if a := env("PPROF_ADDR", "127.0.0.1:6060"); a != "off" {
		go func() { log.Printf("pprof: %v", http.ListenAndServe(a, nil)) }()
	}

	addr := env("ADDR", ":8080")
	log.Printf("listening on %s (udp %s)", addr, env("UDP_ADDR", ":10110"))
	go p.runProbe(probeURL(addr))
	log.Fatal(http.ListenAndServe(addr, httpHandler(p)))
}

// routes is the HTTP surface; openapi_test.go checks it against openapi.json, so a new endpoint fails the
// build until the document mentions it.
func routes(p *Pipeline) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/v0/stream":                     p.serveV0,
		"/v1/stream":                     p.serveV1,
		"/v1/receive":                    p.serveReceive,
		"/v1/keys":                       p.serveKeys,
		"/v1/nmea":                       p.serveNMEA,
		"/v1/stations":                   p.api(corsHeaders, p.serveStations),
		"/v1/stations/":                  p.api(corsHeaders, p.serveStations),
		"/v1/vessels":                    p.api(corsHeaders, p.serveVessels),
		"/v1/vessels/{mmsi}":             p.api(corsHeaders, p.serveVessel),
		"/v1/vessels/{mmsi}/track":       p.api(corsHeaders, p.serveTrack),
		"/v1/vessels/tiles.json":         p.api(corsHeaders, p.serveTileJSON),
		"/v1/vessels/tiles/{z}/{x}/{y}":  p.serveVesselTile,
		"/v1/coverage/tiles.json":        p.api(corsHeaders, p.serveCoverageTileJSON),
		"/v1/coverage/tiles/{z}/{x}/{y}": p.serveCoverageTile,
		"/v1/stats":                      p.api(corsHeaders, p.serveStats),
		"/mcp":                           p.api(mcpHeaders, p.serveMCP),
		"/health":                        p.serveHealth,
		"/metrics":                       p.serveMetrics,
		"/robots.txt":                    serveRobots,
		"/sitemap/vessels":               p.api(corsHeaders, p.serveVesselSitemap),
		"/openapi.json":                  p.api(corsHeaders, serveOpenAPI),
	}
}

func httpHandler(p *Pipeline) http.Handler {
	mux := http.NewServeMux()
	for pat, h := range routes(p) {
		mux.HandleFunc(pat, h)
	}
	mux.Handle("/{$}", http.RedirectHandler("https://openwaters.io/ais/", http.StatusFound))
	return p.countRequests(mux)
}
