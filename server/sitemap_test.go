package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestVesselSitemap(t *testing.T) {
	p := storePipeline(t)
	heardAgo(p, 257000003, "THIRD", 60, 5, time.Minute)
	heardAgo(p, 257000001, "FIRST", 60, 5, time.Hour)
	heardAgo(p, 257000002, "SECOND", 60, 5, 2*time.Hour)
	heardAgo(p, 257000004, "LONG GONE", 60, 5, time.Minute)
	heardAgo(p, 257000006, "BUOY", 60, 5, time.Minute)
	heardAgo(p, 257000007, "NOWHERE", 60, 5, time.Minute)
	// Not a ship's MMSI: a transmitter left at its default, and a search-and-rescue aircraft's range.
	heardAgo(p, 1, "DATAHUB", 60, 5, time.Minute)
	heardAgo(p, 111257001, "RESCUE", 60, 5, time.Minute)
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
	newest := page.Vessels[2].Seen
	if index.Pages[0].LastMod != newest {
		t.Errorf("lastmod = %s, want the newest seen, %s", index.Pages[0].LastMod, newest)
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
	if len(last) != 1 || last[0].Name != "E" || pages[2].LastMod != last[0].Seen {
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
