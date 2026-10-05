package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMarineCadastreListsEveryPage(t *testing.T) {
	pages := map[string]string{
		"": `<?xml version="1.0" encoding="utf-8"?><EnumerationResults><Blobs>
			<Blob><Name>csv2/csv2026/ais-2026-01-01.csv.zst</Name><Properties><Content-Length>239242075</Content-Length><Etag>0x1</Etag></Properties></Blob>
			<Blob><Name>csv2/csv2026/index.html</Name><Properties><Content-Length>10</Content-Length><Etag>0x2</Etag></Properties></Blob>
			</Blobs><NextMarker>m2</NextMarker></EnumerationResults>`,
		"m2": `<?xml version="1.0" encoding="utf-8"?><EnumerationResults><Blobs>
			<Blob><Name>csv2/csv2026/ais-2026-01-02.csv.zst</Name><Properties><Content-Length>5</Content-Length><Etag>0x3</Etag></Properties></Blob>
			</Blobs><NextMarker/></EnumerationResults>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prefix") != "csv2/csv2026/" {
			fmt.Fprint(w, `<EnumerationResults><Blobs/></EnumerationResults>`)
			return
		}
		fmt.Fprint(w, pages[r.URL.Query().Get("marker")])
	}))
	defer srv.Close()
	was := mcContainer
	mcContainer = srv.URL
	defer func() { mcContainer = was }()

	files, err := mcList(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].day.Format("2006-01-02") != "2026-01-01" || files[0].size != 239242075 || files[0].etag != "0x1" ||
		files[0].url != srv.URL+"/csv2/csv2026/ais-2026-01-01.csv.zst" || files[1].day.Format("2006-01-02") != "2026-01-02" {
		t.Errorf("files: %+v", files)
	}
}

// csv2 rows for a fixture day: header, then rows.
func mcFixture(rows ...string) string {
	return "mmsi,base_date_time,longitude,latitude,sog,cog,heading,vessel_name,imo,call_sign,vessel_type,status,length,width,draft,cargo,transceiver\n" +
		strings.Join(rows, "\n") + "\n"
}

// fixtureSource is MarineCadastre reading files from fixtures instead of Azure.
func fixtureSource(files map[string]string) historySource {
	s := marineCadastre(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	s.read = func(f historyFile) string {
		return mcSelect("format('CSVWithNames', " + sqlString(mcColumns) + ", " + sqlString(files[f.name]) + ")")
	}
	return s
}

func TestHistoryLoadsAMarineCadastreDay(t *testing.T) {
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
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })

	// A live copy the network already holds, of the vessel's 12:00:30 report: AISHub's stamp 28 seconds later.
	day := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	liveAt := day.Add(12*time.Hour + 58*time.Second)
	live := trackPoint{mmsi: 367567110, ts: liveAt, txAt: liveAt, txDisc: 77, lat6: int32(math.Round(42.36017 * 600000)), lon6: int32(math.Round(-71.02771 * 600000)),
		sog10: 9, cog10: 3450, heading: 511, navStatus: 0, source: "aishub", station: "aishub", recv: liveAt.Add(7 * time.Second)}
	if err := conn.insert(ctx, "live", []trackPoint{live}); err != nil {
		t.Fatal(err)
	}

	file := mcFixture(
		"367567110,2026-06-30 12:00:00,-71.02800,42.35990,0.9,345.0,,CETACEA,IMO8678968,WDG7421,60,0,25,9,2.0,60,A",
		"367567110,2026-06-30 12:00:30,-71.02771,42.36017,0.9,345.0,,CETACEA,IMO8678968,WDG7421,60,0,25,9,2.0,60,A", // the live copy
		"367567110,2026-06-30 12:00:30,-71.02771,42.36017,0.9,345.0,,CETACEA,IMO8678968,WDG7421,60,0,25,9,2.0,60,A", // the same row again
		"367567110,2026-06-30 12:01:30,-60.00000,42.36000,0.9,345.0,,CETACEA,IMO8678968,WDG7421,60,0,25,9,2.0,60,A", // a lone spike
		"367567110,2026-06-30 12:02:30,-71.02700,42.36050,0.9,345.0,,CETACEA,IMO8678968,WDG7421,60,0,25,9,2.0,60,A",
		"993670001,2026-06-30 12:00:00,-71.0,42.3,,,,BUOY,,,,,,,,,A", // an aid to navigation
		"367567111,2026-06-30 12:00:00,0,0,,,,ZERO,,,,,,,,,B",        // a GPS default, no fix
		"367567112,2026-07-01 00:00:05,-71.0,42.3,,,,LATE,,,,,,,,,B", // the next day's
		`367567113,2026-06-30 13:00:00,-70.5,42.1,5.0,90.0,91,"SMITH, JOHN",,WXY123,37,8,12,4,1.5,,B`,
		"367567114,2026-06-30 14:00:00,-70.9,42.2,0.0,0.0,,MOORED,,,,5,,,,,A", // moored: moving until it has an anchor, then still
		"367567114,2026-06-30 14:03:00,-70.9,42.2,0.0,0.0,,MOORED,,,,5,,,,,A",
		"367567114,2026-06-30 14:06:00,-70.9,42.2,0.0,0.0,,MOORED,,,,5,,,,,A",
	)
	files := map[string]string{"csv2/csv2026/ais-2026-06-30.csv.zst": file}
	s := fixtureSource(files)
	s.list = func(context.Context) ([]historyFile, error) {
		return []historyFile{{name: "csv2/csv2026/ais-2026-06-30.csv.zst", day: day, size: 100, etag: "0x1"}}, nil
	}
	stats := newHistoryStats([]historySource{s})
	if err := conn.loadHistory(ctx, s, stats); err != nil {
		t.Fatal(err)
	}

	type row struct {
		ts                            time.Time
		accepted, implausible, moving bool
		off, delay                    int32
		disc                          uint8
	}
	rows := func() []row {
		t.Helper()
		r, err := conn.conn.Query(ctx, "SELECT ts, accepted, implausible, moving, tx_off, recv_delay, tx_disc FROM "+db+".receptions WHERE source = 'marinecadastre' ORDER BY mmsi, ts")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []row
		for r.Next() {
			var x row
			if err := r.Scan(&x.ts, &x.accepted, &x.implausible, &x.moving, &x.off, &x.delay, &x.disc); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}
	got := rows()
	if len(got) != 8 {
		t.Fatalf("eight receptions, the repeat, the aid, the default, and the next day's left out: %+v", got)
	}
	if !got[0].accepted || got[0].implausible || got[0].off != 0 {
		t.Errorf("an unmatched row is its own transmission: %+v", got[0])
	}
	if got[1].accepted || got[1].off != 28000 || got[1].disc != 77 {
		t.Errorf("the row the network already heard names that transmission, 28 seconds on: %+v", got[1])
	}
	if got[0].delay != math.MaxInt32 {
		t.Errorf("an archive row arrives when it loaded, clamped: %d", got[0].delay)
	}
	if !got[5].moving || got[6].moving || got[7].moving {
		t.Errorf("a moored vessel is still once it has an anchor: %+v", got[5:])
	}
	if !got[2].implausible || got[3].implausible {
		t.Errorf("the spike is flagged, and only the spike: %+v %+v", got[2], got[3])
	}

	var read, unplaced, repeated, kept, matched, implausible uint64
	var complete bool
	if err := conn.conn.QueryRow(ctx, "SELECT complete, read, unplaced, repeated, kept, matched, implausible FROM "+db+".history_loads FINAL").
		Scan(&complete, &read, &unplaced, &repeated, &kept, &matched, &implausible); err != nil {
		t.Fatal(err)
	}
	if !complete || read != 12 || unplaced != 3 || repeated != 1 || kept != 8 || matched != 1 || implausible != 1 {
		t.Errorf("history_loads: complete %v read %d unplaced %d repeated %d kept %d matched %d implausible %d",
			complete, read, unplaced, repeated, kept, matched, implausible)
	}
	if stats.loaded["marinecadastre"].Load() != 1 || stats.rows["marinecadastre"].Load() != 8 || !stats.latest["marinecadastre"].Equal(day) {
		t.Errorf("stats: %d files %d rows latest %v", stats.loaded["marinecadastre"].Load(), stats.rows["marinecadastre"].Load(), stats.latest["marinecadastre"])
	}

	var name, callsign string
	var imo uint32
	var length, draught10 uint16
	q := "SELECT argMaxMerge(name), argMaxMerge(callsign), argMaxMerge(imo), argMaxMerge(length), argMaxMerge(draught10) FROM " + db +
		".vessel_statics WHERE mmsi = ? GROUP BY mmsi, source"
	if err := conn.conn.QueryRow(ctx, q, 367567110).Scan(&name, &callsign, &imo, &length, &draught10); err != nil ||
		name != "CETACEA" || callsign != "WDG7421" || imo != 8678968 || length != 25 || draught10 != 20 {
		t.Errorf("statics: %q %q %d %d %d %v", name, callsign, imo, length, draught10, err)
	}
	if err := conn.conn.QueryRow(ctx, "SELECT argMaxMerge(name) FROM "+db+".vessel_statics WHERE mmsi = 367567113 GROUP BY mmsi").Scan(&name); err != nil || name != "SMITH, JOHN" {
		t.Errorf("a quoted name: %q %v", name, err)
	}

	// The view serves the vessel's day once per transmission, the live copy first, and never the spike.
	points, err := conn.history(ctx, 367567110, day.Add(12*time.Hour), day.Add(13*time.Hour), 0, 100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 || points[1].source != "aishub" {
		t.Errorf("served: %+v", points)
	}

	minutes := func() uint64 {
		var n uint64
		conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions_1m FINAL").Scan(&n)
		return n
	}
	before := minutes()

	// Loading the same file again with nothing changed loads nothing; a changed file replaces its day.
	if err := conn.loadHistory(ctx, s, stats); err != nil || stats.loaded["marinecadastre"].Load() != 1 {
		t.Errorf("an unchanged file is not loaded again: %d loads, %v", stats.loaded["marinecadastre"].Load(), err)
	}
	s.list = func(context.Context) ([]historyFile, error) {
		return []historyFile{{name: "csv2/csv2026/ais-2026-06-30.csv.zst", day: day, size: 101, etag: "0x2"}}, nil
	}
	if err := conn.loadHistory(ctx, s, stats); err != nil {
		t.Fatal(err)
	}
	if again := rows(); len(again) != 8 {
		t.Errorf("a changed file replaces its day rather than adding to it: %d rows", len(again))
	}
	if after := minutes(); after != before {
		t.Errorf("positions_1m is rebuilt with the day: %d rows, %d before", after, before)
	}
}

// Every lightweight delete a reload or rebuild runs waits for its rows to go, even where the session would let it
// return first, so the rows that follow never meet the ones it deletes.
func TestDeletesWaitForTheirRows(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db+"?lightweight_deletes_sync=0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	day := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	s := fixtureSource(map[string]string{"f": mcFixture("367567110,2026-06-30 12:00:00,-71.02800,42.35990,0.9,345.0,,CETACEA,,,,,,,,,A")})
	var session uint64
	if err := conn.conn.QueryRow(ctx, "SELECT toUInt64(getSetting('lightweight_deletes_sync'))").Scan(&session); err != nil || session != 0 {
		t.Fatalf("the session lets a delete return first: %d %v", session, err)
	}
	etag := "1"
	s.list = func(context.Context) ([]historyFile, error) {
		return []historyFile{{name: "f", day: day, size: 1, etag: etag}}, nil
	}
	stats := newHistoryStats([]historySource{s})
	for _, e := range []string{"1", "2"} { // a load, then a reload that deletes the day and rebuilds positions_1m
		etag = e
		if err := conn.loadHistory(ctx, s, stats); err != nil || stats.failed["marinecadastre"].Load() != 0 {
			t.Fatalf("load %s: %v", e, err)
		}
	}
	if err := conn.conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	// The log keeps only settings that differ from the server's default, 2: a delete that returned first shows 0.
	var deletes, waited uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count(), countIf(Settings['lightweight_deletes_sync'] != '0') FROM system.query_log"+
		" WHERE type = 'QueryFinish' AND startsWith(query, 'DELETE FROM') AND position(query, ?) > 0", db).Scan(&deletes, &waited); err != nil {
		t.Fatal(err)
	}
	if deletes < 2 || waited != deletes {
		t.Errorf("%d of %d deletes waited for their rows", waited, deletes)
	}
}

// A listing that stalls gives up, so the next check runs.
func TestHistoryListingThatStallsGivesUp(t *testing.T) {
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
	t.Cleanup(func() { conn.conn.Exec(ctx, "DROP DATABASE "+db); conn.conn.Close() })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // the container never answers
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	was, wasTimeout := mcContainer, historyListTimeout
	mcContainer, historyListTimeout = srv.URL, 200*time.Millisecond
	defer func() { mcContainer, historyListTimeout = was, wasTimeout }()
	s := marineCadastre(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	stats := newHistoryStats([]historySource{s})
	done := make(chan error, 1)
	go func() { done <- conn.loadHistory(ctx, s, stats) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a stalled listing reported no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled listing held the loader")
	}
	// The failed check is counted, and an archive that has never loaded a day reads as stale, not absent, so the
	// alert sees both.
	p := testPipeline(t)
	p.history = stats
	rec := httptest.NewRecorder()
	p.serveMetrics(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`aiscast_history_check_failures_total{source="marinecadastre"} 1`, `aiscast_history_latest_day_timestamp_seconds{source="marinecadastre"} 0`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}
