package main

import (
	"compress/gzip"
	"crypto/sha256"
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
	"slices"
	"sort"
	"strings"
	"sync"
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
	rep, err := diffTrees(*liveDir, *replayDir)
	if err != nil {
		log.Fatalf("normdiff: %v", err)
	}
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

// normSide is one tree, or one hour of it, reduced to what the comparison is about: transmissions by
// id and time, copies, and weather by station and time. Keys and payloads are 16-byte digests, so an
// hour of production traffic fits in memory; names maps a digest back to a readable key, for the few
// that a report prints. Every record counts, so one written twice on one side and once on the other is
// a difference, not a collapse.
type normSide struct {
	events  map[string]string // key digest -> the payload digest of each occurrence, sorted and concatenated
	sources map[string]string // key digest -> source that won the race (informational)
	copies  map[string]int    // digest of id, time, transmission, source, station, license, receive time
	weather map[string]string // key digest -> record digests, as events
	files   int
	lines   int

	names map[string]string // digest -> readable key; nil keeps none, want limits which
	want  map[string]bool
	tally bool // only collect names: read for a report, not a comparison

	nEvents, nCopies, nWeather int // running totals across hours, for render

	resequenced int // multipart sentences whose sequence id was normalized away
}

const digestLen = 16

func occurrences(v string) int { return len(v) / digestLen }

// loadNorm reads a whole tree or stops the run: normdiff is the check the rollout trusts, so a record
// it cannot read must fail the comparison, never quietly leave it.
func loadNorm(dir string) *normSide {
	s, err := readNormTree(dir)
	if err != nil {
		log.Fatalf("normdiff: %v", err)
	}
	return s
}

func newNormSide() *normSide {
	return &normSide{events: map[string]string{}, sources: map[string]string{}, copies: map[string]int{}, weather: map[string]string{}}
}

// readNormTree reads a whole tree, with every name kept. For small trees; diffTrees reads by hour.
func readNormTree(dir string) (*normSide, error) {
	s := newNormSide()
	s.names = map[string]string{}
	files, err := normFiles(dir)
	for _, f := range files {
		if err != nil {
			break
		}
		err = s.read(filepath.Join(dir, f))
	}
	return s, err
}

// normFiles lists a tree's hour files relative to it, in order.
func normFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gz") {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		out = append(out, rel)
		return err
	})
	return out, err
}

// read adds one hour file's records.
func (s *normSide) read(path string) error {
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
}

// diffTrees compares two trees one hour file at a time. A record's file comes from its receive time,
// which replay takes from raw, so live and replay file every record in the same hour and memory stays
// bounded by one hour of traffic. The exception is an event: its file follows the first copy's
// receive time, and a first-copy race that straddles an hour boundary puts it in neighboring hours.
// An event one side lacks entirely carries into the next hour's comparison once before it counts.
func diffTrees(liveDir, replayDir string) (*normReport, error) {
	lf, err := normFiles(liveDir)
	if err != nil {
		return nil, err
	}
	rf, err := normFiles(replayDir)
	if err != nil {
		return nil, err
	}
	hours := slices.Compact(slices.Sorted(slices.Values(append(lf, rf...))))
	total := &normReport{live: &normSide{}, replay: &normSide{}, names: map[string]string{}}
	carryL, carryR := newNormSide(), newNormSide()
	var carryNames []string // hours whose carried events may need names
	for i, h := range hours {
		l, r := newNormSide(), newNormSide()
		paths := [2]string{filepath.Join(liveDir, h), filepath.Join(replayDir, h)}
		for j, side := range []*normSide{l, r} {
			if _, err := os.Stat(paths[j]); err == nil {
				if err := side.read(paths[j]); err != nil {
					return nil, err
				}
			}
		}
		total.addStats(l, r)
		carried := l.absorb(carryL)
		for k := range r.absorb(carryR) {
			carried[k] = true
		}
		rep := diffNorm(l, r)
		last := i == len(hours)-1
		carryL, carryR = newNormSide(), newNormSide()
		rep.eventsOnlyLive = carryOver(rep.eventsOnlyLive, l, r, carryL, carried, last)
		rep.eventsOnlyReplay = carryOver(rep.eventsOnlyReplay, r, l, carryR, carried, last)
		total.merge(rep)
		// names for what this hour reported, read back from the files that hold them
		if err := total.resolve(append(carryNames, paths[:]...), rep); err != nil {
			return nil, err
		}
		carryNames = paths[:]
	}
	return total, nil
}

// resolve reads names for a report's keys back from the files that hold them, a bounded number per
// hour, so a report can print them without a whole hour of names in memory.
func (t *normReport) resolve(paths []string, r *normReport) error {
	want := map[string]bool{}
	for _, keys := range r.groups() {
		for i, k := range keys {
			if i == 100 {
				break
			}
			want[k] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	s := &normSide{names: t.names, want: want, tally: true}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			if err := s.read(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// carryOver moves events the other side lacks entirely into next, unless they were carried already
// or this is the last hour, and returns the ones that count as differences now.
func carryOver(only []string, from, other, next *normSide, carried map[string]bool, last bool) []string {
	var keep []string
	for _, k := range only {
		if last || carried[k] || other.events[k] != "" {
			keep = append(keep, k)
			continue
		}
		next.events[k] = from.events[k]
		next.sources[k] = from.sources[k]
	}
	return keep
}

// absorb adds events carried from the previous hour and returns their keys.
func (s *normSide) absorb(c *normSide) map[string]bool {
	keys := map[string]bool{}
	for k, v := range c.events {
		keys[k] = true
		s.events[k] = mergeDigests(s.events[k], v)
		s.sources[k] = c.sources[k]
	}
	return keys
}

// addStats accumulates what render reports about each side.
func (t *normReport) addStats(l, r *normSide) {
	for _, p := range [][2]*normSide{{t.live, l}, {t.replay, r}} {
		p[0].files += p[1].files
		p[0].lines += p[1].lines
		p[0].resequenced += p[1].resequenced
		p[0].nEvents += p[1].eventCount()
		p[0].nCopies += total(p[1].copies)
		p[0].nWeather += p[1].weatherCount()
	}
}

func (t *normReport) merge(r *normReport) {
	t.eventsOnlyLive = append(t.eventsOnlyLive, r.eventsOnlyLive...)
	t.eventsOnlyReplay = append(t.eventsOnlyReplay, r.eventsOnlyReplay...)
	t.eventsDiffer = append(t.eventsDiffer, r.eventsDiffer...)
	t.copiesOnlyLive = append(t.copiesOnlyLive, r.copiesOnlyLive...)
	t.copiesOnlyReplay = append(t.copiesOnlyReplay, r.copiesOnlyReplay...)
	t.weatherOnlyLive = append(t.weatherOnlyLive, r.weatherOnlyLive...)
	t.weatherOnlyReplay = append(t.weatherOnlyReplay, r.weatherOnlyReplay...)
	t.weatherDiffer = append(t.weatherDiffer, r.weatherDiffer...)
	t.firstCopyRaces += r.firstCopyRaces
}

// key records a digest's readable form when this side keeps names, and returns the digest.
func (s *normSide) key(readable string) string {
	k := digest([]byte(readable))
	if s.names != nil && (s.want == nil || s.want[k]) {
		s.names[k] = readable
	}
	return k
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
		k := s.key(ev.ID + "\t" + ev.Time.UTC().Format(time.RFC3339Nano))
		if s.tally {
			return nil
		}
		// Every field of the event is under comparison, and the flags ride along because withholding
		// an event from the stream is a decision replay must reproduce. Source, station, license, and
		// attribution are deliberately excluded: they name the first copy, and that is the race. So is
		// the envelope's receive time, which is the first copy's; every copy's own is compared below.
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
			Uncorr      bool            `json:"u,omitempty"`
		}{ev.Message, canonNMEA(ev.NMEA), ev.MsgType, ev.MMSI, ev.Channel, ev.Lat, ev.Lon, ev.Synthesized, e.Implausible, e.Stale, e.Uncorroborated})
		s.events[k] = mergeDigests(s.events[k], digest(body))
		s.sources[k] = intern(ev.Source)
		if hasSeqID(ev.NMEA) {
			s.resequenced++
		}
	case "copy":
		var c normCopy
		if err := json.Unmarshal(e.R, &c); err != nil {
			return fmt.Errorf("copy record: %w", err)
		}
		if c.ID == "" || c.Time == "" || c.Tx == "" || c.Source == "" {
			return errors.New("copy record without an id, time, transmission, and source")
		}
		k := s.key(strings.Join([]string{c.ID, c.Time, c.Tx, c.Source, c.Station, c.License, e.T}, "\t"))
		if !s.tally {
			s.copies[k]++
		}
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
		k := s.key(fmt.Sprintf("%d\t%s", w.MMSI, w.Msgtime))
		if !s.tally {
			s.weather[k] = mergeDigests(s.weather[k], digest([]byte(e.T+"\t"+string(e.R))))
		}
	default:
		return fmt.Errorf("unknown record kind %q", e.K)
	}
	return nil
}

// hasSeqID reports a multipart message whose sentences carry a sequence id, which canonNMEA blanks.
func hasSeqID(lines []string) bool {
	for _, l := range lines {
		if f := strings.Split(l, ","); len(f) > 4 && strings.HasSuffix(f[0], "VDM") && f[3] != "" {
			return true
		}
	}
	return false
}

// mergeDigests adds occurrences to a key's sorted digest list. A key can legitimately repeat (a
// crash-window re-accept), and each occurrence's payload must be compared, not only the last one.
func mergeDigests(have, add string) string {
	if have == "" {
		return add
	}
	ds := make([]string, 0, occurrences(have)+occurrences(add))
	for _, v := range []string{have, add} {
		for i := 0; i < len(v); i += digestLen {
			ds = append(ds, v[i:i+digestLen])
		}
	}
	slices.Sort(ds)
	return strings.Join(ds, "")
}

var interned sync.Map

// intern shares one copy of each source name across millions of events.
func intern(s string) string {
	v, _ := interned.LoadOrStore(s, s)
	return v.(string)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return string(sum[:digestLen])
}

func total(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

func (s *normSide) eventCount() (n int) {
	if s.events == nil {
		return s.nEvents
	}
	for _, v := range s.events {
		n += occurrences(v)
	}
	return n
}

func (s *normSide) weatherCount() (n int) {
	if s.weather == nil {
		return s.nWeather
	}
	for _, v := range s.weather {
		n += occurrences(v)
	}
	return n
}

func (s *normSide) copyCount() int {
	if s.copies == nil {
		return s.nCopies
	}
	return total(s.copies)
}

type normReport struct {
	live, replay      *normSide
	names             map[string]string // digest -> readable key, for printing
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

func (r *normReport) groups() map[string][]string {
	return map[string][]string{
		"transmissions only live": r.eventsOnlyLive, "transmissions only replay": r.eventsOnlyReplay,
		"transmissions decoded differently": r.eventsDiffer,
		"copies only live":                  r.copiesOnlyLive, "copies only replay": r.copiesOnlyReplay,
		"weather only live": r.weatherOnlyLive, "weather only replay": r.weatherOnlyReplay, "weather differing": r.weatherDiffer,
	}
}

func diffNorm(live, replay *normSide) *normReport {
	r := &normReport{live: live, replay: replay, names: map[string]string{}}
	for _, s := range []*normSide{live, replay} {
		maps.Copy(r.names, s.names)
	}
	// Each instance one side has beyond the other is its own difference.
	onlyEach := func(a, b map[string]string, only *[]string) {
		for k, v := range a {
			for i := occurrences(b[k]); i < occurrences(v); i++ {
				*only = append(*only, k)
			}
		}
	}
	onlyEach(live.events, replay.events, &r.eventsOnlyLive)
	onlyEach(replay.events, live.events, &r.eventsOnlyReplay)
	for k, v := range live.events {
		switch rv := replay.events[k]; {
		case rv == "":
		case v != rv:
			r.eventsDiffer = append(r.eventsDiffer, k)
		case live.sources[k] != replay.sources[k]:
			r.firstCopyRaces++
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
	onlyEach(live.weather, replay.weather, &r.weatherOnlyLive)
	onlyEach(replay.weather, live.weather, &r.weatherOnlyReplay)
	for k, v := range live.weather {
		if rv := replay.weather[k]; rv != "" && rv != v {
			r.weatherDiffer = append(r.weatherDiffer, k)
		}
	}
	return r
}

// clean reports whether every difference found is one of the known-acceptable kinds.
func (r *normReport) clean() bool {
	for _, keys := range r.groups() {
		if len(keys) > 0 {
			return false
		}
	}
	return true
}

func (r *normReport) render(sample int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "live:   %d files, %d records, %d transmissions, %d copies, %d weather\n",
		r.live.files, r.live.lines, r.live.eventCount(), r.live.copyCount(), r.live.weatherCount())
	fmt.Fprintf(&b, "replay: %d files, %d records, %d transmissions, %d copies, %d weather\n",
		r.replay.files, r.replay.lines, r.replay.eventCount(), r.replay.copyCount(), r.replay.weatherCount())
	fmt.Fprintf(&b, "first-copy races: %d (expected: concurrent copies have no reproducible arrival order)\n", r.firstCopyRaces)
	fmt.Fprintf(&b, "multipart sentences resequenced: live %d, replay %d (expected: the sequence id comes from a per-process counter)\n",
		r.live.resequenced, r.replay.resequenced)
	groups := r.groups()
	for _, name := range []string{"transmissions only live", "transmissions only replay", "transmissions decoded differently",
		"copies only live", "copies only replay", "weather only live", "weather only replay", "weather differing"} {
		keys := groups[name]
		if len(keys) == 0 {
			continue
		}
		shown := make([]string, len(keys))
		for i, k := range keys {
			shown[i] = r.name(k)
		}
		sort.Strings(shown)
		fmt.Fprintf(&b, "%s: %d\n", name, len(keys))
		for i, k := range shown {
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

// name is a key's readable form, or its digest in hex when no file gave it one.
func (r *normReport) name(k string) string {
	if n, ok := r.names[k]; ok {
		return n
	}
	return fmt.Sprintf("%x", k)
}
