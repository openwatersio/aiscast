package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
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

// normSide is one tree reduced to what the comparison is about: messages by id and time, the set
// of copies per transmission, and weather by station and time.
type normSide struct {
	events  map[string]string // id \t canonical time -> canonical JSON of the decoded message
	sources map[string]string // id \t canonical time -> source that won the race (informational)
	copies  map[string]bool   // id \t canonical time \t source \t station
	weather map[string]string // mmsi \t msgtime -> canonical JSON of the record
	files   int
	lines   int

	resequenced int // multipart sentences whose sequence id was normalized away
}

func loadNorm(dir string) *normSide {
	s := &normSide{events: map[string]string{}, sources: map[string]string{}, copies: map[string]bool{}, weather: map[string]string{}}
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
		for {
			var e normEnvelope
			if err := dec.Decode(&e); err != nil {
				if err.Error() == "EOF" {
					return nil
				}
				return fmt.Errorf("%s: %w", path, err)
			}
			s.lines++
			s.add(e)
		}
	})
	if err != nil {
		log.Fatalf("normdiff: %v", err)
	}
	return s
}

func (s *normSide) add(e normEnvelope) {
	switch e.K {
	case "event":
		// v1Event carries Message as an ais.Packet interface, which cannot be unmarshalled into;
		// the comparison wants the decoded message as bytes anyway.
		var ev struct {
			ID      string          `json:"id"`
			Time    time.Time       `json:"time"`
			Source  string          `json:"source"`
			MMSI    uint32          `json:"mmsi"`
			MsgType string          `json:"msg_type"`
			NMEA    []string        `json:"nmea"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(e.R, &ev) != nil {
			return
		}
		k := ev.ID + "\t" + ev.Time.UTC().Format(time.RFC3339Nano)
		// The decoded message is the payload under comparison, with the sentences that produced it;
		// flags ride along because withholding an event from the stream is a decision replay must
		// reproduce. Source is deliberately excluded: that is the first-copy race.
		body, _ := json.Marshal(struct {
			Msg         json.RawMessage `json:"m"`
			NMEA        []string        `json:"n,omitempty"`
			Type        string          `json:"t"`
			MMSI        uint32          `json:"mmsi"`
			Implausible bool            `json:"i,omitempty"`
			Stale       bool            `json:"s,omitempty"`
		}{ev.Message, canonNMEA(ev.NMEA), ev.MsgType, ev.MMSI, e.Implausible, e.Stale})
		s.events[k] = string(body)
		s.sources[k] = ev.Source
		if rawNMEA(ev.NMEA) != rawNMEA(canonNMEA(ev.NMEA)) {
			s.resequenced++
		}
	case "copy":
		var c normCopy
		if json.Unmarshal(e.R, &c) != nil {
			return
		}
		s.copies[c.ID+"\t"+c.Time+"\t"+c.Source+"\t"+c.Station] = true
	case "methyd":
		var w struct {
			MMSI    int    `json:"mmsi"`
			Msgtime string `json:"msgtime"`
		}
		if json.Unmarshal(e.R, &w) != nil {
			return
		}
		s.weather[fmt.Sprintf("%d\t%s", w.MMSI, w.Msgtime)] = string(e.R)
	}
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
	for k, lv := range live.events {
		rv, ok := replay.events[k]
		switch {
		case !ok:
			r.eventsOnlyLive = append(r.eventsOnlyLive, k)
		case lv != rv:
			r.eventsDiffer = append(r.eventsDiffer, k)
		case live.sources[k] != replay.sources[k]:
			r.firstCopyRaces++
		}
	}
	for k := range replay.events {
		if _, ok := live.events[k]; !ok {
			r.eventsOnlyReplay = append(r.eventsOnlyReplay, k)
		}
	}
	for k := range live.copies {
		if !replay.copies[k] {
			r.copiesOnlyLive = append(r.copiesOnlyLive, k)
		}
	}
	for k := range replay.copies {
		if !live.copies[k] {
			r.copiesOnlyReplay = append(r.copiesOnlyReplay, k)
		}
	}
	for k, lv := range live.weather {
		rv, ok := replay.weather[k]
		switch {
		case !ok:
			r.weatherOnlyLive = append(r.weatherOnlyLive, k)
		case lv != rv:
			r.weatherDiffer = append(r.weatherDiffer, k)
		}
	}
	for k := range replay.weather {
		if _, ok := live.weather[k]; !ok {
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
		r.live.files, r.live.lines, len(r.live.events), len(r.live.copies), len(r.live.weather))
	fmt.Fprintf(&b, "replay: %d files, %d records, %d transmissions, %d copies, %d weather\n",
		r.replay.files, r.replay.lines, len(r.replay.events), len(r.replay.copies), len(r.replay.weather))
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
