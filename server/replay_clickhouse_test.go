package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A replayed day replaces the network's own copies that arrived that day, and nothing else: not the day before,
// not an archive's rows. It is compared first, refused when it holds too few of the stored copies, rebuilds
// positions_1m, and marks the archive's day beside it to load again.
func TestReplayReplacesTheNetworksDay(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })

	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	const mmsi = 257000001
	enc := testPipeline(t)
	sentence := func(lat float64) string {
		return enc.encoder.EncodeSentence(aisnmeaPacket('A', enc.codec.EncodePacket(posReport(mmsi, lat, 10.7))))[0]
	}
	// The raw archive: six reports a minute apart on the day, one in the lead-in, which state sees and nothing
	// writes.
	dir := t.TempDir()
	lines := map[string][]string{}
	add := func(at time.Time, lat float64) {
		hour := at.Format("2006/01/02/15")
		lines[hour] = append(lines[hour], at.Format(time.RFC3339Nano)+"\tkystverket\t"+sentence(lat))
	}
	add(day.Add(-30*time.Minute), 59.80)
	for i := range 6 {
		add(day.Add(10*time.Hour+time.Duration(i)*time.Minute), 59.90+float64(i)/100)
	}
	for hour, ls := range lines {
		path := filepath.Join(dir, "NLOD-2.0", "kystverket", hour+".gz")
		os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		gz.Write([]byte(strings.Join(ls, "\n") + "\n"))
		gz.Close()
		f.Close()
	}

	// What receptions holds: the day as an older build wrote it, seven copies at other places; a copy the day
	// before; and an archive's copy on the day, with its load recorded as matching the network's transmissions.
	at := func(ts time.Time, lat float64, source string) trackPoint {
		return trackPoint{mmsi: mmsi, ts: ts, lat6: int32(lat * 600000), lon6: int32(10.7 * 600000), sog10: 1023, cog10: 3600, heading: 511,
			navStatus: 15, source: source, station: source, txAt: ts, txDisc: 1, recv: ts}
	}
	var stored []trackPoint
	for i := range 7 {
		stored = append(stored, at(day.Add(10*time.Hour+time.Duration(i)*time.Minute), 58.0, "kystverket"))
	}
	stored = append(stored, at(day.Add(-time.Hour), 57.0, "kystverket"))
	// A copy that arrived on the day 49 hours after its stamp, a reset clock's: past two days, so it stays.
	late := at(day.Add(-26*time.Hour), 55.0, "kystverket")
	late.recv = day.Add(23 * time.Hour)
	stored = append(stored, late)
	archive := at(day.Add(12*time.Hour), 56.0, "marinecadastre")
	archive.recv = time.Time{}
	if err := c.insert(ctx, "stored", append(stored, archive)); err != nil {
		t.Fatal(err)
	}
	if err := c.conn.Exec(ctx, "INSERT INTO "+db+".history_loads (source, file, day, etag, complete, matched, loaded) VALUES ('marinecadastre', 'f', ?, 'e', true, 1, now64(3))", day); err != nil {
		t.Fatal(err)
	}

	type row struct {
		ts     time.Time
		lat6   int32
		source string
	}
	all := func() []row {
		t.Helper()
		rows, err := c.conn.Query(ctx, "SELECT ts, lat6, source FROM "+db+".receptions ORDER BY source, ts")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			rows.Scan(&r.ts, &r.lat6, &r.source)
			out = append(out, r)
		}
		return out
	}
	before := all()
	warmup := corroborationWindow + vesselTTL
	if err := c.replayDay(ctx, dir, day, warmup, true, false); err != nil {
		t.Fatal(err)
	}
	if got := all(); len(got) != len(before) {
		t.Fatalf("a dry run changed receptions: %d rows, was %d", len(got), len(before))
	}
	if err := c.replayDay(ctx, dir, day, warmup, false, false); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("six replayed copies against seven stored replaced the day without -force: %v", err)
	}
	// Replayed twice, as after a fix that needs a second pass: the second replay's identical blocks land too.
	for range 2 {
		if err := c.replayDay(ctx, dir, day, warmup, false, true); err != nil {
			t.Fatal(err)
		}
	}

	var network, previous, archived []row
	for _, r := range all() {
		switch {
		case r.source == "marinecadastre":
			archived = append(archived, r)
		case r.ts.Before(day):
			previous = append(previous, r)
		default:
			network = append(network, r)
		}
	}
	if len(network) != 6 || network[0].lat6 != int32(59.90*600000) || network[5].lat6 != int32(59.95*600000) {
		t.Errorf("the day's copies are the replayed six: %+v", network)
	}
	if len(previous) != 2 || previous[0].lat6 != int32(55.0*600000) || previous[1].lat6 != int32(57.0*600000) ||
		len(archived) != 1 || archived[0].lat6 != int32(56.0*600000) {
		t.Errorf("the day before, the copy two days late, and the archive's copy are untouched: %+v %+v", previous, archived)
	}
	lats, err := chColumn[int32](ctx, c.conn, "SELECT lat6 FROM "+db+".positions_1m FINAL WHERE slot >= ? AND slot < ? AND source = 'kystverket' ORDER BY slot", day, day.AddDate(0, 0, 1))
	if err != nil || len(lats) != 6 || lats[0] != int32(59.90*600000) {
		t.Errorf("positions_1m holds the replayed track: %v %v", lats, err)
	}
	finest := coverageBands[len(coverageBands)-1].res
	cell := func(lat float64) uint64 {
		t.Helper()
		var n uint64
		if err := c.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s.coverage WHERE day = ? AND res = %d AND cell = geoToH3(?, 10.7, %d)"+
			" SETTINGS geotoh3_argument_order = 'lat_lon'", db, finest, finest), day, lat).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if cell(58.0) != 0 || cell(59.90) == 0 {
		t.Errorf("coverage drops the stored position's cell and has the replayed one's: %d %d", cell(58.0), cell(59.90))
	}
	complete, err := chColumn[bool](ctx, c.conn, "SELECT complete FROM "+db+".history_loads FINAL WHERE file = 'f'")
	if err != nil || len(complete) != 1 || complete[0] {
		t.Errorf("the archive's day is marked to load again: %v %v", complete, err)
	}
	if staging, _ := chColumn[string](ctx, c.conn, "SELECT name FROM system.tables WHERE database = ? AND startsWith(name, ?)", db, replayStaging); len(staging) != 0 {
		t.Error("the staging table is left behind")
	}
}

// Fetching a day lists the bucket a page at a time and takes the day's raw hours and its lead-in's, and no
// normalized or access log hour.
func TestReplayFetchesADaysRawHours(t *testing.T) {
	keys := []string{
		"NLOD-2.0/kystverket/2026/09/01/21.gz", // before the lead-in
		"NLOD-2.0/kystverket/2026/09/01/23.gz", // the lead-in
		"NLOD-2.0/kystverket/2026/09/02/00.gz",
		"CC0-1.0/station/ed25519/abc/2026/09/02/23.gz",
		"NLOD-2.0/kystverket/2026/09/03/00.gz", // the day after
		"normalized/v1/2026/09/02/05.gz",
		"access/v1/2026/09/02/05.gz",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("list-type") == "2" {
			// two keys a page, so the listing has to follow its continuation tokens
			start := 0
			fmt.Sscan(r.URL.Query().Get("continuation-token"), &start)
			end := min(start+2, len(keys))
			fmt.Fprint(w, "<ListBucketResult>")
			for _, k := range keys[start:end] {
				fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>3</Size></Contents>", k)
			}
			fmt.Fprintf(w, "<IsTruncated>%v</IsTruncated><NextContinuationToken>%d</NextContinuationToken></ListBucketResult>", end < len(keys), end)
			return
		}
		fmt.Fprint(w, strings.TrimPrefix(r.URL.Path, "/bucket/"))
	}))
	defer srv.Close()
	s3 := &s3Client{endpoint: srv.URL, region: "auto", bucket: "bucket", accessKey: "k", secretKey: "s"}
	listed, err := s3.list(context.Background(), "")
	if err != nil || len(listed) != len(keys) {
		t.Fatalf("listed %d of %d keys: %v", len(listed), len(keys), err)
	}
	dir := t.TempDir()
	if err := fetchRawDay(s3, listed, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), 90*time.Minute, dir); err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			body, _ := os.ReadFile(path)
			if string(body) != rel {
				t.Errorf("%s holds %q", rel, body)
			}
			got = append(got, rel)
		}
		return nil
	})
	slices.Sort(got)
	want := []string{"CC0-1.0/station/ed25519/abc/2026/09/02/23.gz", "NLOD-2.0/kystverket/2026/09/01/23.gz", "NLOD-2.0/kystverket/2026/09/02/00.gz"}
	if !slices.Equal(got, want) {
		t.Errorf("fetched %v, want %v", got, want)
	}
}

// A batch sent to staging again under its token, after an acknowledgement lost on the way, lands once.
func TestReplayStagingTakesABatchOnce(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })
	stage, err := c.createReplayStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A second run's staging table is its own, so neither drops nor fills the other's.
	other, err := c.createReplayStaging(ctx)
	if err != nil || other == stage {
		t.Fatalf("two runs share %q: %v", stage, err)
	}
	ts := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	batch := []trackPoint{{mmsi: 257000001, ts: ts, lat6: 1, lon6: 1, sog10: 1023, cog10: 3600, heading: 511, navStatus: 15,
		source: "kystverket", station: "kystverket", txAt: ts, txDisc: 1, recv: ts}}
	staging := &chConn{conn: c.conn, db: db, table: stage}
	for range 2 {
		if err := staging.insert(ctx, "token", batch); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := chColumn[uint64](ctx, c.conn, "SELECT count() FROM "+db+"."+stage); err != nil || n[0] != 1 {
		t.Errorf("a batch sent twice under one token: %v %v", n, err)
	}
}

// A day of AISHub snapshots, thousands of vessels to a record, queues more copies than the queue holds long
// before many records are read; replay writes them as it goes and drops none.
func TestReplayKeepsUpWithSnapshots(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	const vessels, snapshots = 2000, 160 // 320,000 copies, past the queue's 300,000
	var b strings.Builder
	for i := range snapshots {
		recv := day.Add(10*time.Hour + time.Duration(i)*time.Minute)
		fmt.Fprintf(&b, "%s\taishub\t[{\"ERROR\":false,\"FORMAT\":\"AIS\",\"RECORDS\":%d},[", recv.Format(time.RFC3339Nano), vessels)
		for v := range vessels {
			if v > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"MMSI":%d,"TIME":"%d","LONGITUDE":%d,"LATITUDE":%d,"COG":3600,"SOG":0,"HEADING":511,"NAVSTAT":15}`,
				257100000+v, recv.Add(-30*time.Second).Unix(), 6420000+v*100, 35940000+i*10)
		}
		b.WriteString("]]\n")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "aishub-terms", "aishub", day.Format("2006/01/02")+"/10.gz")
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Write([]byte(b.String()))
	gz.Close()
	f.Close()
	if err := c.replayDay(ctx, dir, day, corroborationWindow+vesselTTL, false, true); err != nil {
		t.Fatalf("replaying snapshots: %v", err)
	}
	if n, err := chColumn[uint64](ctx, c.conn, "SELECT count() FROM "+db+".receptions"); err != nil || n[0] < vessels*snapshots*9/10 {
		t.Errorf("copies written: %v %v", n, err)
	}
}

// A database still holding first-layout rows, whose arrival is in recv_ts, is not replayed: the day's window reads
// recv_delay and would pick the wrong copies.
func TestReplayWaitsForTheConversion(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })
	if err := c.exec(ctx, "ALTER TABLE {db}.receptions ADD COLUMN tx UInt64 DEFAULT 0"); err != nil {
		t.Fatal(err)
	}
	if err := c.replayDay(ctx, t.TempDir(), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Hour, true, false); err == nil ||
		!strings.Contains(err.Error(), "first layout") {
		t.Errorf("replayed over first-layout rows: %v", err)
	}
}

// A day's raw hours download several at a time, never more than fetchWorkers at once.
func TestReplayFetchesInParallel(t *testing.T) {
	var inFlight, most atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(20 * time.Millisecond) // long enough that the downloads overlap
		fmt.Fprint(w, "x")
	}))
	defer srv.Close()
	s3 := &s3Client{endpoint: srv.URL, region: "auto", bucket: "bucket", accessKey: "k", secretKey: "s"}
	var keys []s3Object
	for i := range 3 * fetchWorkers {
		keys = append(keys, s3Object{Key: fmt.Sprintf("CC0-1.0/station/s%d/2026/09/02/00.gz", i), Size: 1})
	}
	if err := fetchRawDay(s3, keys, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Hour, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if m := most.Load(); m < 2 || m > fetchWorkers {
		t.Errorf("at most %d downloads ran at once, want between 2 and %d", m, fetchWorkers)
	}
}

// A replay judges plausibility from what live held before its lead-in, not from the lead-in's first report or an
// archive's rows. Here
// two vessels share an MMSI: live has held the one off Sicily since before the lead-in, so a report off Gibraltar
// during it is the implausible jump, and the next report off Sicily, on the replayed day, is plausible. A lead-in
// starting from nothing would take Gibraltar first and flag Sicily instead.
func TestReplaySeedsWhatLiveHeldBeforeTheLeadIn(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })

	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	warmup := corroborationWindow + vesselTTL
	const mmsi = 257000001
	// What live held: the vessel off Sicily, 5 minutes before the lead-in starts.
	before := day.Add(-warmup - 5*time.Minute)
	if err := c.conn.Exec(ctx, "INSERT INTO "+db+".positions_1m (mmsi, slot, cell, ts, lat6, lon6, sog10, cog10, heading, navstat, source)"+
		" VALUES (?, ?, 0, ?, ?, ?, 0, 3600, 511, 15, 'kystverket')", mmsi, before.Truncate(time.Minute), before,
		int32(36.74*600000), int32(14.21*600000)); err != nil {
		t.Fatal(err)
	}
	// An archive's later row off Gibraltar, which never passed through live's cache, so it seeds nothing.
	archived := before.Add(2 * time.Minute)
	if err := c.conn.Exec(ctx, "INSERT INTO "+db+".positions_1m (mmsi, slot, cell, ts, lat6, lon6, sog10, cog10, heading, navstat, source)"+
		" VALUES (?, ?, 0, ?, ?, ?, 0, 3600, 511, 15, 'marinecadastre')", mmsi, archived.Truncate(time.Minute), archived,
		int32(35.96*600000), int32(-5.80*600000)); err != nil {
		t.Fatal(err)
	}
	if err := c.conn.Exec(ctx, "INSERT INTO "+db+".history_loads (source, file, day, etag, complete, loaded) VALUES ('marinecadastre', 'f', ?, 'e', true, now64(3))", day); err != nil {
		t.Fatal(err)
	}
	enc := testPipeline(t)
	sentence := func(lat, lon float64) string {
		return enc.encoder.EncodeSentence(aisnmeaPacket('A', enc.codec.EncodePacket(posReport(mmsi, lat, lon))))[0]
	}
	dir := t.TempDir()
	lines := map[string][]string{}
	add := func(at time.Time, lat, lon float64) {
		hour := at.Format("2006/01/02/15")
		lines[hour] = append(lines[hour], at.Format(time.RFC3339Nano)+"\tkystverket\t"+sentence(lat, lon))
	}
	// Both keep reporting through the lead-in and into the day: off Gibraltar every 10 minutes, first at its start,
	// and off Sicily every 20.
	for at := -85 * time.Minute; at <= 5*time.Minute; at += 10 * time.Minute {
		add(day.Add(at), 35.96+float64(at/time.Minute)/1e5, -5.80)
	}
	for at := -80 * time.Minute; at <= 10*time.Minute; at += 20 * time.Minute {
		add(day.Add(at), 36.74+float64(at/time.Minute)/1e5, 14.21)
	}
	for hour, ls := range lines {
		slices.Sort(ls) // a raw hour is in receive order, and each line starts with its receive time
		path := filepath.Join(dir, "NLOD-2.0", "kystverket", hour+".gz")
		os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		gz.Write([]byte(strings.Join(ls, "\n") + "\n"))
		gz.Close()
		f.Close()
	}
	if err := c.replayDay(ctx, dir, day, warmup, false, true); err != nil {
		t.Fatal(err)
	}
	judged := func(lon float64) []bool {
		t.Helper()
		flags, err := chColumn[bool](ctx, c.conn, "SELECT implausible FROM "+db+".receptions WHERE mmsi = ? AND ts >= ? AND lon6 = ?", mmsi, day, int32(lon*600000))
		if err != nil {
			t.Fatal(err)
		}
		return flags
	}
	if sicily, gibraltar := judged(14.21), judged(-5.80); len(sicily) != 1 || sicily[0] || len(gibraltar) != 1 || !gibraltar[0] {
		t.Errorf("as live judged them, off Sicily is plausible and off Gibraltar is not: implausible %v and %v", sicily, gibraltar)
	}
}

// A replay staged before a migration adds a column to receptions still copies its day in: by name, the new column
// takes its default, where by position the insert would fail with the stored day already deleted.
func TestReplayCopiesStagingByName(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	c, err := openClickHouse(ctx, strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Exec(context.Background(), "DROP DATABASE "+db); c.conn.Close() })
	stage, err := c.createReplayStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	pt := trackPoint{mmsi: 257000001, ts: ts, lat6: 1, lon6: 1, sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket", station: "kystverket", txAt: ts, recv: ts}
	if err := (&chConn{conn: c.conn, db: db, table: stage}).insert(ctx, "staged", []trackPoint{pt}); err != nil {
		t.Fatal(err)
	}
	if err := c.conn.Exec(ctx, "ALTER TABLE "+db+".receptions ADD COLUMN added_meanwhile Bool DEFAULT false"); err != nil {
		t.Fatal(err)
	}
	if err := c.copyStaged(ctx, stage, "mmsi = ?", uint32(257000001)); err != nil {
		t.Fatalf("a column added meanwhile failed the copy: %v", err)
	}
	var n uint64
	if err := c.conn.QueryRow(ctx, "SELECT count() FROM "+db+".receptions").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows copied, %v", n, err)
	}
}
