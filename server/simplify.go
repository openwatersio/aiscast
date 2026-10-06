package main

// Track simplification by shape. A track that names no interval is read at full detail and reduced to the
// positions that keep its path. Each segment between silences keeps its two ends. Inside it, positions are
// dropped one at a time, the one nearest the line between its neighbors first, measured at its own time
// (synchronized Euclidean distance): a stop in the middle of a straight leg lies far from where the line puts
// the vessel then, so it stays, and a stay at the dock collapses to its arrival and departure. Dropping from the
// bottom with a heap is O(n log n) for any track, where splitting from the top, as Douglas-Peucker does, can
// take quadratic time.

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// shapeTolerance is the least distance, in meters, a position must lie off its neighbors' line to be kept:
// about GPS noise. A track that fits its limit at this tolerance keeps the same positions whatever its range,
// so 24 hours and 48 hours draw the same path for the same trip.
const shapeTolerance = 15.0

// shape is a track as answered.
type shape struct {
	points     []trackPoint
	more       bool    // positions were left out to fit the limit
	simplified bool    // reduced by shape, not by interval
	breaks     []int   // with simplified, indexes into points that start a segment after a silence
	tolerance  float64 // with simplified, meters: shapeTolerance, or more when the limit forced it
}

// simplify reduces points, oldest first, to at most limit, keeping every position more than shapeTolerance off
// its neighbors' line when that fits, and otherwise the limit's worth that lie furthest off. A silence longer
// than silence ends a segment.
func simplify(points []trackPoint, silence time.Duration, limit int) shape {
	n := len(points)
	seg := make([]int, n) // segment of each position
	for i := 1; i < n; i++ {
		seg[i] = seg[i-1]
		if points[i].ts.Sub(points[i-1].ts) > silence {
			seg[i]++
		}
	}
	rank := rankPositions(points, seg)

	// Keep what lies past the tolerance; past the limit, raise the tolerance to the rank that fits it.
	tolerance := shapeTolerance
	kept := 0
	for _, r := range rank {
		if r > tolerance {
			kept++
		}
	}
	more := false
	if kept > limit {
		sorted := append([]float64(nil), rank...)
		slices.SortFunc(sorted, func(a, b float64) int { return cmp.Compare(b, a) })
		tolerance = sorted[limit]
		more = true
	}
	var keep []int
	if math.IsInf(tolerance, 1) {
		// More segments than the limit holds ends for: a vessel heard in short bursts with silences between.
		// Their ends, spread evenly over the range, rather than only the newest.
		var ends []int
		for i, r := range rank {
			if math.IsInf(r, 1) {
				ends = append(ends, i)
			}
		}
		for k := range limit {
			keep = append(keep, ends[k*(len(ends)-1)/max(limit-1, 1)])
		}
		tolerance = 0
		for _, r := range rank {
			if !math.IsInf(r, 1) {
				tolerance = max(tolerance, r)
			}
		}
		tolerance = max(tolerance, shapeTolerance)
	} else {
		for i, r := range rank {
			if r > tolerance {
				keep = append(keep, i)
			}
		}
	}

	s := shape{simplified: true, more: more, tolerance: tolerance, breaks: []int{}, points: make([]trackPoint, 0, len(keep))}
	for k, i := range keep {
		if k > 0 && seg[i] != seg[keep[k-1]] {
			s.breaks = append(s.breaks, k)
		}
		s.points = append(s.points, points[i])
	}
	return s
}

// rankPositions is, for each position, the largest tolerance it survives: +Inf for a segment's ends, and for
// the rest the distance off its neighbors' line when it was dropped, never less than any dropped before it, so
// the positions kept at any tolerance are the ones still standing when dropping reached it.
func rankPositions(points []trackPoint, seg []int) []float64 {
	n := len(points)
	rank := make([]float64, n)
	prev, next := make([]int, n), make([]int, n)
	h := sedHeap{d: make([]float64, n), at: make([]int, n)}
	// Meters east and north of the first position, and seconds after it, so a distance is plain arithmetic.
	x, y, t := make([]float64, n), make([]float64, n), make([]float64, n)
	if n > 0 {
		cos := math.Cos(float64(points[0].lat6) / 600000 * math.Pi / 180)
		for i, pt := range points {
			x[i] = float64(pt.lon6-points[0].lon6) * metersPerUnit * cos
			y[i] = float64(pt.lat6-points[0].lat6) * metersPerUnit
			t[i] = pt.ts.Sub(points[0].ts).Seconds()
		}
	}
	sed := func(a, b, p int) float64 {
		f := 0.0
		if dt := t[b] - t[a]; dt > 0 {
			f = (t[p] - t[a]) / dt
		}
		return math.Hypot(x[p]-x[a]-f*(x[b]-x[a]), y[p]-y[a]-f*(y[b]-y[a]))
	}
	for i := range n {
		prev[i], next[i] = i-1, i+1
		h.at[i] = -1
		if i == 0 || i == n-1 || seg[i-1] != seg[i] || seg[i+1] != seg[i] {
			rank[i] = math.Inf(1)
			continue
		}
		h.d[i] = sed(i-1, i+1, i)
		h.at[i] = len(h.order)
		h.order = append(h.order, i)
	}
	h.init()
	floor := 0.0
	for len(h.order) > 0 {
		i := h.pop()
		floor = max(floor, h.d[i])
		rank[i] = floor
		p, q := prev[i], next[i]
		next[p], prev[q] = q, p
		for _, j := range [2]int{p, q} {
			if h.at[j] >= 0 {
				h.d[j] = sed(prev[j], next[j], j)
				h.fix(h.at[j])
			}
		}
	}
	return rank
}

// metersPerUnit is a wire unit of latitude, 1/600,000 of a degree, in meters. Distances are synchronized
// Euclidean: how far a position lies from where the line between its neighbors puts the vessel at its time.
// ponytail: an equirectangular projection at the track's first latitude, exact enough for tolerances of meters
// over a track's reach; a track across the antimeridian measures the long way round, which only keeps
// positions that could have gone.
const metersPerUnit = 111320.0 / 600000

// sedHeap is a min-heap of positions by their distance d, which knows where each position sits (at, -1 once
// popped) so a position whose neighbors change is moved in place: the heap never holds more than one entry
// per position, and stays small enough to stay in cache.
type sedHeap struct {
	order []int     // positions, heap-ordered by d
	d     []float64 // distance per position
	at    []int     // index in order per position
}

func (h *sedHeap) less(a, b int) bool {
	i, j := h.order[a], h.order[b]
	return h.d[i] < h.d[j] || h.d[i] == h.d[j] && i < j
}

func (h *sedHeap) swap(a, b int) {
	h.order[a], h.order[b] = h.order[b], h.order[a]
	h.at[h.order[a]], h.at[h.order[b]] = a, b
}

func (h *sedHeap) init() {
	for k := len(h.order)/2 - 1; k >= 0; k-- {
		h.down(k)
	}
}

func (h *sedHeap) pop() int {
	top := h.order[0]
	last := len(h.order) - 1
	h.swap(0, last)
	h.order = h.order[:last]
	h.at[top] = -1
	h.down(0)
	return top
}

// fix restores the order after the distance of the position at k changed.
func (h *sedHeap) fix(k int) {
	if !h.down(k) {
		h.up(k)
	}
}

func (h *sedHeap) up(k int) {
	for k > 0 {
		parent := (k - 1) / 2
		if !h.less(k, parent) {
			return
		}
		h.swap(k, parent)
		k = parent
	}
}

// down sifts the entry at k toward the leaves, and reports whether it moved.
func (h *sedHeap) down(k int) bool {
	start, n := k, len(h.order)
	for {
		least, l, r := k, 2*k+1, 2*k+2
		if l < n && h.less(l, least) {
			least = l
		}
		if r < n && h.less(r, least) {
			least = r
		}
		if least == k {
			return k != start
		}
		h.swap(k, least)
		k = least
	}
}
