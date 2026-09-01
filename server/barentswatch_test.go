package main

import (
	"math"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func bwLine(t *testing.T, p *Pipeline, line string) {
	t.Helper()
	p.barentswatchLine([]byte(line), time.Now())
}

func TestBarentswatchMapping(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Now().UTC().Format(time.RFC3339)
	bwLine(t, p, `{"type":"Position","messageType":1,"courseOverGround":339.9,"aisClass":"A","altitude":null,"latitude":69.909472,"longitude":20.163188,"navigationalStatus":7,"rateOfTurn":0,"speedOverGround":6.5,"trueHeading":245,"mmsi":257152520,"msgtime":"`+now+`","stream":"terra"}`)
	bwLine(t, p, `{"type":"Staticdata","messageType":5,"mmsi":257152520,"msgtime":"`+now+`","imoNumber":null,"callSign":"LM8196","destination":"Tromsø","eta":"12251830","name":"OLIVIA","draught":68,"shipLength":10,"shipWidth":3,"shipType":90,"dimensionA":3,"dimensionB":7,"dimensionC":2,"dimensionD":1,"positionFixingDeviceType":1,"reportClass":"A","stream":"terra"}`)
	bwLine(t, p, `{"type":"Aton","messageType":21,"mmsi":992581027,"msgtime":"`+now+`","dimensionA":0,"dimensionB":0,"dimensionC":0,"dimensionD":0,"typeOfAidsToNavigation":3,"latitude":61.34395,"longitude":2.238663,"name":"HYWIND TAMPEN HY10","typeOfElectronicFixingDevice":1,"stream":"offshore"}`)
	bwLine(t, p, `{"type":"BinaryBroadcastMessageMetHyd","messageType":8,"mmsi":990000001,"msgtime":"`+now+`","stream":"terra"}`) // ignored
	if len(sub.ch) != 3 {
		t.Fatalf("events=%d want 3 (parse_err=%d decode_fail=%d)", len(sub.ch), p.stats.parseErr.Load(), p.stats.decodeFail.Load())
	}
	pos := <-sub.ch
	pr, ok := pos.Packet.(ais.PositionReport)
	if !ok || !pos.Synthesized || pos.MMSI != 257152520 || pos.Station != "barentswatch/terra" {
		t.Fatalf("position event: %+v", pos)
	}
	if math.Abs(float64(pr.Latitude)-69.909472) > 1e-4 || pr.TrueHeading != 245 || float64(pr.Sog) != 6.5 || pr.NavigationalStatus != 7 || pr.RateOfTurn != 0 {
		t.Errorf("position round trip: %+v", pr)
	}
	static := <-sub.ch
	sd, ok := static.Packet.(ais.ShipStaticData)
	// "Tromsø" must survive as valid 6-bit text, not kill the whole packet in the encoder
	if !ok || sd.Name != "OLIVIA" || sd.CallSign != "LM8196" || sd.Destination != "TROMS?" || sd.Type != 90 {
		t.Fatalf("static event: %+v", sd)
	}
	if sd.Eta != (ais.FieldETA{Month: 12, Day: 25, Hour: 18, Minute: 30}) || float64(sd.MaximumStaticDraught) != 6.8 || sd.Dimension.A != 3 {
		t.Errorf("eta/draught/dimension: %+v", sd)
	}
	aton := <-sub.ch
	an, ok := aton.Packet.(ais.AidsToNavigationReport)
	if !ok || an.Name != "HYWIND TAMPEN HY10" || an.Type != 3 {
		t.Fatalf("aton event: %+v", an)
	}
}

func TestBarentswatchClassB(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Now().UTC().Format(time.RFC3339)
	// nulls stay AIS "not available": COG 360, SOG 102.3, heading 511
	bwLine(t, p, `{"type":"Position","messageType":18,"courseOverGround":null,"aisClass":"B","latitude":59.0,"longitude":5.0,"navigationalStatus":null,"rateOfTurn":null,"speedOverGround":null,"trueHeading":null,"mmsi":257000001,"msgtime":"`+now+`","stream":"terra"}`)
	if len(sub.ch) != 1 {
		t.Fatalf("events=%d want 1", len(sub.ch))
	}
	ev := <-sub.ch
	pr, ok := ev.Packet.(ais.StandardClassBPositionReport)
	if !ok || float64(pr.Cog) != 360 || float64(pr.Sog) != 102.3 || pr.TrueHeading != 511 {
		t.Fatalf("class B event: %+v", ev.Packet)
	}
}

func TestBarentswatchEdgeCases(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Now().UTC().Format(time.RFC3339)
	// SAR aircraft at 150 kt: type 9 whole-knot SOG, not a type 1 whose encoder rejects >102.2
	bwLine(t, p, `{"type":"Position","messageType":9,"aisClass":"A","latitude":69.0,"longitude":18.0,"courseOverGround":90,"speedOverGround":150,"rateOfTurn":null,"mmsi":111257001,"msgtime":"`+now+`","stream":"terra"}`)
	// class A with null ROT → n/a (-128), and a null position line is dropped without an event
	bwLine(t, p, `{"type":"Position","messageType":1,"aisClass":"A","latitude":60.0,"longitude":5.0,"rateOfTurn":null,"speedOverGround":1,"mmsi":257000002,"msgtime":"`+now+`","stream":"terra"}`)
	bwLine(t, p, `{"type":"Position","messageType":1,"aisClass":"A","latitude":null,"longitude":null,"mmsi":257000003,"msgtime":"`+now+`","stream":"terra"}`)
	if len(sub.ch) != 2 {
		t.Fatalf("events=%d want 2 (parse_err=%d decode_fail=%d)", len(sub.ch), p.stats.parseErr.Load(), p.stats.decodeFail.Load())
	}
	sar := <-sub.ch
	sp, ok := sar.Packet.(ais.StandardSearchAndRescueAircraftReport)
	if !ok || sp.Sog != 150 {
		t.Fatalf("sar event: %+v", sar.Packet)
	}
	if !sar.HasPos || sar.Lat == 0 {
		t.Errorf("sar position not cached: %+v", sar)
	}
	cls := <-sub.ch
	if pr, ok := cls.Packet.(ais.PositionReport); !ok || pr.RateOfTurn != -128 {
		t.Errorf("null ROT not n/a: %+v", cls.Packet)
	}
	// a class A vessel claiming 120 kt still encodes (SOG capped to the type-1 n/a value) instead of vanishing
	bwLine(t, p, `{"type":"Position","messageType":1,"aisClass":"A","latitude":60.5,"longitude":5.5,"speedOverGround":120,"rateOfTurn":null,"mmsi":257000005,"msgtime":"`+now+`","stream":"terra"}`)
	if len(sub.ch) != 1 {
		t.Fatalf("high-SOG class A dropped (decode_fail=%d)", p.stats.decodeFail.Load())
	}
	if pr, ok := (<-sub.ch).Packet.(ais.PositionReport); !ok || float64(pr.Sog) != 102.3 {
		t.Errorf("high SOG not capped: %+v", pr)
	}
}

// A future msgtime must not fold into the vessel's clock: it would mark every later genuine report stale
// until wall time caught up, and survive snapshot restarts.
func TestBarentswatchFutureTimestamp(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	line := func(at time.Time, lat string) string {
		return `{"type":"Position","messageType":1,"aisClass":"A","latitude":` + lat + `,"longitude":5.0,"speedOverGround":1,"mmsi":257000004,"msgtime":"` + at.UTC().Format(time.RFC3339) + `","stream":"terra"}`
	}
	base := time.Now()
	p.barentswatchLine([]byte(line(base.Add(time.Hour), "59.0")), base) // corrupted future stamp: capped to wall time
	if len(sub.ch) != 1 {
		t.Fatalf("events=%d want 1", len(sub.ch))
	}
	if ev := <-sub.ch; absDur(ev.Time.Sub(base)) > time.Minute {
		t.Fatalf("future stamp used as canonical: %v", ev.Time)
	}
	p.vmu.Lock() // stand in for wall time passing: the vessel clock sits 2 s back from the next report
	p.vessels[257000004].Seen = p.vessels[257000004].Seen.Add(-2 * time.Second)
	p.vessels[257000004].PosAt = p.vessels[257000004].PosAt.Add(-2 * time.Second)
	p.vmu.Unlock()
	p.barentswatchLine([]byte(line(time.Now(), "59.1")), time.Now())
	if len(sub.ch) != 1 { // the next genuine report still flows
		t.Fatalf("later report suppressed (stale=%d)", p.stats.stale.Load())
	}
}

// A rebuilt copy of a transmission another source already delivered is withheld (same message time, so it
// does not advance the vessel's clock); the next transmission only BarentsWatch hears flows immediately.
func TestBarentswatchCopiesWithheld(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	tx := time.Now().Add(-10 * time.Second) // the vessel transmitted, kystverket delivered it first
	p.ingestPacket("kystverket", "kystverket", tx, ais.PositionReport{
		Header: ais.Header{MessageID: 1, UserID: 257152520}, Valid: true,
		Latitude: 69.9, Longitude: 20.1, Cog: 340, Sog: 6, TrueHeading: 245, NavigationalStatus: 7, RateOfTurn: -128,
	})
	if len(sub.ch) != 1 {
		t.Fatalf("kystverket event missing")
	}
	<-sub.ch
	line := func(at time.Time) string {
		return `{"type":"Position","messageType":1,"aisClass":"A","latitude":69.909472,"longitude":20.163188,"navigationalStatus":7,"speedOverGround":6.5,"trueHeading":245,"mmsi":257152520,"msgtime":"` + at.UTC().Format(time.RFC3339) + `","stream":"terra"}`
	}
	bwLine(t, p, line(tx)) // barentswatch's copy of the same transmission, delivered seconds later
	if len(sub.ch) != 0 {
		t.Fatalf("rebuilt copy not withheld (stale=%d)", p.stats.stale.Load())
	}
	bwLine(t, p, line(tx.Add(6*time.Second))) // kystverket died; the next transmission arrives here only
	if len(sub.ch) != 1 {
		t.Fatalf("next transmission did not take over")
	}
}
