package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func unmappedKeys() map[string]bool {
	out := map[string]bool{}
	unmappedFld.Range(func(k, _ any) bool { out[k.(string)] = true; return true })
	unmappedType.Range(func(k, _ any) bool { out["type\t"+k.(string)] = true; return true })
	return out
}

// TestFixtureCorpusFullyCaptured runs real archived records from every source through the live
// adapters with sampling off: any field or record type outside the capture set and the waiver
// ledger fails the build. Extend testdata/sources when a source grows a field, or the ledger
// when declining one; silence is never an option.
func TestFixtureCorpusFullyCaptured(t *testing.T) {
	prev := shadowEvery
	shadowEvery = 1
	defer func() { shadowEvery = prev }()
	before := unmappedKeys()

	p := testPipeline(t)
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
	recv := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	sources := map[string]string{
		"kystverket.lines":   "kystverket",
		"barentswatch.lines": "barentswatch",
		"digitraffic.lines":  "digitraffic",
		"aisstream.lines":    "aisstream",
		"aishub.lines":       "aishub",
		"catcher.lines":      "http:test-station",
	}
	for name, source := range sources {
		for _, rec := range fixtureLines(t, name) {
			dispatch(p, source, Reception{Source: source, Station: rec.station, RecvTime: recv, Body: rec.body}, st)
		}
	}

	var bad []string
	for k := range unmappedKeys() {
		if !before[k] {
			bad = append(bad, strings.ReplaceAll(k, "\t", ": "))
		}
	}
	if len(bad) != 0 {
		sort.Strings(bad)
		t.Fatalf("source data outside the capture set and waiver ledger:\n  %s", strings.Join(bad, "\n  "))
	}
}

func TestShadowFlagsANovelField(t *testing.T) {
	shadowCheck("shadow-test", []byte(`{"brandNew": 1, "known": 2, "nest": {"deep": 3}}`), fieldSet{"known": nil, "nest": fieldSet{}})
	if _, ok := unmappedFld.Load("shadow-test\tbrandNew"); !ok {
		t.Fatal("novel field not flagged")
	}
	if _, ok := unmappedFld.Load("shadow-test\tknown"); ok {
		t.Fatal("known field flagged")
	}
	if _, ok := unmappedFld.Load("shadow-test\tnest.deep"); !ok {
		t.Fatal("novel nested field not flagged")
	}
}

// MetHyd is archived verbatim, so a new weather field would reach the stream and vanish at the
// packager's fixed columns. The capture check must see it.
func TestMetHydNovelFieldIsFlagged(t *testing.T) {
	prev := shadowEvery
	shadowEvery = 1
	defer func() { shadowEvery = prev }()
	p := testPipeline(t)
	for _, rec := range fixtureLines(t, "barentswatch.lines") {
		if !strings.Contains(rec.body, "BinaryBroadcastMessageMetHyd") {
			continue
		}
		body := strings.Replace(rec.body, `{`, `{"uvIndex":3,`, 1)
		p.barentswatchLine([]byte(body), time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
		if _, ok := unmappedFld.Load("barentswatch/methyd\tuvIndex"); !ok {
			t.Fatal("a weather field outside the contract went unreported")
		}
		return
	}
	t.Fatal("fixture has no MetHyd record; resample testdata/sources/barentswatch.lines")
}

// A sender rotating field names must not grow the tracked set, and /metrics with it, without bound.
func TestUnmappedFieldsAreBounded(t *testing.T) {
	t.Cleanup(func() { // give the cap back so later tests still see their own names
		unmappedFld.Range(func(k, _ any) bool {
			if strings.HasPrefix(k.(string), "rotating-test\t") {
				unmappedFld.Delete(k)
				unmappedN.Add(-1)
			}
			return true
		})
	})
	for i := 0; i < 2*maxUnmapped; i++ {
		flagUnmapped("rotating-test", fmt.Sprintf("nonce%d", i))
	}
	n := 0
	unmappedFld.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), "rotating-test\t") {
			n++
		}
		return true
	})
	if n > maxUnmapped+1 {
		t.Fatalf("tracked %d distinct fields for one sender, cap is %d plus overflow", n, maxUnmapped)
	}
	if _, ok := unmappedFld.Load("rotating-test\t(other)"); !ok {
		t.Fatal("names past the cap were not counted under (other)")
	}
}
