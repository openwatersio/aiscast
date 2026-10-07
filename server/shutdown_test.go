package main

import (
	"fmt"
	"testing"
	"time"
)

// A deploy restarts the server while receptions keep arriving. Each must reach both the raw archive and ClickHouse
// or neither: otherwise ClickHouse holds copies its raw archive lost, and replay can no longer rebuild that day.
func TestShutdownKeepsArchiveAndRecordConsistent(t *testing.T) {
	rawDir := t.TempDir()
	p, written := recordingPipeline(t)
	p.arch = newArchive(rawDir, nil)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	go func() {
		for i := 0; !p.closing.Load(); i++ {
			recv := base.Add(time.Duration(i) * time.Millisecond)
			body := fmt.Sprintf(`{"time":%d,"lat":60.1,"lon":10.5,"sog":5.0,"cog":90.0,"heading":90,"navStat":0}`, recv.Unix())
			p.digitrafficMessage(fmt.Sprintf("vessels-v2/%d/location", 230000000+i), []byte(body), recv)
		}
	}()
	time.Sleep(50 * time.Millisecond) // shut down mid-stream, the way a deploy does
	p.closeArchives()
	live := written()

	rp, replayed := recordingPipeline(t)
	day := base.Truncate(24 * time.Hour)
	readers, err := collectReaders(rawDir, day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayReaders(rp, readers, day.AddDate(0, 0, 1), nil); err != nil {
		t.Fatal(err)
	}
	key := func(pts []trackPoint) map[string]bool {
		out := map[string]bool{}
		for _, pt := range pts {
			out[fmt.Sprint(pt.mmsi, pt.ts.UnixMilli(), pt.recv.UnixMilli())] = true
		}
		return out
	}
	a, b := key(live), key(replayed())
	if len(a) == 0 {
		t.Fatal("the producer never ran")
	}
	if len(a) != len(b) {
		t.Fatalf("live wrote %d copies, the raw archive replays to %d, across a shutdown", len(a), len(b))
	}
	for k := range a {
		if !b[k] {
			t.Fatalf("live wrote %s, which the raw archive lost", k)
		}
	}
}
