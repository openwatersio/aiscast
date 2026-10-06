package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
