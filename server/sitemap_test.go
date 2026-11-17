package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/BertoldVdb/go-ais"
)

func TestVesselSitemap(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000003, "THIRD", 60, 5, time.Minute)
	heardAgo(p, 257000001, "FIRST", 60, 5, time.Hour)
	heardAgo(p, 257000002, "SECOND", 60, 5, 2*time.Hour)
	heardAgo(p, 257000004, "LONG GONE", 60, 5, time.Minute)
	heardAgo(p, 257000006, "BUOY", 60, 5, time.Minute)
	heardAgo(p, 257000007, "NOWHERE", 60, 5, time.Minute)
	// Not a ship's MMSI: a transmitter left at its default, a search-and-rescue aircraft's range,
	// and a navigation aid's, which net buoys sending as vessels use.
	heardAgo(p, 1, "DATAHUB", 60, 5, time.Minute)
	heardAgo(p, 111257001, "RESCUE", 60, 5, time.Minute)
	heardAgo(p, 994257001, "NET 1", 60, 5, time.Minute)
	// A ship's range, but no country's MID: a test unit left at its default.
	heardAgo(p, 200000008, "TEST", 60, 5, time.Minute)
	// Nameless: heard only by position.
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(257000005, 60, 5))
	mustFlush(t, p)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := p.store.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Static reports since, but its position is older than the window.
	exec("UPDATE vessels SET pos_at = ? WHERE mmsi = 257000004", unixMs(time.Now().Add(-sitemapAge-time.Hour)))
	exec("UPDATE vessels SET kind = 'aton' WHERE mmsi = 257000006")
	exec("UPDATE vessels SET has_pos = 0 WHERE mmsi = 257000007")

	var index struct {
		PageSize int           `json:"page_size"`
		Pages    []sitemapPage `json:"pages"`
	}
	w := get(t, p, "/sitemap/vessels")
	if w.Code != 200 {
		t.Fatalf("index: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if index.PageSize != sitemapPageSize || len(index.Pages) != 1 || index.Pages[0].Vessels != 3 {
		t.Fatalf("index = %+v, want one page of the three named ships placed recently", index)
	}

	var page struct{ Vessels []sitemapVessel }
	w = get(t, p, "/sitemap/vessels?page=1")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("page 1: %d %s", w.Code, w.Body)
	}
	var got []string
	for _, v := range page.Vessels {
		got = append(got, v.Name)
	}
	if len(got) != 3 || got[0] != "FIRST" || got[1] != "SECOND" || got[2] != "THIRD" {
		t.Fatalf("page 1 = %v, want FIRST SECOND THIRD in MMSI order", got)
	}
	// The index's lastmod is its page's newest.
	newest := time.Now().Add(-time.Hour).Truncate(time.Second)
	exec("UPDATE vessels SET page_at = ? WHERE mmsi = 257000001", unixMs(newest.Add(-2*time.Hour)))
	exec("UPDATE vessels SET page_at = ? WHERE mmsi = 257000002", unixMs(newest))
	exec("UPDATE vessels SET page_at = ? WHERE mmsi = 257000003", unixMs(newest.Add(-time.Hour)))
	p.store.sitemap.answers = nil
	if err := json.Unmarshal(get(t, p, "/sitemap/vessels").Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if want := newest.UTC().Format(time.RFC3339); index.Pages[0].LastMod != want {
		t.Errorf("lastmod = %s, want the newest vessel's, %s", index.Pages[0].LastMod, want)
	}

	if w := get(t, p, "/sitemap/vessels?page=2"); w.Code != 404 {
		t.Errorf("page past the end: %d, want 404", w.Code)
	}
	if w := get(t, p, "/sitemap/vessels?page=0"); w.Code != 400 {
		t.Errorf("page 0: %d, want 400", w.Code)
	}
}

func TestVesselSitemapPaging(t *testing.T) {
	p := storePipeline(t)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		heardAgo(p, uint32(257000001+i), name, 60, 5, time.Duration(5-i)*time.Minute)
	}
	cutoff := unixMs(time.Now().Add(-sitemapAge))
	pages, err := p.store.sitemapPages(cutoff, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 || pages[0].Vessels != 2 || pages[1].Vessels != 2 || pages[2].Vessels != 1 {
		t.Fatalf("pages = %+v, want 2, 2, 1", pages)
	}
	last, err := p.store.sitemapVessels(cutoff, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 || last[0].Name != "E" || pages[2].LastMod != last[0].LastMod {
		t.Fatalf("page 3 = %+v, want E with the page's lastmod %s", last, pages[2].LastMod)
	}
}

func TestVesselSitemapMemo(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000001, "FIRST", 60, 5, time.Minute)
	now := time.Now()
	before, _, err := p.store.sitemapAnswer(0, now)
	if err != nil {
		t.Fatal(err)
	}
	heardAgo(p, 257000002, "SECOND", 60, 5, time.Minute)
	kept, _, _ := p.store.sitemapAnswer(0, now.Add(sitemapMemoFor-time.Second))
	if string(kept) != string(before) {
		t.Errorf("answer rebuilt inside the memo: %s, want %s", kept, before)
	}
	fresh, _, _ := p.store.sitemapAnswer(0, now.Add(sitemapMemoFor))
	if string(fresh) == string(before) {
		t.Errorf("answer kept past the memo: %s", fresh)
	}
}

// A vessel's lastmod moves when the facts its title and description are made of change, from any writer,
// and not with each report.
func TestVesselPageAt(t *testing.T) {
	p := storePipeline(t)
	const mmsi = 257000001
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	// write runs a write and returns the window of wall-clock milliseconds it ran in, after the last one's.
	write := func(f func()) writeWindow {
		t.Helper()
		time.Sleep(2 * time.Millisecond)
		w := writeWindow{from: time.Now().UnixMilli()}
		f()
		w.to = time.Now().UnixMilli()
		return w
	}
	hear := func(after time.Duration, pkt ais.Packet) writeWindow {
		t.Helper()
		return write(func() {
			at := start.Add(after)
			p.ingestPacket("kystverket", "kystverket", at, at, pkt)
			mustFlush(t, p)
		})
	}
	exec := func(q string) writeWindow {
		t.Helper()
		return write(func() {
			if _, err := p.store.db.Exec(q, mmsi); err != nil {
				t.Fatal(err)
			}
		})
	}
	pageAt := func() int64 {
		t.Helper()
		var n int64
		if err := p.store.db.QueryRow("SELECT page_at FROM vessels WHERE mmsi = ?", mmsi).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// A day passes, as far as the debounce can tell. page_at is not a column the triggers watch.
	dayLater := func() int64 {
		t.Helper()
		if _, err := p.store.db.Exec("UPDATE vessels SET page_at = page_at - 2 * 86400000 WHERE mmsi = ?", mmsi); err != nil {
			t.Fatal(err)
		}
		return pageAt()
	}
	static := shipStatic(mmsi, "FIRST").(ais.ShipStaticData)

	if w, got := hear(0, posReport(mmsi, 60.95, 5.1)), pageAt(); !w.has(got) {
		t.Fatalf("new vessel's page_at = %d, want its write's %v", got, w)
	}
	// Its first name brings it into the sitemap, a minute after the record took it.
	if w, got := hear(time.Minute, static), pageAt(); !w.has(got) {
		t.Fatalf("after its first name: %d, want %v", got, w)
	}
	old := dayLater()
	hear(2*time.Minute, posReport(mmsi, 60.96, 5.1))
	// Across 61°N at about six knots.
	hear(30*time.Minute, posReport(mmsi, 61.01, 5.1))
	hear(31*time.Minute, static)
	static.MaximumStaticDraught = 9.8
	static.Eta = ais.FieldETA{Month: 9, Day: 21, Hour: 6}
	hear(32*time.Minute, static)
	if got := pageAt(); got != old {
		t.Errorf("after moves, the same static report, and a new draught and ETA: %d, want %d still", got, old)
	}
	static.Destination = "SEBMA"
	if w, got := hear(33*time.Minute, static), pageAt(); !w.has(got) {
		t.Errorf("after a new destination: %d, want %v", got, w)
	}
	// Inside the day, another version of the static data does not move it again.
	bound := pageAt()
	static.Destination = "NOOSL"
	hear(34*time.Minute, static)
	if got := pageAt(); got != bound {
		t.Errorf("after a second destination inside the day: %d, want %d still", got, bound)
	}
	// Writers that leave seen alone: the import filling a blank, the kind migration.
	dayLater()
	if w, got := exec("UPDATE vessels SET callsign = 'LAJB8' WHERE mmsi = ?"), pageAt(); !w.has(got) {
		t.Errorf("after a call sign changed without a report: %d, want %v", got, w)
	}
	dayLater()
	if w, got := exec("UPDATE vessels SET kind = 'gear', flag = '' WHERE mmsi = ?"), pageAt(); !w.has(got) {
		t.Errorf("after a new kind: %d, want %v", got, w)
	}
}

// writeWindow is the wall-clock milliseconds a write ran in.
type writeWindow struct{ from, to int64 }

func (w writeWindow) has(ms int64) bool { return ms >= w.from && ms <= w.to }

// A record from before page_at gains it, set to each vessel's seen, and the triggers that keep it.
func TestOpenStoreAddsPageAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiscast.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO vessels (mmsi, name, seen, first_seen) VALUES (257000001, 'OLD', 5000, 1000)",
		"DROP TRIGGER vessels_page_insert",
		"DROP TRIGGER vessels_page_update",
		"ALTER TABLE vessels DROP COLUMN page_at",
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.close()
	if st, err = openStore(path); err != nil {
		t.Fatal(err)
	}
	defer st.close()
	var pageAt int64
	if err := st.db.QueryRow("SELECT page_at FROM vessels WHERE mmsi = 257000001").Scan(&pageAt); err != nil || pageAt != 5000 {
		t.Errorf("page_at = %d (%v), want seen, 5000", pageAt, err)
	}
	before := time.Now().UnixMilli()
	if _, err := st.db.Exec("UPDATE vessels SET name = 'NEW' WHERE mmsi = 257000001"); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow("SELECT page_at FROM vessels WHERE mmsi = 257000001").Scan(&pageAt); err != nil || pageAt < before {
		t.Errorf("page_at after a rename = %d (%v), want the time of the write, from %d", pageAt, err, before)
	}
}
