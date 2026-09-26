package main

import (
	"fmt"
	"testing"
	"time"
)

// A deploy restarts the server while receptions keep arriving. Each must land in both archives or
// in neither: otherwise the normalized stream holds events its raw archive lost, and replay can no
// longer regenerate it across the restart.
func TestShutdownKeepsArchivesConsistent(t *testing.T) {
	rawDir, normDir := t.TempDir(), t.TempDir()
	p := testPipeline(t)
	p.arch = newArchive(rawDir, nil)
	p.norm = newNormArchive(normDir, nil)
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

	out := t.TempDir()
	runReplay([]string{"-archive", rawDir, "-out", out, "-from", "2026-09-01", "-to", "2026-09-02"})
	rep := diffNorm(loadNorm(normDir), loadNorm(out))
	if len(rep.live.events) == 0 {
		t.Fatal("the producer never ran")
	}
	if !rep.clean() {
		t.Fatalf("normalized output diverged from its raw archive across shutdown:\n%s", rep.render(3))
	}
}
