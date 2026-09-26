package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// normTree writes a normalized tree from raw lines, as the writer would have.
func normTree(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, normPrefix, "v1", "2026", "09", "01", "12.gz")
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
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	for _, r := range allReaders(t, raw) {
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
		{"missing transmission", func(s *normSide) { delete(s.eventN, someEvent) }, "transmissions only live"},
		{"changed decode", func(s *normSide) { s.events[someEvent] = map[string]int{`{"m":"tampered"}`: 1} }, "transmissions decoded differently"},
		{"missing copy", func(s *normSide) { delete(s.copies, someCopy) }, "copies only live"},
		{"extra copy", func(s *normSide) { s.copies["ffff\t2026-09-01T12:00:00Z\tghost\tghost"] = 1 }, "copies only replay"},
		{"missing weather", func(s *normSide) { delete(s.weatherN, someWeather) }, "weather only live"},
		{"transmission recorded twice", func(s *normSide) { s.eventN[someEvent]++ }, "transmissions only replay"},
		{"copy recorded twice", func(s *normSide) { s.copies[someCopy]++ }, "copies only replay"},
		{"changed weather", func(s *normSide) { s.weather[someWeather] = map[string]int{`{"tampered":true}`: 1} }, "weather differing"},
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

// An envelope normdiff cannot read must stop the comparison: dropping it would let a record present
// on one side only disappear and the diff come back clean.
func TestNormDiffRefusesUnreadableRecords(t *testing.T) {
	cases := map[string]string{
		"unknown version":    `{"k":"copy","v":99,"t":"2026-09-01T12:00:00Z","r":{"id":"a","time":"2026-09-01T12:00:00Z","source":"s"}}`,
		"unknown kind":       `{"k":"mystery","v":1,"t":"2026-09-01T12:00:00Z","r":{}}`,
		"malformed record":   `{"k":"event","v":1,"t":"2026-09-01T12:00:00Z","r":{"id":7}}`,
		"missing identity":   `{"k":"copy","v":1,"t":"2026-09-01T12:00:00Z","r":{"source":"s"}}`,
		"missing weather id": `{"k":"methyd","v":1,"t":"2026-09-01T12:00:00Z","r":{"airTemperature":4.5}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			dir := normTree(t, []string{line})
			if _, err := readNormTree(dir); err == nil || !strings.Contains(err.Error(), "record 1") {
				t.Fatalf("want an error naming the record, got %v", err)
			}
		})
	}
}

// Differences the per-key tamper cases above cannot reach: a repeated key whose earlier payload
// differs, fields of the event beyond the decoded message, and the license on a copy.
func TestNormDiffSeesEveryRecordAndField(t *testing.T) {
	ev := func(extra string) string {
		return `{"k":"event","v":1,"t":"2026-09-01T12:00:00Z","r":{"id":"a","time":"2026-09-01T12:00:00Z","source":"s","mmsi":1,"msg_type":"X","message":{"a":1}` + extra + `}}`
	}
	cp := func(license string) string {
		return `{"k":"copy","v":1,"t":"2026-09-01T12:00:00Z","r":{"id":"a","time":"2026-09-01T12:00:00Z","source":"s","station":"s","license":"` + license + `"}}`
	}
	cases := []struct {
		name         string
		live, replay []string
	}{
		{"earlier repeat differs", []string{ev(`,"lat":1`), ev(`,"lat":2`)}, []string{ev(`,"lat":2`), ev(`,"lat":2`)}},
		{"position differs", []string{ev(`,"lat":1,"lon":1`)}, []string{ev(`,"lat":1,"lon":3`)}},
		{"synthesized differs", []string{ev(``)}, []string{ev(`,"synthesized":true`)}},
		{"license differs", []string{cp("CC0-1.0")}, []string{cp("NLOD-2.0")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := diffNorm(loadNorm(normTree(t, c.live)), loadNorm(normTree(t, c.replay)))
			if rep.clean() {
				t.Fatalf("%s went unreported:\n%s", c.name, rep.render(3))
			}
		})
	}
}
