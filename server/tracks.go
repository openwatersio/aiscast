package main

// Track positions: every copy of every position report goes to ClickHouse (clickhouse.go), which answers every
// track, up to a year per request. Replay writes into a staging table first (replay_clickhouse.go).

import (
	"math"
	"slices"
	"strconv"
	"time"
)

// trackWindow is the recent stretch chTable reads from the raw positions table, whatever the step.
const trackWindow = 48 * time.Hour

// maxPending bounds the copies held for the ClickHouse writer, a few minutes of traffic. Past it ClickHouse has
// stalled, and dropping the oldest copies keeps memory flat; the drops are counted.
const maxPending = 300_000

// maxOwnPending bounds the own-ship sightings waiting for ClickHouse. A station has one own ship, or a few, so
// real traffic is a few keys an hour; the bound is for a station that claims thousands of MMSIs as its own, which
// anyone running one can, and for an outage, when unsent sightings stay in memory.
const maxOwnPending = 10_000

// maxOwnPerStation is how many vessels one station may claim as its own in an hour. A boat has one, and a few
// cover a mothership and its tender or a receiver moved between boats; past it a station is not reporting a
// ship it is on, and taking more would let it crowd every other station's sightings out of maxOwnPending.
const maxOwnPerStation = 4

// trackPoint is one accepted position report. Positions and motion are held in AIS's own integer
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
	// accepted, corroborated, moving, and a transmission of its own.
	txAt           time.Time // the transmission this is a copy of: its accepted copy's canonical time
	txDisc         uint8     // and discOf that copy's event id, telling apart transmissions stamped in the same millisecond
	recv           time.Time // when this copy arrived
	station        string
	dup            bool // a later copy of a transmission another copy delivered first
	uncorroborated bool // from an unauthenticated sender, for a vessel no trusted source heard lately
	implausible    bool // the fold judged it an impossible jump from the vessel's last position
	clockBad       bool // stamped clockBadAge or more before it arrived
	still          bool // not moving: reporting half a knot or less, and within movedM of where the vessel was last moving
	stale          bool // the fold judged it older than a report the vessel had already sent, so the stream and the station counts left it out; dedupe's copies are not judged, and count as heard
}

// discOf is one byte of an event id, the low byte of its first 64 bits, which with the vessel and the time its
// accepted copy was stamped names a transmission: ids repeat for identical payloads minutes apart, so the time
// tells those apart, and the byte tells apart two transmissions of one vessel stamped in the same millisecond.
// On three hours of production receptions the time and the byte together merged 83 of 5 M transmissions, all
// a vessel's reports in one millisecond.
func discOf(id string) uint8 {
	if len(id) < 16 { // not an event id, as for an event built without one; hash it into one
		id = eventID(id)
	}
	h, _ := strconv.ParseUint(id[:16], 16, 64)
	return uint8(h)
}

// movedM is how far a vessel must be from the last place it was moving to count as moving again: under it, a
// moored vessel's GPS jitter and a swing at anchor are noise.
const movedM = 50

// anchor is where a vessel was last moving, which decides whether its next report is moving. Measuring from it
// rather than from the report before catches a vessel drifting slower than half a knot: each report is a few
// meters on, but they add up past movedM. In three hours of production positions, 260 vessels reported half a
// knot or less throughout yet ended more than 300 m from where they started; speed alone gave them 1.7 rows
// each in positions_1m, and an anchor at 50 m gives them about 10, for about 2% more rows overall.
type anchor struct {
	lat6, lon6 int32
	set        bool
}

// still reports whether pt is a vessel sitting still, for positions_1m: not reporting more than half a knot,
// and within movedM of the anchor. Reported speed counts when there is one, since a vessel underway says so
// before it has gone 50 m; about 0.3% of reports carry none, from a transmitter whose GPS gives it no speed,
// and distance alone decides those. An anchor not yet set starts at seed, the vessel's last known position, such
// as the one the vessel cache restores at start; with neither, pt is moving, so a voyage is never hidden. A
// moving pt becomes the anchor when advance is set, which it is only for a report that enters positions_1m.
func (a *anchor) still(pt trackPoint, seed *[2]int32, advance bool) bool {
	if !a.set && seed != nil {
		a.lat6, a.lon6, a.set = seed[0], seed[1], true
	}
	moving := !a.set || pt.sog10 != 1023 && pt.sog10 > 5 ||
		nm(float64(a.lat6)/600000, float64(a.lon6)/600000, float64(pt.lat6)/600000, float64(pt.lon6)/600000)*1852 > movedM
	if moving && advance {
		a.lat6, a.lon6, a.set = pt.lat6, pt.lon6, true
	}
	return !moving
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
	recentKeep = 5 * time.Minute
	recentMax  = 32 // positions a vessel keeps, enough for one reporting every 10 s
	recentBad  = 4  // implausible ones it keeps beside them, so their bytes stay taken
	// recentNearA is how far, in wire units of latitude or longitude (1/600,000 of a degree), a copy may sit
	// from its transmission: 4, about 0.75 m. A source that rounds to 5 decimal places, as MarineCadastre does,
	// is off by up to 3, and converting its float back can add one.
	recentNearA = 4
)

type recentPos struct {
	ms         int64 // Unix milliseconds, the transmission's time too: 24 bytes an entry where a time.Time would make it 40
	lat6, lon6 int32
	disc       uint8
	bad        bool // implausible: never repeated or tested against, but its byte stays taken
	still      bool // the transmission's verdict on moving, which its copies carry
}

// remember adds a transmission of the vessel's own to its recent ones, dropping those too old to be repeated.
// Implausible ones have recentBad places of their own beside the recentMax plausible ones, the oldest giving way
// to the newest, so a station sending a vessel impossible positions, which anyone can run, can neither push out
// the ones its copies are matched to nor leave its byte free for a valid report in its millisecond.
// ponytail: more than recentBad implausible reports in one millisecond free the oldest's byte; a flood that
// dense is already flagged at every copy.
func (v *vessel) remember(pt trackPoint) {
	ms := pt.ts.UnixMilli()
	keep := v.recent[:0]
	same := 0
	for _, r := range v.recent {
		if ms-r.ms < recentKeep.Milliseconds() {
			keep = append(keep, r)
			if r.bad == pt.implausible {
				same++
			}
		}
	}
	if pt.implausible && same >= recentBad || same >= recentMax {
		oldest := slices.IndexFunc(keep, func(r recentPos) bool { return r.bad == pt.implausible })
		keep = slices.Delete(keep, oldest, oldest+1)
	}
	v.recent = append(keep, recentPos{ms, pt.lat6, pt.lon6, pt.txDisc, pt.implausible, pt.still})
}

// repeats is the transmission among the vessel's recent positions that pt is a copy of: the nearest in time at
// pt's position.
func (v *vessel) repeats(pt trackPoint) (recentPos, bool) {
	ms := pt.ts.UnixMilli()
	var best recentPos
	bestDt := int64(-1)
	for _, r := range v.recent {
		dt := max(ms-r.ms, r.ms-ms)
		if !r.bad && dt < recentKeep.Milliseconds() && absInt(r.lat6-pt.lat6) <= recentNearA && absInt(r.lon6-pt.lon6) <= recentNearA && (bestDt < 0 || dt < bestDt) {
			best, bestDt = r, dt
		}
	}
	return best, bestDt >= 0
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
		if !r.bad && (!found || max(ms-r.ms, r.ms-ms) < max(ms-at, at-ms)) {
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

// freeDisc gives pt a byte no recent transmission of the vessel stamped in the same millisecond holds, so the
// view never takes two reports for one transmission. Two distinct reports of a vessel share a stamp mostly where a
// source stamps whole seconds; one byte of their ids then matched in 83 of 5 M transmissions in a production
// sample, each merge losing a position, or hiding it if the other was implausible. Bumping the byte costs nothing
// a row, where a wider one would cost every row a byte and still collide.
// ponytail: the ring sees only the last five minutes, or 32 reports. A report later than that, 122 of 25.6 M
// accepted in 12 hours on 2026-10-04, can still take a forgotten one's byte, one or two losses a year at that rate.
func (v *vessel) freeDisc(pt *trackPoint) {
	ms := pt.txAt.UnixMilli()
	for range 256 {
		taken := false
		for _, r := range v.recent {
			if r.ms == ms && r.disc == pt.txDisc {
				taken = true
				break
			}
		}
		if !taken {
			return
		}
		pt.txDisc++
	}
}

// sent is the vessel's recent transmission stamped at ms at the position: a dedupe copy carries its
// transmission's payload, so the same position.
func (v *vessel) sent(ms int64, lat6, lon6 int32) (recentPos, bool) {
	for _, r := range v.recent {
		if r.ms == ms && r.lat6 == lat6 && r.lon6 == lon6 {
			return r, true
		}
	}
	return recentPos{}, false
}

// fromTransmission gives a dedupe copy its transmission's byte, which freeDisc may have moved, and its verdict
// on moving, so a rebuild of positions_1m from the copy after a purge keeps the track's shape. A transmission
// the vessel no longer keeps leaves the id's byte, and the vessel's anchor judges the copy without moving. The
// caller holds no lock.
func (p *Pipeline) fromTransmission(pt *trackPoint) {
	p.vmu.RLock()
	defer p.vmu.RUnlock()
	v := p.vessels[pt.mmsi]
	if v == nil {
		return
	}
	if r, ok := v.sent(pt.txAt.UnixMilli(), pt.lat6, pt.lon6); ok {
		pt.txDisc, pt.still = r.disc, r.still
		return
	}
	pt.still = v.moved.still(*pt, nil, false) // neither seeds nor moves the anchor, so a read lock holds
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
	if pt.recv.Before(p.replayGate) {
		return // a replay's lead-in builds state and writes nothing
	}
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

// noteOwn gathers a station's own-ship message for station_own, position or static, keeping the latest per
// station, hour, and vessel until the next flush.
func (p *Pipeline) noteOwn(ev *Event) {
	if ev.RecvTime.Before(p.replayGate) {
		return // a replay's lead-in builds state and writes nothing
	}
	k := ownKey{ev.Station, ev.Time.Unix() / 3600, ev.Packet.GetHeader().UserID}
	// Claims count by the hour the message arrived, which the server sets, not the hour it is stamped, which the
	// sender does: stamps across many hours would otherwise open a fresh allowance for each.
	recvHour := ev.RecvTime.Unix() / 3600
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if p.chOwn == nil {
		return
	}
	p.chOwnHW = max(p.chOwnHW, recvHour)
	sh := ownKey{station: k.station, hour: recvHour}
	claimed, known := p.chOwnClaimed[sh]
	last, ok := p.chOwn[k]
	if !claimed[k.mmsi] && len(claimed) >= maxOwnPerStation || !ok && len(p.chOwn) >= maxOwnPending ||
		!known && len(p.chOwnClaimed) >= maxOwnPending {
		p.ch.ownDropped.Add(1)
		return
	}
	if !known {
		claimed = map[uint32]bool{}
		p.chOwnClaimed[sh] = claimed
	}
	claimed[k.mmsi] = true
	if ev.Time.After(last) {
		p.chOwn[k] = ev.Time
	}
}

// noteCopy queues a copy that dedupe matched by payload to the transmission accepted at tx. It decodes to the
// accepted copy's position, so it needs no fold, and it is implausible when that copy was: otherwise a read that
// skips flagged copies would serve the position through this one.
func (p *Pipeline) noteCopy(ev *Event, key string, tx time.Time, implausible bool) {
	if pt, ok := copyPoint(ev, key, tx); ok {
		pt.implausible = implausible
		p.fromTransmission(&pt)
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
	pt.txAt, pt.txDisc, pt.dup = tx, discOf(eventID(key)), true
	pt.recv, pt.station = ev.RecvTime, ev.Station
	pt.uncorroborated = lowTrust(ev.Source)
	pt.clockBad = ev.RecvTime.Sub(ev.Time) >= clockBadAge
	return pt, true
}
