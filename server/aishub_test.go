package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestAishubSnapshot(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Unix(1625826600, 0)
	body := `[{"ERROR":false,"USERNAME":"AH_TEST","FORMAT":"AIS","RECORDS":1},[{"MMSI":244750034,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144,"COG":3600,"SOG":0,"HEADING":511,"ROT":128,"NAVSTAT":8,"IMO":0,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"","ETA":1596}]]`
	n, err := p.ingestAishub([]byte(body), now)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	pos := <-sub.ch
	pr := pos.Packet.(ais.PositionReport)
	if pos.MMSI != 244750034 || !pos.Synthesized || pos.Source != "aishub" || pos.Time.Unix() != 1625826523 {
		t.Errorf("position event: %+v", pos)
	}
	if d := float64(pr.Latitude) - 52.46024; d > 1e-4 || d < -1e-4 {
		t.Errorf("lat %v", pr.Latitude)
	}
	if float64(pr.Cog) != 360 || pr.TrueHeading != 511 || pr.NavigationalStatus != 8 {
		t.Errorf("sentinels not preserved: %+v", pr)
	}
	stc := <-sub.ch
	sd := stc.Packet.(ais.ShipStaticData)
	if sd.Name != "CHATEAUROUX" || sd.CallSign != "PH7002" || sd.Type != 69 || float64(sd.MaximumStaticDraught) != 1.2 || sd.Dimension.A != 24 {
		t.Errorf("static: %+v", sd)
	}
	// same snapshot again: nothing new (TIME and static unchanged)
	n, _ = p.ingestAishub([]byte(body), now.Add(time.Minute))
	if n != 0 || len(sub.ch) != 0 {
		t.Errorf("repeat snapshot produced %d events", n)
	}
	// error envelope
	if _, err := p.ingestAishub([]byte(`[{"ERROR":true,"ERROR_MESSAGE":"Invalid username"}]`), now); err == nil {
		t.Error("error envelope not reported")
	}
}

// AISHub's aggregate flips a vessel's static fields between two stations' versions while TIME, the
// canonical time, stands still. Re-emitting the flip-back would repeat an (id, time) the archive and
// stream already carry; once TIME advances the current version must flow again.
func TestAishubStaticFlipBack(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Unix(1625826600, 0)
	row := func(time, dest string) string {
		return fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%s","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"%s","ETA":1596}]]`, time, dest)
	}
	if n, err := p.ingestAishub([]byte(row("1625826523", "NLRTM")), now); err != nil || n != 2 {
		t.Fatalf("first snapshot: n=%d err=%v", n, err) // position + static
	}
	// AISHub's aggregate flips the vessel's statics between stations' versions while TIME stands still.
	// Neither the flip nor the flip back is news to the cache: the vessel's static time has not moved.
	if n, _ := p.ingestAishub([]byte(row("1625826523", "NLAMS")), now.Add(time.Minute)); n != 0 {
		t.Fatalf("static changed under an unchanged TIME: n=%d, want 0", n)
	}
	if n, _ := p.ingestAishub([]byte(row("1625826523", "NLRTM")), now.Add(3*time.Minute)); n != 0 {
		t.Errorf("flip-back re-emitted: n=%d, want 0", n)
	}
	// TIME advanced: a new position, and the static changed since what the cache holds goes out with it
	if n, _ := p.ingestAishub([]byte(row("1625826583", "NLAMS")), now.Add(4*time.Minute)); n != 2 {
		t.Errorf("after TIME advance: n=%d, want position and changed static", n)
	}
	// an unchanged static under an advanced TIME is not news
	if n, _ := p.ingestAishub([]byte(row("1625826643", "NLAMS")), now.Add(5*time.Minute)); n != 1 {
		t.Errorf("unchanged static after TIME advance: n=%d, want the position alone", n)
	}
	for i := 0; i < 4; i++ {
		<-sub.ch
	}
	last := <-sub.ch
	if pr, ok := last.Packet.(ais.PositionReport); !ok || last.Time.Unix() != 1625826643 {
		t.Errorf("last event: %+v %v", last, pr)
	}
}

// A future-stamped row is not news yet: capping its time to the receive time would make the same
// unchanged row look newer on every snapshot. It goes through once, with its true time, in the first
// snapshot after that time has passed.
func TestAishubFutureStampWaitsForItsTime(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Unix(1625826600, 0)
	body := []byte(`[[{"MMSI":244750034,"TIME":"1625826610","LONGITUDE":3022815,"LATITUDE":31476144}]]`) // 10 s ahead
	if n, _ := p.ingestAishub(body, now); n != 0 {
		t.Fatalf("future-stamped row emitted: n=%d", n)
	}
	if n, _ := p.ingestAishub(body, now.Add(20*time.Second)); n != 1 {
		t.Fatalf("row not emitted once its time passed: n=%d", n)
	}
	if ev := <-sub.ch; ev.Time.Unix() != 1625826610 {
		t.Errorf("event time %v, want the row's own", ev.Time)
	}
	if n, _ := p.ingestAishub(body, now.Add(40*time.Second)); n != 0 {
		t.Errorf("unchanged row re-emitted: n=%d", n)
	}
}

// A row older than the vessel cache's window is ignored: AISHub keeps vessels it has not heard for hours.
func TestAishubIgnoresRowsOlderThanTheCache(t *testing.T) {
	p := testPipeline(t)
	now := time.Unix(1625826600, 0)
	old := fmt.Sprint(now.Add(-vesselTTL - time.Minute).Unix())
	if n, _ := p.ingestAishub([]byte(`[[{"MMSI":244750034,"TIME":"`+old+`","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX"}]]`), now); n != 0 {
		t.Fatalf("row older than the cache's window emitted: n=%d", n)
	}
}

func TestAishubSkipsPositionsTheCacheWouldHold(t *testing.T) {
	p := testPipeline(t)
	now := time.Unix(1625826600, 0)
	row := func(secs int64) []byte {
		return []byte(fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%d","LONGITUDE":3022815,"LATITUDE":31476144}]]`, secs))
	}
	if n, _ := p.ingestAishub(row(now.Unix()-120), now); n != 1 {
		t.Fatalf("first position: n=%d", n)
	}
	// Another source's static advances Seen past the next AISHub position without moving PosAt.
	p.ingestPacketAt("aisstream", "aisstream", now.Add(-10*time.Second), now, aishubRow{MMSI: 244750034, Name: "CHATEAUROUX"}.static())
	if n, _ := p.ingestAishub(row(now.Unix()-60), now.Add(20*time.Second)); n != 0 {
		t.Fatalf("position the cache would mark stale emitted: n=%d", n)
	}
}

func TestAishubSendsStaticsOlderThanTheLastPosition(t *testing.T) {
	p := testPipeline(t)
	now := time.Unix(1625826600, 0)
	p.ingestPacketAt("aisstream", "aisstream", now.Add(-10*time.Second), now, aishubRow{MMSI: 244750034, Latitude: 31476144, Longitude: 3022815}.position(now.Add(-10*time.Second)))
	body := []byte(fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%d","NAME":"CHATEAUROUX","DEST":"NLRTM"}]]`, now.Unix()-120))
	if n, _ := p.ingestAishub(body, now); n != 1 {
		t.Fatalf("static behind the last position not sent: n=%d", n)
	}
	if v := p.vessels[244750034]; v.Destination != "NLRTM" {
		t.Fatalf("destination not folded: %q", v.Destination)
	}
}

func TestAishubSkipsStaticsWithinASecondOfTheLast(t *testing.T) {
	p := testPipeline(t)
	now := time.Unix(1625826600, 0)
	row := func(secs int64, dest string) []byte {
		return []byte(fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%d","NAME":"CHATEAUROUX","DEST":"%s"}]]`, secs, dest))
	}
	if n, _ := p.ingestAishub(row(now.Unix()-60, "NLRTM"), now); n != 1 {
		t.Fatalf("first static: n=%d", n)
	}
	// The cache marks a rebuilt static within a second of the last one stale, so it is not sent.
	if n, _ := p.ingestAishub(row(now.Unix()-59, "NLAMS"), now.Add(20*time.Second)); n != 0 {
		t.Fatalf("static a second after the last sent: n=%d", n)
	}
}

func TestAishubStaticFlipBehindTheLastPosition(t *testing.T) {
	p := testPipeline(t)
	now := time.Unix(1625826600, 0)
	p.ingestPacketAt("aisstream", "aisstream", now.Add(-10*time.Second), now, aishubRow{MMSI: 244750034, Latitude: 31476144, Longitude: 3022815}.position(now.Add(-10*time.Second)))
	row := func(dest string) []byte {
		return []byte(fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%d","NAME":"CHATEAUROUX","DEST":"%s"}]]`, now.Unix()-120, dest))
	}
	for i, dest := range []string{"NLRTM", "NLAMS", "NLRTM"} {
		want := 0
		if i == 0 {
			want = 1
		}
		if n, _ := p.ingestAishub(row(dest), now.Add(time.Duration(i)*20*time.Second)); n != want {
			t.Fatalf("snapshot %d (%s): n=%d, want %d", i, dest, n, want)
		}
	}
}

func TestAishubSkipsRowsWithoutAUsableTime(t *testing.T) {
	p := testPipeline(t)
	for _, tm := range []string{``, `,"TIME":"soon"`, `,"TIME":"0"`} {
		body := []byte(`[[{"MMSI":244750034` + tm + `,"LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX"}]]`)
		if n, err := p.ingestAishub(body, time.Unix(1625826600, 0)); err != nil || n != 0 {
			t.Fatalf("TIME %q: n=%d err=%v", tm, n, err)
		}
	}
}

// After a restart the cache is restored from the vessel record, and the first AISHub snapshot does not
// re-send what the stream already carried.
func TestAishubAfterRestartSendsOnlyNews(t *testing.T) {
	now := time.Now() // the restore keeps vessels heard within the cache's window of the wall clock
	body := []byte(`[[{"MMSI":244750034,"TIME":"` + fmt.Sprint(now.Add(-time.Minute).Unix()) + `","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"NLRTM","ETA":1596}]]`)
	p := storePipeline(t)
	if n, _ := p.ingestAishub(body, now); n != 2 {
		t.Fatalf("first snapshot: n=%d", n)
	}
	mustFlush(t, p)
	restarted := restartedPipeline(t, p)
	if n, _ := restarted.ingestAishub(body, now.Add(20*time.Second)); n != 0 {
		t.Fatalf("after a restart the unchanged snapshot re-sent %d events", n)
	}
}

func TestReencodeKeepsChannel(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.Ingest(Reception{Source: "t", Station: "t", RecvTime: time.Now(), Body: `\s:2573010,c:1787234980*03\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43`})
	ev := <-sub.ch
	lines := p.encoder.EncodeSentence(aisnmeaPacket(ev.Channel, ev.Payload))
	if len(lines) != 1 || lines[0][:14] != "!AIVDM,1,1,,B," {
		t.Errorf("re-encoded as %v, want channel B", lines)
	}
}

func TestFeedableExcludesPublicSources(t *testing.T) {
	pkt := ais.PositionReport{}
	for src, want := range map[string]bool{"udp:abc": true, "mmsi:368168720": true, "station:station-1": true, "station:ed25519:k": true, "kystverket": false, "digitraffic": false, "aisstream": false, "aishub": false} {
		if got := feedable(&Event{Source: src, Packet: pkt}); got != want {
			t.Errorf("feedable(%s) = %v, want %v", src, got, want)
		}
	}
	if feedable(&Event{Source: "udp:abc", Packet: pkt, Synthesized: true}) {
		t.Error("synthesized event feedable")
	}
}

func TestSelfReportedOwnShipIsSynthesized(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.Ingest(Reception{Source: "station:ed25519:k", Station: "station:ed25519:k", RecvTime: time.Now(), Body: `\s:self*55\!AIVDO,1,1,,A,B1mg=5@3wh<?d@8TIb3Q3wv00000,0*39`})
	ev := <-sub.ch
	if !ev.Synthesized || ev.Station != "station:ed25519:k/self" {
		t.Errorf("synthesized=%v station=%q", ev.Synthesized, ev.Station)
	}
	if feedable(ev) {
		t.Error("self-reported own ship fed to AISHub")
	}
	// The tag only marks own-ship sentences: a received !AIVDM carrying s:self stays a real reception.
	p.Ingest(Reception{Source: "station:ed25519:k", Station: "station:ed25519:k", RecvTime: time.Now(), Body: `\s:self*55\!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23`})
	ev = <-sub.ch
	if ev.Synthesized {
		t.Error("received VDM misclassified as synthesized")
	}
}

// A snapshot is ingested at once, in the order replay reproduces, and only its delivery to
// subscribers is paced.
func TestAishubIngestsAtOnceAndDeliversPaced(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.startAishubPacing(200 * time.Millisecond)
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"MMSI":%d,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144}`, 200000000+i)
	}
	start := time.Now()
	n, err := p.aishubSnapshot([]byte("[["+strings.Join(rows, ",")+"]]"), time.Unix(1625826600, 0))
	if err != nil || n != 20 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Errorf("ingest took %s; it must not wait on delivery", el)
	}
	if v := p.vesselCount(); v != 20 {
		t.Errorf("vessel cache holds %d of the snapshot's 20 vessels right after ingest", v)
	}
	for i := 0; i < 20; i++ {
		<-sub.ch
	}
	// 20 events over 200 ms: the last waits 190 ms, so anything under that means no pacing; the upper
	// bound only guards against a runaway sleep, loose enough for a slow CI runner
	if el := time.Since(start); el < 190*time.Millisecond || el > 2*time.Second {
		t.Errorf("20 events delivered over %s, want about 200ms", el)
	}
}

// Receptions are processed whole: another source's reception never lands between two rows of an AISHub
// snapshot, since replay processes the snapshot as one record. Without the ordering lock the concurrent
// Digitraffic reports below interleave with the rows.
func TestAishubSnapshotIsNotInterleaved(t *testing.T) {
	// The stream mirrors ingest order here (pacing is off), so the subscriber's queue must hold all of it.
	defer func(n int) { subBuffer = n }(subBuffer)
	subBuffer = 1 << 17
	p := testPipeline(t)
	sub := p.subscribe()
	const n = 20000
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"MMSI":%d,"TIME":"%d","LONGITUDE":3022815,"LATITUDE":31476144}`, 200000000+i, time.Now().Add(-time.Minute).Unix())
	}
	body := []byte("[[" + strings.Join(rows, ",") + "]]")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // another adapter, running concurrently as it does live
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p.digitrafficMessage(fmt.Sprintf("vessels-v2/%d/location", 230000000+i),
				[]byte(fmt.Sprintf(`{"time":%d,"sog":1,"cog":1,"navStat":0,"rot":0,"posAcc":false,"raim":false,"heading":1,"lon":20.3,"lat":60.0}`, time.Now().Unix())), time.Now())
		}
	}()
	time.Sleep(20 * time.Millisecond) // let the other adapter get going
	if _, ok := p.admit(time.Now()); !ok {
		t.Fatal("not admitted")
	}
	_, err := p.ingestAishub(body, time.Now())
	p.release()
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	first, last, seen, others := -1, -1, 0, 0
	for i := 0; len(sub.ch) > 0; i++ {
		if ev := <-sub.ch; ev.Source == "aishub" {
			if first < 0 {
				first = i
			}
			last = i
			seen++
		} else {
			others++
		}
	}
	if others == 0 {
		t.Fatal("the other adapter produced no events, so the test shows nothing")
	}
	if seen != n || last-first+1 != n {
		t.Fatalf("snapshot's %d events spread over %d positions in the stream: another reception interleaved", seen, last-first+1)
	}
}

// AISHub's PAC is the position accuracy flag; it reaches the position report.
func TestAishubPositionAccuracy(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	body := `[{"ERROR":false,"USERNAME":"AH_TEST","FORMAT":"AIS","RECORDS":1},[{"MMSI":244750034,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144,"COG":3600,"SOG":0,"HEADING":511,"ROT":128,"PAC":1,"NAVSTAT":8,"IMO":0,"NAME":"","CALLSIGN":"","TYPE":0,"A":0,"B":0,"C":0,"D":0,"DRAUGHT":0,"DEST":"","ETA":0}]]`
	if _, err := p.ingestAishub([]byte(body), time.Unix(1625826600, 0)); err != nil {
		t.Fatal(err)
	}
	if len(sub.ch) != 1 {
		t.Fatalf("events = %d, want the position", len(sub.ch))
	}
	if pr, ok := (<-sub.ch).Packet.(ais.PositionReport); !ok || !pr.PositionAccuracy {
		t.Fatalf("PAC 1 did not set position accuracy: %+v", pr)
	}
}

// Snapshots can arrive faster than a budget's delivery (the poll runs every 20 s). A new one joins the
// backlog and everything pending goes out within one budget of it, in order, so delivery never falls
// behind and the poll loop never blocks on it.
func TestPacedDeliveryKeepsUpWithSnapshots(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.startAishubPacing(200 * time.Millisecond)
	batch := func(base int) []*Event {
		b := make([]*Event, 20)
		for i := range b {
			b[i] = &Event{Source: "aishub", MMSI: uint32(base + i)}
		}
		return b
	}
	start := time.Now()
	for k := 0; k < 3; k++ { // three snapshots, back to back
		select {
		case p.aishubPace <- batch(k * 100):
		case <-time.After(time.Second):
			t.Fatal("the poll loop blocked handing a snapshot to delivery")
		}
	}
	for i := 0; i < 60; i++ {
		ev := <-sub.ch
		if want := uint32(i/20*100 + i%20); ev.MMSI != want {
			t.Fatalf("event %d is %d, want %d: delivery must keep order", i, ev.MMSI, want)
		}
	}
	if el := time.Since(start); el > 400*time.Millisecond {
		t.Fatalf("three snapshots took %s to deliver; the backlog must go out within one budget of the newest", el)
	}
}
