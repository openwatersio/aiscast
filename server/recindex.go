package main

// The record index: the vessel record's positions in memory, filed by one-degree cell as the cache's index
// files the cache. Each entry holds what a tile filters and thins on, so a tile finds its record vessels and
// picks the newest in each cell without reading SQLite, and decodes rows only for the vessels it keeps.
//
// SQLite stays the truth. The index loads from it when the record opens, and after every write to the
// record (the once-a-second flush, each page of a history import) the rows just written are read back and
// filed again, so the index follows the record's own merge rules, at most a write behind. Refreshes run one at a
// time, and each reads the rows as they are when it runs, so a later refresh never files an older row.

import (
	"strings"
	"sync"
	"time"
)

// recEntry is one vessel with a position, as the record holds it.
type recEntry struct {
	lat, lon            float64
	sog                 float64
	seen                int64 // unix ms
	cell                cellKey
	kind, class         uint8 // into recKinds, recClasses
	shipType, navStatus uint8
}

var (
	recKinds   = []string{"vessel", "aton", "base", "sar"}
	recClasses = []string{"", "A", "B"}
)

func recCode(names []string, s string) uint8 {
	for i, n := range names {
		if n == s {
			return uint8(i)
		}
	}
	return 0
}

func (e *recEntry) facts() vesselFacts {
	return vesselFacts{kind: recKinds[e.kind], class: recClasses[e.class], shipType: e.shipType, navStatus: e.navStatus,
		sog: e.sog, seen: time.UnixMilli(e.seen)}
}

type recordIndex struct {
	mu      sync.RWMutex
	entries map[uint32]*recEntry
	cells   map[cellKey]map[uint32]*recEntry

	refreshMu sync.Mutex // one refresh at a time, read and file together
}

// recIndexCols are the columns an entry is read from.
const recIndexCols = "mmsi, has_pos, lat, lon, sog, seen, kind, class, ship_type, nav_status"

// loadRecordIndex reads every vessel the record holds.
func loadRecordIndex(s *store) (*recordIndex, error) {
	x := &recordIndex{entries: map[uint32]*recEntry{}, cells: map[cellKey]map[uint32]*recEntry{}}
	return x, x.read(s, "SELECT "+recIndexCols+" FROM vessels WHERE has_pos")
}

// refresh files again the rows of mmsis, as the record holds them now.
func (x *recordIndex) refresh(s *store, mmsis []uint32) error {
	x.refreshMu.Lock()
	defer x.refreshMu.Unlock()
	for len(mmsis) > 0 {
		n := min(len(mmsis), maxParams)
		args := make([]any, n)
		for i, m := range mmsis[:n] {
			args[i] = m
		}
		q := "SELECT " + recIndexCols + " FROM vessels WHERE mmsi IN (?" + strings.Repeat(",?", n-1) + ")"
		if err := x.read(s, q, args...); err != nil {
			return err
		}
		mmsis = mmsis[n:]
	}
	return nil
}

// read files every row a query answers, reading them all before taking the lock so a tile waits only on
// the filing.
func (x *recordIndex) read(s *store, q string, args ...any) error {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		mmsi   uint32
		hasPos bool
		e      recEntry
	}
	var got []row
	for rows.Next() {
		var r row
		var kind, class string
		if err := rows.Scan(&r.mmsi, &r.hasPos, &r.e.lat, &r.e.lon, &r.e.sog, &r.e.seen, &kind, &class, &r.e.shipType, &r.e.navStatus); err != nil {
			return err
		}
		r.e.kind, r.e.class, r.e.cell = recCode(recKinds, kind), recCode(recClasses, class), cellOf(r.e.lat, r.e.lon)
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, r := range got {
		if old := x.entries[r.mmsi]; old != nil {
			x.unfile(r.mmsi, old)
		}
		if r.hasPos {
			e := r.e
			x.file(r.mmsi, &e)
		}
	}
	return nil
}

func (x *recordIndex) file(mmsi uint32, e *recEntry) {
	x.entries[mmsi] = e
	m := x.cells[e.cell]
	if m == nil {
		m = map[uint32]*recEntry{}
		x.cells[e.cell] = m
	}
	m[mmsi] = e
}

func (x *recordIndex) unfile(mmsi uint32, e *recEntry) {
	delete(x.entries, mmsi)
	if m := x.cells[e.cell]; m != nil {
		delete(m, mmsi)
		if len(m) == 0 {
			delete(x.cells, e.cell)
		}
	}
}

// each calls fn for every entry inside box that q's age window, MMSIs, and filter admit; boxes, prefix, and
// the other record-only terms of q are not read. fn runs under the read lock and must not keep e.
func (x *recordIndex) each(box bbox, q recordQuery, fn func(mmsi uint32, e *recEntry)) {
	var mmsis map[uint32]bool
	if q.mmsis != nil {
		mmsis = make(map[uint32]bool, len(q.mmsis))
		for _, m := range q.mmsis {
			mmsis[m] = true
		}
	}
	since, before, boxes := unixMs(q.since), unixMs(q.before), []bbox{box}
	visit := func(mmsi uint32, e *recEntry) {
		switch {
		case !q.since.IsZero() && e.seen < since,
			!q.before.IsZero() && e.seen >= before,
			mmsis != nil && !mmsis[mmsi],
			!inAny(boxes, e.lat, e.lon),
			q.filter != nil && !q.filter.matchFacts(e.facts(), q.now):
			return
		}
		fn(mmsi, e)
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	r0, c0 := cellRowCol(box[0], box[1])
	r1, c1 := cellRowCol(box[2], box[3])
	// A wide box covers more cells than hold any vessel, and walking the occupied cells is cheaper.
	if int(r1-r0+1)*int(c1-c0+1) > len(x.cells) {
		for _, m := range x.cells {
			for mmsi, e := range m {
				visit(mmsi, e)
			}
		}
		return
	}
	for r := r0; r <= r1; r++ {
		for c := c0; c <= c1; c++ {
			for mmsi, e := range x.cells[cellKey(r*360+c)] {
				visit(mmsi, e)
			}
		}
	}
}

// len is the number of vessels with a position the index holds.
func (x *recordIndex) len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.entries)
}
