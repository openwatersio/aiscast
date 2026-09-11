package main

import (
	"bufio"
	"compress/gzip"
	"container/heap"
	"flag"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// aiscast replay: run archived raw days back through the same source adapters that ran live, with
// the normalized stream as the only output. A raw archive line is a serialized adapter input
// (receive time, station, source-native body), so backfill is replay, not reimplementation.
// Reads and writes local trees only; syncing either side with a bucket is the caller's business.
func runReplay(args []string) {
	fset := flag.NewFlagSet("replay", flag.ExitOnError)
	archiveDir := fset.String("archive", "archive", "raw archive tree to read")
	out := fset.String("out", "normalized-replay", "normalized output tree to write")
	fromS := fset.String("from", "", "first day to write, YYYY-MM-DD UTC (required)")
	toS := fset.String("to", "", "day to stop before, YYYY-MM-DD UTC (required)")
	warmup := fset.Duration("warmup", 30*time.Minute, "state-building lead-in replayed before -from but not written")
	fset.Parse(args)
	from, err1 := time.ParseInLocation("2006-01-02", *fromS, time.UTC)
	to, err2 := time.ParseInLocation("2006-01-02", *toS, time.UTC)
	if err1 != nil || err2 != nil || !to.After(from) {
		log.Fatalf("replay: -from and -to must be YYYY-MM-DD with from < to")
	}

	p := newPipeline(newArchive("", nil)) // no raw writes: the raw archive is the input here
	p.norm = newNormArchive(*out, nil)
	p.norm.blocking = true // completeness over liveness: replay must never drop a record
	p.normGate = from      // the warm-up builds dedupe and per-source state, silently

	readers := collectReaders(*archiveDir, from.Add(-*warmup), to)
	if len(readers) == 0 {
		log.Fatalf("replay: no raw files under %s for %s..%s", *archiveDir, *fromS, *toS)
	}
	h := &readerHeap{}
	for _, r := range readers {
		if r.next() {
			heap.Push(h, r)
		}
	}
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	var n int64
	for h.Len() > 0 {
		r := (*h)[0]
		rx := r.cur
		if !rx.RecvTime.Before(to) {
			heap.Pop(h) // this source is past the range; its remaining files only get later
			continue
		}
		dispatch(p, r.source, rx, st)
		n++
		if r.next() {
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}
	p.norm.shutdown()
	log.Printf("replay: %d receptions -> %d events, %d dups, %d parse errors",
		n, p.stats.events.Load()+p.stats.implausible.Load()+p.stats.stale.Load(), p.stats.dup.Load(), p.stats.parseErr.Load())
}

// dispatch feeds one archived reception to the adapter that consumed it live. Receptions a live
// feeder marked Buffered are not distinguishable in the archive, so a replayed backlog can emit
// where live suppressed it; the diff harness tolerates that the way it tolerates first-copy races.
func dispatch(p *Pipeline, source string, rx Reception, st *aishubState) {
	switch {
	case source == "barentswatch":
		p.barentswatchLine([]byte(rx.Body), rx.RecvTime)
	case source == "digitraffic":
		topic, body, ok := strings.Cut(rx.Body, " ")
		if ok {
			p.digitrafficMessage(topic, []byte(body), rx.RecvTime)
		}
	case source == "aisstream":
		p.aisstreamMessage([]byte(rx.Body), rx.RecvTime)
	case source == "aishub":
		if _, err := p.ingestAishub([]byte(rx.Body), rx.RecvTime, st, 0); err != nil {
			p.stats.parseErr.Add(1)
		}
	case strings.HasPrefix(strings.TrimSpace(rx.Body), "{"):
		p.ingestCatcher(source, []byte(rx.Body), rx.RecvTime)
	default:
		p.ingestLine(rx)
	}
}

// collectReaders builds one sequential reader per source over its hour files in [start, end).
// The path is the inverse of archive.key: <license>/<source with ':' as '/'>/YYYY/MM/DD/HH.gz.
func collectReaders(dir string, start, end time.Time) []*rawReader {
	files := map[string][]string{} // source -> files, appended in walk order (lexical = chronological)
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gz") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 6 { // license, source..., Y, M, D, HH.gz
			return nil
		}
		hour, err := time.ParseInLocation("2006/01/02/15", strings.Join(parts[len(parts)-4:], "/")[:13], time.UTC)
		if err != nil {
			return nil
		}
		if hour.Before(start.Truncate(time.Hour)) || !hour.Before(end) {
			return nil
		}
		source := strings.Join(parts[1:len(parts)-4], ":")
		files[source] = append(files[source], path)
		return nil
	})
	var out []*rawReader
	for source, paths := range files {
		out = append(out, &rawReader{source: source, paths: paths, floor: start})
	}
	return out
}

// rawReader streams one source's hour files in order, yielding one Reception per record.
// Digitraffic bodies are pretty-printed JSON spanning lines; continuation lines have no tab
// columns and a record closes on a line opening with '}'.
type rawReader struct {
	source string
	paths  []string
	floor  time.Time // records before this never reach the heap; files start at the truncated hour
	f      *os.File
	gz     *gzip.Reader
	sc     *bufio.Scanner
	pend   *Reception
	cur    Reception
}

func (r *rawReader) next() bool {
	for {
		if r.sc == nil && !r.open() {
			if r.pend != nil {
				r.cur, r.pend = *r.pend, nil
				return true
			}
			return false
		}
		for r.sc.Scan() {
			line := r.sc.Text()
			recv, rest, ok := cutRecord(line)
			if !ok {
				if r.pend != nil {
					r.pend.Body += "\n" + line
					if strings.HasPrefix(line, "}") {
						r.cur, r.pend = *r.pend, nil
						return true
					}
				}
				continue
			}
			station, body, _ := strings.Cut(rest, "\t")
			rx := Reception{Source: r.source, Station: station, RecvTime: recv, Body: body}
			if r.pend != nil { // a new record closes a dangling multi-line body
				r.cur, r.pend = *r.pend, nil
				if r.startPend(rx) {
					return true
				}
				r.pend = &rx
				return true
			}
			if rx.RecvTime.Before(r.floor) {
				continue
			}
			if r.startPend(rx) {
				continue
			}
			r.cur = rx
			return true
		}
		r.closeFile()
	}
}

// startPend reports whether rx opens a multi-line digitraffic body and must wait for its close.
func (r *rawReader) startPend(rx Reception) bool {
	if r.source == "digitraffic" && strings.Contains(rx.Body, " {") && !strings.HasSuffix(strings.TrimSpace(rx.Body), "}") {
		r.pend = &rx
		return true
	}
	return false
}

func (r *rawReader) open() bool {
	if len(r.paths) == 0 {
		return false
	}
	path := r.paths[0]
	r.paths = r.paths[1:]
	f, err := os.Open(path)
	if err != nil {
		log.Printf("replay: %v", err)
		return r.open()
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		log.Printf("replay: %s: %v", path, err)
		f.Close()
		return r.open()
	}
	gz.Multistream(true)
	r.f, r.gz, r.sc = f, gz, bufio.NewScanner(gz)
	r.sc.Buffer(make([]byte, 1<<20), 256<<20) // an aishub snapshot is one very long line
	return true
}

func (r *rawReader) closeFile() {
	if r.gz != nil {
		r.gz.Close()
		r.f.Close()
	}
	r.gz, r.f, r.sc = nil, nil, nil
}

// cutRecord splits a raw archive line into (recv, station\tbody); continuation lines fail the parse.
func cutRecord(line string) (time.Time, string, bool) {
	ts, rest, ok := strings.Cut(line, "\t")
	if !ok {
		return time.Time{}, "", false
	}
	recv, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", false
	}
	return recv, rest, true
}

// readerHeap orders readers by (receive time, source): the deterministic cross-source merge.
type readerHeap []*rawReader

func (h readerHeap) Len() int { return len(h) }
func (h readerHeap) Less(i, j int) bool {
	if !h[i].cur.RecvTime.Equal(h[j].cur.RecvTime) {
		return h[i].cur.RecvTime.Before(h[j].cur.RecvTime)
	}
	return h[i].source < h[j].source
}
func (h readerHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *readerHeap) Push(x any)   { *h = append(*h, x.(*rawReader)) }
func (h *readerHeap) Pop() any     { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }
