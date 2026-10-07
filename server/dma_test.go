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

func TestDMAListsEveryLayout(t *testing.T) {
	pages := map[string]string{
		"": `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t2</NextContinuationToken>
			<Contents><Key>!_README_information_CSV_files.txt</Key><ETag>&quot;r&quot;</ETag><Size>2296</Size></Contents>
			<Contents><Key>2017/all_sources_2017-02.zip</Key><ETag>&quot;a-1&quot;</ETag><Size>100</Size></Contents>
			<Contents><Key>2018/aisdk-2018-01.zip</Key><ETag>&quot;b-2&quot;</ETag><Size>200</Size></Contents>
			</ListBucketResult>`,
		"t2": `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>2024/aisdk-2024-03-01.zip</Key><ETag>&quot;c-3&quot;</ETag><Size>300</Size></Contents>
			<Contents><Key>aisdk-2026-10-01.zip</Key><ETag>&quot;d-4&quot;</ETag><Size>724971502</Size></Contents>
			</ListBucketResult>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aisdata.ais.dk" || r.URL.Query().Get("list-type") != "2" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, pages[r.URL.Query().Get("continuation-token")])
	}))
	defer srv.Close()
	was := dmaBucket
	dmaBucket = &s3Client{endpoint: srv.URL, bucket: "aisdata.ais.dk"}
	defer func() { dmaBucket = was }()

	files, err := dmaList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 28+31+1+1 {
		t.Fatalf("a file a day, of every month: %d", len(files))
	}
	byName := map[string]historyFile{}
	for _, f := range files {
		byName[f.name] = f
	}
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	for name, want := range map[string]historyFile{
		"2017/all_sources_2017-02.zip :: 2017-02-28": {url: srv.URL + "/aisdata.ais.dk/2017/all_sources_2017-02.zip :: *{20170228,2017-02-28}.csv", day: day("2017-02-28"), size: 100, etag: "a-1"},
		"2018/aisdk-2018-01.zip :: 2018-01-15":       {url: srv.URL + "/aisdata.ais.dk/2018/aisdk-2018-01.zip :: *{20180115,2018-01-15}.csv", day: day("2018-01-15"), size: 200, etag: "b-2"},
		"2024/aisdk-2024-03-01.zip":                  {url: srv.URL + "/aisdata.ais.dk/2024/aisdk-2024-03-01.zip :: *.csv", day: day("2024-03-01"), size: 300, etag: "c-3"},
		"aisdk-2026-10-01.zip":                       {url: srv.URL + "/aisdata.ais.dk/aisdk-2026-10-01.zip :: *.csv", day: day("2026-10-01"), size: 724971502, etag: "d-4"},
	} {
		got := byName[name]
		if got.url != want.url || !got.day.Equal(want.day) || got.size != want.size || got.etag != want.etag {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// A file's read takes the CSV its url names, anonymously.
func TestDMAReadsTheFilesEntry(t *testing.T) {
	q := dmaRead(historyFile{url: "https://s3.eu-central-1.amazonaws.com/aisdata.ais.dk/2018/aisdk-2018-01.zip :: *{20180115,2018-01-15}.csv"})
	if !strings.Contains(q, "s3('https://s3.eu-central-1.amazonaws.com/aisdata.ais.dk/2018/aisdk-2018-01.zip :: *{20180115,2018-01-15}.csv', NOSIGN, 'CSVWithNames'") {
		t.Errorf("read: %s", q)
	}
}

const (
	dmaHeader22 = "# Timestamp,Type of mobile,MMSI,Latitude,Longitude,Navigational status,ROT,SOG,COG,Heading,IMO,Callsign,Name,Ship type,Cargo type,Width,Length,Type of position fixing device,Draught,Destination,ETA,Data source type"
	dmaHeader26 = dmaHeader22 + ",A,B,C,D"
)

func TestHistoryLoadsDMADaysOfBothEras(t *testing.T) {
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

	// AISHub's copy of the vessel's 12:00:10 report, stamped 28 seconds later.
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	liveAt := day.Add(12*time.Hour + 38*time.Second)
	live := trackPoint{mmsi: 219000001, ts: liveAt, txAt: liveAt, txDisc: 77, lat6: int32(math.Round(55.7005 * 600000)), lon6: int32(math.Round(12.601 * 600000)),
		sog10: 102, cog10: 450, heading: 44, source: "aishub", station: "aishub", recv: liveAt.Add(7 * time.Second)}
	if err := conn.insert(ctx, "live", []trackPoint{live}); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"aisdk-2026-09-30.zip": strings.Join([]string{dmaHeader26,
			"30/09/2026 12:00:00,Class A,219000001,55.700000,12.600000,Under way using engine,0.0,10.2,45.0,44,Unknown,Unknown,,Undefined,,,,Undefined,,Unknown,,AIS,,,,",
			"30/09/2026 12:00:10,Class A,219000001,55.700500,12.601000,Under way using engine,0.0,10.2,45.0,44,Unknown,Unknown,,Undefined,,,,Undefined,,Unknown,,AIS,,,,", // AISHub's
			"30/09/2026 12:00:10,Class A,219000001,55.700500,12.601000,Under way using engine,0.0,10.2,45.0,44,Unknown,Unknown,,Undefined,,,,Undefined,,Unknown,,AIS,,,,", // again
			"30/09/2026 12:00:20,Base Station,2190064,56.716560,11.519037,Unknown value,,,,,Unknown,Unknown,,Undefined,,,,GPS,,Unknown,,AIS,,,,",
			"30/09/2026 12:00:20,AtoN,992111851,54.441580,7.678878,Unknown value,,,,,Unknown,Unknown,NSO 0 PLATFORM,Undefined,,,,GPS,,Unknown,,AIS,,,,",
			"30/09/2026 12:05:00,Class A,219000001,91.000000,0.000000,Unknown value,,,,,9234567,OXAB2,NORDIC STAR,Cargo,No additional information,20,120,GPS,6.5,COPENHAGEN,,AIS,100,20,10,10", // a static report
			"01/10/2026 00:00:05,Class A,219000001,55.800000,12.700000,Under way using engine,0.0,10.2,45.0,44,Unknown,Unknown,,Undefined,,,,Undefined,,Unknown,,AIS,,,,",                      // the next day's
			`30/09/2026 13:00:00,Class B,219000002,55.600000,12.500000,Unknown value,,0.0,,,Unknown,Unknown,"SEA, BREEZE",Pleasure,,3,10,Undefined,,Unknown,,AIS,,,,`,
			"30/09/2026 14:00:00,Class A,219000003,55.500000,12.400000,Moored,0.0,0.0,,,Unknown,Unknown,,Tanker,,,,Undefined,,Unknown,,AIS,,,,",
		}, "\n") + "\n",
		"2018/aisdk-2018-01.zip :: 2018-01-15": strings.Join([]string{dmaHeader22,
			"15/01/2018 08:00:00,Class A,219000004,55.400000,12.300000,At anchor,0.0,0.1,10.0,5,Unknown,,HAVFRUE,Fishing,,6,24,Undefined,3.2,,,AIS",
		}, "\n") + "\n",
	}
	old := time.Date(2018, 1, 15, 0, 0, 0, 0, time.UTC)
	s := dma(old)
	s.read = func(f historyFile) string {
		return dmaSelect("format('CSVWithNames', " + sqlString(dmaColumns) + ", " + sqlString(files[f.name]) + ")")
	}
	s.list = func(context.Context) ([]historyFile, error) {
		return []historyFile{{name: "aisdk-2026-09-30.zip", day: day, size: 100, etag: "d-1"}, {name: "2018/aisdk-2018-01.zip :: 2018-01-15", day: old, size: 200, etag: "b-2"}}, nil
	}
	stats := newHistoryStats([]historySource{s})
	if err := conn.loadHistory(ctx, s, stats); err != nil {
		t.Fatal(err)
	}
	if stats.loaded["dma"].Load() != 2 || stats.failed["dma"].Load() != 0 {
		t.Fatalf("both days load: %d loaded, %d failed", stats.loaded["dma"].Load(), stats.failed["dma"].Load())
	}

	type row struct {
		mmsi                  uint32
		ts                    time.Time
		sog10, cog10, heading uint16
		navstat               uint8
		accepted              bool
		off                   int32
		disc                  uint8
	}
	r, err := conn.conn.Query(ctx, "SELECT mmsi, ts, sog10, cog10, heading, navstat, accepted, tx_off, tx_disc FROM "+db+".receptions WHERE source = 'dma' ORDER BY mmsi, ts")
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for r.Next() {
		var x row
		if err := r.Scan(&x.mmsi, &x.ts, &x.sog10, &x.cog10, &x.heading, &x.navstat, &x.accepted, &x.off, &x.disc); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	r.Close()
	if len(got) != 5 {
		t.Fatalf("five receptions, without the repeat, the stations, the static report, and the next day's: %+v", got)
	}
	if !got[0].ts.Equal(day.Add(12*time.Hour)) || got[0].sog10 != 102 || got[0].cog10 != 450 || got[0].heading != 44 || got[0].navstat != 0 || !got[0].accepted {
		t.Errorf("a day-first stamp, motion, and status: %+v", got[0])
	}
	if got[1].accepted || got[1].off != 28000 || got[1].disc != 77 {
		t.Errorf("the row AISHub delivered names its transmission, 28 seconds on: %+v", got[1])
	}
	if got[2].navstat != 15 || got[2].cog10 != 3600 || got[2].heading != 511 {
		t.Errorf("unknown status, course, and heading: %+v", got[2])
	}
	if got[3].navstat != 5 {
		t.Errorf("moored: %+v", got[3])
	}
	if !got[4].ts.Equal(old.Add(8*time.Hour)) || got[4].navstat != 1 || got[4].sog10 != 1 || got[4].heading != 5 {
		t.Errorf("a row from the 22-column era: %+v", got[4])
	}

	var read, unplaced, repeated, kept, matched uint64
	if err := conn.conn.QueryRow(ctx, "SELECT read, unplaced, repeated, kept, matched FROM "+db+".history_loads FINAL WHERE file = 'aisdk-2026-09-30.zip'").
		Scan(&read, &unplaced, &repeated, &kept, &matched); err != nil {
		t.Fatal(err)
	}
	if read != 9 || unplaced != 4 || repeated != 1 || kept != 4 || matched != 1 {
		t.Errorf("history_loads: read %d unplaced %d repeated %d kept %d matched %d", read, unplaced, repeated, kept, matched)
	}

	type statics struct {
		name, callsign, destination string
		imo                         uint32
		shipType                    uint8
		length, beam, draught10     uint16
	}
	static := func(mmsi uint32) statics {
		t.Helper()
		var s statics
		if err := conn.conn.QueryRow(ctx, "SELECT argMaxMerge(name), argMaxMerge(callsign), argMaxMerge(destination), argMaxMerge(imo), argMaxMerge(ship_type), "+
			"argMaxMerge(length), argMaxMerge(beam), argMaxMerge(draught10) FROM "+db+".vessel_statics WHERE mmsi = ? AND source = 'dma' GROUP BY mmsi", mmsi).
			Scan(&s.name, &s.callsign, &s.destination, &s.imo, &s.shipType, &s.length, &s.beam, &s.draught10); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := static(219000001); s != (statics{"NORDIC STAR", "OXAB2", "COPENHAGEN", 9234567, 70, 120, 20, 65}) {
		t.Errorf("a static report without a position: %+v", s)
	}
	if s := static(219000002); s != (statics{name: "SEA, BREEZE", shipType: 37, length: 10, beam: 3}) {
		t.Errorf("a quoted name, and Unknown as empty: %+v", s)
	}
	if s := static(219000004); s != (statics{name: "HAVFRUE", shipType: 30, length: 24, beam: 6, draught10: 32}) {
		t.Errorf("statics from the 22-column era: %+v", s)
	}
	var stations uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".vessel_statics WHERE mmsi IN (2190064, 992111851)").Scan(&stations); err != nil || stations != 0 {
		t.Errorf("base stations and aids to navigation are not vessels: %d %v", stations, err)
	}
}
