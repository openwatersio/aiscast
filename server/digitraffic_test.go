package main

import (
	"math"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestDigitrafficMapping(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.digitrafficMessage("vessels-v2/230985000/location", []byte(`{"time":1668075025,"sog":10.7,"cog":326.6,"navStat":0,"rot":0,"posAcc":true,"raim":false,"heading":325,"lon":20.345818,"lat":60.03802}`), time.Now())
	p.digitrafficMessage("vessels-v2/230985000/metadata", []byte(`{"timestamp":1668075026035,"destination":"UST LUGA","name":"ARUNA CIHAN","draught":68,"eta":733376,"posType":15,"refA":160,"refB":33,"refC":20,"refD":12,"callSign":"V7WW7","imo":9543756,"type":70}`), time.Now())
	if len(sub.ch) != 2 {
		t.Fatalf("events=%d want 2 (parse_err=%d decode_fail=%d)", len(sub.ch), p.stats.parseErr.Load(), p.stats.decodeFail.Load())
	}
	loc := <-sub.ch
	pr, ok := loc.Packet.(ais.PositionReport)
	if !ok || !loc.Synthesized || loc.MMSI != 230985000 || loc.Type != "PositionReport" {
		t.Fatalf("location event: %+v", loc)
	}
	if math.Abs(float64(pr.Latitude)-60.03802) > 1e-4 || math.Abs(float64(pr.Longitude)-20.345818) > 1e-4 || pr.TrueHeading != 325 || float64(pr.Sog) != 10.7 {
		t.Errorf("position round trip: %+v", pr)
	}
	if len(loc.Sentences) != 1 || loc.Sentences[0][:7] != "!AIVDM," {
		t.Errorf("no re-encoded sentence: %v", loc.Sentences)
	}
	if !loc.HasPos || loc.Lat == 0 {
		t.Errorf("vessel cache not updated: %+v", loc)
	}
	meta := <-sub.ch
	sd, ok := meta.Packet.(ais.ShipStaticData)
	if !ok || sd.Name != "ARUNA CIHAN" || sd.CallSign != "V7WW7" || sd.ImoNumber != 9543756 || sd.Type != 70 || sd.Destination != "UST LUGA" {
		t.Fatalf("static event: %+v", sd)
	}
	if sd.Eta != (ais.FieldETA{Month: 11, Day: 6, Hour: 3, Minute: 0}) || float64(sd.MaximumStaticDraught) != 6.8 || sd.Dimension.A != 160 {
		t.Errorf("eta/draught/dimension: %+v", sd)
	}
	// the static event routes by the cached position from the location message
	if !meta.HasPos || meta.Name != "ARUNA CIHAN" {
		t.Errorf("static not routed via cache: %+v", meta)
	}
	// wall-clock far from the message time: canonical time falls back to receive time
	if absDur(loc.Time.Sub(time.Now())) > time.Minute {
		t.Errorf("stale source time used as canonical: %v", loc.Time)
	}
}

// Real records that failed to encode and were lost: a SAR aircraft at 114 kn from Digitraffic, which
// a type 1 report cannot carry, and a BarentsWatch satellite report with an impossible course.
func TestFastAircraftAndImpossibleCourseStillBecomeEvents(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	p.digitrafficMessage("vessels-v2/111265584/location", []byte(`{"cog":194.6,"heading":511,"lat":63.790185,"lon":20.251485,"navStat":15,"posAcc":false,"raim":false,"rot":-128,"sog":114.0,"time":1790446488}`), time.Unix(1790446489, 0))
	p.barentswatchLine([]byte(`{"type":"Position","messageType":27,"courseOverGround":438,"aisClass":"A","altitude":null,"latitude":76.99333333333334,"longitude":3.2800000000000002,"navigationalStatus":5,"rateOfTurn":null,"speedOverGround":21,"trueHeading":null,"mmsi":412410064,"msgtime":"2026-09-26T17:57:32+00:00","stream":"satellite"}`), time.Date(2026, 9, 26, 17, 57, 33, 0, time.UTC))
	if len(sub.ch) != 2 {
		t.Fatalf("events=%d want 2 (decode_fail=%d)", len(sub.ch), p.stats.decodeFail.Load())
	}
	sar, ok := (<-sub.ch).Packet.(ais.StandardSearchAndRescueAircraftReport)
	if !ok || sar.Sog != 114 || math.Abs(float64(sar.Cog)-194.6) > 0.05 || math.Abs(float64(sar.Latitude)-63.790185) > 1e-4 {
		t.Fatalf("aircraft: %+v", sar)
	}
	pos := (<-sub.ch).Packet.(ais.PositionReport)
	if float64(pos.Cog) != 360 || math.Abs(float64(pos.Latitude)-76.993333) > 1e-4 || float64(pos.Sog) != 21 {
		t.Fatalf("impossible course not marked n/a with the position kept: %+v", pos)
	}
}
