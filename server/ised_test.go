package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeISED answers the registry: one boat with a record, one with none, one the service errors on.
func fakeISED(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	requests := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("mmsi") {
		case "316061185":
			// stray spaces, as the real service writes them
			fmt.Fprint(w, `{"records":[{"mmsi":" 316061185","vesselName":"RUBY'S STAR","vesselId":null,"callSign":"CFN5678"}],"moreRecordsAvailable":false}`)
		case "316999991", "316999992", "316999993", "316999994":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			fmt.Fprint(w, `{"records":[],"moreRecordsAvailable":false}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

func TestISEDRoundsAndServe(t *testing.T) {
	p := storePipeline(t)
	url, requests := fakeISED(t)
	isedPause = 0
	now := time.Now().UTC()

	// Two Canadian vessels and one American, which the rounds must never ask about.
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(316061185, 49.3, -123.1))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(316061185, "RUBYS STAR", ""))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(316000002, "NO RECORD BOAT", ""))
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(366000777, "AMERICAN BOAT", ""))
	mustFlush(t, p)
	// A Canadian base station is not a vessel and must never cost a registry ask.
	if _, err := p.store.db.Exec(`UPDATE vessels SET kind = 'base' WHERE mmsi = 316000002`); err != nil {
		t.Fatal(err)
	}
	p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(316000003, "NO RECORD BOAT", ""))
	mustFlush(t, p)

	if n := p.backfillISED(now, url, time.Minute); n != 2 {
		t.Fatalf("checked %d vessels, want 2", n)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("asked %d times, want 2: the US vessel must stay out", got)
	}
	// Both answers are stored, so the next round asks nothing.
	if n := p.backfillISED(now, url, time.Minute); n != 0 || requests.Load() != 2 {
		t.Errorf("re-asked within the season: n=%d requests=%d", n, requests.Load())
	}
	// After the recheck window both are due again, and the fleet gauge stays a count, not a tally.
	if n := p.backfillISED(now.Add(isedRecheck+time.Hour), url, time.Minute); n != 2 {
		t.Errorf("recheck round checked %d, want 2", n)
	}
	if g := p.ised.ships.Load(); g != 1 {
		t.Errorf("ships gauge %d after a recheck, want 1", g)
	}

	type props struct {
		Properties struct {
			Particulars *particulars         `json:"particulars"`
			Provenance  map[string]string    `json:"provenance"`
			Sources     map[string]sourceRef `json:"sources"`
		} `json:"properties"`
	}
	var f props
	json.Unmarshal(get(t, p, "/v1/vessels/316061185").Body.Bytes(), &f)
	m := f.Properties.Particulars
	if m == nil || m.RegisteredName != "RUBY'S STAR" || m.CallSign != "CFN5678" || m.Registry != "Canada" {
		t.Errorf("particulars: %+v", m)
	}
	if f.Properties.Provenance["registered_name"] != "ised" || f.Properties.Provenance["callsign"] != "ised" ||
		f.Properties.Sources["ised"].URL != "https://ised-isde.canada.ca/mmsi-ismm/eng/shipSearch.html?mmsi=316061185" {
		t.Errorf("provenance %v sources %+v", f.Properties.Provenance, f.Properties.Sources)
	}
	// A no-record answer serves nothing, and stays answered.
	f = props{}
	json.Unmarshal(get(t, p, "/v1/vessels/316000002").Body.Bytes(), &f)
	if f.Properties.Sources["ised"].Credit != "" {
		t.Errorf("a no-record vessel got the registry: %+v", f.Properties.Sources)
	}
	if err := p.loadISEDStats(); err != nil || p.ised.ships.Load() != 1 {
		t.Errorf("stats from boot: %d, %v", p.ised.ships.Load(), err)
	}
}

// Repeated service failures end the round rather than hammering a down registry: exactly the cutoff
// number of asks, and the vessels behind them wait for the next round.
func TestISEDFailuresEndRound(t *testing.T) {
	p := storePipeline(t)
	url, requests := fakeISED(t)
	isedPause = 0
	now := time.Now().UTC()
	// Four failing vessels heard recently, and a good one heard earlier, so the round meets the
	// failures first and must stop before reaching it.
	p.ingestPacket("kystverket", "kystverket", now.Add(-time.Hour), now.Add(-time.Hour), staticCallSign(316061185, "RUBYS STAR", ""))
	for _, mmsi := range []uint32{316999991, 316999992, 316999993, 316999994} {
		p.ingestPacket("kystverket", "kystverket", now, now, staticCallSign(mmsi, "BOOM BOAT", ""))
	}
	mustFlush(t, p)
	if n := p.backfillISED(now, url, time.Minute); n != 0 {
		t.Errorf("checked %d, want 0", n)
	}
	if got := requests.Load(); got != int64(isedFailuresInARow) {
		t.Errorf("made %d asks, want exactly the cutoff %d", got, isedFailuresInARow)
	}
	if p.ised.failures.Load() != int64(isedFailuresInARow) {
		t.Errorf("failures %d, want %d", p.ised.failures.Load(), isedFailuresInARow)
	}
}
