package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// fakeStatics records the static states it is handed, and fails the first `fail` inserts.
type fakeStatics struct {
	fail    int
	batches []map[staticKey]staticTimes
}

func (f *fakeStatics) insertStatics(_ context.Context, rows map[staticKey]staticTimes) error {
	f.batches = append(f.batches, maps.Clone(rows))
	if f.fail > 0 {
		f.fail--
		return errors.New("clickhouse is down")
	}
	return nil
}

// Each static message gives the state it carries, as sent; other messages give none.
func TestStaticOfReadsEachStaticMessage(t *testing.T) {
	five := ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000001}, Valid: true, Name: "CETACEA", CallSign: "LAXY",
		ImoNumber: 9123456, Type: 70, Dimension: ais.FieldDimension{A: 100, B: 20, C: 8, D: 7}, MaximumStaticDraught: 7.4,
		Destination: "OSLO", Eta: ais.FieldETA{Month: 10, Day: 14, Hour: 8, Minute: 30}}
	k, ok := staticOf(five)
	if !ok || k.message != "5" || k.name != "CETACEA" || k.callsign != "LAXY" || k.imo != 9123456 || k.shipType != 70 ||
		k.toBow != 100 || k.toStern != 20 || k.toPort != 8 || k.toStarboard != 7 || k.draught10 != 74 || k.destination != "OSLO" || k.eta != "10-14 08:30" {
		t.Errorf("type 5: %+v", k)
	}
	// ETA's not-available values are kept as sent.
	five.Eta = ais.FieldETA{Hour: 24, Minute: 60}
	if k, _ := staticOf(five); k.eta != "00-00 24:60" {
		t.Errorf("an ETA not sent: %q", k.eta)
	}
	a := ais.StaticDataReport{Header: ais.Header{MessageID: 24, UserID: 257000002}, Valid: true}
	a.ReportA.Valid, a.ReportA.Name = true, "TENDER"
	if k, ok := staticOf(a); !ok || k.message != "24A" || k.name != "TENDER" || k.shipType != 0 {
		t.Errorf("24A: %+v", k)
	}
	b := ais.StaticDataReport{Header: ais.Header{MessageID: 24, UserID: 257000002}, Valid: true, PartNumber: true}
	b.ReportB.Valid, b.ReportB.CallSign, b.ReportB.ShipType = true, "LBXZ", 37
	b.ReportB.Dimension = ais.FieldDimension{A: 6, B: 2, C: 1, D: 2}
	if k, ok := staticOf(b); !ok || k.message != "24B" || k.callsign != "LBXZ" || k.shipType != 37 || k.toBow != 6 || k.name != "" {
		t.Errorf("24B: %+v", k)
	}
	nineteen := ais.ExtendedClassBPositionReport{Header: ais.Header{MessageID: 19, UserID: 257000003}, Valid: true, Name: "SKAGEN", Type: 36}
	if k, ok := staticOf(nineteen); !ok || k.message != "19" || k.name != "SKAGEN" || k.shipType != 36 {
		t.Errorf("19: %+v", k)
	}
	if _, ok := staticOf(posReport(257000004, 59.9, 10.7)); ok {
		t.Error("a position report gave a static state")
	}
}

// Every copy of a static message counts under its source, the first delivered and the duplicates alike, as one row
// per vessel, day, source, and state with when it was first and last heard. A failed insert keeps its rows for the
// next flush, and the positions still go.
func TestStaticsGatherPerSourceAndFlush(t *testing.T) {
	p := testPipeline(t)
	st := &fakeStatics{fail: 1}
	ch := &fakeCH{}
	p.attachClickHouse(&chStore{w: ch, own: &fakeOwn{}, statics: st})
	now := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	msg := ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: 257000001}, Valid: true, Name: "CETACEA", Type: 70}
	p.ingestPacket("kystverket", "kystverket", now, now, msg)
	p.ingestPacket("aishub", "aishub", now, now.Add(time.Second), msg) // the same message, a duplicate from another source
	p.ingestPacket("kystverket", "kystverket", now.Add(6*time.Minute), now.Add(6*time.Minute), msg)
	ingestAt(p, 257000001, now, 59.9)
	if err := p.flushClickHouse(); err == nil || !strings.Contains(err.Error(), "statics") {
		t.Errorf("a failed statics insert reads as written: %v", err)
	}
	var sent int
	for _, b := range ch.batches {
		sent += len(b)
	}
	if sent == 0 {
		t.Error("the positions waited on the statics")
	}
	if len(st.batches) == 0 {
		t.Fatal("no statics sent")
	}
	rows := st.batches[0]
	if len(rows) != 2 {
		t.Fatalf("%d rows, want one per source: %+v", len(rows), rows)
	}
	for k, at := range rows {
		switch k.source {
		case "kystverket":
			if !at.first.Equal(now) || !at.last.Equal(now.Add(6*time.Minute)) {
				t.Errorf("kystverket heard %v to %v, want %v to %v", at.first, at.last, now, now.Add(6*time.Minute))
			}
		case "aishub":
			if !at.first.Equal(now) || !at.last.Equal(now) {
				t.Errorf("aishub's duplicate: %+v", at)
			}
		default:
			t.Errorf("source %q", k.source)
		}
		if k.day != now.Unix()/86400 || k.message != "5" || k.name != "CETACEA" {
			t.Errorf("key %+v", k)
		}
	}
	// The failed rows go again with the next flush.
	p.flushClickHouse()
	if len(st.batches) < 2 || !maps.Equal(st.batches[0], st.batches[1]) {
		t.Errorf("a failed insert kept its rows for the next: %d batches", len(st.batches))
	}
}

// TestStaticsFromClickHouse runs against a real server named by CLICKHOUSE_TEST_URL: rows of one state merge to one
// with the earliest and latest times, and the record seeds one row per vessel, once.
func TestStaticsFromClickHouse(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	k := staticKey{mmsi: 257000001, day: day.Unix() / 86400, source: "kystverket", message: "5", name: "CETACEA", shipType: 70}
	for _, at := range []staticTimes{{day.Add(time.Hour), day.Add(2 * time.Hour)}, {day.Add(30 * time.Minute), day.Add(time.Hour)}} {
		if err := conn.insertStatics(ctx, map[staticKey]staticTimes{k: at}); err != nil {
			t.Fatal(err)
		}
	}
	var n uint64
	var first, last time.Time
	if err := conn.conn.QueryRow(ctx, "SELECT count(), min(first_ts), max(last_ts) FROM (SELECT * FROM "+db+".statics FINAL)").Scan(&n, &first, &last); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !first.Equal(day.Add(30*time.Minute)) || !last.Equal(day.Add(2*time.Hour)) {
		t.Errorf("merged to %d rows, %v to %v", n, first, last)
	}

	st, err := openStore(filepath.Join(t.TempDir(), "aiscast.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	staticAt := day.Add(3 * time.Hour).UnixMilli()
	for _, q := range []string{
		fmt.Sprintf("INSERT INTO vessels (mmsi, name, callsign, ship_type, destination, eta, draught, to_bow, to_stern, static_at, seen, first_seen) VALUES (257000002, 'SKAGEN', 'LAXZ', 30, 'BERGEN', %d, 4.2, 20, 5, %d, %d, %d)",
			10<<24|14<<16|8<<8|30, staticAt, staticAt, staticAt),
		fmt.Sprintf("INSERT INTO vessels (mmsi, ship_type, seen, first_seen) VALUES (257000003, 52, %d, %d)", staticAt, staticAt), // no static time
		fmt.Sprintf("INSERT INTO vessels (mmsi, seen, first_seen) VALUES (257000004, %d, %d)", staticAt, staticAt),                // no static data
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // the second start finds it seeded
		if err := conn.seedStatics(ctx, st.db); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := conn.conn.Query(ctx, "SELECT mmsi, name, destination, eta, draught10, to_bow, first_ts, count() OVER () FROM "+db+".statics WHERE source = 'record' ORDER BY mmsi")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var mmsi uint32
		var name, dest, eta string
		var draught10, toBow uint16
		var at time.Time
		var total uint64
		if err := rows.Scan(&mmsi, &name, &dest, &eta, &draught10, &toBow, &at, &total); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d %s %s %s %d %d %s %d", mmsi, name, dest, eta, draught10, toBow, at.Format("15:04"), total))
	}
	want := []string{"257000002 SKAGEN BERGEN 10-14 08:30 42 20 03:00 2", "257000003    0 0 03:00 2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("seeded %q, want %q", got, want)
	}
}

// An AISHub snapshot repeats every vessel each minute, so a row counts for statics once per TIME it carries, and
// again when the TIME moves on, whether or not the row is news to the vessel cache.
func TestAishubStaticsOncePerTime(t *testing.T) {
	p := testPipeline(t)
	st := &fakeStatics{}
	p.attachClickHouse(&chStore{w: &fakeCH{}, own: &fakeOwn{}, statics: st})
	row := func(ts, dest string) string {
		return fmt.Sprintf(`[[{"MMSI":244750034,"TIME":"%s","LONGITUDE":3022815,"LATITUDE":31476144,"NAME":"CHATEAUROUX","CALLSIGN":"PH7002","TYPE":69,"A":24,"B":6,"C":0,"D":6,"DRAUGHT":12,"DEST":"%s","ETA":1596}]]`, ts, dest)
	}
	now := time.Unix(1625826600, 0)
	p.ingestAishub([]byte(row("1625826523", "NLRTM")), now)
	p.ingestAishub([]byte(row("1625826523", "NLRTM")), now.Add(time.Minute)) // the same row in the next snapshot
	p.flushClickHouse()
	p.ingestAishub([]byte(row("1625826583", "NLRTM")), now.Add(2*time.Minute)) // heard again, unchanged
	p.flushClickHouse()
	if len(st.batches) != 2 {
		t.Fatalf("%d batches, want one per flush with a new TIME", len(st.batches))
	}
	for i, b := range st.batches {
		if len(b) != 1 {
			t.Fatalf("batch %d: %+v", i, b)
		}
		for k, at := range b {
			if k.source != "aishub" || k.station != "aishub" || k.destination != "NLRTM" || !at.first.Equal(at.last) {
				t.Errorf("batch %d: %+v %+v", i, k, at)
			}
		}
	}
}

// A volunteer's state is under its station, so one station can be purged alone; a feed's under its source, as its
// receivers are one source. A copy stamped a day before it arrived is a bad clock's and goes nowhere. Past the
// pending bound a new state is dropped and counted.
func TestStaticsStationsClocksAndBound(t *testing.T) {
	p := testPipeline(t)
	st := &fakeStatics{}
	c := &chStore{w: &fakeCH{}, own: &fakeOwn{}, statics: st}
	p.attachClickHouse(c)
	now := time.Now().UTC()
	msg := func(mmsi uint32) ais.ShipStaticData {
		return ais.ShipStaticData{Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true, Name: "X"}
	}
	p.ingestPacket("station:harbor", "station:harbor", now, now, msg(257000001))
	p.ingestPacket("kystverket", "kystverket/2573010", now, now, msg(257000002))
	p.ingestPacket("kystverket", "kystverket", now.Add(-25*time.Hour), now, msg(257000003))
	p.noteStaticPacket("kystverket", "kystverket", now.Add(-clockBadAge), now, msg(257000007)) // exactly at the bound receptions mark clock_bad at
	invalid := msg(257000004)
	invalid.Valid = false
	p.ingestPacket("kystverket", "kystverket", now, now, invalid)
	p.flushClickHouse()
	got := map[uint32]string{}
	for k := range st.batches[0] {
		got[k.mmsi] = k.source + " " + k.station
	}
	if want := map[uint32]string{257000001: "station station:harbor", 257000002: "kystverket kystverket"}; !maps.Equal(got, want) {
		t.Errorf("states %v, want %v", got, want)
	}
	was := maxStaticsPending
	maxStaticsPending = 1
	defer func() { maxStaticsPending = was }()
	p.ingestPacket("kystverket", "kystverket", now, now, msg(257000005))
	p.ingestPacket("kystverket", "kystverket", now, now, msg(257000006))
	if d := c.staticsDropped.Load(); d != 1 {
		t.Errorf("%d states dropped past a bound of 1, want 1", d)
	}
}

// An AISHub row carrying any static field counts, not only one with a name or IMO; and the TIMEs kept per vessel
// for that are forgotten once older than a bad clock's bound.
func TestAishubStaticFieldsAndPrune(t *testing.T) {
	p := testPipeline(t)
	st := &fakeStatics{}
	p.attachClickHouse(&chStore{w: &fakeCH{}, own: &fakeOwn{}, statics: st})
	now := time.Unix(1625826600, 0)
	body := `[[{"MMSI":244750035,"TIME":"1625826523","LONGITUDE":3022815,"LATITUDE":31476144,"TYPE":70,"DEST":"NLRTM"}]]`
	p.ingestAishub([]byte(body), now)
	p.flushClickHouse()
	if len(st.batches) != 1 || len(st.batches[0]) != 1 {
		t.Fatalf("a row with a type and destination but no name: %+v", st.batches)
	}
	for k := range st.batches[0] {
		if k.shipType != 70 || k.destination != "NLRTM" {
			t.Errorf("state %+v", k)
		}
	}
	p.chMu.Lock()
	p.aishubStaticAt[1] = now.Add(-clockBadAge - time.Second).Unix()
	p.chMu.Unlock()
	p.pruneAishubStatics(now)
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if _, old := p.aishubStaticAt[1]; old || len(p.aishubStaticAt) != 1 {
		t.Errorf("after pruning: %v", p.aishubStaticAt)
	}
}
