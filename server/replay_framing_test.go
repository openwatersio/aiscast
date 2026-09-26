package main

import (
	"compress/gzip"
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
	p.arch.blocking = true
	p.norm = newNormArchive(normDir, nil)
	p.norm.blocking = true
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	body := "{\n  \"protocol\": \"jsonaiscatcher\",\n  \"msgs\": [\n    {\"class\": \"AIS\", \"channel\": \"A\", \"rxtime\": \"20260901120039\", \"nmea\": [\"" + testSentence + "\"]}\n  ]\n}"
	if !p.ingestCatcher("http:pretty", []byte(body), recv) {
		t.Fatal("live rejected a valid envelope")
	}
	p.closeArchives()

	out := t.TempDir()
	runReplay([]string{"-archive", rawDir, "-out", out, "-from", "2026-09-01", "-to", "2026-09-02"})
	rep := diffNorm(loadNorm(normDir), loadNorm(out))
	if n := total(rep.live.eventN); n != 1 {
		t.Fatalf("live produced %d transmissions, want 1", n)
	}
	if !rep.clean() {
		t.Fatalf("replay lost the multi-line reception:\n%s", rep.render(3))
	}
}

// A continuation line with no record header before it is corrupt framing: replay must refuse it
// rather than skip it and come out shorter than the archive.
func TestReplayRefusesALineWithNoRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "NLOD-2.0", "kystverket", "2026", "09", "01", "12.gz")
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Write([]byte("  an orphaned continuation\n2026-09-01T12:00:00Z\tkystverket\t" + testSentence + "\n"))
	gz.Close()
	f.Close()

	rs := collectReaders(dir, time.Time{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(rs) != 1 {
		t.Fatalf("readers = %d", len(rs))
	}
	if rs[0].next() || rs[0].err == nil || !strings.Contains(rs[0].err.Error(), "no record before it") {
		t.Fatalf("corrupt framing accepted: err = %v", rs[0].err)
	}
}
