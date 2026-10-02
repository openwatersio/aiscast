package main

import (
	"fmt"
	"reflect"
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
			dispatch(p, source, Reception{Source: source, Station: rec.station, RecvTime: recv, Body: rec.body})
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

// AIS-catcher's full JSON: each message's decoded fields and the receiver's details beside the
// sentence. A real envelope (station position and id replaced) passes the capture check, and a field
// new at the envelope level, outside the waived message fields, is still flagged.
func TestCatcherFullJSONIsCapturedOrWaived(t *testing.T) {
	env := `{"protocol":"jsonaiscatcher","encodetime":"20260927000012","stationid":"test-station","station_lat":0.0,"station_lon":0.0,"receiver":{"description":"AIS-catcher v0.70","version":70,"engine":null,"setting":null},"device":{"product":null,"vendor":null,"serial":null,"setting":null},"msgs":[{"class":"AIS","device":"AIS-catcher","version":70,"driver":1,"hardware":"RTL2838UHIDIR","rxtime":"20260926235957","rxuxtime":1790467197.359632,"scaled":true,"channel":"B","nmea":["!AIVDM,1,1,,B,14eG;u@000o<Ev:L2bEbEQah08QS,0*4A"],"signalpower":-37.397194,"ppm":-0.289352,"type":1,"repeat":0,"mmsi":316001269,"country":"Canada","country_code":"CA","status":0,"status_text":"Under way using engine","turn_unscaled":0,"turn":0,"speed":0,"accuracy":true,"lon":-123.132683,"lat":49.006222,"course":264.600006,"heading":52,"second":56,"maneuver":0,"power":false,"raim":false,"radio":34915,"sync_state":0,"slot_timeout":2,"slot_number":2147},{"class":"AIS","device":"AIS-catcher","version":70,"driver":1,"hardware":"RTL2838UHIDIR","rxtime":"20260927000000","rxuxtime":1790467200.51887,"scaled":true,"channel":"B","nmea":["!AIVDM,1,1,,B,14eGkT002lo<6sfL22EC<2Qn080A,0*11"],"signalpower":-36.3507,"ppm":1.736111,"type":1,"repeat":0,"mmsi":316011408,"country":"Canada","country_code":"CA","status":0,"status_text":"Under way using engine","turn_unscaled":0,"turn":0,"speed":18,"accuracy":true,"lon":-123.184013,"lat":48.989155,"course":81.599998,"heading":80,"second":59,"maneuver":0,"power":false,"raim":false,"radio":32785,"sync_state":0,"slot_timeout":2,"slot_number":17}]}`
	site := "catcher-full-test"
	known := withWaivers("catcher", walkStruct(reflect.TypeOf(jsonaiscatcher{})))
	shadowCheck(site, []byte(env), known)
	var leaked []string
	unmappedFld.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), site+"\t") {
			leaked = append(leaked, k.(string))
		}
		return true
	})
	if len(leaked) > 0 {
		t.Fatalf("real full-JSON envelope flagged %v", leaked)
	}
	shadowCheck(site, []byte(`{"protocol":"jsonaiscatcher","brandNewTopLevel":1,"msgs":[]}`), known)
	if _, ok := unmappedFld.Load(site + "\tbrandNewTopLevel"); !ok {
		t.Fatal("a new envelope field went unflagged: the message wildcard must not waive the envelope")
	}
}

// aisstream answers our subscription with a confirmation that carries no data; it is not an
// unhandled record type.
func TestAisstreamSubscriptionConfirmationIsNotUnmapped(t *testing.T) {
	p := testPipeline(t)
	p.aisstreamMessage([]byte(`{"MessageType":"SubscriptionConfirmation","Message":{}}`), time.Now())
	if _, ok := unmappedType.Load("aisstream\tSubscriptionConfirmation"); ok {
		t.Fatal("the subscription confirmation counted as an unhandled record type")
	}
}
