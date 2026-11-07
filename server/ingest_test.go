package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

const testSentence = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"

// memWriter stands in for ClickHouse and keeps every copy the pipeline writes, in the order it writes them.
type memWriter struct {
	mu  sync.Mutex
	pts []trackPoint
}

func (m *memWriter) insert(_ context.Context, _ string, pts []trackPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pts = append(m.pts, pts...)
	return nil
}

// recordingPipeline is a test pipeline whose copies land in a memWriter; written flushes and returns them.
func recordingPipeline(t *testing.T) (*Pipeline, func() []trackPoint) {
	t.Helper()
	p := testPipeline(t)
	w := &memWriter{}
	p.attachClickHouse(&chStore{w: w})
	return p, func() []trackPoint {
		t.Helper()
		for range 2 { // a batch held back to resend goes first
			if err := p.flushClickHouse(); err != nil {
				t.Fatal(err)
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		return slices.Clone(w.pts)
	}
}

// eventSentences drains a subscriber and returns each event's sentences, joined.
func eventSentences(s *subscriber) []string {
	var out []string
	for {
		select {
		case ev := <-s.ch:
			out = append(out, strings.Join(ev.Sentences, "|"))
		default:
			return out
		}
	}
}

// The dedupe window survives a restart: a copy heard just after it is a copy, not a new transmission.
func TestDedupePersistsAcrossRestart(t *testing.T) {
	p1 := testPipeline(t)
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	p1.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: recv, Body: testSentence})
	state := filepath.Join(t.TempDir(), "dedupe.json")
	if err := p1.saveDedupe(state); err != nil {
		t.Fatal(err)
	}
	p2 := testPipeline(t)
	if n, err := p2.loadDedupe(state); err != nil || n != 1 {
		t.Fatalf("loadDedupe = %d, %v", n, err)
	}
	p2.ingestLine(Reception{Source: "udp:bbbb", Station: "udp:bbbb", RecvTime: recv.Add(2 * time.Second), Body: testSentence})
	if p2.stats.events.Load() != 0 || p2.stats.dup.Load() != 1 {
		t.Fatalf("after a restart: %d events, %d copies; want the copy only", p2.stats.events.Load(), p2.stats.dup.Load())
	}
}

// A replay's lead-in builds state and writes nothing: only copies received from the gate on reach ClickHouse.
func TestReplayGateHoldsTheLeadInBack(t *testing.T) {
	p, written := recordingPipeline(t)
	gate := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	p.replayGate = gate
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: gate.Add(-time.Minute), Body: testSentence})
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: gate.Add(time.Minute), Body: testSentence})
	pts := written()
	if len(pts) != 1 || pts[0].recv.Before(gate) {
		t.Fatalf("written %+v, want only the copy after the gate", pts)
	}
}

func TestBarentswatchAltitudeCaptured(t *testing.T) {
	found := false
	for _, rec := range fixtureLines(t, "barentswatch.lines") {
		var m bwMessage
		if err := json.Unmarshal([]byte(rec.body), &m); err != nil {
			t.Fatal(err)
		}
		if m.Altitude == nil || m.MessageType != 9 {
			continue
		}
		found = true
		sar, ok := m.position().(ais.StandardSearchAndRescueAircraftReport)
		if !ok {
			t.Fatal("type 9 did not map to a SAR report")
		}
		if sar.Altitude == 4095 {
			t.Fatalf("altitude %v decayed to the n/a sentinel", *m.Altitude)
		}
	}
	if !found {
		t.Fatal("fixture has no type 9 with altitude; resample testdata/sources/barentswatch.lines")
	}
}

// Replaying the same raw day twice writes the same copies: replay depends on nothing but its input.
func TestReplayTwiceIsIdentical(t *testing.T) {
	raw := writeRawTree(t)
	run := func() []trackPoint {
		p, written := recordingPipeline(t)
		readers, err := collectReaders(raw, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := replayReaders(p, readers, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), nil); err != nil {
			t.Fatal(err)
		}
		return written()
	}
	first, second := run(), run()
	a, _ := json.Marshal(fmt.Sprintf("%+v", first))
	b, _ := json.Marshal(fmt.Sprintf("%+v", second))
	if len(first) == 0 || string(a) != string(b) {
		t.Fatalf("replay wrote %d copies, then %d, or differed between runs", len(first), len(second))
	}
}

// writeRawTree lays the fixture corpus out as a raw archive day, timestamps rebased to one hour.
func writeRawTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	trees := map[string]string{
		"kystverket.lines":   "NLOD-2.0/kystverket",
		"barentswatch.lines": "NLOD-2.0/barentswatch",
		"digitraffic.lines":  "CC-BY-4.0/digitraffic",
		"aisstream.lines":    "aisstream-io-terms/aisstream",
		"aishub.lines":       "aishub-terms/aishub",
		"catcher.lines":      "CC0-1.0/http/test-station",
	}
	i := 0
	for name, prefix := range trees {
		path := filepath.Join(dir, prefix, base.Format("2006/01/02/15")+".gz")
		os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		for _, rec := range fixtureLines(t, name) {
			i++
			ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
			gz.Write([]byte(ts + "\t" + rec.station + "\t" + rec.body + "\n"))
		}
		gz.Close()
		f.Close()
	}
	return dir
}

type fixtureRec struct{ station, body string }

// fixtureLines yields one record per fixture entry; digitraffic bodies keep their inner newlines.
func fixtureLines(t *testing.T, name string) []fixtureRec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sources", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []fixtureRec
	var cur *fixtureRec
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &fixtureRec{station: parts[1], body: parts[2]}
		} else if cur != nil {
			cur.body += "\n" + line
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// Replay skips the retired normalized stream and the access log, whose hour keys look like raw ones.
func TestReplaySkipsWhatSharesTheBucket(t *testing.T) {
	raw := writeRawTree(t)
	for _, prefix := range []string{"normalized", accessPrefix} {
		stream := filepath.Join(raw, prefix, "v1", "2026", "09", "01", "12.gz")
		os.MkdirAll(filepath.Dir(stream), 0o755)
		f, err := os.Create(stream)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		gz.Write([]byte("2026-09-01T12:00:00Z\tv1\t!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23\n")) // parseable, so only the skip keeps it out
		gz.Close()
		f.Close()
	}
	for _, r := range allReaders(t, raw) {
		for _, path := range r.paths {
			for _, prefix := range []string{"normalized", accessPrefix} {
				if strings.Contains(filepath.ToSlash(path), "/"+prefix+"/") {
					t.Fatalf("replay would read %s as source %q: %s", prefix, r.source, path)
				}
			}
		}
	}
}

// Raw hours recorded before station ids name contributors by transport. Backfill replays them, and
// their copies must carry the contributor license, not "unspecified".
func TestHistoricalContributorSourcesStayCC0(t *testing.T) {
	for _, src := range []string{"http:abc", "v1:ed25519:xyz", "v1:mmsi:368168720", "station:abc"} {
		if got := licenseOf(src); got != "CC0-1.0" {
			t.Errorf("licenseOf(%q) = %q, want CC0-1.0", src, got)
		}
	}
}

// Corroboration is runtime state the archive cannot derive again, so each copy records it: set for a position only
// an unauthenticated sender heard, clear for one from a trusted source.
func TestCopiesRecordCorroboration(t *testing.T) {
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		source string
		want   bool
	}{{"udp:aaaa", true}, {"kystverket", false}} {
		p, written := recordingPipeline(t)
		p.ingestLine(Reception{Source: c.source, Station: c.source, RecvTime: recv, Body: testSentence})
		pts := written()
		if len(pts) != 1 || pts[0].uncorroborated != c.want {
			t.Fatalf("%s: written %+v, want one copy with uncorroborated %v", c.source, pts, c.want)
		}
	}
}

// A multipart message's sentences are exactly its own. Real traffic from one station: part 1 of sequence 7
// never completed, then sequence 9 arrived whole. And another station sends parts in reverse order, which still
// assembles with both sentences.
func TestMultipartCarriesOnlyItsOwnSentences(t *testing.T) {
	stale := "!AIVDM,2,1,7,A,55RGLH82G=qO<DuSB20d4d58TdV2222222222216?hP7B6LB0C3VUk1p,0*1A"
	seq9 := []string{"!AIVDM,2,1,9,B,55RGLH82G=qO<DuSB20d4d58TdV2222222222216?hP7B6LB0C3VUk1p,0*17", "!AIVDM,2,2,9,B,888888888888880,2*2E"}
	reversed := []string{"!AIVDM,2,2,0,B,000000000000000,2*27", "!AIVDM,2,1,0,B,53GQrVH2HFaLD4Hv221@4h5HE86222222222220l1P;4840Ht0000000,0*1E"}
	for _, c := range []struct {
		name  string
		lines []string
		want  []string
	}{
		{"stale fragment before a whole message", append([]string{stale}, seq9...), seq9},
		{"fragments in reverse order", reversed, reversed},
	} {
		p := testPipeline(t)
		sub := p.subscribe()
		recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		for i, l := range c.lines {
			p.ingestLine(Reception{Source: "station:s", Station: "station:s", RecvTime: recv.Add(time.Duration(i) * time.Second), Body: l})
		}
		if got := eventSentences(sub); len(got) != 1 || got[0] != strings.Join(c.want, "|") {
			t.Fatalf("%s: events carry sentences %q, want one with %q", c.name, got, c.want)
		}
	}
}

// The vessel cache sweeps on the reception clock, so replay, which runs far faster than real time
// and has no ticker, drops a vessel at the same point live did. The cache gives a static its
// position and decides staleness, and both are archived.
func TestVesselCacheSweepsOnTheReceptionClock(t *testing.T) {
	p := testPipeline(t)
	cached := func() map[uint32]bool {
		p.vmu.RLock()
		defer p.vmu.RUnlock()
		out := map[uint32]bool{}
		for m := range p.vessels {
			out[m] = true
		}
		return out
	}
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: recv, Body: testSentence})
	first := cached()
	if len(first) != 1 {
		t.Fatalf("vessels = %v, want 1", first)
	}
	// another vessel, heard past the TTL on the reception clock; no wall-clock time has passed
	later := recv.Add(vesselTTL + time.Minute)
	other := tagBlock(map[byte]string{'c': fmt.Sprint(later.Unix())}) + "!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: later, Body: other})
	now := cached()
	for m := range first {
		if now[m] || len(now) != 1 {
			t.Fatalf("after vessel %d went unheard past the TTL: cached %v, want only the second vessel", m, now)
		}
	}
}

// Two multipart messages from one station on one channel, fragments interleaved: pending fragments
// are keyed by the sentence's sequence id (go-nmea's VDMVDO.MessageID is that field, not the AIS
// message type), so each message carries exactly its own sentences.
func TestInterleavedMultipartMessagesStaySeparate(t *testing.T) {
	a := []string{"!AIVDM,2,1,9,B,55RGLH82G=qO<DuSB20d4d58TdV2222222222216?hP7B6LB0C3VUk1p,0*17", "!AIVDM,2,2,9,B,888888888888880,2*2E"}
	b := []string{"!AIVDM,2,1,0,B,53GQrVH2HFaLD4Hv221@4h5HE86222222222220l1P;4840Ht0000000,0*1E", "!AIVDM,2,2,0,B,000000000000000,2*27"}
	p := testPipeline(t)
	sub := p.subscribe()
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, l := range []string{a[0], b[0], a[1], b[1]} {
		p.ingestLine(Reception{Source: "station:s", Station: "station:s", RecvTime: recv.Add(time.Duration(i) * time.Second), Body: l})
	}
	got := eventSentences(sub)
	want := []string{strings.Join(a, "|"), strings.Join(b, "|")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("interleaved messages carry\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Live stamps a reception's receive time when it is admitted, inside the ordering lock, so the order receptions
// are processed and written is receive-time order: the order the raw archive keeps and replay merges. Times taken
// before the lock, as a caller's clock or a fetch's start, would not be.
func TestAdmissionTimeIsProcessingOrder(t *testing.T) {
	p, written := recordingPipeline(t)
	p.stampAtAdmission = true
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				now := time.Now() // the caller's clock, as a source adapter takes it before admission
				runtime.Gosched()
				src := fmt.Sprintf("udp:%04d", g)
				p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: testSentence})
			}
		}(g)
	}
	wg.Wait()
	pts := written()
	for i := 1; i < len(pts); i++ {
		if pts[i].recv.Before(pts[i-1].recv) {
			t.Fatalf("copy %d received %s after one received %s: processing order is not receive-time order", i,
				pts[i].recv.Format(time.RFC3339Nano), pts[i-1].recv.Format(time.RFC3339Nano))
		}
	}
	if len(pts) < 1600 {
		t.Fatalf("%d copies written, want every reception", len(pts))
	}
}

// tcpListener serves TCP ingest on a loopback port for one test.
func tcpListener(t *testing.T, p *Pipeline) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serveTCP(p, ln)
	return ln.Addr().String()
}

// dialTCP connects and returns the connection with a reader for the server's reply line.
func dialTCP(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c)
}

// closed reports whether the server has closed c, reading past anything it wrote first.
func closed(c net.Conn, br *bufio.Reader) bool {
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadAll(br)
	var ne net.Error
	return !errors.As(err, &ne) || !ne.Timeout() // EOF, or a reset when the server closed with input unread
}

// tagged puts a TAG block with a valid checksum in front of a sentence.
func tagged(tag, line string) string {
	var sum byte
	for i := range len(tag) {
		sum ^= tag[i]
	}
	return fmt.Sprintf("\\%s*%02X\\%s", tag, sum, line)
}

// A token on the first line, then NMEA: the lines are the token's station's, as a /v1/stream publish frame's are,
// and a refused token is answered and the connection closed.
func TestTCPIngest(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	exp := time.Now().Add(time.Hour).Unix()
	feeder, _ := signToken(priv, Claims{Kid: kid, Sub: "st-9", Role: "feeder", Exp: exp})
	partner, _ := signToken(priv, Claims{Kid: kid, Sub: "acme", Role: "partner", Exp: exp})
	elsewhere, _ := signToken(priv, Claims{Kid: kid, Sub: "far", Role: "feeder", Exp: exp, CIDR: []string{"203.0.113.0/24"}})
	sub := p.subscribe()
	addr := tcpListener(t, p)

	c, br := dialTCP(t, addr)
	fmt.Fprintf(c, "AUTH %s\r\n", feeder)
	if l, err := br.ReadString('\n'); err != nil || l != "OK "+termsURL+"\r\n" {
		t.Fatalf("reply %q %v, want OK and the terms", l, err)
	}
	// a blank line is skipped, and a TAG block is read as on UDP
	fmt.Fprintf(c, "\r\n%s\r\n%s\n", testSentence, tagged(fmt.Sprintf("s:dock,c:%d", time.Now().Unix()), "!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"))
	for _, want := range []uint32{227006760, 258857000} {
		select {
		case ev := <-sub.ch:
			if ev.Source != "station:st-9" || ev.Station != "station:st-9" || ev.MMSI != want {
				t.Errorf("event %s %s %d, want station:st-9 and %d", ev.Source, ev.Station, ev.MMSI, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no event for %d (parse_err=%d)", want, p.stats.parseErr.Load())
		}
	}

	for name, first := range map[string]string{
		"partner may not publish": "AUTH " + partner,
		"token bound elsewhere":   "AUTH " + elsewhere,
		"garbage":                 "AUTH ak1.nope",
		"no AUTH":                 testSentence,
	} {
		c, br := dialTCP(t, addr)
		fmt.Fprintf(c, "%s\n%s\n", first, testSentence)
		if l, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(l, "ERR ") {
			t.Errorf("%s: reply %q %v, want ERR", name, l, err)
		}
		if !closed(c, br) {
			t.Errorf("%s: connection stayed open", name)
		}
	}
	select {
	case ev := <-sub.ch:
		t.Errorf("refused connection ingested %d", ev.MMSI)
	case <-time.After(100 * time.Millisecond):
	}
}

// A connection that sends no token closes after the timeout; one past the address's cap, or a line past the
// limit, closes at once, so an open port cannot be made to hold memory.
func TestTCPIngestBounds(t *testing.T) {
	p := testPipeline(t)
	addr := tcpListener(t, p)

	tcpAuthTimeout = 200 * time.Millisecond
	defer func() { tcpAuthTimeout = 5 * time.Second }()
	c, br := dialTCP(t, addr)
	start := time.Now()
	if !closed(c, br) || time.Since(start) > time.Second {
		t.Errorf("silent connection open after %s", time.Since(start))
	}
	tcpAuthTimeout = 5 * time.Second

	var held []net.Conn
	for range tcpPerAddr {
		c, _ := dialTCP(t, addr)
		held = append(held, c)
	}
	waitTCPSlots(t, tcpPerAddr) // accepted and counted, so the next is the one past the cap
	c, br = dialTCP(t, addr)
	if l, _ := br.ReadString('\n'); l != "ERR concurrent connections per address exceeded\r\n" {
		t.Errorf("connection past the cap: reply %q", l)
	}
	for _, h := range held {
		h.Close()
	}
	waitTCPSlots(t, 0) // the slots come back as the held connections close

	c, br = dialTCP(t, addr)
	fmt.Fprintf(c, "AUTH anything\n") // ALLOW_ANON: any token is the anonymous admin
	if l, _ := br.ReadString('\n'); !strings.HasPrefix(l, "OK ") {
		t.Fatalf("reply %q", l)
	}
	c.Write([]byte(strings.Repeat("x", tcpMaxLine+1) + "\n"))
	if !closed(c, br) {
		t.Error("connection open after an overlong line")
	}
}

// waitTCPSlots waits for loopback to hold n TCP connection slots.
func waitTCPSlots(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		tcpAddrConns.mu.Lock()
		got := tcpAddrConns.n["127.0.0.1"]
		tcpAddrConns.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d TCP slots held, want %d", got, n)
		}
	}
}

// withSeq gives a sentence another sequence id and the checksum that goes with it.
func withSeq(line string, seq int) string {
	f := strings.Split(line[1:strings.LastIndex(line, "*")], ",")
	f[3] = strconv.Itoa(seq)
	body := strings.Join(f, ",")
	var sum byte
	for i := range len(body) {
		sum ^= body[i]
	}
	return fmt.Sprintf("!%s*%02X", body, sum)
}

// Two TCP connections under one token number their sequences on their own. Each keeps its own reassembler,
// so the same sequence id from both, interleaved, still makes two messages; and a closed connection's
// unfinished fragments are dropped with its reassembler.
func TestTCPConnectionsReassembleApart(t *testing.T) {
	a := []string{"!AIVDM,2,1,9,B,55RGLH82G=qO<DuSB20d4d58TdV2222222222216?hP7B6LB0C3VUk1p,0*17", "!AIVDM,2,2,9,B,888888888888880,2*2E"}
	b := []string{withSeq("!AIVDM,2,1,0,B,53GQrVH2HFaLD4Hv221@4h5HE86222222222220l1P;4840Ht0000000,0*1E", 9), withSeq("!AIVDM,2,2,0,B,000000000000000,2*27", 9)}
	if b[1] != "!AIVDM,2,2,9,B,000000000000000,2*2E" {
		t.Fatalf("withSeq: %s", b[1])
	}
	p := testPipeline(t)
	sub := p.subscribe()
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, rx := range []Reception{{Body: a[0], Conn: "tcp:1"}, {Body: b[0], Conn: "tcp:2"}, {Body: a[1], Conn: "tcp:1"}, {Body: b[1], Conn: "tcp:2"}} {
		rx.Source, rx.Station, rx.RecvTime = "station:s", "station:s", recv.Add(time.Duration(i)*time.Second)
		p.ingestLine(rx)
	}
	got := eventSentences(sub)
	want := []string{strings.Join(a, "|"), strings.Join(b, "|")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("interleaved connections carry\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	p.ingestLine(Reception{Source: "station:s", Station: "station:s", RecvTime: recv, Body: a[0], Conn: "tcp:1"})
	p.ingestLine(Reception{Source: "station:s", Station: "station:s", RecvTime: recv, Body: b[0], Conn: "tcp:12"})
	p.forgetConn("tcp:1")
	p.mu.Lock()
	_, kept := p.codecs["station:s\x00tcp:12"]
	_, gone := p.codecs["station:s\x00tcp:1"]
	pending := len(p.pending)
	p.mu.Unlock()
	if gone || !kept || pending != 1 {
		t.Errorf("after forgetting tcp:1: its codec kept %v, tcp:12's kept %v, %d pending, want false, true, 1", gone, kept, pending)
	}
}
