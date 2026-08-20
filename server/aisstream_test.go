package main

import (
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestAisstreamEnvelopeMapping(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	// an envelope as aisstream emits it (the golden from the research doc)
	body := `{"Message":{"PositionReport":{"Cog":360,"CommunicationState":81982,"Latitude":55.990715,"Longitude":-3.183045,"MessageID":3,"NavigationalStatus":3,"PositionAccuracy":false,"Raim":false,"RateOfTurn":-128,"RepeatIndicator":0,"Sog":0,"Spare":0,"SpecialManoeuvreIndicator":0,"Timestamp":36,"TrueHeading":511,"UserID":250013879,"Valid":true}},"MessageType":"PositionReport","MetaData":{"MMSI":250013879,"MMSI_String":250013879,"ShipName":"DINOPOTES           ","latitude":55.990715,"longitude":-3.183045,"time_utc":"2023-10-22 22:47:36.94034384 +0000 UTC"}}`
	now := time.Date(2023, 10, 22, 22, 47, 40, 0, time.UTC)
	p.aisstreamMessage([]byte(body), now)
	p.aisstreamMessage([]byte(body), now) // duplicate
	p.aisstreamMessage([]byte(`{"MessageType":"UnknownMessage","Message":{"UnknownMessage":{}}}`), now)
	if len(sub.ch) != 1 {
		t.Fatalf("events=%d want 1 (parse_err=%d decode_fail=%d dup=%d)", len(sub.ch), p.stats.parseErr.Load(), p.stats.decodeFail.Load(), p.stats.dup.Load())
	}
	ev := <-sub.ch
	pr := ev.Packet.(ais.PositionReport)
	if ev.MMSI != 250013879 || pr.MessageID != 3 || pr.NavigationalStatus != 3 || !ev.Synthesized || ev.Source != "aisstream" {
		t.Errorf("event: %+v", ev)
	}
	if ev.Time != time.Date(2023, 10, 22, 22, 47, 36, 940343840, time.UTC) {
		t.Errorf("time_utc not used as canonical time: %v", ev.Time)
	}
	if float64(pr.Latitude) != 55.990715 || float64(pr.Longitude) != -3.183045 {
		t.Errorf("position round trip: %v %v", pr.Latitude, pr.Longitude)
	}
}
