package main

import (
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Capture completeness: every field a source sends is either mapped by an adapter struct or named
// in the waiver ledger, never silently dropped — at any nesting depth. Live, a sampled shadow
// decode compares real records against both sets and reports anything new to /metrics within hours
// of an upstream adding it. In CI, coverage_test.go runs the fixture corpus in testdata/sources
// through the same check with sampling off, so an adapter edit that drops a field fails the build.

// waivers is the ledger of fields deliberately not captured, as dotted paths per site; each entry
// is a reviewed decision.
var waivers = map[string][]string{
	"barentswatch": {
		"shipLength", "shipWidth", // redundant: dimensions A+B and C+D carry them
		"reportClass", // duplicative of aisClass for the message types we map
	},
	"barentswatch/methyd": {
		"type", "messageType", "stream", // envelope markers
		"designatedAreaCode",    // 1 for every IMO message
		"day", "hour", "minute", // embedded observation time, broken on real stations; msgtime is the clock
	},
	"catcher": {
		"protocol",                   // constant envelope marker
		"msgs.class", "msgs.channel", // class is constant "AIS"; channel is in the sentence itself
	},
}

var shadowEvery = int64(1024) // sample rate; tests set 1 to check every record

var (
	shadowN      sync.Map // site -> *atomic.Int64, the sampling counter
	unmappedFld  sync.Map // site \t dotted field path -> *atomic.Int64
	unmappedType sync.Map // site \t type -> *atomic.Int64
)

func shadowSample(site string) bool {
	c, _ := shadowN.LoadOrStore(site, new(atomic.Int64))
	return c.(*atomic.Int64).Add(1)%shadowEvery == 1 || shadowEvery == 1
}

// fieldSet is the capture tree: a nil subtree is a leaf, a non-nil one descends into an object
// (or each element of an array of objects).
type fieldSet map[string]fieldSet

func flat(keys ...string) fieldSet {
	out := fieldSet{}
	for _, k := range keys {
		out[k] = nil
	}
	return out
}

// shadowCheck decodes raw generically and counts any key outside known, recursively; the first
// sighting of a path logs.
func shadowCheck(site string, raw []byte, known fieldSet) {
	shadowWalk(site, "", raw, known)
}

func shadowWalk(site, prefix string, raw []byte, known fieldSet) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for k, v := range m {
		sub, ok := known[k]
		if !ok {
			flagUnmapped(site, prefix+k)
			continue
		}
		if sub == nil {
			continue
		}
		vv := trimLeft(v)
		switch {
		case len(vv) > 0 && vv[0] == '{':
			shadowWalk(site, prefix+k+".", v, sub)
		case len(vv) > 0 && vv[0] == '[':
			var items []json.RawMessage
			if json.Unmarshal(v, &items) == nil {
				for _, it := range items {
					shadowWalk(site, prefix+k+".", it, sub)
				}
			}
		}
	}
}

func trimLeft(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

func flagUnmapped(site, path string) {
	c, loaded := unmappedFld.LoadOrStore(site+"\t"+path, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
	if !loaded {
		log.Printf("coverage: %s sends field %q we do not capture", site, path)
	}
}

func countUnmappedType(site, typ string) {
	c, loaded := unmappedType.LoadOrStore(site+"\t"+typ, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
	if !loaded {
		log.Printf("coverage: %s sends record type %q we do not handle", site, typ)
	}
}

var timeType = reflect.TypeOf(time.Time{})

// fieldsOf is the capture tree of a struct: json tag names (field names when untagged, matching
// encoding/json), descending into named struct fields and elements of slices of structs, plus the
// site's waivers.
func fieldsOf(site string, t reflect.Type) fieldSet {
	return withWaivers(site, walkStruct(t))
}

// withWaivers adds a site's waivers to a capture set.
func withWaivers(site string, fs fieldSet) fieldSet {
	for _, w := range waivers[site] {
		insertPath(fs, strings.Split(w, "."))
	}
	return fs
}

func walkStruct(t reflect.Type) fieldSet {
	out := fieldSet{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct { // embedded structs promote their keys
			for k, v := range walkStruct(f.Type) {
				out[k] = v
			}
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			if n, _, _ := strings.Cut(tag, ","); n != "" {
				name = n
			}
		}
		ft := f.Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != timeType {
			out[name] = walkStruct(ft)
		} else {
			out[name] = nil
		}
	}
	return out
}

func insertPath(fs fieldSet, path []string) {
	head, rest := path[0], path[1:]
	if len(rest) == 0 {
		if _, ok := fs[head]; !ok {
			fs[head] = nil
		}
		return
	}
	if fs[head] == nil {
		fs[head] = fieldSet{}
	}
	insertPath(fs[head], rest)
}

var (
	bwKnown      = fieldsOf("barentswatch", reflect.TypeOf(bwMessage{}))
	dtLocKnown   = fieldsOf("digitraffic/location", reflect.TypeOf(dtLocation{}))
	dtMetaKnown  = fieldsOf("digitraffic/metadata", reflect.TypeOf(dtMetadata{}))
	aishubKnown  = fieldsOf("aishub", reflect.TypeOf(aishubRow{}))
	catcherKnown = fieldsOf("catcher", reflect.TypeOf(jsonaiscatcher{}))

	// MetHyd is archived verbatim, so nothing is lost at ingest; the loss point is the packager's
	// fixed weather columns. This set is that contract: the fields it reads, plus the waivers above.
	// packager/test_packager.py checks the same record against the columns themselves.
	metHydKnown = withWaivers("barentswatch/methyd", flat(
		"mmsi", "msgtime", "functionalId", "latitude", "longitude",
		"avgWindSpeed", "windGust", "windDirection", "windGustDirection", "airTemperature",
		"relativeHumidity", "dewPoint", "airPressure", "horizontalVisibility", "waterLevel",
		"surfaceCurrentSpeed", "surfaceCurrentDirection", "currentSpeed2", "currentDirection2",
		"currentMeasuringLevel2", "currentSpeed3", "currentDirection3", "currentMeasuringLevel3",
		"significantWaveHeight", "wavePeriod", "waveDirection", "swellHeight", "swellPeriod",
		"swellDirection", "waterTemperature", "salinity",
		"airPressureTendency", "waterLevelTrend", "seaState", "precipitationType", "ice",
	))

	// Message is a map keyed by type name and checked per type below; MetaData has its own site.
	v0EnvKnown  = flat("MessageType", "MetaData", "Message")
	v0MetaKnown = flat("MMSI", "MMSI_String", "ShipName", "latitude", "longitude", "time_utc")

	v0FieldsMu sync.Mutex
	v0Fields   = map[string]fieldSet{} // per aisstream message type, from the go-ais struct
)

func aisstreamKnown(msgType string) fieldSet {
	v0FieldsMu.Lock()
	defer v0FieldsMu.Unlock()
	if f, ok := v0Fields[msgType]; ok {
		return f
	}
	f := fieldsOf("aisstream/"+msgType, v0Types[msgType])
	v0Fields[msgType] = f
	return f
}
