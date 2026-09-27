package main

import "math"

// Spatial index over the vessel cache: vessels with a position, filed by the one-degree cell they are in.
// A bbox query visits only the cells its boxes overlap, so a small box in busy water costs the vessels
// near it rather than a scan of the whole cache. A vessel is in the index exactly when it has a position,
// and every write happens under vmu next to the matching change to p.vessels.

// cellKey numbers the one-degree cells row by row from (-90, -180).
type cellKey int32

// cellRowCol is the cell holding a coordinate. The north pole and the antimeridian fold into the last
// row and column, so a box edge on 90 or 180 lands in the same cell as a vessel sitting on it.
func cellRowCol(lat, lon float64) (row, col int32) {
	row = min(max(int32(math.Floor(lat))+90, 0), 179)
	col = min(max(int32(math.Floor(lon))+180, 0), 359)
	return row, col
}

func cellOf(lat, lon float64) cellKey {
	r, c := cellRowCol(lat, lon)
	return cellKey(r*360 + c)
}

// putVesselLocked adds a vessel to the cache and, when it has a position, to the index. The caller holds vmu.
func (p *Pipeline) putVesselLocked(mmsi uint32, v *vessel) {
	p.vessels[mmsi] = v
	if v.HasPos {
		p.indexLocked(mmsi, v)
	}
}

// indexLocked files a vessel under the cell of its current position, moving it if the position left its
// old cell. The caller holds vmu.
func (p *Pipeline) indexLocked(mmsi uint32, v *vessel) {
	c := cellOf(v.Lat, v.Lon)
	if v.indexed {
		if v.cell == c {
			return
		}
		p.unindexLocked(mmsi, v)
	}
	m := p.cells[c]
	if m == nil {
		m = map[uint32]*vessel{}
		p.cells[c] = m
	}
	m[mmsi] = v
	v.cell, v.indexed = c, true
}

// unindexLocked removes a vessel from the index. The caller holds vmu.
func (p *Pipeline) unindexLocked(mmsi uint32, v *vessel) {
	if !v.indexed {
		return
	}
	m := p.cells[v.cell]
	delete(m, mmsi)
	if len(m) == 0 {
		delete(p.cells, v.cell)
	}
	v.indexed = false
}

// vesselsIn calls fn once for every cached vessel whose position lies inside one of boxes. The caller
// holds vmu, at least for reading.
func (p *Pipeline) vesselsIn(boxes []bbox, fn func(uint32, *vessel)) {
	span := 0
	for _, b := range boxes {
		r0, c0 := cellRowCol(b[0], b[1])
		r1, c1 := cellRowCol(b[2], b[3])
		span += int(r1-r0+1) * int(c1-c0+1)
	}
	// Wide boxes cover more cells than hold any vessel, and walking the occupied cells is cheaper.
	if span > len(p.cells) {
		for _, m := range p.cells {
			for mmsi, v := range m {
				if inAny(boxes, v.Lat, v.Lon) {
					fn(mmsi, v)
				}
			}
		}
		return
	}
	// Each vessel sits in one cell, so visiting each cell once visits each vessel once, even where boxes overlap.
	var visited map[cellKey]bool
	if len(boxes) > 1 {
		visited = map[cellKey]bool{}
	}
	for _, b := range boxes {
		r0, c0 := cellRowCol(b[0], b[1])
		r1, c1 := cellRowCol(b[2], b[3])
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				k := cellKey(r*360 + c)
				if visited != nil {
					if visited[k] {
						continue
					}
					visited[k] = true
				}
				for mmsi, v := range p.cells[k] {
					if inAny(boxes, v.Lat, v.Lon) {
						fn(mmsi, v)
					}
				}
			}
		}
	}
}

// eachMatch calls fn once for every cached vessel the subscription matches, as v1Sub.match would decide
// for its latest state: everything, a followed MMSI whether or not it has a position, or a position inside
// a box. The caller holds vmu, at least for reading.
func (p *Pipeline) eachMatch(s *v1Sub, fn func(uint32, *vessel)) {
	if s.everything {
		for mmsi, v := range p.vessels {
			fn(mmsi, v)
		}
		return
	}
	p.vesselsIn(s.boxes, fn)
	for mmsi := range s.mmsi {
		// the box pass already took a followed vessel that is also inside a box
		if v := p.vessels[mmsi]; v != nil && !(v.HasPos && inAny(s.boxes, v.Lat, v.Lon)) {
			fn(mmsi, v)
		}
	}
}
