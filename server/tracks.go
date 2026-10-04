package main

// Track positions: every copy of every position report goes to ClickHouse (clickhouse.go), which answers every
// track. Nothing attaches ClickHouse in replay, so replay never writes there.

import (
	"math"
	"strconv"
	"time"
)

// trackWindow is how far back an anonymous or personal track reaches.
const trackWindow = 48 * time.Hour

// maxPending bounds the copies held for the ClickHouse writer, a few minutes of traffic. Past it ClickHouse has
// stalled, and dropping the oldest copies keeps memory flat; the drops are counted.
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

	// What a copy adds as a reception, written and not read back. The zero values are the table's defaults:
	// accepted, corroborated, and a transmission of its own.
	tx             uint64    // the transmission this is a copy of, txOf its accepted copy's id and time
	recv           time.Time // when this copy arrived
	station        string
	dup            bool // a later copy of a transmission another copy delivered first
	uncorroborated bool // from an unauthenticated sender, for a vessel no trusted source heard lately
	implausible    bool // the fold judged it an impossible jump from the vessel's last position
	clockBad       bool // stamped clockBadAge or more before it arrived
}

// txOf names a transmission by its accepted copy's event id and canonical time, the pair the normalized archive
// joins copies on: ids repeat for identical payloads minutes apart. It is the id's first 64 bits, a hash of the
// payload, XORed with the time in milliseconds. Reads group a vessel's copies by it across a month, so it must
// not collide among a month's transmissions: at 32 bits a vessel reporting every 10 s would see several
// collisions a month, each merging two reports into one. ClickHouse computes the same value from the hex id,
// so a load from the lake names transmissions as the server does.
func txOf(id string, t time.Time) uint64 {
	if len(id) < 16 { // not an event id, as for an event built without one; hash it into one
		id = eventID(id)
	}
	h, _ := strconv.ParseUint(id[:16], 16, 64)
	return h ^ uint64(t.UnixMilli())
}

// clockBadAge is how far before its arrival a copy's stamp may be before the copy is kept out of history: past
// a satellite pass's hours of delay, and short of a device whose reset clock stamps it years back.
const clockBadAge = 24 * time.Hour

// A rebuilt copy (AISHub, aisstream, BarentsWatch) never byte-matches the raw copy it repeats, and AISHub's
// arrives about a minute behind, after the vessel has sent newer reports, so the fold cannot tell it from a late
// report by time alone. Each vessel keeps its accepted positions of the last few minutes, and a stale rebuilt
// copy at one of their positions is a copy of that transmission. A vessel underway moves between reports, so its
// position names one; a moored one repeats its position, and any of those transmissions is the same point.
const (
	recentKeep  = 5 * time.Minute
	recentMax   = 32 // positions a vessel keeps, enough for one reporting every 10 s
	recentNearA = 3  // wire units of latitude or longitude, about 5 m, for a source that rounds its coordinates
)

type recentPos struct {
	ms         int64 // Unix milliseconds: 24 bytes an entry where a time.Time would make it 40
	lat6, lon6 int32
	tx         uint64
}

// remember adds an accepted position to the vessel's recent ones, dropping those too old to be repeated.
func (v *vessel) remember(pt trackPoint) {
	ms := pt.ts.UnixMilli()
	keep := v.recent[:0]
	for _, r := range v.recent {
		if ms-r.ms < recentKeep.Milliseconds() {
			keep = append(keep, r)
		}
	}
	if len(keep) == recentMax {
		keep = append(keep[:0], keep[1:]...)
	}
	v.recent = append(keep, recentPos{ms, pt.lat6, pt.lon6, pt.tx})
}

// repeats is the transmission among the vessel's recent positions that pt is a copy of: the nearest in time at
// pt's position.
func (v *vessel) repeats(pt trackPoint) (uint64, bool) {
	ms := pt.ts.UnixMilli()
	var best recentPos
	bestDt := int64(-1)
	for _, r := range v.recent {
		dt := max(ms-r.ms, r.ms-ms)
		if dt < recentKeep.Milliseconds() && absInt(r.lat6-pt.lat6) <= recentNearA && absInt(r.lon6-pt.lon6) <= recentNearA && (bestDt < 0 || dt < bestDt) {
			best, bestDt = r, dt
		}
	}
	return best.tx, bestDt >= 0
}

// jumps reports whether pt implies an impossible speed from the vessel's position nearest it in time, among its
// recent ones and its latest: the fold's own test, for a stale report the fold does not test.
func (v *vessel) jumps(pt trackPoint) bool {
	ms := pt.ts.UnixMilli()
	lat, lon, at, found := 0.0, 0.0, int64(0), false
	if v.HasPos {
		lat, lon, at, found = v.Lat, v.Lon, v.PosAt.UnixMilli(), true
	}
	for _, r := range v.recent {
		if !found || max(ms-r.ms, r.ms-ms) < max(ms-at, at-ms) {
			lat, lon, at, found = float64(r.lat6)/600000, float64(r.lon6)/600000, r.ms, true
		}
	}
	if !found {
		return false
	}
	dt := float64(max(ms-at, at-ms)) / 1000
	d := nm(lat, lon, float64(pt.lat6)/600000, float64(pt.lon6)/600000)
	return dt >= 1 && d > implausibleJumpNM && d/(dt/3600) > implausibleKnots
}

func absInt(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
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

// ClickHouse keeps every accepted position, but a track drawn straight through them kinks wherever a
// report's stamp disagrees with its fix: AISHub's snapshot stamps run tens of seconds off the
// positions they carry, and a few vessels broadcast broken fixes outright. Those errors are metres
// to a few hundred metres — far under the ingest gate's 10 NM teleport floor — so they are only
// visible here, as segments implying two to forty times the vessel's speed. Serving is the one
// place that can judge them: the fix time never arrives to correct the stamp, and dropping a point
// from a drawn line loses nothing ClickHouse does not still hold.
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

// noteReception queues a copy for ClickHouse.
func (p *Pipeline) noteReception(pt trackPoint) {
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if p.chQueue == nil {
		return
	}
	// A full queue gives up its oldest tenth, so it holds the latest copies; a batch waiting to be sent again is
	// kept apart and still goes. A tenth at a time keeps the copy rare.
	if len(p.chQueue) >= maxPending {
		n := maxPending / 10
		p.ch.dropped.Add(int64(n))
		p.chQueue = append(p.chQueue[:0], p.chQueue[n:]...)
	}
	p.chQueue = append(p.chQueue, pt)
}

// noteCopy queues a copy that dedupe matched by payload to the transmission accepted at tx. It decodes to the
// accepted copy's position, so it needs no fold, and it is implausible when that copy was: otherwise a read that
// skips flagged copies would serve the position through this one.
func (p *Pipeline) noteCopy(ev *Event, key string, tx time.Time, implausible bool) {
	if pt, ok := copyPoint(ev, key, tx); ok {
		pt.implausible = implausible
		p.noteReception(pt)
	}
}

// copyPoint is a dedupe copy as a reception, or false when it carries no position.
func copyPoint(ev *Event, key string, tx time.Time) (trackPoint, bool) {
	u, hasPos, _ := foldOf(ev.Packet)
	if !hasPos {
		return trackPoint{}, false
	}
	pt := newTrackPoint(ev.Packet.GetHeader().UserID, ev.Time, u, ev.Source)
	pt.tx, pt.dup = txOf(eventID(key), tx), true
	pt.recv, pt.station = ev.RecvTime, ev.Station
	pt.uncorroborated = lowTrust(ev.Source)
	pt.clockBad = ev.RecvTime.Sub(ev.Time) >= clockBadAge
	return pt, true
}
