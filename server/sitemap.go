package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A sitemap lists at most 50,000 URLs, so the web client's vessel sitemap is one file per page.
const sitemapPageSize = 50000

// sitemapAge is how recently a vessel's position was heard for it to be listed. A page about a vessel's
// live position that the network last placed months ago is a stale page, and it comes back into the list
// when the vessel does.
const sitemapAge = 30 * 24 * time.Hour

// sitemapWhere is the vessels worth a search engine's visit: ships, named, with a position heard within
// sitemapAge. pos_at rather than seen, which a static report alone moves. A nameless vessel's page is a
// bare MMSI, which the web client marks noindex.
const sitemapWhere = " WHERE kind = 'vessel' AND name != '' AND has_pos = 1 AND pos_at >= ?"

type sitemapPage struct {
	Vessels int    `json:"vessels"`
	LastMod string `json:"lastmod"`
}

type sitemapVessel struct {
	MMSI uint32 `json:"mmsi"`
	Name string `json:"name"`
	Seen string `json:"seen"`
}

// serveVesselSitemap: GET /v1/vessels/sitemap → the pages of vessels a sitemap lists, each with its count and
// the newest seen in it; with page=n (from 1), that page's vessels in MMSI order. The web client builds
// openwaters.io/ais/sitemap.xml from these.
func (p *Pipeline) serveVesselSitemap(w http.ResponseWriter, r *http.Request) {
	if _, err := p.requestClaims(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if p.store == nil {
		http.Error(w, "vessel record unavailable", http.StatusServiceUnavailable)
		return
	}
	page := 0
	if q := r.URL.Query().Get("page"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 1000 {
			http.Error(w, "page=<n>, counting from 1", http.StatusBadRequest)
			return
		}
		page = n
	}
	body, found, err := p.store.sitemapAnswer(page, time.Now())
	if err != nil {
		log.Printf("store: %v", err)
		http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !found {
		w.WriteHeader(http.StatusNotFound)
	}
	w.Write(body)
}

// sitemapMemoFor is how long an answer is kept. Each is a read of the whole record, and the web client
// keeps what it builds for an hour anyway.
const sitemapMemoFor = 10 * time.Minute

// sitemapMemo keeps each answer, page 0 being the list of pages. Its lock lets one answer be built at a
// time, so a burst of requests costs the record one read rather than holding its connections.
type sitemapMemo struct {
	mu      sync.Mutex
	answers map[int]sitemapMemoAnswer
}

type sitemapMemoAnswer struct {
	at    time.Time
	body  []byte
	found bool
}

// sitemapAnswer is the JSON for page (0 for the list of pages), and whether there is such a page.
func (s *store) sitemapAnswer(page int, now time.Time) (body []byte, found bool, err error) {
	m := &s.sitemap
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.answers[page]; ok && now.Sub(a.at) < sitemapMemoFor {
		return a.body, a.found, nil
	}
	cutoff := unixMs(now.Add(-sitemapAge))
	var out any
	found = true
	if page == 0 {
		pages, err := s.sitemapPages(cutoff, sitemapPageSize)
		if err != nil {
			return nil, false, err
		}
		out = map[string]any{"page_size": sitemapPageSize, "max_age_s": int(sitemapAge.Seconds()), "pages": pages}
	} else {
		vessels, err := s.sitemapVessels(cutoff, sitemapPageSize, page)
		if err != nil {
			return nil, false, err
		}
		if found = len(vessels) > 0; found {
			out = map[string]any{"vessels": vessels}
		} else {
			out = map[string]string{"error": "no such page"}
		}
	}
	body, err = json.Marshal(out)
	if err != nil {
		return nil, false, err
	}
	body = append(body, '\n')
	if m.answers == nil {
		m.answers = map[int]sitemapMemoAnswer{}
	}
	// Answers older than the memo are dropped, so a scan of page numbers past the end leaves nothing behind.
	for k, a := range m.answers {
		if now.Sub(a.at) >= sitemapMemoFor {
			delete(m.answers, k)
		}
	}
	m.answers[page] = sitemapMemoAnswer{at: now, body: body, found: found}
	return body, found, nil
}

// sitemapPages numbers the listed vessels in MMSI order and counts each page of them, as sitemapVessels
// pages them.
func (s *store) sitemapPages(cutoff int64, size int) ([]sitemapPage, error) {
	rows, err := s.db.Query(`SELECT count(*), max(seen) FROM (
		SELECT seen, (row_number() OVER (ORDER BY mmsi) - 1) / ? AS page FROM vessels`+sitemapWhere+`
	) GROUP BY page ORDER BY page`, size, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := []sitemapPage{}
	for rows.Next() {
		var n int
		var seen int64
		if err := rows.Scan(&n, &seen); err != nil {
			return nil, err
		}
		pages = append(pages, sitemapPage{Vessels: n, LastMod: fromMs(seen).Format(time.RFC3339)})
	}
	return pages, rows.Err()
}

// sitemapVessels is page n of the listed vessels, from 1.
func (s *store) sitemapVessels(cutoff int64, size, n int) ([]sitemapVessel, error) {
	rows, err := s.db.Query("SELECT mmsi, name, seen FROM vessels"+sitemapWhere+" ORDER BY mmsi LIMIT ? OFFSET ?",
		cutoff, size, (n-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sitemapVessel
	for rows.Next() {
		var v sitemapVessel
		var seen int64
		if err := rows.Scan(&v.MMSI, &v.Name, &seen); err != nil {
			return nil, err
		}
		v.Seen = fromMs(seen).Format(time.RFC3339)
		out = append(out, v)
	}
	return out, rows.Err()
}
