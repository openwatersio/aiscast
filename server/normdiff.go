package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// aiscast normdiff: compare a normalized tree written live against one produced by replaying the
// same days from raw. This is the evidence for the dual-write confidence phase, and the check that
// a decoder change did not move history. Reads local trees only.
//
// Live and replayed output are not expected to be byte-identical, and the differences that are
// acceptable are known in advance. Two copies of one transmission arriving microseconds apart are
// ordered by goroutine scheduling live and by the archived receive-time merge in replay, so which
// copy is "first" (and therefore which source the event record names) can differ. Everything else
// is a real divergence: a message present on one side only, a decoded field that changed, a copy
// that appeared or vanished.
func runNormDiff(args []string) {
	fset := flag.NewFlagSet("normdiff", flag.ExitOnError)
	liveDir := fset.String("live", "", "normalized tree written live (required)")
	replayDir := fset.String("replay", "", "normalized tree produced by replay (required)")
	sample := fset.Int("examples", 5, "divergences of each kind to print")
	fset.Parse(args)
	if *liveDir == "" || *replayDir == "" {
		log.Fatalf("normdiff: -live and -replay are required")
	}
	live, replay := loadNorm(*liveDir), loadNorm(*replayDir)
	rep := diffNorm(live, replay)
	fmt.Print(rep.render(*sample))
	if !rep.clean() {
		os.Exit(1)
	}
}

// canonNMEA blanks the multipart sequence id and the checksum that depends on it. A re-encoded
// message takes its sequence id from a process-local counter, so a long-running server and a
// replay run disagree on it while carrying identical payloads; the id groups fragments for
// reassembly and is not data. Everything else in the sentence stays under comparison.
func canonNMEA(lines []string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if body, _, ok := strings.Cut(l, "*"); ok {
			l = body
		}
		if f := strings.Split(l, ","); len(f) > 4 && strings.HasSuffix(f[0], "VDM") {
			f[3] = ""
			l = strings.Join(f, ",")
		}
		out[i] = l
	}
	return out
}

func rawNMEA(lines []string) string { return strings.Join(lines, "|") }

// normSide is one tree reduced to what the comparison is about: transmissions by id and time,
// copies, and weather by station and time. Every map counts occurrences, so a record written twice on
// one side and once on the other is a difference, not a collapse.
type normSide struct {
	events   map[string]map[string]int // id \t canonical time -> each compared payload, counted
	eventN   map[string]int
	sources  map[string]string         // id \t canonical time -> source that won the race (informational)
	copies   map[string]int            // id \t canonical time \t source \t station \t license
	weather  map[string]map[string]int // mmsi \t msgtime -> each record, counted
	weatherN map[string]int
	files    int
	lines    int

	resequenced int // multipart sentences whose sequence id was normalized away
}

// loadNorm reads a tree or stops the run: normdiff is the check the rollout trusts, so a record it
// cannot read must fail the comparison, never quietly leave it.
func loadNorm(dir string) *normSide {
	s, err := readNormTree(dir)
	if err != nil {
		log.Fatalf("normdiff: %v", err)
	}
	return s
}

func readNormTree(dir string) (*normSide, error) {
	s := &normSide{events: map[string]map[string]int{}, eventN: map[string]int{}, sources: map[string]string{},
		copies: map[string]int{}, weather: map[string]map[string]int{}, weatherN: map[string]int{}}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gz") {
			return err
		}
		s.files++
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		defer gz.Close()
		dec := json.NewDecoder(gz)
		for n := 1; ; n++ {
			var e normEnvelope
			if err := dec.Decode(&e); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return fmt.Errorf("%s: record %d: %w", path, n, err)
			}
			s.lines++
			if err := s.add(e); err != nil {
				return fmt.Errorf("%s: record %d: %w", path, n, err)
			}
		}
	})
	return s, err
}

func (s *normSide) add(e normEnvelope) error {
	if e.V != normVersion {
		return fmt.Errorf("envelope version %d; normdiff speaks v%d", e.V, normVersion)
	}
	switch e.K {
	case "event":
		// v1Event carries Message as an ais.Packet interface, which cannot be unmarshalled into;
		// the comparison wants the decoded message as bytes anyway.
		var ev struct {
			ID          string          `json:"id"`
			Time        time.Time       `json:"time"`
			Source      string          `json:"source"`
			Channel     string          `json:"channel"`
			MMSI        uint32          `json:"mmsi"`
			MsgType     string          `json:"msg_type"`
			Lat         *float64        `json:"lat"`
			Lon         *float64        `json:"lon"`
			NMEA        []string        `json:"nmea"`
			Message     json.RawMessage `json:"message"`
			Synthesized bool            `json:"synthesized"`
		}
		if err := json.Unmarshal(e.R, &ev); err != nil {
			return fmt.Errorf("event record: %w", err)
		}
		if ev.ID == "" || ev.Time.IsZero() {
			return errors.New("event record without an id and time")
		}
		k := ev.ID + "\t" + ev.Time.UTC().Format(time.RFC3339Nano)
		// Every field of the event is under comparison, and the flags ride along because withholding
		// an event from the stream is a decision replay must reproduce. Source, station, license, and
		// attribution are deliberately excluded: they name the first copy, and that is the race.
		body, _ := json.Marshal(struct {
			Msg         json.RawMessage `json:"m"`
			NMEA        []string        `json:"n,omitempty"`
			Type        string          `json:"t"`
			MMSI        uint32          `json:"mmsi"`
			Channel     string          `json:"c"`
			Lat         *float64        `json:"lat"`
			Lon         *float64        `json:"lon"`
			Synthesized bool            `json:"syn,omitempty"`
			Implausible bool            `json:"i,omitempty"`
			Stale       bool            `json:"s,omitempty"`
		}{ev.Message, canonNMEA(ev.NMEA), ev.MsgType, ev.MMSI, ev.Channel, ev.Lat, ev.Lon, ev.Synthesized, e.Implausible, e.Stale})
		count(s.events, k, string(body))
		s.eventN[k]++
		s.sources[k] = ev.Source
		if rawNMEA(ev.NMEA) != rawNMEA(canonNMEA(ev.NMEA)) {
			s.resequenced++
		}
	case "copy":
		var c normCopy
		if err := json.Unmarshal(e.R, &c); err != nil {
			return fmt.Errorf("copy record: %w", err)
		}
		if c.ID == "" || c.Time == "" || c.Source == "" {
			return errors.New("copy record without an id, time, and source")
		}
		s.copies[c.ID+"\t"+c.Time+"\t"+c.Source+"\t"+c.Station+"\t"+c.License]++
	case "methyd":
		var w struct {
			MMSI    int    `json:"mmsi"`
			Msgtime string `json:"msgtime"`
		}
		if err := json.Unmarshal(e.R, &w); err != nil {
			return fmt.Errorf("weather record: %w", err)
		}
		if w.MMSI == 0 || w.Msgtime == "" {
			return errors.New("weather record without an mmsi and msgtime")
		}
		k := fmt.Sprintf("%d\t%s", w.MMSI, w.Msgtime)
		count(s.weather, k, string(e.R))
		s.weatherN[k]++
	default:
		return fmt.Errorf("unknown record kind %q", e.K)
	}
	return nil
}

// count adds one occurrence of v under k. A key can legitimately repeat (a crash-window re-accept),
// and each occurrence's payload must be compared, not only the last one written.
func count(m map[string]map[string]int, k, v string) {
	if m[k] == nil {
		m[k] = map[string]int{}
	}
	m[k][v]++
}

func total(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

type normReport struct {
	live, replay      *normSide
	eventsOnlyLive    []string
	eventsOnlyReplay  []string
	eventsDiffer      []string
	copiesOnlyLive    []string
	copiesOnlyReplay  []string
	weatherOnlyLive   []string
	weatherOnlyReplay []string
	weatherDiffer     []string
	firstCopyRaces    int // same transmission, different winning source: expected, not a defect
}

func diffNorm(live, replay *normSide) *normReport {
	r := &normReport{live: live, replay: replay}
	// Each instance one side has beyond the other is its own difference.
	for k, ln := range live.eventN {
		rn := replay.eventN[k]
		for i := rn; i < ln; i++ {
			r.eventsOnlyLive = append(r.eventsOnlyLive, k)
		}
		switch {
		case rn == 0:
		case !maps.Equal(live.events[k], replay.events[k]):
			r.eventsDiffer = append(r.eventsDiffer, k)
		case live.sources[k] != replay.sources[k]:
			r.firstCopyRaces++
		}
	}
	for k, rn := range replay.eventN {
		for i := live.eventN[k]; i < rn; i++ {
			r.eventsOnlyReplay = append(r.eventsOnlyReplay, k)
		}
	}
	for k, ln := range live.copies {
		for i := replay.copies[k]; i < ln; i++ {
			r.copiesOnlyLive = append(r.copiesOnlyLive, k)
		}
	}
	for k, rn := range replay.copies {
		for i := live.copies[k]; i < rn; i++ {
			r.copiesOnlyReplay = append(r.copiesOnlyReplay, k)
		}
	}
	for k, ln := range live.weatherN {
		rn := replay.weatherN[k]
		for i := rn; i < ln; i++ {
			r.weatherOnlyLive = append(r.weatherOnlyLive, k)
		}
		if rn > 0 && !maps.Equal(live.weather[k], replay.weather[k]) {
			r.weatherDiffer = append(r.weatherDiffer, k)
		}
	}
	for k, rn := range replay.weatherN {
		for i := live.weatherN[k]; i < rn; i++ {
			r.weatherOnlyReplay = append(r.weatherOnlyReplay, k)
		}
	}
	return r
}

// clean reports whether every difference found is one of the known-acceptable kinds.
func (r *normReport) clean() bool {
	return len(r.eventsOnlyLive) == 0 && len(r.eventsOnlyReplay) == 0 && len(r.eventsDiffer) == 0 &&
		len(r.copiesOnlyLive) == 0 && len(r.copiesOnlyReplay) == 0 &&
		len(r.weatherOnlyLive) == 0 && len(r.weatherOnlyReplay) == 0 && len(r.weatherDiffer) == 0
}

func (r *normReport) render(sample int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "live:   %d files, %d records, %d transmissions, %d copies, %d weather\n",
		r.live.files, r.live.lines, total(r.live.eventN), total(r.live.copies), total(r.live.weatherN))
	fmt.Fprintf(&b, "replay: %d files, %d records, %d transmissions, %d copies, %d weather\n",
		r.replay.files, r.replay.lines, total(r.replay.eventN), total(r.replay.copies), total(r.replay.weatherN))
	fmt.Fprintf(&b, "first-copy races: %d (expected: concurrent copies have no reproducible arrival order)\n", r.firstCopyRaces)
	fmt.Fprintf(&b, "multipart sentences resequenced: live %d, replay %d (expected: the sequence id comes from a per-process counter)\n",
		r.live.resequenced, r.replay.resequenced)
	for _, g := range []struct {
		name string
		keys []string
	}{
		{"transmissions only live", r.eventsOnlyLive},
		{"transmissions only replay", r.eventsOnlyReplay},
		{"transmissions decoded differently", r.eventsDiffer},
		{"copies only live", r.copiesOnlyLive},
		{"copies only replay", r.copiesOnlyReplay},
		{"weather only live", r.weatherOnlyLive},
		{"weather only replay", r.weatherOnlyReplay},
		{"weather differing", r.weatherDiffer},
	} {
		if len(g.keys) == 0 {
			continue
		}
		sort.Strings(g.keys)
		fmt.Fprintf(&b, "%s: %d\n", g.name, len(g.keys))
		for i, k := range g.keys {
			if i == sample {
				break
			}
			fmt.Fprintf(&b, "    %s\n", strings.ReplaceAll(k, "\t", "  "))
		}
	}
	if r.clean() {
		b.WriteString("clean: replay reproduces live apart from first-copy attribution\n")
	}
	return b.String()
}
