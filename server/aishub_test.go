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
	st := newAishubState()
	now := time.Unix(1625826600, 0)
	body := `[{"ERROR":false,"USERNAME":"AH_TEST","FORMAT":"AIS","RECORDS":1},[{"MMSI":244750034,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144,"COG":3600,"SOG":0,"HEADING":511,"ROT":128,"NAVSTAT":8,"IMO":0,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"","ETA":1596}]]`
	n, err := p.ingestAishub([]byte(body), now, st)
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
	n, _ = p.ingestAishub([]byte(body), now.Add(time.Minute), st)
	if n != 0 || len(sub.ch) != 0 {
		t.Errorf("repeat snapshot produced %d events", n)
	}
	// error envelope
	if _, err := p.ingestAishub([]byte(`[{"ERROR":true,"ERROR_MESSAGE":"Invalid username"}]`), now, st); err == nil {
		t.Error("error envelope not reported")
	}
}

// AISHub's aggregate flips a vessel's static fields between two stations' versions while TIME, the
// canonical time, stands still. Re-emitting the flip-back would repeat an (id, time) the archive and
// stream already carry; once TIME advances the current version must flow again.
func TestAishubStaticFlipBack(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	st := newAishubState()
	now := time.Unix(1625826600, 0)
	row := func(time, dest string) string {
		return fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%s","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"%s","ETA":1596}]]`, time, dest)
	}
	if n, err := p.ingestAishub([]byte(row("1625826523", "NLRTM")), now, st); err != nil || n != 2 {
		t.Fatalf("first snapshot: n=%d err=%v", n, err) // position + static
	}
	if n, _ := p.ingestAishub([]byte(row("1625826523", "NLAMS")), now.Add(time.Minute), st); n != 1 {
		t.Fatalf("changed static: n=%d, want 1", n)
	}
	// the flip back: same static, same TIME as its first emission, minutes later
	if n, _ := p.ingestAishub([]byte(row("1625826523", "NLRTM")), now.Add(3*time.Minute), st); n != 0 {
		t.Errorf("flip-back re-emitted: n=%d, want 0", n)
	}
	// TIME advanced: the flip-back is a fresh (id, time) and must reach subscribers again
	if n, _ := p.ingestAishub([]byte(row("1625826583", "NLRTM")), now.Add(4*time.Minute), st); n != 2 {
		t.Errorf("static after TIME advance: n=%d, want 2", n)
	}
	// Broadcast sees position@523, static NLRTM@523, position@583, static NLRTM@583: the NLAMS
	// static shares the vessel's static time, so it is archived stale and never broadcast.
	recv := func() *Event {
		select {
		case ev := <-sub.ch:
			return ev
		case <-time.After(time.Second):
			t.Fatal("subscriber starved: expected another broadcast event")
			return nil
		}
	}
	for i := 0; i < 3; i++ {
		recv()
	}
	last := recv()
	if sd, ok := last.Packet.(ais.ShipStaticData); !ok || sd.Destination != "NLRTM" || last.Time.Unix() != 1625826583 {
		t.Errorf("last event: %+v", last)
	}
}

// A future-stamped row's canonical time is capped to the receive time by ingestPacket, so the
// flip-back window must track the capped time: keyed on the raw future TIME it would suppress a
// flip-back whose actual (id, time) is unique, because each snapshot's receive time differs.
func TestAishubFutureStampFlipBack(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	st := newAishubState()
	now := time.Unix(1625826600, 0)
	row := func(dest string) string { // TIME five minutes ahead of every snapshot's receive time
		return fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"1625826900","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"%s","ETA":1596}]]`, dest)
	}
	if n, err := p.ingestAishub([]byte(row("NLRTM")), now, st); err != nil || n != 2 {
		t.Fatalf("first snapshot: n=%d err=%v", n, err)
	}
	if ev := <-sub.ch; !ev.Time.Equal(now) {
		t.Errorf("future stamp not capped to receive time: %v", ev.Time)
	}
	if n, _ := p.ingestAishub([]byte(row("NLAMS")), now.Add(time.Minute), st); n != 1 {
		t.Fatalf("changed static: n=%d, want 1", n)
	}
	// the flip back: TIME is still the same future stamp, but the canonical time is this
	// snapshot's receive time, so the key is fresh and the static must be emitted
	if n, _ := p.ingestAishub([]byte(row("NLRTM")), now.Add(2*time.Minute), st); n != 1 {
		t.Errorf("flip-back with a future stamp: n=%d, want 1", n)
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
	p.aishubPace = make(chan []*Event, 4)
	go p.deliverPaced(p.aishubPace, 200*time.Millisecond)
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"MMSI":%d,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144}`, 200000000+i)
	}
	start := time.Now()
	n, err := p.aishubSnapshot([]byte("[["+strings.Join(rows, ",")+"]]"), time.Unix(1625826600, 0), newAishubState())
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
		rows[i] = fmt.Sprintf(`{"MMSI":%d,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144}`, 200000000+i)
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
	if !p.admit() {
		t.Fatal("not admitted")
	}
	_, err := p.ingestAishub(body, time.Now(), newAishubState())
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
