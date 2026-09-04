package main

import (
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestBroadcastDelay(t *testing.T) {
	t0 := time.Date(2026, 9, 4, 12, 0, 30, 0, time.UTC)
	for _, c := range []struct {
		sec  uint8
		want float64
	}{
		{25, 5},  // same minute
		{33, 0},  // slight skew reads as zero, never negative
		{50, 40}, // previous minute
	} {
		if got := broadcastDelay(c.sec, t0, t0); got != c.want {
			t.Errorf("sec %d: got %v want %v", c.sec, got, c.want)
		}
	}
}

func TestDelayStats(t *testing.T) {
	var d delayStats
	now := time.Date(2026, 9, 4, 12, 0, 30, 0, time.UTC)
	for i := uint8(0); i < 30; i++ {
		d.observe(&Event{Source: "udp:x", Time: now, Packet: ais.PositionReport{Timestamp: i}}, now)
	}
	d.observe(&Event{Source: "udp:y", Time: now, Packet: ais.PositionReport{Timestamp: 60}}, now) // unavailable
	d.observe(&Event{Source: "udp:y", Time: now, Packet: ais.ShipStaticData{}}, now)              // no timestamp
	if got := d.snapshot("udp"); got["n"] != 30 || got["p50"] != 16.0 || got["p99"] != 30.0 {
		t.Errorf("udp: %v", got)
	}
	if got := d.snapshot("aishub"); got != nil {
		t.Errorf("unheard kind should have no delay: %v", got)
	}
}

func TestDelayRingWindow(t *testing.T) {
	var r delayRing
	for i := 0; i < delaySamples+100; i++ {
		r.add(float64(i))
	}
	// Only the newest 512 samples (100..611) remain.
	if got := r.percentiles(); got["n"] != delaySamples || got["p50"] != 356.0 || got["p99"] != 606.0 {
		t.Errorf("window: %v", got)
	}
}
