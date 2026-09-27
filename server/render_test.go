package main

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func renderedEvent(t testing.TB) *Event {
	p := testPipeline(nil)
	sub := p.subscribe()
	p.Ingest(Reception{Source: "kystverket", Station: "kystverket", RecvTime: time.Now(), Body: "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"})
	select {
	case ev := <-sub.ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("no event")
		return nil
	}
}

// Every subscriber gets the same bytes the per-client marshal produced, from one shared render.
func TestRenderOncePerEvent(t *testing.T) {
	ev := renderedEvent(t)
	want, _ := json.Marshal(renderV1(ev))
	first, second := ev.renderV1JSON(), ev.renderV1JSON()
	if !bytes.Equal(first, want) {
		t.Fatalf("v1 frame\n got %s\nwant %s", first, want)
	}
	if &first[0] != &second[0] {
		t.Error("v1 frame rendered twice")
	}
	if got := ev.renderNMEA(); string(got) != ev.nmeaText() || &got[0] != &ev.renderNMEA()[0] {
		t.Errorf("nmea frame %q, want one shared render of %q", got, ev.nmeaText())
	}
}

// BenchmarkV1Fanout is the per-event rendering cost of the /v1 writers at today's stream count.
func BenchmarkV1Fanout(b *testing.B) {
	ev := renderedEvent(b)
	b.ReportAllocs()
	for b.Loop() {
		ev.v1Once, ev.v1 = sync.Once{}, nil // a fresh event each round
		for range 125 {
			_ = ev.renderV1JSON()
		}
	}
}
