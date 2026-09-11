package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

const testSentence = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"

// readNorm parses every envelope under a normalized output tree, in file order.
func readNorm(t *testing.T, dir string) []normEnvelope {
	t.Helper()
	var out []normEnvelope
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gz") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(gz)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var e normEnvelope
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("bad envelope %q: %v", line, err)
			}
			out = append(out, e)
		}
		return nil
	})
	return out
}

func kinds(envs []normEnvelope) map[string]int {
	m := map[string]int{}
	for _, e := range envs {
		m[e.K]++
	}
	return m
}

func normPipeline(t *testing.T) (*Pipeline, string) {
	t.Helper()
	dir := t.TempDir()
	p := testPipeline(t)
	p.norm = newNormArchive(dir, nil)
	p.norm.blocking = true // a test must never lose a record to the queue
	return p, dir
}

func TestNormalizedStream(t *testing.T) {
	p, dir := normPipeline(t)
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: recv, Body: testSentence})
	p.ingestLine(Reception{Source: "udp:aaaa", Station: "udp:aaaa", RecvTime: recv.Add(time.Second), Body: testSentence})
	p.writeMetHyd([]byte(`{"type":"BinaryBroadcastMessageMetHyd","mmsi":2573775,"airTemperature":4.5}`), recv.Add(2*time.Second))
	p.norm.shutdown()

	envs := readNorm(t, dir)
	if got := kinds(envs); got["event"] != 1 || got["copy"] != 2 || got["methyd"] != 1 {
		t.Fatalf("kinds = %v, want 1 event, 2 copies, 1 methyd", got)
	}
	var ev v1Event
	var copies []normCopy
	for _, e := range envs {
		switch e.K {
		case "event":
			json.Unmarshal(e.R, &ev)
		case "copy":
			var c normCopy
			json.Unmarshal(e.R, &c)
			copies = append(copies, c)
		}
		if e.V != normVersion {
			t.Fatalf("envelope version %d, want %d", e.V, normVersion)
		}
	}
	if ev.ID == "" || ev.MMSI == 0 || len(ev.NMEA) == 0 {
		t.Fatalf("event record incomplete: %+v", ev)
	}
	for _, c := range copies {
		if c.ID != ev.ID {
			t.Fatalf("copy id %s != event id %s", c.ID, ev.ID)
		}
	}
	if copies[0].License != "NLOD-2.0" || copies[1].License != "CC0-1.0" {
		t.Fatalf("licenses = %s, %s", copies[0].License, copies[1].License)
	}
	if copies[1].Source != "udp:aaaa" {
		t.Fatalf("dup copy source = %s", copies[1].Source)
	}
}

func TestDedupePersistsAcrossRestart(t *testing.T) {
	p1, dir1 := normPipeline(t)
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	p1.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: recv, Body: testSentence})
	state := filepath.Join(t.TempDir(), "dedupe.json")
	if err := p1.saveDedupe(state); err != nil {
		t.Fatal(err)
	}
	p1.norm.shutdown()

	p2, dir2 := normPipeline(t)
	if n, err := p2.loadDedupe(state); err != nil || n != 1 {
		t.Fatalf("loadDedupe = %d, %v", n, err)
	}
	p2.ingestLine(Reception{Source: "udp:bbbb", Station: "udp:bbbb", RecvTime: recv.Add(2 * time.Second), Body: testSentence})
	p2.norm.shutdown()

	if got := kinds(readNorm(t, dir1)); got["event"] != 1 {
		t.Fatalf("first run kinds = %v", got)
	}
	if got := kinds(readNorm(t, dir2)); got["event"] != 0 || got["copy"] != 1 {
		t.Fatalf("post-restart copy re-accepted: kinds = %v, want copy only", got)
	}
}

func TestNormGateHoldsWarmupBack(t *testing.T) {
	p, dir := normPipeline(t)
	gate := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	p.normGate = gate
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: gate.Add(-time.Minute), Body: testSentence})
	p.ingestLine(Reception{Source: "kystverket", Station: "kystverket", RecvTime: gate.Add(time.Minute), Body: testSentence})
	p.norm.shutdown()
	envs := readNorm(t, dir)
	// the warm-up reception built dedupe state silently; the in-range one is its duplicate... but
	// 61 s apart is outside the window, so it is a fresh accept. Only the in-range record appears.
	if got := kinds(envs); got["event"] != 1 || got["copy"] != 1 {
		t.Fatalf("kinds = %v, want exactly the in-range event", got)
	}
	for _, e := range envs {
		ts, err := time.Parse(time.RFC3339Nano, e.T)
		if err != nil || ts.Before(gate) {
			t.Fatalf("record before the gate leaked out: %s", e.T)
		}
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

func TestReplayTwiceIsIdentical(t *testing.T) {
	raw := writeRawTree(t)
	out1, out2 := t.TempDir(), t.TempDir()
	runReplay([]string{"-archive", raw, "-out", out1, "-from", "2026-09-01", "-to", "2026-09-02"})
	runReplay([]string{"-archive", raw, "-out", out2, "-from", "2026-09-01", "-to", "2026-09-02"})
	h1, h2 := hashTree(t, out1), hashTree(t, out2)
	if h1 != h2 {
		t.Fatalf("replay output differs between runs: %s vs %s", h1, h2)
	}
	envs := readNorm(t, out1)
	got := kinds(envs)
	if got["event"] == 0 || got["copy"] < got["event"] || got["methyd"] != 1 {
		t.Fatalf("replay kinds = %v", got)
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

func hashTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(gz)
		h.Write([]byte(rel))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
