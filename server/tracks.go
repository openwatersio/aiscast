package main

// Track positions: every position report the pipeline accepts goes to ClickHouse (clickhouse.go), which
// answers every track. A track reaches back 48 hours for anonymous and personal tokens and a year for the
// rest. Nothing attaches ClickHouse in replay, so replay never writes there.

import (
	"math"
	"time"
)

// trackWindow is how far back an anonymous or personal track reaches.
const trackWindow = 48 * time.Hour

// maxPending bounds the positions held for the ClickHouse writer, about eight minutes of traffic at the 600 or
// so positions a second the network carries at peak, around 20 MB.
const maxPending = 300_000

// trackPoint is one accepted position report. Positions and motion are held in the lake's integer
// encodings: 1/600000 degree, 0.1 knot (1023 not available), 0.1 degree (3600 not available).
type trackPoint struct {
	mmsi      uint32
	ts        time.Time
	lat6      int32
	lon6      int32
	sog10     uint16
	cog10     uint16
	heading   uint16 // 511 not available
	navStatus uint8  // 15 not available
	source    string // source kind, for attribution
}

func newTrackPoint(mmsi uint32, ts time.Time, u *vessel, source string) trackPoint {
	pt := trackPoint{mmsi: mmsi, ts: ts, lat6: int32(math.Round(u.Lat * 600000)), lon6: int32(math.Round(u.Lon * 600000)),
		sog10: 1023, cog10: 3600, heading: u.Heading, navStatus: u.NavStatus, source: sourceKind(source)}
	if u.Sog < 102.3 {
		pt.sog10 = uint16(math.Round(u.Sog * 10))
	}
	if u.Cog < 360 {
		pt.cog10 = uint16(math.Round(u.Cog * 10))
	}
	return pt
}

// The store keeps every accepted position, but a track drawn straight through them kinks wherever a
// report's stamp disagrees with its fix: AISHub's snapshot stamps run tens of seconds off the
// positions they carry, and a few vessels broadcast broken fixes outright. Those errors are metres
// to a few hundred metres — far under the ingest gate's 10 NM teleport floor — so they are only
// visible here, as segments implying two to forty times the vessel's speed. Serving is the one
// place that can judge them: the fix time never arrives to correct the stamp, and dropping a point
// from a drawn line loses nothing the store does not still hold.
const (
	despikeFloorNM  = 0.03 // under ~55 m a segment cannot draw a visible kink, and jitter over a short dt implies any speed
	despikeMinKnots = 25.0 // fastest implied speed always kept, whatever the vessel reports
	despikeMaxRun   = 3    // consecutive drops before the run outvotes the anchor and the next point re-anchors
)

// despike walks a track oldest first and drops each position implying an impossible speed from the
// last kept one: over despikeMinKnots and more than twice either endpoint's reported speed. A run of
// drops longer than despikeMaxRun outvotes the anchor, and the next position re-anchors rather than
// erasing the rest of the track. An anchor with no plausible kept segment behind it — the oldest
// point, or a prior forced keep — was never corroborated and goes with its run, so the drawn line
// does not connect two impossible positions. A corroborated anchor stays: both sides are then real
// reports (a duplicate MMSI transmitting from two places), a LineString cannot show a break, and one
// straight jump is the honest rendering.
func despike(points []trackPoint) []trackPoint {
	kept := points[:0]
	run := 0
	corroborated := false // the current anchor has a plausible kept segment behind it
	for _, pt := range points {
		if len(kept) == 0 {
			kept = append(kept, pt)
			continue
		}
		a := kept[len(kept)-1]
		dt := pt.ts.Sub(a.ts).Seconds()
		d := nm(float64(a.lat6)/600000, float64(a.lon6)/600000, float64(pt.lat6)/600000, float64(pt.lon6)/600000)
		vmax := despikeMinKnots
		if a.sog10 != 1023 {
			vmax = max(vmax, 2*float64(a.sog10)/10)
		}
		if pt.sog10 != 1023 {
			vmax = max(vmax, 2*float64(pt.sog10)/10)
		}
		// Equal stamps are distinct reports the store keeps (see TestTrackKeepsEqualTimeReports);
		// with no time between them there is no speed to judge.
		if dt > 0 && d > despikeFloorNM && d/(dt/3600) > vmax {
			if run < despikeMaxRun {
				run++
				continue
			}
			if !corroborated {
				kept = kept[:len(kept)-1]
			}
			kept = append(kept, pt)
			corroborated = false
			run = 0
			continue
		}
		run = 0
		kept = append(kept, pt)
		corroborated = true
	}
	return kept
}

// notePosition queues an accepted position for ClickHouse. The caller holds vmu.
func (p *Pipeline) notePosition(mmsi uint32, ts time.Time, u *vessel, source string) {
	if p.chQueue == nil {
		return
	}
	// A full queue gives up its oldest tenth, so it holds the latest positions; a batch waiting to be sent
	// again is kept apart and still goes. A tenth at a time keeps the copy rare.
	if len(p.chQueue) >= maxPending {
		n := maxPending / 10
		p.ch.dropped.Add(int64(n))
		p.chQueue = append(p.chQueue[:0], p.chQueue[n:]...)
	}
	p.chQueue = append(p.chQueue, newTrackPoint(mmsi, ts, u, source))
}
