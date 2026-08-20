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
	p.digitrafficMessage("vessels-v2/230985000/location", []byte(`{"time":1668075025,"sog":10.7,"cog":326.6,"navStat":0,"rot":0,"posAcc":true,"raim":false,"heading":325,"lon":20.345818,"lat":60.03802}`))
	p.digitrafficMessage("vessels-v2/230985000/metadata", []byte(`{"timestamp":1668075026035,"destination":"UST LUGA","name":"ARUNA CIHAN","draught":68,"eta":733376,"posType":15,"refA":160,"refB":33,"refC":20,"refD":12,"callSign":"V7WW7","imo":9543756,"type":70}`))
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
