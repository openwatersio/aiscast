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
	newest := page.Vessels[2].LastMod
	if index.Pages[0].LastMod != newest {
		t.Errorf("lastmod = %s, want the newest vessel's, %s", index.Pages[0].LastMod, newest)
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

// A vessel's lastmod moves when what its page says changes, not with each report.
func TestVesselPageAt(t *testing.T) {
	p := storePipeline(t)
	const mmsi = 257000001
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	hear := func(after time.Duration, pkt ais.Packet) int64 {
		t.Helper()
		at := start.Add(after)
		p.ingestPacket("kystverket", "kystverket", at, at, pkt)
		mustFlush(t, p)
		return unixMs(at)
	}
	pageAt := func() int64 {
		t.Helper()
		var n int64
		if err := p.store.db.QueryRow("SELECT page_at FROM vessels WHERE mmsi = ?", mmsi).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	static := shipStatic(mmsi, "FIRST").(ais.ShipStaticData)

	first := hear(0, posReport(mmsi, 60.95, 5.1))
	if got := pageAt(); got != first {
		t.Fatalf("new vessel's page_at = %d, want its first report's %d", got, first)
	}
	named := hear(time.Minute, static)
	if got := pageAt(); got != named {
		t.Fatalf("after its name: %d, want %d", got, named)
	}
	hear(2*time.Minute, posReport(mmsi, 60.96, 5.1))
	hear(3*time.Minute, static)
	if got := pageAt(); got != named {
		t.Errorf("after a move within its cell and the same static report: %d, want %d still", got, named)
	}
	static.Destination = "SEBMA"
	bound := hear(4*time.Minute, static)
	if got := pageAt(); got != bound {
		t.Errorf("after a new destination: %d, want %d", got, bound)
	}
	// Across 61°N at about six knots.
	moved := hear(30*time.Minute, posReport(mmsi, 61.01, 5.1))
	if got := pageAt(); got != moved {
		t.Errorf("after a move into another cell: %d, want %d", got, moved)
	}
}

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
	if _, err := st.db.Exec("UPDATE vessels SET name = 'NEW', seen = 9000 WHERE mmsi = 257000001"); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow("SELECT page_at FROM vessels WHERE mmsi = 257000001").Scan(&pageAt); err != nil || pageAt != 9000 {
		t.Errorf("page_at after a rename = %d (%v), want 9000", pageAt, err)
	}
}
