package main

// The record mirror answers every record read but text search from memory; filed vessels are replaced, never changed.

import (
	"container/heap"
	"errors"
	"slices"
	"sync"
	"time"
)

type mirrorEntry struct {
	v         *vessel
	firstSeen time.Time
}

type recordMirror struct {
	mu      sync.RWMutex
	entries map[uint32]*mirrorEntry
	cells   map[cellKey]map[uint32]*mirrorEntry // the vessels with a position, by the cell of it
	imos    map[uint32]map[uint32]bool          // the MMSIs reporting each IMO number
	strs    map[string]string                   // one copy of each source, station, and message type

	refreshMu sync.Mutex          // one refresh at a time, read and filed together
	pending   map[uint32]struct{} // MMSIs whose refresh failed, retried with the next; under refreshMu
}

// loadMirror reads every vessel the record holds.
func loadMirror(s *store) (*recordMirror, error) {
	m := &recordMirror{entries: map[uint32]*mirrorEntry{}, cells: map[cellKey]map[uint32]*mirrorEntry{},
		imos: map[uint32]map[uint32]bool{}, strs: map[string]string{}, pending: map[uint32]struct{}{}}
	recs, err := s.scan("SELECT " + recordCols + " FROM vessels") // in table order: no index to walk, no sort
	if err != nil {
		return nil, err
	}
	m.file(recs)
	return m, nil
}

// refresh retries earlier failures too, so a vessel that goes quiet is not left stale.
func (m *recordMirror) refresh(s *store, mmsis []uint32) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	for mmsi := range m.pending {
		mmsis = append(mmsis, mmsi)
	}
	clear(m.pending)
	var errs []error
	for len(mmsis) > 0 {
		n := min(len(mmsis), maxParams)
		chunk := mmsis[:n]
		mmsis = mmsis[n:]
		recs, err := s.findSQL(recordQuery{mmsis: chunk})
		if err != nil {
			errs = append(errs, err)
			for _, mmsi := range chunk {
				m.pending[mmsi] = struct{}{}
			}
			continue
		}
		m.file(recs)
	}
	return errors.Join(errs...)
}

func (m *recordMirror) file(recs []record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		v := r.v
		v.Source, v.Station, v.MsgType = m.intern(v.Source), m.intern(v.Station), m.intern(v.MsgType)
		v.Kind, v.Class = m.intern(v.Kind), m.intern(v.Class)
		if old := m.entries[r.mmsi]; old != nil {
			if old.v.HasPos {
				m.unfileCell(r.mmsi, old.v.cell)
			}
			if set := m.imos[old.v.IMO]; set != nil {
				delete(set, r.mmsi)
				if len(set) == 0 {
					delete(m.imos, old.v.IMO)
				}
			}
		}
		e := &mirrorEntry{v: v, firstSeen: r.firstSeen}
		m.entries[r.mmsi] = e
		if v.IMO != 0 {
			if m.imos[v.IMO] == nil {
				m.imos[v.IMO] = map[uint32]bool{}
			}
			m.imos[v.IMO][r.mmsi] = true
		}
		if v.HasPos {
			v.cell = cellOf(v.Lat, v.Lon)
			c := m.cells[v.cell]
			if c == nil {
				c = map[uint32]*mirrorEntry{}
				m.cells[v.cell] = c
			}
			c[r.mmsi] = e
		}
	}
}

func (m *recordMirror) unfileCell(mmsi uint32, k cellKey) {
	if c := m.cells[k]; c != nil {
		delete(c, mmsi)
		if len(c) == 0 {
			delete(m.cells, k)
		}
	}
}

func (m *recordMirror) intern(s string) string {
	if i, ok := m.strs[s]; ok {
		return i
	}
	m.strs[s] = s
	return s
}

// answers reports whether the mirror can answer q: everything but the text search SQLite indexes.
func (m *recordMirror) answers(q recordQuery) bool {
	return m != nil && q.prefix == "" && q.contains == "" && q.flag == "" && !q.byName
}

// mirrorQuery is a recordQuery made ready to test entries against.
type mirrorQuery struct {
	recordQuery
	mmsis, imos   map[uint32]bool
	since, before int64 // unix ms, as the record compares them; 0 when unset
}

func newMirrorQuery(q recordQuery) (*mirrorQuery, error) {
	if len(q.boxes) > maxBoxes || len(q.mmsis)+len(q.imos) > maxParams { // the bounds SQLite enforces, kept
		return nil, errTooManyTerms
	}
	mq := &mirrorQuery{recordQuery: q, since: unixMs(q.since), before: unixMs(q.before)}
	set := func(ids []uint32) map[uint32]bool {
		if ids == nil {
			return nil
		}
		s := make(map[uint32]bool, len(ids))
		for _, id := range ids {
			s[id] = true
		}
		return s
	}
	mq.mmsis, mq.imos = set(q.mmsis), set(q.imos)
	return mq, nil
}

// match is where for one entry. A box matches only a vessel with a position.
func (q *mirrorQuery) match(mmsi uint32, v *vessel) bool {
	switch {
	case q.mmsis != nil && !q.mmsis[mmsi],
		q.imos != nil && !q.imos[v.IMO],
		q.hasPos && !v.HasPos,
		q.since != 0 && unixMs(v.Seen) < q.since,
		q.before != 0 && unixMs(v.Seen) >= q.before,
		len(q.boxes) > 0 && !(v.HasPos && inAny(q.boxes, v.Lat, v.Lon)),
		q.filter != nil && !q.filter.match(v, q.now):
		return false
	}
	return true
}

// each runs fn under the read lock: fn must not keep or modify the vessel.
func (m *recordMirror) each(q *mirrorQuery, fn func(mmsi uint32, e *mirrorEntry)) {
	visit := func(mmsi uint32, e *mirrorEntry) {
		if q.match(mmsi, e.v) {
			fn(mmsi, e)
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch {
	case q.mmsis != nil:
		for mmsi := range q.mmsis {
			if e := m.entries[mmsi]; e != nil {
				visit(mmsi, e)
			}
		}
	case q.imos != nil:
		for imo := range q.imos {
			for mmsi := range m.imos[imo] {
				visit(mmsi, m.entries[mmsi])
			}
		}
	case len(q.boxes) > 0:
		span := 0
		for _, b := range q.boxes {
			r0, c0 := cellRowCol(b[0], b[1])
			r1, c1 := cellRowCol(b[2], b[3])
			span += int(r1-r0+1) * int(c1-c0+1)
		}
		// Wide boxes cover more cells than hold any vessel, and walking the occupied cells is cheaper.
		if span > len(m.cells) {
			for _, c := range m.cells {
				for mmsi, e := range c {
					visit(mmsi, e)
				}
			}
			return
		}
		// Each vessel sits in one cell, so visiting each cell once visits each vessel once, even where boxes overlap.
		visited := map[cellKey]bool{}
		for _, b := range q.boxes {
			r0, c0 := cellRowCol(b[0], b[1])
			r1, c1 := cellRowCol(b[2], b[3])
			for r := r0; r <= r1; r++ {
				for c := c0; c <= c1; c++ {
					k := cellKey(r*360 + c)
					if visited[k] {
						continue
					}
					visited[k] = true
					for mmsi, e := range m.cells[k] {
						visit(mmsi, e)
					}
				}
			}
		}
	default:
		for mmsi, e := range m.entries {
			visit(mmsi, e)
		}
	}
}

// find breaks ties by lower MMSI and returns copies of the filed vessels.
func (m *recordMirror) find(q recordQuery) ([]record, error) {
	mq, err := newMirrorQuery(q)
	if err != nil {
		return nil, err
	}
	var hits mirrorHits
	m.each(mq, func(mmsi uint32, e *mirrorEntry) {
		h := mirrorHit{mmsi: mmsi, seen: unixMs(e.v.Seen), e: e}
		switch {
		case q.limit <= 0 || len(hits) < q.limit:
			heap.Push(&hits, h)
		case h.before(hits[0]): // a limit keeps the best q.limit in a heap rather than sorting every match
			hits[0] = h
			heap.Fix(&hits, 0)
		}
	})
	slices.SortFunc(hits, func(a, b mirrorHit) int {
		if a.before(b) {
			return -1
		}
		return 1
	})
	out := make([]record, len(hits))
	for i, h := range hits {
		// Filed vessels are replaced, never modified, so the copy needs no lock.
		out[i] = record{mmsi: h.mmsi, v: h.e.v.state(), firstSeen: h.e.firstSeen}
	}
	return out, nil
}

// mirrorHit ranks as the record orders: most recently heard first by the millisecond, lower MMSI on a tie.
type mirrorHit struct {
	mmsi uint32
	seen int64
	e    *mirrorEntry
}

func (a mirrorHit) before(b mirrorHit) bool {
	return a.seen > b.seen || a.seen == b.seen && a.mmsi < b.mmsi
}

// mirrorHits is a heap with the worst-ranked hit on top, so a limit evicts it first.
type mirrorHits []mirrorHit

func (h mirrorHits) Len() int           { return len(h) }
func (h mirrorHits) Less(i, j int) bool { return h[j].before(h[i]) }
func (h mirrorHits) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *mirrorHits) Push(x any)        { *h = append(*h, x.(mirrorHit)) }
func (h *mirrorHits) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// count is store.count answered from memory.
func (m *recordMirror) count(q recordQuery) (int, error) {
	mq, err := newMirrorQuery(q)
	if err != nil {
		return 0, err
	}
	n := 0
	m.each(mq, func(uint32, *mirrorEntry) { n++ })
	return n, nil
}

// positions is store.positions answered from memory.
func (m *recordMirror) positions(q recordQuery) ([]storedPos, error) {
	mq, err := newMirrorQuery(q)
	if err != nil {
		return nil, err
	}
	var out []storedPos
	m.each(mq, func(mmsi uint32, e *mirrorEntry) {
		out = append(out, storedPos{mmsi: mmsi, lat: e.v.Lat, lon: e.v.Lon, posAt: e.v.PosAt})
	})
	return out, nil
}

// counts is store.counts answered from memory, in one pass.
func (m *recordMirror) counts(now time.Time) *recordCounts {
	c := &recordCounts{Heard: map[string]int{}, New: map[string]int{}}
	cuts := make([]int64, len(recordWindows))
	for i, w := range recordWindows {
		cuts[i] = unixMs(now.Add(-w.age))
		c.Heard[w.key], c.New[w.key] = 0, 0 // every window reported, even when empty
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	c.Total = len(m.entries)
	for _, e := range m.entries {
		seen, first := unixMs(e.v.Seen), unixMs(e.firstSeen)
		for i, w := range recordWindows {
			if seen >= cuts[i] {
				c.Heard[w.key]++
			}
			if first >= cuts[i] {
				c.New[w.key]++
			}
		}
	}
	return c
}

// countFirstSeen is store.countFirstSeen answered from memory.
func (m *recordMirror) countFirstSeen(since time.Time) int {
	cut := unixMs(since)
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, e := range m.entries {
		if unixMs(e.firstSeen) >= cut {
			n++
		}
	}
	return n
}

// len is the number of vessels the mirror holds.
func (m *recordMirror) len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
