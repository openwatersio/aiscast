package main

import (
	"context"
	"fmt"
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

	// A live copy the network already holds, of the vessel's 12:00:30 report: AISHub's stamp two seconds later.
	day := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	live := trackPoint{mmsi: 367567110, ts: day.Add(12*time.Hour + 32*time.Second), lat6: int32(42.36017 * 600000), lon6: int32(-71.02771 * 600000),
		sog10: 9, cog10: 3450, heading: 511, navStatus: 0, source: "aishub", station: "aishub", tx: 77, recv: day.Add(12*time.Hour + 40*time.Second)}
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
		ts                    time.Time
		accepted, implausible bool
		tx                    uint64
	}
	rows := func() []row {
		t.Helper()
		r, err := conn.conn.Query(ctx, "SELECT ts, accepted, implausible, tx FROM "+db+".receptions WHERE source = 'marinecadastre' ORDER BY mmsi, ts")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []row
		for r.Next() {
			var x row
			if err := r.Scan(&x.ts, &x.accepted, &x.implausible, &x.tx); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}
	got := rows()
	if len(got) != 5 {
		t.Fatalf("five receptions, the repeat, the aid, the default, and the next day's left out: %+v", got)
	}
	if !got[0].accepted || got[0].implausible {
		t.Errorf("an unmatched row is its own transmission: %+v", got[0])
	}
	if got[1].accepted || got[1].tx != 77 {
		t.Errorf("the row the network already heard names that transmission: %+v", got[1])
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
	if !complete || read != 9 || unplaced != 3 || repeated != 1 || kept != 5 || matched != 1 || implausible != 1 {
		t.Errorf("history_loads: complete %v read %d unplaced %d repeated %d kept %d matched %d implausible %d",
			complete, read, unplaced, repeated, kept, matched, implausible)
	}
	if stats.loaded["marinecadastre"].Load() != 1 || stats.rows["marinecadastre"].Load() != 5 || !stats.latest["marinecadastre"].Equal(day) {
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
	if again := rows(); len(again) != 5 {
		t.Errorf("a changed file replaces its day rather than adding to it: %d rows", len(again))
	}
}
