package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// normTree writes a normalized tree from raw lines, as the writer would have.
func normTree(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v1", "2026", "09", "01", "12.gz")
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	for _, l := range lines {
		gz.Write([]byte(l + "\n"))
	}
	gz.Close()
	f.Close()
	return dir
}

// liveAndReplay produces two normalized trees from the same fixture raw day: one by replay, and
// one by feeding the same receptions through the pipeline in the order they were written.
func liveAndReplay(t *testing.T) (live, replay string) {
	t.Helper()
	raw := writeRawTree(t)
	replay = t.TempDir()
	runReplay([]string{"-archive", raw, "-out", replay, "-from", "2026-09-01", "-to", "2026-09-02"})

	live = t.TempDir()
	p := testPipeline(t)
	p.norm = newNormArchive(live, nil)
	p.norm.blocking = true
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	for _, r := range collectReaders(raw, time.Time{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		for r.next() {
			dispatch(p, r.source, r.cur, st)
		}
	}
	p.norm.shutdown()
	return live, replay
}

func TestNormDiffCleanOnFaithfulReplay(t *testing.T) {
	live, replay := liveAndReplay(t)
	rep := diffNorm(loadNorm(live), loadNorm(replay))
	if !rep.clean() {
		t.Fatalf("faithful replay reported divergence:\n%s", rep.render(5))
	}
	if len(rep.live.events) == 0 || len(rep.live.copies) == 0 || len(rep.live.weather) == 0 {
		t.Fatalf("comparison covered nothing: %s", rep.render(0))
	}
}

func TestNormDiffCatchesEachDivergence(t *testing.T) {
	live, _ := liveAndReplay(t)
	base := loadNorm(live)
	var someEvent, someCopy, someWeather string
	for k := range base.events {
		someEvent = k
		break
	}
	for k := range base.copies {
		someCopy = k
		break
	}
	for k := range base.weather {
		someWeather = k
		break
	}

	cases := []struct {
		name   string
		break_ func(s *normSide)
		want   string
	}{
		{"missing transmission", func(s *normSide) { delete(s.events, someEvent) }, "transmissions only live"},
		{"changed decode", func(s *normSide) { s.events[someEvent] = `{"m":"tampered"}` }, "transmissions decoded differently"},
		{"missing copy", func(s *normSide) { delete(s.copies, someCopy) }, "copies only live"},
		{"extra copy", func(s *normSide) { s.copies["ffff\t2026-09-01T12:00:00Z\tghost\tghost"] = true }, "copies only replay"},
		{"missing weather", func(s *normSide) { delete(s.weather, someWeather) }, "weather only live"},
		{"changed weather", func(s *normSide) { s.weather[someWeather] = `{"tampered":true}` }, "weather differing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, r := loadNorm(live), loadNorm(live)
			c.break_(r)
			rep := diffNorm(l, r)
			if rep.clean() {
				t.Fatalf("%s went unreported", c.name)
			}
			if out := rep.render(2); !strings.Contains(out, c.want) {
				t.Fatalf("report does not name %q:\n%s", c.want, out)
			}
		})
	}
}

// A first-copy race is a different source winning the same transmission: reported as expected,
// never as a divergence.
func TestNormDiffTreatsFirstCopyRaceAsExpected(t *testing.T) {
	live, _ := liveAndReplay(t)
	l, r := loadNorm(live), loadNorm(live)
	var k string
	for key := range r.sources {
		k = key
		break
	}
	r.sources[k] = "some-other-source"
	rep := diffNorm(l, r)
	if !rep.clean() {
		t.Fatalf("a first-copy race was reported as divergence:\n%s", rep.render(3))
	}
	if rep.firstCopyRaces != 1 {
		t.Fatalf("races = %d, want 1", rep.firstCopyRaces)
	}
}

// The multipart sequence id comes from a per-process counter, so live and replay disagree on it
// while carrying identical payloads. It must not read as divergence, and the rest of the sentence
// must still be compared.
func TestNormDiffToleratesSequenceIDButNotPayload(t *testing.T) {
	seq1 := []string{"!AIVDM,2,1,1,A,54`V0cP2CNtt,0*76", "!AIVDM,2,2,1,A,DjCP0000000,2*18"}
	seq2 := []string{"!AIVDM,2,1,7,A,54`V0cP2CNtt,0*70", "!AIVDM,2,2,7,A,DjCP0000000,2*1E"}
	if rawNMEA(canonNMEA(seq1)) != rawNMEA(canonNMEA(seq2)) {
		t.Fatalf("sequence ids not normalized:\n %v\n %v", canonNMEA(seq1), canonNMEA(seq2))
	}
	tampered := []string{"!AIVDM,2,1,1,A,DIFFERENTPAYLOAD,0*76", "!AIVDM,2,2,1,A,DjCP0000000,2*18"}
	if rawNMEA(canonNMEA(seq1)) == rawNMEA(canonNMEA(tampered)) {
		t.Fatal("a changed payload survived normalization")
	}
	chanSwap := []string{"!AIVDM,2,1,1,B,54`V0cP2CNtt,0*76", "!AIVDM,2,2,1,A,DjCP0000000,2*18"}
	if rawNMEA(canonNMEA(seq1)) == rawNMEA(canonNMEA(chanSwap)) {
		t.Fatal("a changed channel survived normalization")
	}
}
