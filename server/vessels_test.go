package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestUpdateVesselStalePosition(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	pos := func(at time.Time, lat float64) *Event {
		return &Event{Time: at, Source: "local", MMSI: 1, Type: "PositionReport", Packet: ais.PositionReport{
			Header: ais.Header{UserID: 1}, Latitude: ais.FieldLatLonFine(lat), Longitude: 10, Cog: 90, Sog: 5, TrueHeading: 511, NavigationalStatus: 0}}
	}
	p.updateVessel(pos(t0, 50))

	// late report from a slow source: older time, different position, and a name (ExtendedClassB carries one)
	late := &Event{Time: t0.Add(-90 * time.Second), Source: "aishub", MMSI: 1, Type: "ExtendedClassBPositionReport", Packet: ais.ExtendedClassBPositionReport{
		Header: ais.Header{UserID: 1}, Latitude: 49, Longitude: 10, Cog: 180, Sog: 1, TrueHeading: 511, Name: "LATE"}}
	p.updateVessel(late)

	v := p.vessels[1]
	if v.Lat != 50 || v.Cog != 90 || v.Sog != 5 || !v.Seen.Equal(t0) || v.Source != "local" {
		t.Fatalf("stale event overwrote cache: %+v", v)
	}
	if v.Name != "LATE" {
		t.Fatalf("static field from stale event not folded: %+v", v)
	}
	if late.Lat != 49 || late.Name != "LATE" {
		t.Fatalf("stale event not stamped with own position/name: lat=%v name=%q", late.Lat, late.Name)
	}

	// within 1 s counts as fresh (sources stamp whole seconds)
	p.updateVessel(pos(t0.Add(-500*time.Millisecond), 51))
	if p.vessels[1].Lat != 51 {
		t.Fatalf("sub-second-older event should apply: %+v", p.vessels[1])
	}
}

func TestStaleEventNotBroadcast(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	report := func(lat float64) ais.Packet {
		return ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 257000002}, Valid: true,
			Latitude: ais.FieldLatLonFine(lat), Longitude: 10, Cog: 360, Sog: 102.3, TrueHeading: 511, NavigationalStatus: 15}
	}
	p.ingestPacket("kystverket", "kystverket", t0, t0, report(50))
	if len(sub.ch) != 1 {
		t.Fatalf("fresh event not broadcast: %d", len(sub.ch))
	}
	<-sub.ch

	// a slow source's copy of an already-superseded report: archived and folded, never streamed
	before := p.stats.stale.Load()
	p.ingestPacket("aishub", "aishub", t0.Add(-90*time.Second), t0.Add(-90*time.Second), report(49))
	if len(sub.ch) != 0 || p.stats.stale.Load() != before+1 {
		t.Fatalf("stale event broadcast: events=%d stale=%d→%d", len(sub.ch), before, p.stats.stale.Load())
	}
}

// quantStep is the AIS lat/lon resolution: 1/10000 minute, i.e. 1/600000 of a degree. A round trip through
// ais.FieldLatLonFine loses at most one step (49.48 comes back as 49.479998333).
const quantStep = 1.0 / 600000

func posAt(t *testing.T, v *vessel, lat, lon float64) bool {
	t.Helper()
	return v.HasPos && absF(v.Lat-lat) < 2*quantStep && absF(v.Lon-lon) < 2*quantStep
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func posReport(mmsi uint32, lat, lon float64) ais.Packet {
	return ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true,
		Latitude: ais.FieldLatLonFine(lat), Longitude: ais.FieldLatLonFine(lon),
		Cog: 360, Sog: 102.3, TrueHeading: 511, NavigationalStatus: 15}
}

// The implausibility check is not limited to UDP: the upstream aggregates carry bad positions too.
func TestImplausibleJumpFromAnySource(t *testing.T) {
	for i, src := range []string{"aishub", "aisstream", "kystverket"} {
		p := testPipeline(t)
		mmsi := uint32(257003000 + i)
		t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
		p.ingestPacket(src, src, t0, t0, posReport(mmsi, 49.48, 0.13))

		before := p.stats.implausible.Load()
		ev := &Event{Time: t0.Add(3 * time.Second), Source: src, MMSI: mmsi, Type: "PositionReport",
			Packet: posReport(mmsi, -0.52, 0.13)} // 3,002 nm away
		p.updateVessel(ev)
		if !ev.Implausible {
			t.Errorf("%s: 3,000 nm in 3 s not flagged implausible", src)
		}
		if !posAt(t, p.vessels[mmsi], 49.48, 0.13) {
			t.Errorf("%s: implausible position folded into the cache: %+v", src, p.vessels[mmsi])
		}
		p.emit(ev) // emit counts it and withholds it from subscribers
		if p.stats.implausible.Load() != before+1 {
			t.Errorf("%s: implausible not counted", src)
		}
	}
}

// A jump shorter than implausibleJumpNM is left alone however fast it implies: at second-level spacing two
// sources reporting the same vessel disagree by metres, and 100 m in 1 s already implies 117 kn.
func TestShortJumpNotImplausible(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	p.ingestPacket("kystverket", "kystverket", t0, t0, posReport(4242, 69.9, 20.1))
	ev := &Event{Time: t0.Add(time.Second), Source: "barentswatch", MMSI: 4242, Type: "PositionReport",
		Packet: posReport(4242, 69.909472, 20.163188)}
	p.updateVessel(ev)
	if ev.Implausible {
		t.Errorf("cross-source jitter of %.2f nm flagged implausible", nm(69.9, 20.1, 69.909472, 20.163188))
	}
}

// (0,0) is a GPS default, not a fix. It is a valid coordinate, so the 91/181 range test passes it.
func TestNullIslandIsNotAPosition(t *testing.T) {
	p := testPipeline(t)
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// a vessel already tracked keeps the position it had
	p.ingestPacket("aisstream", "aisstream", t0, t0, posReport(257000001, 49.48, 0.13))
	p.ingestPacket("aisstream", "aisstream", t0.Add(time.Minute), t0.Add(time.Minute), posReport(257000001, 0, 0))
	if !posAt(t, p.vessels[257000001], 49.48, 0.13) {
		t.Errorf("null island folded into the cache: %+v", p.vessels[257000001])
	}
	// a vessel seen only at (0,0) has no position at all, so it never reaches /v1/vessels
	p.ingestPacket("aishub", "aishub", t0, t0, posReport(257000002, 0, 0))
	if v := p.vessels[257000002]; v.HasPos {
		t.Errorf("vessel known only at (0,0) has a position: lat=%v lon=%v", v.Lat, v.Lon)
	}
	// the meridian and the equator on their own are ordinary water: Greenwich is on longitude 0
	p.ingestPacket("aishub", "aishub", t0, t0, posReport(257000003, 51.5, 0))
	if !posAt(t, p.vessels[257000003], 51.5, 0) {
		t.Errorf("Greenwich position rejected: %+v", p.vessels[257000003])
	}
}

func TestStaticParticulars(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.ingestPacket("kystverket", "kystverket", now, now, ais.PositionReport{Header: ais.Header{MessageID: 1, UserID: 257000009}, Valid: true,
		NavigationalStatus: 0, Latitude: 60, Longitude: 5, Sog: 10, Cog: 90, TrueHeading: 511})
	p.ingestPacket("kystverket", "kystverket", now, now, ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true,
		Name: "STATIC STAR", Type: 70, ImoNumber: 9319466, CallSign: "LAJB7", Destination: "NOOSL",
		Eta: ais.FieldETA{Month: 9, Day: 19, Hour: 6, Minute: 0}, MaximumStaticDraught: 5.2,
		Dimension: ais.FieldDimension{A: 100, B: 50, C: 10, D: 12}})
	v := p.vessels[257000009]
	if v.IMO != 9319466 || v.CallSign != "LAJB7" || v.Destination != "NOOSL" || v.Draught != 5.2 || v.Length != 150 || v.Beam != 22 || v.ETA.Month != 9 {
		t.Fatalf("particulars not folded: %+v", v)
	}
	props := v.feature(257000009).Properties
	if props.Flag != "NO" || props.IMO != 9319466 || props.ETA != "09-19 06:00" || props.Length != 150 || props.Draught != 5.2 {
		t.Errorf("feature: %+v", props)
	}
	// an ETA without a time keeps the date; an ETA with no month is not folded over a known one
	p.ingestPacket("kystverket", "kystverket", now.Add(time.Second), now.Add(time.Second), ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true,
		Name: "STATIC STAR", Type: 70, Eta: ais.FieldETA{Month: 9, Day: 20, Hour: 24, Minute: 60}})
	if got := etaString(p.vessels[257000009].ETA); got != "09-20" {
		t.Errorf("date-only eta: %q", got)
	}
	p.ingestPacket("kystverket", "kystverket", now.Add(2*time.Second), now.Add(2*time.Second), ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true, Name: "STATIC STAR", Type: 70})
	if v := p.vessels[257000009]; v.ETA.Day != 20 || v.IMO != 9319466 || v.Destination != "NOOSL" {
		t.Errorf("empty static wiped particulars: %+v", v)
	}
	// a later message with a length but no beam keeps the beam, and the reverse keeps the length
	p.ingestPacket("kystverket", "kystverket", now.Add(3*time.Second), now.Add(3*time.Second), ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true,
		Name: "STATIC STAR", Type: 70, Dimension: ais.FieldDimension{A: 120, B: 40}})
	if v := p.vessels[257000009]; v.Length != 160 || v.Beam != 22 || v.Dim.A != 120 || v.Dim.B != 40 || v.Dim.C+v.Dim.D != 22 {
		t.Errorf("partial dimensions wiped a value: length %d beam %d dim %+v", v.Length, v.Beam, v.Dim)
	}
	p.ingestPacket("kystverket", "kystverket", now.Add(4*time.Second), now.Add(4*time.Second), ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true,
		Name: "STATIC STAR", Type: 70, Dimension: ais.FieldDimension{C: 11, D: 12}})
	if v := p.vessels[257000009]; v.Length != 160 || v.Beam != 23 || v.Dim != (ais.FieldDimension{A: 120, B: 40, C: 11, D: 12}) {
		t.Errorf("partial dimensions wiped a value: length %d beam %d dim %+v", v.Length, v.Beam, v.Dim)
	}
	// the feature carries the four offsets, a zero one included once its total is known
	p.ingestPacket("kystverket", "kystverket", now.Add(5*time.Second), now.Add(5*time.Second), ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000009}, Valid: true,
		Name: "STATIC STAR", Type: 70, Dimension: ais.FieldDimension{A: 0, B: 160}})
	b, _ := json.Marshal(p.vessels[257000009].feature(257000009).Properties)
	for _, want := range []string{`"to_bow":0`, `"to_stern":160`, `"to_port":11`, `"to_starboard":12`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("feature lacks %s: %s", want, b)
		}
	}
	// type 24 part B carries the call sign and dimensions of a class B vessel
	p.ingestPacket("kystverket", "kystverket", now, now, ais.StaticDataReport{Header: ais.Header{MessageID: 24, UserID: 257000010}, Valid: true, PartNumber: true,
		ReportB: ais.StaticDataReportB{Valid: true, ShipType: 36, CallSign: "LG1234", Dimension: ais.FieldDimension{A: 6, B: 6, C: 2, D: 2}}})
	if v := p.vessels[257000010]; v.CallSign != "LG1234" || v.Length != 12 || v.Beam != 4 || v.ShipType != 36 {
		t.Errorf("type 24 B: %+v", v)
	}
}

func TestFlagOf(t *testing.T) {
	for mmsi, want := range map[uint32]string{257000001: "NO", 230000001: "FI", 538005000: "MH", 366999999: "US", 992571234: "NO",
		825712345: "NO", 111257001: "NO", 2570001: "NO", 25700001: "NO", 243000001: "HU", 271000001: "TR", 218000001: "DE", 306000001: "CW", 501000001: "TF", 303000001: "US", 608000001: "SH", 970123456: "", 199000000: "", 900000000: ""} {
		if got := flagOf(mmsi); got != want {
			t.Errorf("flagOf(%d) = %q, want %q", mmsi, got, want)
		}
	}
}

func TestValidMMSI(t *testing.T) {
	for mmsi, want := range map[uint32]bool{
		// ships, coast stations, groups, SAR aircraft, handhelds, craft, aids to navigation, SART/MOB/EPIRB
		257000001: true, 2573104: true, 2190047: true, 25700001: true, 111257005: true, 825701381: true, 982570310: true,
		992576072: true, 970123456: true, 974018432: true,
		// unique numbers outside the M.585 formats name one transmitter, so they stay
		109080372: true, 199000000: true, 900000000: true,
		// the edges of the MID range in the zero-padded formats
		2_000_000: true, 7_999_999: true, 20_000_000: true, 79_999_999: true,
		1_999_999: false, 8_000_000: false, 19_999_999: false, 80_000_000: false, 99_999_999: false,
		// no room for a MID
		0: false, 1: false, 2: false, 1111: false, 3638: false, 1234567: false, 12345678: false,
		// defaults
		100_000_000: false, 111_111_111: false, 123_456_789: false, 555_555_555: false, 987_654_321: false, 999_999_999: false,
		// past nine digits, which the 30-bit field allows
		1_000_000_000: false, 1<<30 - 1: false,
	} {
		if got := validMMSI(mmsi); got != want {
			t.Errorf("validMMSI(%d) = %v, want %v", mmsi, got, want)
		}
	}
}

// A message under an MMSI many transmitters share is counted and kept out of the cache, the record, history,
// and the stream, while the vessels around it go on as before.
func TestInvalidMMSIStaysOffTheMap(t *testing.T) {
	p, written := recordingPipeline(t)
	st, err := openStore(filepath.Join(t.TempDir(), "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.close() })
	if err := p.attachStore(st); err != nil {
		t.Fatal(err)
	}
	sub := p.subscribe()
	now := time.Now().Truncate(time.Second)
	for _, mmsi := range []uint32{0, 123456789, 1234567} {
		p.ingestPacket("aisstream", "aisstream", now, now, posReport(mmsi, 59.9, 10.7))
		p.ingestPacket("aisstream", "aisstream", now, now, shipStatic(mmsi, "DATAHUB"))
	}
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(257000001, 59.9, 10.7))
	mustFlush(t, p)
	if got := p.stats.invalidMMSI.Load(); got != 6 {
		t.Errorf("invalid MMSI messages counted %d, want 6", got)
	}
	// They skip dedupe, so crediting them would let a station repeat one line into the feeder tier.
	for range 3 {
		p.ingestPacket("station:k", "station:k", now, now, posReport(0, 59.9, 10.7))
	}
	if got := p.stations.events24h([]string{"station:k"}, now); got != 0 {
		t.Errorf("a station is credited with %d messages under MMSI 0, want 0", got)
	}
	// AISHub repeats a row on every snapshot, and the cache that says whether it is news never holds these MMSIs,
	// so the decoder skips the rows rather than send the same one again each time.
	before := p.stats.invalidMMSI.Load()
	for range 2 {
		snap := `[[{"MMSI":123456789,"TIME":"` + fmt.Sprint(now.Add(-time.Minute).Unix()) + `","LONGITUDE":3022815,"LATITUDE":31476144}]]`
		if n, err := p.aishubSnapshot([]byte(snap), now); err != nil || n != 0 {
			t.Errorf("AISHub snapshot with a default MMSI: %d events, %v", n, err)
		}
	}
	if got := p.stats.invalidMMSI.Load(); got != before {
		t.Errorf("AISHub snapshot rows counted %d times", got-before)
	}
	if n := len(sub.ch); n != 1 {
		t.Errorf("%d events streamed, want only the valid vessel's", n)
	}
	if n := p.vesselCount(); n != 1 {
		t.Errorf("cache holds %d vessels, want 1", n)
	}
	for _, mmsi := range []uint32{0, 123456789, 1234567} {
		if _, ok, _ := st.get(mmsi); ok {
			t.Errorf("record has a row for %d", mmsi)
		}
	}
	for _, pt := range written() {
		if pt.mmsi != 257000001 {
			t.Errorf("history got a copy for %d", pt.mmsi)
		}
	}
}

// A UDP sender claiming a default MMSI as its own keeps its address, so senders sharing the default are not
// merged into one station.
func TestUDPSenderKeepsItsAddressForADefaultOwnShip(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	now := time.Now()
	p.Ingest(Reception{Source: "udp:boat", Station: "udp:boat", RecvTime: now, Body: ownSentence(p, posReport(123456789, 41.5, -70.6))})
	p.Ingest(Reception{Source: "udp:boat", Station: "udp:boat", RecvTime: now, Body: testSentence})
	ev := <-sub.ch
	if ev.Source != "udp:boat" || ev.Station != "udp:boat" {
		t.Errorf("relabeled by a default own-ship MMSI: source %q station %q", ev.Source, ev.Station)
	}
}

func TestDerivedKind(t *testing.T) {
	// The cases the web client's copy of the rule is tested against too.
	b, err := os.ReadFile("testdata/derived_kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			MMSI         uint32
			Name         string
			ShipType     uint8
			Length, Beam uint16
			NavStatus    uint8
			Want         string
		}
	}
	if err := json.Unmarshal(b, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	for _, c := range fixture.Cases {
		v := newVessel()
		v.Name, v.ShipType, v.Length, v.Beam, v.NavStatus = c.Name, c.ShipType, c.Length, c.Beam, c.NavStatus
		if got := derivedKind(c.MMSI, v); got != c.Want {
			t.Errorf("%d %q type %d %dx%d nav %d: %s, want %s", c.MMSI, c.Name, c.ShipType, c.Length, c.Beam, c.NavStatus, got, c.Want)
		}
	}
}

// netBuoyStatic is an HSD-NET buoy's static report as heard: its name and battery level, a 10 m square hull around
// the antenna, and no ship type.
func netBuoyStatic(mmsi uint32, name string) ais.Packet {
	return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: name,
		Dimension: ais.FieldDimension{A: 5, B: 5, C: 5, D: 5}}
}

// A buoy is gear once its static data arrives, and shows no flag; a beacon is sar while it says AIS-SART active; a
// ship on a beacon's number stays a vessel; and a kind a message sets wins, so an aid stays aton when a feed rebuilds
// its report as a vessel's, in the cache, the record, and the lookup.
func TestKindFromWhatAStationSays(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Second)
	// A row the record already holds as a vessel becomes gear, and loses its flag, when its static data arrives.
	if _, err := p.store.db.Exec(`INSERT INTO vessels (mmsi, name, kind, flag, seen, first_seen) VALUES (254301782, 'X', 'vessel', 'MC', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	p.ingestPacket("aishub", "aishub", now, now, posReport(254301782, 44.13, -125.03))
	mustFlush(t, p)
	if k := p.vessels[254301782].Kind; k != "vessel" {
		t.Errorf("a buoy before its static data is %q, want vessel", k)
	}
	p.ingestPacket("aishub", "aishub", now.Add(time.Second), now.Add(time.Second), netBuoyStatic(254301782, "HSD-NET-84%"))
	beacon := posReport(970123456, 59.9, 10.7).(ais.PositionReport)
	beacon.NavigationalStatus = 14
	p.ingestPacket("kystverket", "kystverket", now, now, beacon)
	p.ingestPacket("aishub", "aishub", now, now, posReport(972168869, 10.1, 107.2))
	p.ingestPacket("aishub", "aishub", now.Add(time.Second), now.Add(time.Second), shipStatic(972168869, "TRU0NG HUY A2"))
	p.ingestPacket("kystverket", "kystverket", now, now, posReport(979123456, 59.92, 10.72))
	p.ingestPacket("barentswatch", "barentswatch", now, now, ais.AidsToNavigationReport{Header: ais.Header{MessageID: 21, UserID: 992576072}, Valid: true,
		Type: 30, Name: "AQUACULTURE 1", Latitude: 59.91, Longitude: 10.71, Timestamp: 60})
	p.ingestPacket("aishub", "aishub", now.Add(time.Minute), now.Add(time.Minute), posReport(992576072, 59.91, 10.71))
	p.ingestPacket("aishub", "aishub", now.Add(time.Minute), now.Add(time.Minute), netBuoyStatic(992576072, "AQUACULTURE BUOY 1"))
	mustFlush(t, p)
	want := map[uint32]string{254301782: "gear", 970123456: "sar", 972168869: "vessel", 979123456: "gear", 992576072: "aton"}
	for m, k := range want {
		if got := p.vessels[m].Kind; got != k {
			t.Errorf("cache kind of %d = %q, want %q", m, got, k)
		}
		if rec, _, _ := p.store.get(m); rec.v.Kind != k {
			t.Errorf("record kind of %d = %q, want %q", m, rec.v.Kind, k)
		}
	}
	var flag string
	if err := p.store.db.QueryRow(`SELECT flag FROM vessels WHERE mmsi = 254301782`).Scan(&flag); err != nil || flag != "" {
		t.Errorf("gear's stored flag %q (%v), want none", flag, err)
	}
	if w := get(t, p, "/v1/vessels/254301782"); strings.Contains(w.Body.String(), `"flag"`) || !strings.Contains(w.Body.String(), `"kind":"gear"`) {
		t.Errorf("gear's lookup: %s", w.Body)
	}
	if r := mcpRow(254301782, p.vessels[254301782], now); r.Flag != "" {
		t.Errorf("gear's MCP row has flag %q", r.Flag)
	}
	if mcpMatch("", nil, "MC", 254301782, p.vessels[254301782]) {
		t.Error("gear matches a flag filter by the MID it picked")
	}
	if w := get(t, p, "/v1/vessels/992576072"); !strings.Contains(w.Body.String(), `"flag":"NO"`) {
		t.Errorf("the aid keeps its flag: %s", w.Body)
	}
	if w := get(t, p, "/v1/vessels?mmsi=970123456,254301782&kind=sar,gear"); !strings.Contains(w.Body.String(), "970123456") || !strings.Contains(w.Body.String(), "254301782") {
		t.Errorf("kind=sar,gear does not find the beacon and the buoy: %s", w.Body)
	}
	// The beacon number's next report says it is under way, no longer active: a vessel again, in the cache and the record.
	underWay := posReport(970123456, 59.9, 10.7).(ais.PositionReport)
	underWay.NavigationalStatus = 0
	p.ingestPacket("kystverket", "kystverket", now.Add(time.Minute), now.Add(time.Minute), underWay)
	mustFlush(t, p)
	if k := p.vessels[970123456].Kind; k != "vessel" {
		t.Errorf("the beacon number after an ordinary report: cache %q, want vessel", k)
	}
	if rec, _, _ := p.store.get(970123456); rec.v.Kind != "vessel" {
		t.Errorf("the beacon number after an ordinary report: record %q, want vessel", rec.v.Kind)
	}
	// Back after the cache forgot it, the aid's first report a rebuilt one with a buoy's name: the cache takes it for
	// gear, the record keeps the aid and its flag, and its lookup answers aton.
	forget(p)
	later := now.Add(time.Hour)
	p.ingestPacket("aishub", "aishub", later, later, netBuoyStatic(992576072, "AQUACULTURE BUOY 1"))
	mustFlush(t, p)
	if err := p.store.db.QueryRow(`SELECT kind || ' ' || flag FROM vessels WHERE mmsi = 992576072`).Scan(&flag); err != nil || flag != "aton NO" {
		t.Errorf("the aid's record after a rebuilt report = %q (%v), want aton NO", flag, err)
	}
	if w := get(t, p, "/v1/vessels/992576072"); !strings.Contains(w.Body.String(), `"kind":"aton"`) {
		t.Errorf("the aid's lookup: %s", w.Body)
	}
	// The buoy back after the sweep, its position before its static data: the cache does not know it yet, and the row
	// keeps gear and no flag.
	p.ingestPacket("aishub", "aishub", later, later, posReport(254301782, 44.13, -125.03))
	mustFlush(t, p)
	if err := p.store.db.QueryRow(`SELECT kind || ' ' || flag FROM vessels WHERE mmsi = 254301782`).Scan(&flag); err != nil || flag != "gear " {
		t.Errorf("the buoy's row after a position report = %q (%v), want gear and no flag", flag, err)
	}
}

// Gear's MMSI is whatever its maker or owner chose, so no register's facts are served for it: here a buoy on a
// Canadian number that ISED once answered for.
func TestGearHasNoRegisterFacts(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Second)
	if _, err := p.store.db.Exec(`INSERT INTO ised (mmsi, name, callsign, checked_at) VALUES (316123456, 'ATLANTIC STAR', 'CFA1234', 1)`); err != nil {
		t.Fatal(err)
	}
	p.ingestPacket("aishub", "aishub", now, now, posReport(316123456, 44.6, -63.5))
	p.ingestPacket("aishub", "aishub", now.Add(time.Second), now.Add(time.Second), netBuoyStatic(316123456, "HSD-NET-71%"))
	mustFlush(t, p)
	if w := get(t, p, "/v1/vessels/316123456"); !strings.Contains(w.Body.String(), `"kind":"gear"`) || strings.Contains(w.Body.String(), "ATLANTIC STAR") {
		t.Errorf("gear's lookup carries a register's facts: %s", w.Body)
	}
}

// A class B yacht sends its name in part A of message 24 and its ship type in part B. A buoy-like name with no type
// yet reads as gear; the type that follows makes it a vessel again, flag and all.
func TestAShipTypeUndoesGear(t *testing.T) {
	p := storePipeline(t)
	now := time.Now().Truncate(time.Second)
	partA := ais.StaticDataReport{Header: ais.Header{MessageID: 24, UserID: 368472570}, Valid: true,
		ReportA: ais.StaticDataReportA{Valid: true, Name: "BUOY TIME"}}
	partB := ais.StaticDataReport{Header: ais.Header{MessageID: 24, UserID: 368472570}, Valid: true, PartNumber: true,
		ReportB: ais.StaticDataReportB{Valid: true, ShipType: 37, CallSign: "WDK1234"}}
	p.ingestPacket("aisstream", "aisstream", now, now, partA)
	mustFlush(t, p)
	if k := p.vessels[368472570].Kind; k != "gear" {
		t.Fatalf("after part A: %q, want gear", k)
	}
	p.ingestPacket("aisstream", "aisstream", now.Add(time.Second), now.Add(time.Second), partB)
	mustFlush(t, p)
	var row string
	if err := p.store.db.QueryRow(`SELECT kind || ' ' || flag FROM vessels WHERE mmsi = 368472570`).Scan(&row); err != nil || row != "vessel US" {
		t.Errorf("after part B the row is %q (%v), want vessel US", row, err)
	}
	if k := p.vessels[368472570].Kind; k != "vessel" {
		t.Errorf("after part B the cache says %q, want vessel", k)
	}
}

// A snapshot rebuilds a station as the report it sends: gear and beacons send a vessel's, and a SAR aircraft and an
// aid their own.
func TestSnapshotRebuildsTheReportTheStationSends(t *testing.T) {
	for _, c := range []struct {
		mmsi uint32
		kind string
		want string
	}{
		{970123456, "sar", "PositionReport"},
		{111257005, "sar", "StandardSearchAndRescueAircraftReport"},
		{994123456, "gear", "PositionReport"},
		{992576072, "aton", "AidsToNavigationReport"},
		{2573104, "base", "BaseStationReport"},
	} {
		v := newVessel()
		v.Kind, v.HasPos, v.Lat, v.Lon, v.PosAt = c.kind, true, 59.9, 10.7, time.Now()
		if got := v.synthPos(c.mmsi).Type; got != c.want {
			t.Errorf("%d %s rebuilt as %s, want %s", c.mmsi, c.kind, got, c.want)
		}
	}
}

// Gear drifts with the nets it marks, so it ages out of an area like a vessel, its speed known or not; an aid stays.
func TestGearAgesOutLikeAVessel(t *testing.T) {
	p := storePipeline(t)
	at := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	drifting := posReport(979123456, 59.9, 10.7).(ais.PositionReport)
	drifting.Sog = 2.5
	p.ingestPacket("kystverket", "kystverket", at, at, drifting)
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(979123457, 59.92, 10.72)) // speed not available
	p.ingestPacket("kystverket", "kystverket", at, at, ais.AidsToNavigationReport{Header: ais.Header{MessageID: 21, UserID: 992576072}, Valid: true,
		Type: 30, Name: "AQUACULTURE 1", Latitude: 59.91, Longitude: 10.71, Timestamp: 60})
	mustFlush(t, p)
	forget(p)
	if got := ids(getFC(t, p, "/v1/vessels?bbox=59,10,60,11")); len(got) != 1 || got[0] != 992576072 {
		t.Errorf("an area five hours on holds %v, want only the aid", got)
	}
}

func TestMCPNamesGearsTypeAsAShipType(t *testing.T) {
	now := time.Now()
	buoy := newVessel()
	buoy.Kind, buoy.ShipType = "gear", 30
	if got := mcpRow(994123456, buoy, now).TypeName; got != shipTypeName(30) {
		t.Errorf("gear type named %q, want %q", got, shipTypeName(30))
	}
	aid := newVessel()
	aid.Kind, aid.ShipType = "aton", 30
	if got := mcpRow(992576072, aid, now).TypeName; got != atonTypeName(30) {
		t.Errorf("aid type named %q, want %q", got, atonTypeName(30))
	}
}
