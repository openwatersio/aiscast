package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
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
var timedRoutes = []string{"/v1/vessels", "/mcp"}

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

// statusWriter records the status a handler sends. Unwrap keeps WebSocket hijacking and SSE flushing working.
type statusWriter struct {
	http.ResponseWriter
	status int
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
	return w.ResponseWriter.Write(b)
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
	counter("aiscast_archive_drops_total", "receptions dropped because the archive queue was full", p.arch.drops.Load())
	counter("aiscast_archive_upload_failures_total", "archive hour uploads to the bucket that failed; the hourly sweep retries them", p.arch.uploadFailures.Load())
	counter("aiscast_ratelimited_total", "requests rejected by rate limits", p.stats.rateLimited.Load())
	counter("aiscast_thinned_total", "events withheld from connections over their per-second rate", p.stats.thinned.Load())
	counter("aiscast_implausible_total", "positions dropped for implying an impossible speed", p.stats.implausible.Load())
	counter("aiscast_stale_total", "events withheld from the stream for being older than the vessel's newest", p.stats.stale.Load())
	counter("aiscast_uncorroborated_total", "low-trust events kept local because no trusted source has heard the vessel", p.stats.uncorroborated.Load())

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

	metricHead(w, "aiscast_archive_staged_bytes", "gauge", "archive bytes on local disk at the last hourly sweep, not yet reclaimed after upload")
	fmt.Fprintf(w, "aiscast_archive_staged_bytes %d\n", p.arch.staged.Load())

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
	metricHead(w, "aiscast_http_request_duration_seconds", "histogram", "time to serve /v1/vessels and /mcp")
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
func writeProcessMetrics(w io.Writer) {
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
}
