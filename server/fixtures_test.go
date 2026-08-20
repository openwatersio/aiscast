package main

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

// Public corpora: GPSD's sample.aivdm (many message types, odd receivers) and libais's tagblock.nmea
// (TAG blocks with g:/q:/T: fields and interleaved multipart groups).
func TestPublicFixtures(t *testing.T) {
	for _, tc := range []struct {
		file      string
		minEvents int
	}{{"testdata/gpsd_sample.aivdm", 95}, {"testdata/tagblock.nmea", 3}} {
		p := testPipeline(t)
		sub := p.subscribe()
		f, err := os.Open(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		lines := 0
		for sc.Scan() {
			l := sc.Text()
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			lines++
			p.Ingest(Reception{Source: "fx", Station: "fx", RecvTime: time.Unix(1418169601, 0), Body: l})
		}
		f.Close()
		types := map[string]int{}
		n := 0
		for len(sub.ch) > 0 {
			ev := <-sub.ch
			n++
			types[ev.Type]++
		}
		t.Logf("%s: lines=%d events=%d parse_err=%d decode_fail=%d dup=%d types=%v", tc.file, lines, n,
			p.stats.parseErr.Load(), p.stats.decodeFail.Load(), p.stats.dup.Load(), types)
		if n < tc.minEvents {
			t.Errorf("%s: events=%d want ≥%d", tc.file, n, tc.minEvents)
		}
	}
}
