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
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	now := time.Unix(1625826600, 0)
	body := `[{"ERROR":false,"USERNAME":"AH_TEST","FORMAT":"AIS","RECORDS":1},[{"MMSI":244750034,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144,"COG":3600,"SOG":0,"HEADING":511,"ROT":128,"NAVSTAT":8,"IMO":0,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"","ETA":1596}]]`
	n, err := p.ingestAishub([]byte(body), now, st, 0)
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
	n, _ = p.ingestAishub([]byte(body), now.Add(time.Minute), st, 0)
	if n != 0 || len(sub.ch) != 0 {
		t.Errorf("repeat snapshot produced %d events", n)
	}
	// error envelope
	if _, err := p.ingestAishub([]byte(`[{"ERROR":true,"ERROR_MESSAGE":"Invalid username"}]`), now, st, 0); err == nil {
		t.Error("error envelope not reported")
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
	for src, want := range map[string]bool{"udp:abc": true, "mmsi:368168720": true, "http:station-1": true, "v1:ed25519:k": true, "kystverket": false, "digitraffic": false, "aisstream": false, "aishub": false} {
		if got := feedable(&Event{Source: src, Packet: pkt}); got != want {
			t.Errorf("feedable(%s) = %v, want %v", src, got, want)
		}
	}
	if feedable(&Event{Source: "udp:abc", Packet: pkt, Synthesized: true}) {
		t.Error("synthesized event feedable")
	}
}

func TestAishubPacing(t *testing.T) {
	p := testPipeline(t)
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"MMSI":%d,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144}`, 200000000+i)
	}
	body := "[[" + strings.Join(rows, ",") + "]]"
	start := time.Now()
	n, err := p.ingestAishub([]byte(body), start, st, 200*time.Millisecond)
	if err != nil || n != 20 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// 20 rows over 200 ms: the last row waits 190 ms, so anything under that means no pacing; the upper bound
	// only guards against a runaway sleep, loose enough for a slow CI runner
	if el := time.Since(start); el < 190*time.Millisecond || el > 2*time.Second {
		t.Errorf("20 rows over 200ms took %s", el)
	}
}
