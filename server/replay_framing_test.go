package main

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The raw writer keeps a body's own newlines, so a valid pretty-printed envelope from any source
// spans lines. Replay must reassemble it and reproduce what live ingested, not only for digitraffic.
func TestReplayReassemblesMultiLineBodiesFromAnySource(t *testing.T) {
	rawDir := t.TempDir()
	p, written := recordingPipeline(t)
	p.arch = newArchive(rawDir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	body := "{\n  \"protocol\": \"jsonaiscatcher\",\n  \"msgs\": [\n    {\"class\": \"AIS\", \"channel\": \"A\", \"rxtime\": \"20260901120039\", \"nmea\": [\"" + testSentence + "\"]}\n  ]\n}"
	if !p.ingestCatcher("http:pretty", []byte(body), recv) {
		t.Fatal("live rejected a valid envelope")
	}
	p.closeArchives()

	live := written()
	if len(live) != 1 {
		t.Fatalf("live wrote %d copies, want 1", len(live))
	}
	sameCopies(t, "replay lost the multi-line reception", live, replayRaw(t, rawDir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
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
	rawDir := t.TempDir()
	p, written := recordingPipeline(t)
	p.arch = newArchive(rawDir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	body := tagBlock(map[byte]string{'c': fmt.Sprint(recv.Add(-2 * replayAge).Unix())}) + testSentence
	p.Ingest(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: body, Buffered: true})
	p.closeArchives()

	if p.stats.replayed.Load() != 1 {
		t.Fatalf("live withheld %d backlog sentences, want 1", p.stats.replayed.Load())
	}
	sameCopies(t, "replay wrote what live withheld", written(), replayRaw(t, rawDir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
}

// /v1 publish lines and /v1/receive envelopes archive under the same station:<sub> source, so a
// published line whose body is a valid AIS-catcher envelope must round-trip its mark: live fed it
// to the NMEA parser and produced nothing, and replay must not unpack it into events.
func TestReplayKeepsAPublishedEnvelopeALine(t *testing.T) {
	rawDir := t.TempDir()
	p, written := recordingPipeline(t)
	p.arch = newArchive(rawDir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	envelope := `{"protocol":"jsonaiscatcher","msgs":[{"class":"AIS","channel":"A","rxtime":"20260901120039","nmea":["` + testSentence + `"]}]}`
	p.Ingest(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: envelope, Published: true})
	if p.stats.parseErr.Load() != 1 {
		t.Fatalf("live parse errors = %d, want 1 (a publish line goes through the NMEA parser)", p.stats.parseErr.Load())
	}
	p.closeArchives()

	rs := allReaders(t, rawDir)
	if len(rs) != 1 || !rs[0].next() {
		t.Fatal("the raw archive did not round-trip the record")
	}
	if rx := rs[0].cur; !rx.Published || rx.Station != "station:boat" {
		t.Fatalf("round-tripped record: Published = %v, Station = %q", rx.Published, rx.Station)
	}

	if live := written(); len(live) != 0 {
		t.Fatalf("live wrote %d copies from a published line, want 0", len(live))
	}
	sameCopies(t, "replay unpacked a published line as an envelope", nil, replayRaw(t, rawDir, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
}

// Both marks apply to one record when a publisher replays its offline backlog. The writer emits
// them in a fixed order and the reader strips both.
func TestRawArchiveRoundTripsBothMarks(t *testing.T) {
	dir := t.TempDir()
	a := newArchive(dir, nil)
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	a.write(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: testSentence, Published: true, Buffered: true})
	a.shutdown()
	rs := allReaders(t, dir)
	if len(rs) != 1 || !rs[0].next() {
		t.Fatal("the raw archive did not round-trip the record")
	}
	rx := rs[0].cur
	if rx.Station != "station:boat" || !rx.Published || !rx.Buffered {
		t.Fatalf("Station = %q, Published = %v, Buffered = %v; want the bare station with both marks set", rx.Station, rx.Published, rx.Buffered)
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
	if err := dispatch(p, "digitraffic", Reception{Source: "digitraffic", Body: "no-topic-separator"}); err == nil {
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
		events, parseErrs  int64
	}{
		{"udp garbage opening with {", "udp:7f2c05cb43eb", "{jR\"\x05%Qa\t!AIVDM,1,1,,B,D028jK1EhN?", 0, 1},
		{"station envelope", "station:x", envelope, 1, 0},
		{"station line that only looks like json", "station:x", "{not an envelope", 0, 1},
		{"udp never parses json", "udp:aaaa", envelope, 0, 1},
	} {
		p := testPipeline(t)
		sub := p.subscribe()
		if err := dispatch(p, c.source, Reception{Source: c.source, Station: c.source, RecvTime: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Body: c.body}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if int64(len(sub.ch)) != c.events || p.stats.parseErr.Load() != c.parseErrs {
			t.Fatalf("%s: %d events and %d parse errors, want %d and %d (a line goes through the NMEA parser)",
				c.name, len(sub.ch), p.stats.parseErr.Load(), c.events, c.parseErrs)
		}
	}
	// http: is /v1/receive alone, so an envelope there that does not parse is corrupt
	if err := dispatch(testPipeline(t), "http:x", Reception{Source: "http:x", Body: `{"msgs": [`}); err == nil {
		t.Fatal("a corrupt envelope under http: was replayed as a line")
	}
	// the published mark means /v1 publish, which only ever took lines, so even a valid envelope stays one
	pp := testPipeline(t)
	psub := pp.subscribe()
	if err := dispatch(pp, "station:x", Reception{Source: "station:x", Station: "station:x", Body: envelope, Published: true}); err != nil {
		t.Fatal(err)
	}
	if len(psub.ch) != 0 || pp.stats.parseErr.Load() != 1 {
		t.Fatalf("published envelope: %d events and %d parse errors, want 0 and 1", len(psub.ch), pp.stats.parseErr.Load())
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

	pts := replayRaw(t, dir, from)
	if len(pts) != 1 {
		t.Fatalf("replay wrote %d copies, want the one low-trust report after the gate", len(pts))
	}
	if pts[0].uncorroborated {
		t.Fatal("the lead-in missed the trusted report 45 minutes before the gate; replay wrote the copy uncorroborated")
	}
}

// A buffered line's TAG time between maxSkew and replayAge stamps the event: falling back to the
// receive time would compress a flushed backlog's movement into the seconds of the flush. A live
// line the same age off the clock is still distrusted.
func TestBufferedBacklogKeepsTagTime(t *testing.T) {
	recv := time.Date(2026, 9, 1, 12, 0, 40, 0, time.UTC)
	st := recv.Add(-45 * time.Second)
	body := tagBlock(map[byte]string{'c': fmt.Sprint(st.Unix())}) + testSentence

	p := testPipeline(t)
	p.Ingest(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: body, Buffered: true})
	if v := p.vessels[227006760]; v == nil || !v.PosAt.Equal(st) {
		t.Fatalf("buffered backlog stamped %+v, want the TAG time %v", v, st)
	}

	p = testPipeline(t)
	p.Ingest(Reception{Source: "station:boat", Station: "station:boat", RecvTime: recv, Body: body})
	if v := p.vessels[227006760]; v == nil || !v.PosAt.Equal(recv) {
		t.Fatalf("live line beyond maxSkew stamped %+v, want the receive time %v", v, recv)
	}
}

// replayRaw replays the raw tree under dir for day, after the default lead-in, the way replay -clickhouse does, and
// returns the copies it writes.
func replayRaw(t *testing.T, dir string, day time.Time) []trackPoint {
	t.Helper()
	p, written := recordingPipeline(t)
	p.replayGate = day
	readers, err := collectReaders(dir, day.Add(-(corroborationWindow + vesselTTL)), day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayReaders(p, readers, day.AddDate(0, 0, 1), nil); err != nil {
		t.Fatal(err)
	}
	return written()
}

// sameCopies fails unless live and replayed wrote the same copies: vessel, stamp, arrival, source, and whether each
// was the accepted one.
func sameCopies(t *testing.T, what string, live, replayed []trackPoint) {
	t.Helper()
	key := func(pts []trackPoint) []string {
		var out []string
		for _, pt := range pts {
			out = append(out, fmt.Sprint(pt.mmsi, pt.ts.UnixMilli(), pt.recv.UnixMilli(), pt.source, pt.dup, pt.uncorroborated))
		}
		slices.Sort(out)
		return out
	}
	if a, b := key(live), key(replayed); !slices.Equal(a, b) {
		t.Fatalf("%s: live wrote %v, replay %v", what, a, b)
	}
}
