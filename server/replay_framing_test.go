package main

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The raw writer keeps a body's own newlines, so a valid pretty-printed envelope from any source
// spans lines. Replay must reassemble it and reproduce what live ingested, not only for digitraffic.
func TestReplayReassemblesMultiLineBodiesFromAnySource(t *testing.T) {
	rawDir, normDir := t.TempDir(), t.TempDir()
	p := testPipeline(t)
	p.arch = newArchive(rawDir, nil)
	p.norm = newNormArchive(normDir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	body := "{\n  \"protocol\": \"jsonaiscatcher\",\n  \"msgs\": [\n    {\"class\": \"AIS\", \"channel\": \"A\", \"rxtime\": \"20260901120039\", \"nmea\": [\"" + testSentence + "\"]}\n  ]\n}"
	if !p.ingestCatcher("http:pretty", []byte(body), recv) {
		t.Fatal("live rejected a valid envelope")
	}
	p.closeArchives()

	out := t.TempDir()
	runReplay([]string{"-archive", rawDir, "-out", out, "-from", "2026-09-01", "-to", "2026-09-02"})
	rep := diffNorm(loadNorm(normDir), loadNorm(out))
	if n := rep.live.eventCount(); n != 1 {
		t.Fatalf("live produced %d transmissions, want 1", n)
	}
	if !rep.clean() {
		t.Fatalf("replay lost the multi-line reception:\n%s", rep.render(3))
	}
}

// allReaders collects readers over every hour in dir, failing the test on a walk error.
func allReaders(t *testing.T, dir string) []*rawReader {
	t.Helper()
	rs, err := collectReaders(dir, time.Time{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// writeRawHour writes one raw hour for kystverket with the given content.
func writeRawHour(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, "NLOD-2.0", "kystverket", "2026", "09", "01", "12.gz")
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Write([]byte(content))
	gz.Close()
	f.Close()
}

// Corrupt framing must stop replay rather than be skipped: history cannot come out shorter than the archive.
func TestReplayRefusesCorruptFraming(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"orphaned continuation", "  an orphaned continuation\n2026-09-01T12:00:00Z\tkystverket\t" + testSentence + "\n", "no record before it"},
		{"no station column", "2026-09-01T12:00:00Z\t" + testSentence + "\n", "no station column"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawHour(t, dir, tc.content)
			rs := allReaders(t, dir)
			if len(rs) != 1 {
				t.Fatalf("readers = %d", len(rs))
			}
			for rs[0].next() {
			}
			if rs[0].err == nil || !strings.Contains(rs[0].err.Error(), tc.want) {
				t.Fatalf("corrupt framing accepted: err = %v", rs[0].err)
			}
		})
	}
}

// A directory replay cannot read is history it would leave out, so the walk must fail.
func TestReplayFailsOnAnUnreadableDirectory(t *testing.T) {
	dir := t.TempDir()
	writeRawHour(t, dir, "2026-09-01T12:00:00Z\tkystverket\t"+testSentence+"\n")
	locked := filepath.Join(dir, "NLOD-2.0", "kystverket", "2026")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := collectReaders(dir, time.Time{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("an unreadable directory produced a partial reader set without an error")
	}
}

// A feeder's offline backlog is archived raw but withheld from the stream. The raw record must
// carry that mark so replay withholds it too.
func TestReplayWithholdsABufferedBacklog(t *testing.T) {
	rawDir, normDir := t.TempDir(), t.TempDir()
	p := testPipeline(t)
	p.arch = newArchive(rawDir, nil)
	p.norm = newNormArchive(normDir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	body := tagBlock(map[byte]string{'c': fmt.Sprint(recv.Add(-2 * replayAge).Unix())}) + testSentence
	p.Ingest(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: body, Buffered: true})
	p.closeArchives()

	out := t.TempDir()
	runReplay([]string{"-archive", rawDir, "-out", out, "-from", "2026-09-01", "-to", "2026-09-02"})
	rep := diffNorm(loadNorm(normDir), loadNorm(out))
	if n := rep.live.eventCount(); n != 0 {
		t.Fatalf("live emitted %d transmissions from a stale backlog, want 0", n)
	}
	if p.stats.replayed.Load() != 1 {
		t.Fatalf("live withheld %d backlog sentences, want 1", p.stats.replayed.Load())
	}
	if !rep.clean() {
		t.Fatalf("replay emitted what live withheld:\n%s", rep.render(3))
	}
}

// A raw hour replay cannot place, or a record it cannot route, is history it would leave out.
func TestReplayRefusesWhatItCannotPlace(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, "NLOD-2.0", "kystverket", "2026", "09", "01", "twelve.gz")
	os.MkdirAll(filepath.Dir(stray), 0o755)
	os.WriteFile(stray, nil, 0o644)
	if _, err := collectReaders(dir, time.Time{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("a raw hour with an unparseable path was skipped")
	}
	p := testPipeline(t)
	if err := dispatch(p, "digitraffic", Reception{Source: "digitraffic", Body: "no-topic-separator"}, nil); err == nil {
		t.Fatal("a digitraffic record without a topic was skipped")
	}
}

// Replay routes a record the way live did, by the transport it arrived on. A UDP datagram of garbage
// that opens with '{' was a parse error live (UDP only takes lines) and stopped a production backfill
// when replay tried it as an AIS-catcher envelope.
func TestReplayRoutesByTransport(t *testing.T) {
	envelope := `{"protocol":"jsonaiscatcher","msgs":[{"class":"AIS","channel":"A","rxtime":"20260901120000","nmea":["` + testSentence + `"]}]}`
	for _, c := range []struct {
		name, source, body string
		events             int
	}{
		{"udp garbage opening with {", "udp:7f2c05cb43eb", "{jR\"\x05%Qa\t!AIVDM,1,1,,B,D028jK1EhN?", 0},
		{"station envelope", "station:x", envelope, 1},
		{"station line that only looks like json", "station:x", "{not an envelope", 0},
		{"udp never parses json", "udp:aaaa", envelope, 0},
	} {
		p := testPipeline(t)
		sub := p.subscribe()
		if err := dispatch(p, c.source, Reception{Source: c.source, Station: c.source, RecvTime: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Body: c.body}, nil); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(sub.ch) != c.events {
			t.Fatalf("%s: %d events, want %d", c.name, len(sub.ch), c.events)
		}
	}
}

// A trusted report 45 minutes before -from corroborates a low-trust report just after it, live, while
// the vessel stays cached. The
// default lead-in has to replay far enough back to see it, or replay writes the same event
// uncorroborated.
func TestReplayLeadInCoversCorroboration(t *testing.T) {
	dir := t.TempDir()
	from := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	write := func(rel string, recv time.Time, station string) {
		path := filepath.Join(dir, rel, recv.Format("2006/01/02/15")+".gz")
		os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		gz.Write([]byte(recv.Format(time.RFC3339Nano) + "\t" + station + "\t" + testSentence + "\n"))
		gz.Close()
		f.Close()
	}
	write("NLOD-2.0/kystverket", from.Add(-45*time.Minute), "kystverket")
	// the same sender 20 minutes before -from keeps the vessel cached, so live still holds the trusted
	// report when the one after -from arrives; a 30-minute lead-in sees this and not the trusted one
	write("CC0-1.0/udp/aaaa", from.Add(-20*time.Minute), "udp:aaaa")
	write("CC0-1.0/udp/aaaa", from.Add(5*time.Minute), "udp:aaaa")

	out := t.TempDir()
	runReplay([]string{"-archive", dir, "-out", out, "-from", "2026-09-02", "-to", "2026-09-03"})
	events := 0
	for _, e := range readNorm(t, out) {
		if e.K == "event" {
			events++
			if e.Uncorroborated {
				t.Fatal("the lead-in missed the trusted report 45 minutes before -from; replay wrote the event uncorroborated")
			}
		}
	}
	if events != 1 {
		t.Fatalf("events = %d, want the one low-trust report after -from", events)
	}
}
