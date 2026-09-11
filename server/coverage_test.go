package main

import (
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
	shadowCheck("shadow-test", []byte(`{"brandNew": 1, "known": 2}`), map[string]bool{"known": true})
	if _, ok := unmappedFld.Load("shadow-test\tbrandNew"); !ok {
		t.Fatal("novel field not flagged")
	}
	if _, ok := unmappedFld.Load("shadow-test\tknown"); ok {
		t.Fatal("known field flagged")
	}
}
