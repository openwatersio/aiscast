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
	if err := dispatch(p, "station:x", Reception{Source: "station:x", Body: `{"msgs": [`}, nil); err == nil {
		t.Fatal("a catcher envelope that does not parse was skipped")
	}
}
