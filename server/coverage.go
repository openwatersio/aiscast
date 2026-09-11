package main

import (
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

// Capture completeness: every field a source sends is either mapped by an adapter struct or named
// in the waiver ledger, never silently dropped. Live, a sampled shadow decode compares real records
// against both sets and reports anything new to /metrics within hours of an upstream adding it.
// In CI, coverage_test.go runs the fixture corpus in testdata/sources through the same check with
// sampling off, so an adapter edit that drops a field fails the build.

// waivers is the ledger of fields deliberately not captured; each entry is a reviewed decision.
var waivers = map[string][]string{
	"barentswatch": {
		"shipLength", "shipWidth", // redundant: dimensions A+B and C+D carry them
		"reportClass", // duplicative of aisClass for the message types we map
	},
	"catcher":     {"protocol"},         // constant envelope marker
	"catcher/msg": {"class", "channel"}, // class is constant "AIS"; channel is in the sentence itself
}

var shadowEvery = int64(1024) // sample rate; tests set 1 to check every record

var (
	shadowN      sync.Map // site -> *atomic.Int64, the sampling counter
	unmappedFld  sync.Map // site \t field -> *atomic.Int64
	unmappedType sync.Map // site \t type  -> *atomic.Int64
)

func shadowSample(site string) bool {
	c, _ := shadowN.LoadOrStore(site, new(atomic.Int64))
	return c.(*atomic.Int64).Add(1)%shadowEvery == 1 || shadowEvery == 1
}

// shadowCheck decodes raw generically and counts any key outside known; the first sighting logs.
func shadowCheck(site string, raw []byte, known map[string]bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for k := range m {
		if known[k] {
			continue
		}
		c, loaded := unmappedFld.LoadOrStore(site+"\t"+k, new(atomic.Int64))
		c.(*atomic.Int64).Add(1)
		if !loaded {
			log.Printf("coverage: %s sends field %q we do not capture", site, k)
		}
	}
}

func countUnmappedType(site, typ string) {
	c, loaded := unmappedType.LoadOrStore(site+"\t"+typ, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
	if !loaded {
		log.Printf("coverage: %s sends record type %q we do not handle", site, typ)
	}
}

// fieldsOf is the capture set of a struct: its json tag names (field names when untagged, matching
// encoding/json), plus the site's waivers.
func fieldsOf(site string, t reflect.Type) map[string]bool {
	out := map[string]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct { // embedded structs promote their keys
				walk(f.Type)
				continue
			}
			name := f.Name
			if tag, ok := f.Tag.Lookup("json"); ok {
				if n, _, _ := strings.Cut(tag, ","); n != "" {
					name = n
				}
			}
			out[name] = true
		}
	}
	walk(t)
	for _, w := range waivers[site] {
		out[w] = true
	}
	return out
}

var (
	bwKnown      = fieldsOf("barentswatch", reflect.TypeOf(bwMessage{}))
	dtLocKnown   = fieldsOf("digitraffic/location", reflect.TypeOf(dtLocation{}))
	dtMetaKnown  = fieldsOf("digitraffic/metadata", reflect.TypeOf(dtMetadata{}))
	aishubKnown  = fieldsOf("aishub", reflect.TypeOf(aishubRow{}))
	catcherKnown = fieldsOf("catcher", reflect.TypeOf(jsonaiscatcher{}))
	catcherMsg   = fieldsOf("catcher/msg", reflect.TypeOf(jsonaiscatcher{}.Msgs).Elem())

	v0EnvKnown  = map[string]bool{"MessageType": true, "MetaData": true, "Message": true}
	v0MetaKnown = map[string]bool{"MMSI": true, "MMSI_String": true, "ShipName": true, "latitude": true, "longitude": true, "time_utc": true}

	v0FieldsMu sync.Mutex
	v0Fields   = map[string]map[string]bool{} // per aisstream message type, from the go-ais struct
)

func aisstreamKnown(msgType string) map[string]bool {
	v0FieldsMu.Lock()
	defer v0FieldsMu.Unlock()
	if f, ok := v0Fields[msgType]; ok {
		return f
	}
	f := fieldsOf("aisstream/"+msgType, v0Types[msgType])
	v0Fields[msgType] = f
	return f
}
