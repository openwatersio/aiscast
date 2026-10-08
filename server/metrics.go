package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// /metrics is Prometheus text written by hand; no client library for a few dozen series. It is box-local:
// Caddy answers it with 404 and Alloy on the box scrapes it over loopback. The alert rules and dashboard in
// deploy/grafana/ query these names, so a rename here breaks them.

var (
	streamProtocols = []string{"v0", "v1", "sse", "nmea", "mqtt"}
	streamTiers     = []string{"anonymous", "personal", "feeder", "peer", "partner", "admin"}
)

// tierOf is a connection's tier label: its role, or feeder for a personal token currently earning that tier.
func tierOf(c *Claims) string {
	if c.Feeder {
		return "feeder"
	}
	if slices.Contains(streamTiers, c.Role) {
		return c.Role
	}
	return "other"
}

// streamGauge counts open streams by protocol and tier.
type streamGauge struct {
	mu sync.Mutex
	n  map[[2]string]int
}

// open counts one stream and returns the call that uncounts it.
func (g *streamGauge) open(protocol string, c *Claims) func() {
	k := [2]string{protocol, tierOf(c)}
	g.mu.Lock()
	if g.n == nil {
		g.n = map[[2]string]int{}
	}
	g.n[k]++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.n[k]--
		g.mu.Unlock()
	}
}

// snapshot copies the counts with every protocol × known tier present, so an idle series reads 0 rather than absent.
func (g *streamGauge) snapshot() map[[2]string]int {
	out := map[[2]string]int{}
	for _, p := range streamProtocols {
		for _, t := range streamTiers {
			out[[2]string{p, t}] = 0
		}
	}
	g.mu.Lock()
	for k, v := range g.n {
		out[k] = v
	}
	g.mu.Unlock()
	return out
}

// fanoutCounter counts live events written to stream clients and their bytes before compression.
type fanoutCounter struct{ sends, bytes atomic.Int64 }

func (f *fanoutCounter) add(n int) {
	f.sends.Add(1)
	f.bytes.Add(int64(n))
}

// latencyBuckets bound the request histogram. 0.1 and 0.5 are the /v1/vessels p99 alert lines.
var latencyBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// timedRoutes get a latency histogram: they do real work per request, and the rest are cheap or are streams.
var timedRoutes = []string{"/v1/vessels", "/v1/vessels/{mmsi}", "/v1/vessels/{mmsi}/track", "/v1/vessels/tiles/{z}/{x}/{y}", "/mcp"}

type histogram struct {
	counts [len(latencyBuckets) + 1]int64 // per bucket, not cumulative; the last is above every bound
	sum    float64
}

// requestMetrics counts every HTTP request by route pattern and status. A stream counts once, when it closes.
type requestMetrics struct {
	mu    sync.Mutex
	count map[[2]string]int64 // route, status
	hist  map[string]*histogram
}

func (m *requestMetrics) observe(route string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.count == nil {
		m.count, m.hist = map[[2]string]int64{}, map[string]*histogram{}
	}
	m.count[[2]string{route, strconv.Itoa(status)}]++
	if !slices.Contains(timedRoutes, route) {
		return
	}
	h := m.hist[route]
	if h == nil {
		h = &histogram{}
		m.hist[route] = h
	}
	s := d.Seconds()
	h.counts[sort.SearchFloat64s(latencyBuckets[:], s)]++
	h.sum += s
}

// statusWriter: Unwrap keeps WebSocket hijacking and SSE flushing working.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func metricHead(w io.Writer, name, typ, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *Pipeline) serveMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	counter := func(name, help string, v int64) {
		metricHead(w, name, "counter", help)
		fmt.Fprintf(w, "%s %d\n", name, v)
	}
	counter("aiscast_events_total", "decoded, deduplicated AIS messages", p.stats.events.Load())
	counter("aiscast_duplicates_total", "messages dropped as duplicates", p.stats.dup.Load())
	counter("aiscast_parse_errors_total", "unparseable input lines", p.stats.parseErr.Load())
	counter("aiscast_replayed_total", "buffered sentences archived without live emit (TAG time older than 60 s)", p.stats.replayed.Load())
	counter("aiscast_decode_failures_total", "sentences that did not decode to an AIS message", p.stats.decodeFail.Load())
	counter("aiscast_client_drops_total", "events dropped because a client queue was full", p.stats.clientDrops.Load())
	counter("aiscast_ping_timeouts_total", "stream connections closed because the client stopped answering pings", p.stats.pingTimeouts.Load())
	counter("aiscast_ratelimited_total", "requests rejected by rate limits", p.stats.rateLimited.Load())
	metricHead(w, "aiscast_keys_minted_total", "counter", "personal tokens minted, by whether the request was signed with the key; unsigned mints are refused once these stop")
	fmt.Fprintf(w, "aiscast_keys_minted_total{signed=\"true\"} %d\naiscast_keys_minted_total{signed=\"false\"} %d\n", p.stats.keysSigned.Load(), p.stats.keysUnsigned.Load())
	counter("aiscast_thinned_total", "events withheld from connections over their per-second rate", p.stats.thinned.Load())
	counter("aiscast_implausible_total", "positions dropped for implying an impossible speed", p.stats.implausible.Load())
	counter("aiscast_stale_total", "events withheld from the stream for being older than the vessel's newest", p.stats.stale.Load())
	counter("aiscast_uncorroborated_total", "low-trust events kept local because no trusted source has heard the vessel", p.stats.uncorroborated.Load())
	counter("aiscast_invalid_mmsi_total", "messages archived but kept off the map because their MMSI cannot name one station", p.stats.invalidMMSI.Load())

	p.vmu.RLock()
	nv := len(p.vessels)
	p.vmu.RUnlock()
	p.smu.RLock()
	ns := len(p.subs)
	p.smu.RUnlock()
	metricHead(w, "aiscast_vessels", "gauge", "vessels in the cache")
	fmt.Fprintf(w, "aiscast_vessels %d\n", nv)
	metricHead(w, "aiscast_clients", "gauge", "fan-out subscribers, the loopback health probe included")
	fmt.Fprintf(w, "aiscast_clients %d\n", ns)

	archives := []struct {
		name string
		a    *archive
	}{{"raw", p.arch}, {"access", p.access}}
	metricHead(w, "aiscast_udp_datagrams_total", "counter", "raw NMEA datagrams received, per UDP listener")
	for _, l := range p.udp {
		fmt.Fprintf(w, "aiscast_udp_datagrams_total{listener=%q} %d\n", l.label, l.datagrams.Load())
	}

	metricHead(w, "aiscast_archive_upload_failures_total", "counter", "archive hour uploads to the bucket that failed; the hourly sweep retries them")
	for _, a := range archives {
		fmt.Fprintf(w, "aiscast_archive_upload_failures_total{archive=%q} %d\n", a.name, a.a.uploadFailures.Load())
	}
	metricHead(w, "aiscast_access_dropped_total", "counter", "access log lines dropped because the writer fell behind the requests")
	fmt.Fprintf(w, "aiscast_access_dropped_total %d\n", p.accessDropped.Load())
	metricHead(w, "aiscast_archive_staged_bytes", "gauge", "archive bytes on local disk at the last hourly sweep, not yet reclaimed after upload")
	for _, a := range archives {
		fmt.Fprintf(w, "aiscast_archive_staged_bytes{archive=%q} %d\n", a.name, a.a.staged.Load())
	}

	up := 0
	if p.store != nil {
		up = 1
	}
	metricHead(w, "aiscast_store_up", "gauge", "1 when the durable vessel record is attached; 0 means lookups past the 30-minute cache are failing")
	fmt.Fprintf(w, "aiscast_store_up %d\n", up)
	if st := p.store; st != nil {
		metricHead(w, "aiscast_store_flushes_total", "counter", "once-a-second writes of folded vessels to the record")
		fmt.Fprintf(w, "aiscast_store_flushes_total %d\n", st.flushes.Load())
		metricHead(w, "aiscast_store_flush_failures_total", "counter", "record writes that failed; each vessel's next fold rewrites it")
		fmt.Fprintf(w, "aiscast_store_flush_failures_total %d\n", st.flushFailures.Load())
		metricHead(w, "aiscast_store_flush_seconds_total", "counter", "time spent writing to the record")
		fmt.Fprintf(w, "aiscast_store_flush_seconds_total %.3f\n", float64(st.flushNanos.Load())/1e9)
		metricHead(w, "aiscast_store_rows_written_total", "counter", "vessel rows written to the record")
		fmt.Fprintf(w, "aiscast_store_rows_written_total %d\n", st.rowsWritten.Load())
		metricHead(w, "aiscast_store_bytes", "gauge", "size of the record database and its write-ahead log")
		fmt.Fprintf(w, "aiscast_store_bytes %d\n", st.bytes())
		metricHead(w, "aiscast_store_mirror_vessels", "gauge", "vessels in the in-memory mirror of the record")
		fmt.Fprintf(w, "aiscast_store_mirror_vessels %d\n", st.mirror.len())
		metricHead(w, "aiscast_store_mirror_failures_total", "counter", "mirror refreshes that failed after a write; their vessels are read back again with the next write")
		fmt.Fprintf(w, "aiscast_store_mirror_failures_total %d\n", st.mirrorFailures.Load())
	}
	p.vmu.RLock()
	c := p.ch
	p.vmu.RUnlock()
	chUp := 0
	if c != nil && !c.failing.Load() {
		chUp = 1
	}
	metricHead(w, "aiscast_clickhouse_up", "gauge", "1 when ClickHouse is connected and took the last batch; 0 while CLICKHOUSE_URL is unset, it has not answered, or batches are failing")
	fmt.Fprintf(w, "aiscast_clickhouse_up %d\n", chUp)
	if c != nil {
		metricHead(w, "aiscast_clickhouse_points_written_total", "counter", "copies of position reports written to ClickHouse")
		fmt.Fprintf(w, "aiscast_clickhouse_points_written_total %d\n", c.written.Load())
		metricHead(w, "aiscast_clickhouse_write_failures_total", "counter", "ClickHouse batches that failed; the positions are retried on the next flush")
		fmt.Fprintf(w, "aiscast_clickhouse_write_failures_total %d\n", c.failures.Load())
		metricHead(w, "aiscast_clickhouse_write_seconds_total", "counter", "time spent writing batches to ClickHouse")
		fmt.Fprintf(w, "aiscast_clickhouse_write_seconds_total %.3f\n", float64(c.writeNanos.Load())/1e9)
		metricHead(w, "aiscast_clickhouse_points_dropped_total", "counter", "positions dropped because the ClickHouse writer fell behind by more than its queue holds, or in a batch ClickHouse refused for 10 minutes")
		fmt.Fprintf(w, "aiscast_clickhouse_points_dropped_total %d\n", c.dropped.Load())
		metricHead(w, "aiscast_clickhouse_own_dropped_total", "counter", "own-ship sightings not written: refused past a sender's 4 vessels an hour, or full past 10,000 senders an hour or 10,000 waiting for ClickHouse")
		fmt.Fprintf(w, "aiscast_clickhouse_own_dropped_total{reason=\"refused\"} %d\n", c.ownRefused.Load())
		fmt.Fprintf(w, "aiscast_clickhouse_own_dropped_total{reason=\"full\"} %d\n", c.ownDropped.Load())
		metricHead(w, "aiscast_clickhouse_rebuilt_copies_total", "counter", "stale copies from rebuilt sources: matched to a transmission the vessel sent in the last five minutes, or kept as a late report of its own")
		fmt.Fprintf(w, "aiscast_clickhouse_rebuilt_copies_total{matched=\"true\"} %d\n", c.rebuiltMatched.Load())
		fmt.Fprintf(w, "aiscast_clickhouse_rebuilt_copies_total{matched=\"false\"} %d\n", c.rebuiltLate.Load())
	}
	if p.vesselHistory != nil {
		metricHead(w, "aiscast_import_runs_total", "counter", "daily merges of ClickHouse's vessel history into the record")
		fmt.Fprintf(w, "aiscast_import_runs_total %d\n", p.imports.runs.Load())
		metricHead(w, "aiscast_import_failures_total", "counter", "merges of ClickHouse's vessel history that failed; the next check retries")
		fmt.Fprintf(w, "aiscast_import_failures_total %d\n", p.imports.failures.Load())
		metricHead(w, "aiscast_import_rows_total", "counter", "vessels merged into the record from ClickHouse")
		fmt.Fprintf(w, "aiscast_import_rows_total %d\n", p.imports.rows.Load())
		if t := p.imports.lastSuccess.Load(); t > 0 {
			metricHead(w, "aiscast_import_last_success_timestamp_seconds", "gauge", "when ClickHouse's vessel history last merged into the record")
			fmt.Fprintf(w, "aiscast_import_last_success_timestamp_seconds %d\n", t)
		}
	}
	if h := p.history; h != nil {
		h.mu.Lock()
		metricHead(w, "aiscast_history_latest_day_timestamp_seconds", "gauge", "the newest day of each historical archive loaded into ClickHouse, 0 until one has")
		for _, s := range h.sources {
			var at int64 // 0 until a day loads, so an archive that never loads reads as stale rather than absent
			if t := h.latest[s]; !t.IsZero() {
				at = t.Unix()
			}
			fmt.Fprintf(w, "aiscast_history_latest_day_timestamp_seconds{source=%q} %d\n", s, at)
		}
		h.mu.Unlock()
		metricHead(w, "aiscast_history_files_total", "counter", "historical archive files loaded, or failed and left for the next check")
		for _, s := range h.sources {
			fmt.Fprintf(w, "aiscast_history_files_total{source=%q,result=\"loaded\"} %d\n", s, h.loaded[s].Load())
			fmt.Fprintf(w, "aiscast_history_files_total{source=%q,result=\"failed\"} %d\n", s, h.failed[s].Load())
		}
		metricHead(w, "aiscast_history_check_failures_total", "counter", "historical archive checks that failed before any file: the listing, history_loads, or the connection")
		for _, s := range h.sources {
			fmt.Fprintf(w, "aiscast_history_check_failures_total{source=%q} %d\n", s, h.checks[s].Load())
		}
		metricHead(w, "aiscast_history_rows_total", "counter", "receptions loaded from historical archives")
		for _, s := range h.sources {
			fmt.Fprintf(w, "aiscast_history_rows_total{source=%q} %d\n", s, h.rows[s].Load())
		}
	}
	if p.store != nil {
		metricHead(w, "aiscast_wikidata_syncs_total", "counter", "weekly syncs of vessel particulars from Wikidata")
		fmt.Fprintf(w, "aiscast_wikidata_syncs_total %d\n", p.wikidata.runs.Load())
		metricHead(w, "aiscast_wikidata_sync_failures_total", "counter", "Wikidata syncs that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_wikidata_sync_failures_total %d\n", p.wikidata.failures.Load())
		metricHead(w, "aiscast_wikidata_ships", "gauge", "IMO numbers with particulars from Wikidata")
		fmt.Fprintf(w, "aiscast_wikidata_ships %d\n", p.wikidata.ships.Load())
		if t := p.wikidata.lastSuccess.Load(); p.wikidata.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_wikidata_last_success_timestamp_seconds", "gauge", "when vessel particulars last synced from Wikidata")
			fmt.Fprintf(w, "aiscast_wikidata_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_uscg_syncs_total", "counter", "weekly listings of US-flag vessels from the Coast Guard's PSIX")
		fmt.Fprintf(w, "aiscast_uscg_syncs_total %d\n", p.uscg.runs.Load())
		metricHead(w, "aiscast_uscg_sync_failures_total", "counter", "PSIX listings that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_uscg_sync_failures_total %d\n", p.uscg.failures.Load())
		metricHead(w, "aiscast_uscg_vessels", "gauge", "US-flag vessels with a call sign or an official number listed from PSIX")
		fmt.Fprintf(w, "aiscast_uscg_vessels %d\n", p.uscg.vessels.Load())
		metricHead(w, "aiscast_uscg_details_total", "counter", "matched vessels whose dimensions and tonnage were read from PSIX")
		fmt.Fprintf(w, "aiscast_uscg_details_total %d\n", p.uscg.details.Load())
		metricHead(w, "aiscast_uscg_detail_failures_total", "counter", "PSIX dimension and tonnage reads that failed; the round stops and the next resumes")
		fmt.Fprintf(w, "aiscast_uscg_detail_failures_total %d\n", p.uscg.detailFailures.Load())
		if t := p.uscg.lastSuccess.Load(); p.uscg.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_uscg_last_success_timestamp_seconds", "gauge", "when US-flag vessels were last listed from PSIX")
			fmt.Fprintf(w, "aiscast_uscg_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_fiskeridir_syncs_total", "counter", "weekly syncs of Norway's fishing vessel register")
		fmt.Fprintf(w, "aiscast_fiskeridir_syncs_total %d\n", p.fiskeridir.runs.Load())
		metricHead(w, "aiscast_fiskeridir_sync_failures_total", "counter", "register syncs that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_fiskeridir_sync_failures_total %d\n", p.fiskeridir.failures.Load())
		metricHead(w, "aiscast_fiskeridir_vessels", "gauge", "registered Norwegian fishing vessels with a call sign stored")
		fmt.Fprintf(w, "aiscast_fiskeridir_vessels %d\n", p.fiskeridir.vessels.Load())
		if t := p.fiskeridir.lastSuccess.Load(); p.fiskeridir.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_fiskeridir_last_success_timestamp_seconds", "gauge", "when the register was last synced")
			fmt.Fprintf(w, "aiscast_fiskeridir_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_fcc_syncs_total", "counter", "weekly syncs of FCC ship station licenses")
		fmt.Fprintf(w, "aiscast_fcc_syncs_total %d\n", p.fcc.runs.Load())
		metricHead(w, "aiscast_fcc_sync_failures_total", "counter", "license syncs that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_fcc_sync_failures_total %d\n", p.fcc.failures.Load())
		metricHead(w, "aiscast_fcc_ships", "gauge", "active FCC ship licenses with an MMSI stored")
		fmt.Fprintf(w, "aiscast_fcc_ships %d\n", p.fcc.ships.Load())
		if t := p.fcc.lastSuccess.Load(); p.fcc.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_fcc_last_success_timestamp_seconds", "gauge", "when the licenses were last synced")
			fmt.Fprintf(w, "aiscast_fcc_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_tc_syncs_total", "counter", "weekly syncs of Transport Canada's vessel register")
		fmt.Fprintf(w, "aiscast_tc_syncs_total %d\n", p.tc.runs.Load())
		metricHead(w, "aiscast_tc_sync_failures_total", "counter", "register syncs that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_tc_sync_failures_total %d\n", p.tc.failures.Load())
		metricHead(w, "aiscast_tc_vessels", "gauge", "registered Canadian vessels with an IMO stored")
		fmt.Fprintf(w, "aiscast_tc_vessels %d\n", p.tc.vessels.Load())
		if t := p.tc.lastSuccess.Load(); p.tc.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_tc_last_success_timestamp_seconds", "gauge", "when the register was last synced")
			fmt.Fprintf(w, "aiscast_tc_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_amsa_syncs_total", "counter", "weekly syncs of AMSA's list of registered ships")
		fmt.Fprintf(w, "aiscast_amsa_syncs_total %d\n", p.amsa.runs.Load())
		metricHead(w, "aiscast_amsa_sync_failures_total", "counter", "list syncs that failed; the next hourly check retries")
		fmt.Fprintf(w, "aiscast_amsa_sync_failures_total %d\n", p.amsa.failures.Load())
		metricHead(w, "aiscast_amsa_vessels", "gauge", "registered Australian vessels with an IMO stored")
		fmt.Fprintf(w, "aiscast_amsa_vessels %d\n", p.amsa.vessels.Load())
		if t := p.amsa.lastSuccess.Load(); p.amsa.enabled.Load() && t > 0 {
			metricHead(w, "aiscast_amsa_last_success_timestamp_seconds", "gauge", "when the list was last synced")
			fmt.Fprintf(w, "aiscast_amsa_last_success_timestamp_seconds %d\n", t)
		}
		metricHead(w, "aiscast_ised_checked_total", "counter", "Canadian vessels asked about in ISED's MMSI registry")
		fmt.Fprintf(w, "aiscast_ised_checked_total %d\n", p.ised.checked.Load())
		metricHead(w, "aiscast_ised_check_failures_total", "counter", "registry asks that failed; the vessel waits for the next round")
		fmt.Fprintf(w, "aiscast_ised_check_failures_total %d\n", p.ised.failures.Load())
		metricHead(w, "aiscast_ised_ships", "gauge", "Canadian vessels with a registry record stored")
		fmt.Fprintf(w, "aiscast_ised_ships %d\n", p.ised.ships.Load())
	}

	metricHead(w, "aiscast_streams", "gauge", "open streams by protocol and tier; the loopback health probe is one v1 anonymous stream")
	streams := p.streams.snapshot()
	keys := make([][2]string, 0, len(streams))
	for k := range streams {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	for _, k := range keys {
		fmt.Fprintf(w, "aiscast_streams{protocol=%q,tier=%q} %d\n", k[0], k[1], streams[k])
	}

	fan := []struct {
		protocol string
		c        *fanoutCounter
	}{{"v0", &p.fanout.v0}, {"v1", &p.fanout.v1}, {"sse", &p.fanout.sse}, {"nmea", &p.fanout.nmea}}
	metricHead(w, "aiscast_fanout_sends_total", "counter", "live events written to stream clients")
	for _, f := range fan {
		fmt.Fprintf(w, "aiscast_fanout_sends_total{protocol=%q} %d\n", f.protocol, f.c.sends.Load())
	}
	metricHead(w, "aiscast_fanout_bytes_total", "counter", "bytes of live events written to stream clients, before compression")
	for _, f := range fan {
		fmt.Fprintf(w, "aiscast_fanout_bytes_total{protocol=%q} %d\n", f.protocol, f.c.bytes.Load())
	}

	p.writeRequestMetrics(w)

	metricHead(w, "aiscast_source_last_age_seconds", "gauge", "seconds since the last event from each source")
	var sources []string
	p.lastBySource.Range(func(k, _ any) bool { sources = append(sources, k.(string)); return true })
	sort.Strings(sources)
	for _, s := range sources {
		fmt.Fprintf(w, "aiscast_source_last_age_seconds{source=%q} %.0f\n", s, p.sourceAge(s).Seconds())
	}
	metricHead(w, "aiscast_source_events_total", "counter", "events per source")
	p.stats.bySource.Range(func(k, v any) bool {
		fmt.Fprintf(w, "aiscast_source_events_total{source=%q} %d\n", k.(string), v.(*counterT).Load())
		return true
	})
	metricHead(w, "aiscast_source_delay_seconds", "gauge", "broadcast-to-arrival delay per source kind over its last 512 position reports, as in /v1/stats")
	var kinds []string
	p.delays.by.Range(func(k, _ any) bool { kinds = append(kinds, k.(string)); return true })
	sort.Strings(kinds)
	for _, k := range kinds {
		r, _ := p.delays.by.Load(k)
		if _, p50, p99, ok := r.(*delayRing).quantiles(); ok {
			fmt.Fprintf(w, "aiscast_source_delay_seconds{source=%q,quantile=\"0.5\"} %.1f\n", k, p50)
			fmt.Fprintf(w, "aiscast_source_delay_seconds{source=%q,quantile=\"0.99\"} %.1f\n", k, p99)
		}
	}

	metricHead(w, "aiscast_unmapped_fields_total", "counter", "source fields outside the capture set and the waiver ledger")
	unmappedFld.Range(func(k, v any) bool {
		site, field, _ := strings.Cut(k.(string), "\t")
		fmt.Fprintf(w, "aiscast_unmapped_fields_total{site=%q,field=%q} %d\n", site, field, v.(*atomic.Int64).Load())
		return true
	})
	metricHead(w, "aiscast_unmapped_types_total", "counter", "source record types no adapter handles")
	unmappedType.Range(func(k, v any) bool {
		site, typ, _ := strings.Cut(k.(string), "\t")
		fmt.Fprintf(w, "aiscast_unmapped_types_total{site=%q,type=%q} %d\n", site, typ, v.(*atomic.Int64).Load())
		return true
	})

	writeProcessMetrics(w)
}

func (p *Pipeline) writeRequestMetrics(w io.Writer) {
	m := &p.requests
	m.mu.Lock()
	defer m.mu.Unlock()
	metricHead(w, "aiscast_http_requests_total", "counter", "HTTP requests by route pattern and status; a stream counts when it closes")
	keys := make([][2]string, 0, len(m.count))
	for k := range m.count {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	for _, k := range keys {
		fmt.Fprintf(w, "aiscast_http_requests_total{route=%q,status=%q} %d\n", k[0], k[1], m.count[k])
	}
	metricHead(w, "aiscast_http_request_duration_seconds", "histogram", "time to serve the vessel routes and /mcp")
	for _, route := range timedRoutes {
		h := m.hist[route]
		if h == nil {
			h = &histogram{}
		}
		var cum int64
		for i, le := range latencyBuckets {
			cum += h.counts[i]
			fmt.Fprintf(w, "aiscast_http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", route, le, cum)
		}
		cum += h.counts[len(latencyBuckets)]
		fmt.Fprintf(w, "aiscast_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", route, cum)
		fmt.Fprintf(w, "aiscast_http_request_duration_seconds_sum{route=%q} %g\n", route, h.sum)
		fmt.Fprintf(w, "aiscast_http_request_duration_seconds_count{route=%q} %d\n", route, cum)
	}
}

// writeProcessMetrics uses the standard process_ names so dashboards and alerts read them like any exporter's.
// RSS and open files come from /proc and are absent off Linux.
// buildRevision is the git commit the binary was built from, "unknown" outside a checkout (go test, go run of
// a tarball). A new value on the next start is a deploy; a start with the same value is a restart.
var buildRevision = func() string {
	rev, modified := "unknown", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if modified {
		rev += "-modified"
	}
	return rev
}()

func writeProcessMetrics(w io.Writer) {
	metricHead(w, "aiscast_build_info", "gauge", "always 1; the revision label is the git commit this binary was built from")
	fmt.Fprintf(w, "aiscast_build_info{revision=%q,go_version=%q} 1\n", buildRevision, runtime.Version())
	metricHead(w, "process_start_time_seconds", "gauge", "start time of the process since the unix epoch in seconds")
	fmt.Fprintf(w, "process_start_time_seconds %d\n", bootTime.Unix())
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		metricHead(w, "process_cpu_seconds_total", "counter", "total user and system CPU time spent in seconds")
		fmt.Fprintf(w, "process_cpu_seconds_total %.3f\n", float64(ru.Utime.Nano()+ru.Stime.Nano())/1e9)
	}
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 1 {
			if pages, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				metricHead(w, "process_resident_memory_bytes", "gauge", "resident memory size in bytes")
				fmt.Fprintf(w, "process_resident_memory_bytes %d\n", pages*int64(os.Getpagesize()))
			}
		}
	}
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		metricHead(w, "process_open_fds", "gauge", "number of open file descriptors")
		fmt.Fprintf(w, "process_open_fds %d\n", len(fds))
	}
	metricHead(w, "go_goroutines", "gauge", "number of goroutines that currently exist")
	fmt.Fprintf(w, "go_goroutines %d\n", runtime.NumGoroutine())
	writeRuntimeMetrics(w)
}

// runtimeMetrics show the garbage collector's cost, which the record mirror adds to.
var runtimeMetrics = []struct{ name, typ, help, key string }{
	{"go_gc_cpu_seconds_total", "counter", "CPU time the garbage collector used, estimated by the runtime", "/cpu/classes/gc/total:cpu-seconds"},
	{"go_gc_cycles_total", "counter", "completed garbage collection cycles", "/gc/cycles/total:gc-cycles"},
	{"go_heap_allocs_bytes_total", "counter", "bytes allocated on the heap", "/gc/heap/allocs:bytes"},
	{"go_heap_live_bytes", "gauge", "heap bytes live at the end of the last collection", "/gc/heap/live:bytes"},
}

func writeRuntimeMetrics(w io.Writer) {
	samples := make([]metrics.Sample, len(runtimeMetrics))
	for i, m := range runtimeMetrics {
		samples[i].Name = m.key
	}
	metrics.Read(samples)
	for i, m := range runtimeMetrics {
		metricHead(w, m.name, m.typ, m.help)
		switch v := samples[i].Value; v.Kind() {
		case metrics.KindUint64:
			fmt.Fprintf(w, "%s %d\n", m.name, v.Uint64())
		case metrics.KindFloat64:
			fmt.Fprintf(w, "%s %g\n", m.name, v.Float64())
		}
	}
}
