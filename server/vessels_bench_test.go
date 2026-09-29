package main

import (
	"math/rand/v2"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// benchFleet fills the cache with n vessels shaped like production: most in a few busy waters, the rest
// spread over the oceans, so small boxes in busy water see hundreds of vessels and most of the world is empty.
func benchFleet(p *Pipeline, n int) {
	busy := []bbox{{57, 5, 62, 12}, {53, 3, 56, 9}, {59, 18, 61, 30}, {35, 120, 40, 125}, {25, -95, 30, -80}}
	r := rand.New(rand.NewPCG(1, 2))
	now := time.Now()
	p.vmu.Lock()
	defer p.vmu.Unlock()
	for i := 0; i < n; i++ {
		v := newVessel()
		if i%10 < 7 {
			b := busy[i%len(busy)]
			v.Lat, v.Lon = b[0]+r.Float64()*(b[2]-b[0]), b[1]+r.Float64()*(b[3]-b[1])
		} else {
			v.Lat, v.Lon = r.Float64()*140-70, r.Float64()*360-180
		}
		v.HasPos, v.Seen, v.PosAt = true, now, now
		v.Name, v.Source, v.Station, v.MsgType = "BENCH VESSEL", "aishub", "aishub", "PositionReport"
		v.Cog, v.Sog, v.Heading, v.NavStatus, v.ShipType = 123.4, 11.2, 124, 0, 70
		p.putVesselLocked(uint32(200000000+i), v)
	}
}

// benchVessels times one /v1/vessels request. cold clears every cached Feature first, outside the timer,
// the state right after each vessel's next fold; warm measures repeat polling of a box.
func benchVessels(b *testing.B, bbox string, cold bool) {
	p := testPipeline(nil)
	benchFleet(p, 60000)
	req := httptest.NewRequest("GET", "/v1/vessels?bbox="+bbox, nil)
	b.ReportAllocs()
	for b.Loop() {
		if cold {
			b.StopTimer()
			for _, v := range p.vessels {
				v.feat.Store(nil)
			}
			b.StartTimer()
		}
		p.serveVessels(httptest.NewRecorder(), req)
	}
}

func BenchmarkVessels1SqDeg(b *testing.B)       { benchVessels(b, "59,10,60,11", false) }
func BenchmarkVessels1SqDegCold(b *testing.B)   { benchVessels(b, "59,10,60,11", true) }
func BenchmarkVessels400SqDeg(b *testing.B)     { benchVessels(b, "50,0,70,20", false) }
func BenchmarkVessels400SqDegCold(b *testing.B) { benchVessels(b, "50,0,70,20", true) }

// benchFold times folding one position report into a 60k-vessel cache. recorded attaches the vessel
// record's dirty set and the track queue, the fold's only extra work when the stores are on; the writes
// themselves run outside the fold, once a second.
func benchFold(b *testing.B, recorded bool) {
	p := testPipeline(nil)
	benchFleet(p, 60000)
	if recorded {
		p.dirty = map[uint32]struct{}{}
		p.tracks, p.trackQueue = &trackStore{}, make([]trackPoint, 0, maxPending)
	}
	start := time.Now()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		at := start.Add(time.Duration(i) * 3 * time.Second) // each report newer than the last, as a live vessel's are
		p.updateVessel(&Event{MMSI: uint32(200000000 + i%60000), Time: at, RecvTime: at,
			Packet: posReport(uint32(200000000+i%60000), 59.5+float64(i%100)/10000, 10.5), Type: "PositionReport"})
	}
}

func BenchmarkFold(b *testing.B)         { benchFold(b, false) }
func BenchmarkFoldRecorded(b *testing.B) { benchFold(b, true) }

// benchTile times building and gzipping one tile, the work a cache miss does; hits cost a map lookup.
func benchTile(b *testing.B, z, x, y int) {
	p := testPipeline(nil)
	benchFleet(p, 60000)
	b.ReportAllocs()
	var n int
	for b.Loop() {
		n = len(gzipBytes(p.vesselTile(z, x, y, &tileFilter{}, time.Now())))
	}
	b.ReportMetric(float64(n), "gz-bytes")
}

func BenchmarkTileZ0(b *testing.B)          { benchTile(b, 0, 0, 0) }
func BenchmarkTileZ4Skagerrak(b *testing.B) { benchTile(b, 4, 8, 4) }
func BenchmarkTileZ8Oslofjord(b *testing.B) { benchTile(b, 8, 135, 74) }

// benchTileRecord adds 300,000 record rows heard over the last two weeks, half of them last reported
// stationary, to the 60,000-vessel cache: the low-zoom tiles read most of them.
func benchTileRecord(b *testing.B, z, x, y int) {
	p := testPipeline(nil)
	benchFleet(p, 60000)
	st, err := openStore(b.TempDir() + "/aiscast.db")
	if err != nil {
		b.Fatal(err)
	}
	defer st.close()
	p.attachStore(st)
	r := rand.New(rand.NewPCG(3, 4))
	now := time.Now()
	var rows []record
	for i := range 300000 {
		v := newVessel()
		v.Lat, v.Lon, v.HasPos = r.Float64()*140-70, r.Float64()*360-180, true
		v.Seen = now.Add(-time.Duration(r.Int64N(int64(14 * 24 * time.Hour))))
		v.PosAt, v.Name, v.Source, v.ShipType = v.Seen, "BENCH RECORD", "aishub", 70
		if i%2 == 0 {
			v.Sog = 0
		} else {
			v.Sog = 11.2
		}
		rows = append(rows, record{mmsi: uint32(300000000 + i), v: v, firstSeen: v.Seen})
	}
	if err := st.upsert(rows); err != nil {
		b.Fatal(err)
	}
	rules, _ := parseAgeRules(url.Values{})
	b.ReportAllocs()
	var n int
	for b.Loop() {
		n = len(gzipBytes(p.vesselTile(z, x, y, &tileFilter{ageRules: *rules}, time.Now())))
	}
	b.ReportMetric(float64(n), "gz-bytes")
}

func BenchmarkTileRecordZ0(b *testing.B)          { benchTileRecord(b, 0, 0, 0) }
func BenchmarkTileRecordZ8Oslofjord(b *testing.B) { benchTileRecord(b, 8, 135, 74) }
