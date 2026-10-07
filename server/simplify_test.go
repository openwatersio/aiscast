package main

import (
	"math"
	"testing"
	"time"
)

// at is a position dx, dy meters east and north of 34.7 N 76.66 W, t after t0.
func at(t0 time.Time, t time.Duration, dx, dy float64) trackPoint {
	lat := 34.7 + dy/111320
	lon := -76.66 + dx/(111320*math.Cos(34.7*math.Pi/180))
	return trackPoint{ts: t0.Add(t), lat6: int32(math.Round(lat * 600000)), lon6: int32(math.Round(lon * 600000)), sog10: 1023, cog10: 3600, heading: 511, navStatus: 15}
}

func TestSimplifyKeepsATripAndCollapsesTheDock(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	var points []trackPoint
	jitter := func(i int) float64 { return float64(i%5) - 2 } // GPS noise of a couple of meters
	tm := time.Duration(0)
	for i := range 120 { // six hours at the dock, a report every 3 minutes
		points = append(points, at(t0, tm, jitter(i), jitter(i+2)))
		tm += 3 * time.Minute
	}
	// Out at 3 m/s (about 6 kn), a report every 10 s: 2 km east, then 2 km north.
	x, y := 0.0, 0.0
	for range 66 {
		tm += 10 * time.Second
		x += 30
		points = append(points, at(t0, tm, x, y))
	}
	corner := len(points) - 1
	for range 66 {
		tm += 10 * time.Second
		y += 30
		points = append(points, at(t0, tm, x, y))
	}
	for i := range 60 { // three hours tied up there
		tm += 3 * time.Minute
		points = append(points, at(t0, tm, x+jitter(i), y+jitter(i+1)))
	}

	s := simplify(points, 30*time.Minute, 1000)
	if !s.simplified || s.more || s.tolerance != shapeTolerance || len(s.breaks) != 0 {
		t.Fatalf("a track that fits keeps the floor tolerance and no breaks: %+v", s)
	}
	if len(s.points) > 12 {
		t.Errorf("the dock's reports and the straight legs collapse: kept %d of %d", len(s.points), len(points))
	}
	found := false
	for _, pt := range s.points {
		found = found || pt.ts.Equal(points[corner].ts)
	}
	if !found {
		t.Error("the corner is kept, so the line does not cut across it")
	}
	if !s.points[0].ts.Equal(points[0].ts) || !s.points[len(s.points)-1].ts.Equal(points[len(points)-1].ts) {
		t.Error("the track keeps its first and last position")
	}
}

func TestSimplifyKeepsAStopOnAStraightLeg(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	var points []trackPoint
	x := 0.0
	tm := time.Duration(0)
	for i := range 120 {
		tm += 10 * time.Second
		if i < 40 || i >= 80 { // stopped for the middle 400 s, on the same straight line
			x += 30
		}
		points = append(points, at(t0, tm, x, 0))
	}
	s := simplify(points, 30*time.Minute, 1000)
	var stopStart, stopEnd bool
	for _, pt := range s.points {
		stopStart = stopStart || pt.ts.Equal(points[39].ts)
		stopEnd = stopEnd || pt.ts.Equal(points[79].ts)
	}
	if !stopStart || !stopEnd {
		t.Errorf("a stop on a straight leg keeps where it began and ended, for the speed chart: %v", s.points)
	}
}

func TestSimplifyBreaksOnlyAtSilences(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	points := []trackPoint{
		at(t0, 0, 0, 0), at(t0, 10*time.Minute, 500, 0), at(t0, 20*time.Minute, 1000, 300),
		at(t0, 45*time.Minute, 1500, 0),                                                                         // 25 minutes on: heard throughout
		at(t0, 165*time.Minute, 9000, 0), at(t0, 175*time.Minute, 9500, 400), at(t0, 185*time.Minute, 10000, 0), // two hours unheard
	}
	s := simplify(points, 30*time.Minute, 1000)
	if len(s.breaks) != 1 || !s.points[s.breaks[0]].ts.Equal(points[4].ts) || !s.points[s.breaks[0]-1].ts.Equal(points[3].ts) {
		t.Errorf("one break, between the two sides of the silence: %v in %d points", s.breaks, len(s.points))
	}
	// Heartbeats 55 minutes apart in positions_1m are a vessel heard throughout.
	beats := []trackPoint{at(t0, 0, 0, 0), at(t0, 55*time.Minute, 5, 5), at(t0, 110*time.Minute, 0, 0)}
	if s := simplify(beats, time.Hour, 1000); len(s.breaks) != 0 {
		t.Errorf("heartbeats under the hour are no break: %v", s.breaks)
	}
}

func TestSimplifyRaisesTheToleranceOnlyPastTheLimit(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	var points []trackPoint
	for i := range 200 { // a zigzag 100 m either side of the course
		points = append(points, at(t0, time.Duration(i)*time.Minute, float64(i)*200, float64(i%2)*200-100))
	}
	if s := simplify(points, 30*time.Minute, 1000); len(s.points) != 200 || s.tolerance != shapeTolerance || s.more {
		t.Errorf("every corner fits: %d points at %v m", len(s.points), s.tolerance)
	}
	s := simplify(points, 30*time.Minute, 50)
	if len(s.points) > 50 || s.tolerance <= shapeTolerance || !s.more {
		t.Errorf("past the limit the tolerance rises to fit it: %d points at %v m, more %v", len(s.points), s.tolerance, s.more)
	}
}

func TestSimplifySpreadsShortBurstsOverTheRange(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	var points []trackPoint
	for i := range 100 { // heard alone every 45 minutes: every position is a segment of its own
		points = append(points, at(t0, time.Duration(i)*45*time.Minute, float64(i)*1000, 0))
	}
	s := simplify(points, 30*time.Minute, 10)
	if len(s.points) != 10 || !s.more || !s.points[0].ts.Equal(points[0].ts) || !s.points[9].ts.Equal(points[99].ts) {
		t.Errorf("ten positions spread from the first to the last: %d, more %v", len(s.points), s.more)
	}
	if len(s.breaks) != 9 {
		t.Errorf("each kept position follows a silence: %v", s.breaks)
	}
	// Counts that do not divide evenly still pick distinct positions: more ends than the limit makes every step
	// between picks more than one.
	s7 := simplify(points, 30*time.Minute, 7)
	for k := 1; k < len(s7.points); k++ {
		if !s7.points[k].ts.After(s7.points[k-1].ts) {
			t.Errorf("a position picked twice: %v", s7.points)
		}
	}
	if s := simplify(nil, 30*time.Minute, 10); len(s.points) != 0 || s.breaks == nil {
		t.Errorf("no positions: %+v", s)
	}
}

// BenchmarkSimplify times the most a simplified track reads, shapeRows positions of a vessel weaving underway.
func BenchmarkSimplify(b *testing.B) {
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	points := make([]trackPoint, shapeRows)
	for i := range points {
		points[i] = at(t0, time.Duration(i)*2*time.Second, float64(i)*10, 300*math.Sin(float64(i)/50))
	}
	b.ReportAllocs()
	for b.Loop() {
		simplify(points, 30*time.Minute, 1000)
	}
}

func TestShapeReadGroupsOnlyWhatOverflows(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		back, span    time.Duration
		step, silence time.Duration
	}{
		{7 * time.Hour, 7 * time.Hour, 0, 30 * time.Minute},                                        // every position
		{48 * time.Hour, 48 * time.Hour, 4 * time.Second, 30*time.Minute + 4*time.Second},          // 172,800 s over 50,000 rows
		{30 * 24 * time.Hour, 7 * 24 * time.Hour, time.Minute, time.Hour + time.Minute},            // positions_1m as it is
		{366 * 24 * time.Hour, 366 * 24 * time.Hour, 11 * time.Minute, time.Hour + 11*time.Minute}, // a year, grouped
	} {
		from := now.Add(-c.back)
		if step, silence := shapeRead(from, from.Add(c.span), now); step != c.step || silence != c.silence {
			t.Errorf("%v back over %v: step %v silence %v, want %v %v", c.back, c.span, step, silence, c.step, c.silence)
		}
	}
}
