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
	if err := c.replayDay(ctx, dir, day, warmup, false, true); err != nil {
		t.Fatal(err)
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
	if len(previous) != 1 || previous[0].lat6 != int32(57.0*600000) || len(archived) != 1 || archived[0].lat6 != int32(56.0*600000) {
		t.Errorf("the day before and the archive's copy are untouched: %+v %+v", previous, archived)
	}
	lats, err := chColumn[int32](ctx, c.conn, "SELECT lat6 FROM "+db+".positions_1m FINAL WHERE slot >= ? AND slot < ? AND source = 'kystverket' ORDER BY slot", day, day.AddDate(0, 0, 1))
	if err != nil || len(lats) != 6 || lats[0] != int32(59.90*600000) {
		t.Errorf("positions_1m holds the replayed track: %v %v", lats, err)
	}
	complete, err := chColumn[bool](ctx, c.conn, "SELECT complete FROM "+db+".history_loads FINAL WHERE file = 'f'")
	if err != nil || len(complete) != 1 || complete[0] {
		t.Errorf("the archive's day is marked to load again: %v %v", complete, err)
	}
	if staging, _ := chColumn[string](ctx, c.conn, "SELECT name FROM system.tables WHERE database = ? AND name = ?", db, replayStaging); len(staging) != 0 {
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
	listed, err := s3.list("")
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
