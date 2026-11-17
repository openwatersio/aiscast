package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The client's slug and this one answer the same for every name in the shared fixture, so a URL the
// server submits is the page's canonical address rather than one that redirects.
func TestVesselSlugMatchesClient(t *testing.T) {
	raw, err := os.ReadFile("testdata/vessel-slugs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Slug string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("%d cases; the fixture is missing its names", len(cases))
	}
	for _, c := range cases {
		if got := vesselSlug(c.Name); got != c.Slug {
			t.Errorf("vesselSlug(%q) = %q, the client says %q", c.Name, got, c.Slug)
		}
	}
	if got := vesselPagePath(257000001, "Ærø Færgen"); got != "/ais/vessels/257000001-aero-faergen" {
		t.Errorf("path = %s", got)
	}
	if got := vesselPagePath(257000001, "***"); got != "/ais/vessels/257000001" {
		t.Errorf("path without a slug = %s", got)
	}
}

// Engines verify the key by fetching it from the site, so the web client must serve the same key.
func TestIndexNowKeyFile(t *testing.T) {
	raw, err := os.ReadFile("../client/public/ais/vessels/" + indexNowKey + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != indexNowKey {
		t.Errorf("key file holds %q", raw)
	}
}

type indexNowRequest struct {
	Host, Key, KeyLocation string
	URLList                []string
}

// fakeIndexNow answers each submission with status and keeps what it was sent. It also serves the key file
// the round checks first, at the URL it points indexNowKeyURL to, while serveKey is true.
func fakeIndexNow(t *testing.T, status *int) (string, func() []indexNowRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []indexNowRequest
	serveKey.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key.txt" {
			if !serveKey.Load() {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(indexNowKey + "\n"))
			return
		}
		var req indexNowRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
		w.WriteHeader(*status)
	}))
	t.Cleanup(srv.Close)
	keyURL := indexNowKeyURL
	indexNowKeyURL = srv.URL + "/key.txt"
	t.Cleanup(func() { indexNowKeyURL = keyURL })
	return srv.URL, func() []indexNowRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]indexNowRequest(nil), got...)
	}
}

var serveKey atomic.Bool

// The endpoint answers 202 before an engine has fetched the key, and the round records what it sent on
// that answer, so a key file that is not served stops the round before anything is submitted.
func TestIndexNowChecksKeyFirst(t *testing.T) {
	p := storePipeline(t)
	status := http.StatusAccepted
	url, requests := fakeIndexNow(t, &status)
	heardAgo(p, 257000001, "FIRST", 60, 5, time.Hour)
	now := time.Now()
	serveKey.Store(false)
	if n := p.submitIndexNow(now, url, 48); n != 0 || len(requests()) != 0 {
		t.Errorf("without the key file: submitted %d in %d requests", n, len(requests()))
	}
	if p.indexNow.failures.Load() != 1 {
		t.Errorf("failures %d, want 1", p.indexNow.failures.Load())
	}
	serveKey.Store(true)
	if n := p.submitIndexNow(now.Add(time.Hour), url, 48); n != 1 {
		t.Errorf("with the key file: submitted %d, want 1", n)
	}
}

func TestIndexNowRounds(t *testing.T) {
	p := storePipeline(t)
	status := http.StatusAccepted
	url, requests := fakeIndexNow(t, &status)
	heardAgo(p, 257000001, "OLDEST", 60, 5, 3*time.Hour)
	heardAgo(p, 257000002, "MIDDLE", 60, 5, 2*time.Hour)
	heardAgo(p, 257000003, "NEWEST", 60, 5, time.Hour)
	// Not listed in the sitemap, so not submitted: an aid to navigation's MMSI.
	heardAgo(p, 994257001, "NET 1", 60, 5, time.Minute)
	now := time.Now()

	// A daily budget of 48 is two URLs a round: the two newest to the record go first.
	if n := p.submitIndexNow(now, url, 48); n != 2 {
		t.Fatalf("submitted %d, want 2", n)
	}
	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Host != "openwaters.io" || r.Key != indexNowKey || r.KeyLocation != "https://openwaters.io/ais/vessels/"+indexNowKey+".txt" {
		t.Errorf("request = %+v", r)
	}
	want := []string{"https://openwaters.io/ais/vessels/257000003-newest", "https://openwaters.io/ais/vessels/257000002-middle"}
	if strings.Join(r.URLList, " ") != strings.Join(want, " ") {
		t.Errorf("urls = %v, want %v", r.URLList, want)
	}

	// The next round sends what is left, and a round with nothing left sends nothing.
	if n := p.submitIndexNow(now.Add(time.Hour), url, 48); n != 1 || requests()[1].URLList[0] != "https://openwaters.io/ais/vessels/257000001-oldest" {
		t.Errorf("second round: %d, %v", n, requests()[1:])
	}
	if n := p.submitIndexNow(now.Add(2*time.Hour), url, 48); n != 0 || len(requests()) != 2 {
		t.Errorf("third round submitted %d in %d requests, want nothing", n, len(requests()))
	}

	if got := p.indexNow.submitted.Load(); got != 3 {
		t.Errorf("submitted metric %d, want 3", got)
	}
}

// The budget is a rolling day summed from the rounds, so a restart does not refill it.
func TestIndexNowDailyBudget(t *testing.T) {
	p := storePipeline(t)
	status := http.StatusOK
	url, requests := fakeIndexNow(t, &status)
	for i := range uint32(3) {
		heardAgo(p, 257000001+i, "VESSEL", 60, 5, time.Duration(3-i)*time.Hour)
	}
	now := time.Now()
	// A budget of 2 a day sends one URL a round until the day's two are spent.
	for i, want := range []int{1, 1, 0} {
		if n := p.submitIndexNow(now.Add(time.Duration(i)*time.Hour), url, 2); n != want {
			t.Errorf("round %d submitted %d, want %d", i, n, want)
		}
	}
	if n := p.submitIndexNow(now.Add(25*time.Hour), url, 2); n != 1 {
		t.Errorf("round a day later submitted %d", n)
	}
	if len(requests()) != 3 {
		t.Errorf("%d requests, want 3", len(requests()))
	}
}

// A rename that moves a page goes ahead of vessels never submitted, once a week has passed since the
// vessel was last sent. A name change the address does not see, such as a feed's padding, is never one.
func TestIndexNowRenames(t *testing.T) {
	p := storePipeline(t)
	status := http.StatusOK
	url, requests := fakeIndexNow(t, &status)
	heardAgo(p, 257000001, "OLD", 60, 5, 3*time.Hour)
	heardAgo(p, 257000002, "SAME PAGE", 60, 5, 3*time.Hour)
	now := time.Now()
	if n := p.submitIndexNow(now, url, 48); n != 2 {
		t.Fatalf("first round: %d", n)
	}
	// A backlog newer to the record than either vessel.
	for i := range uint32(3) {
		heardAgo(p, 257000010+i, "BACKLOG", 60, 5, time.Hour)
	}
	heardAgo(p, 257000001, "OLD RENAMED", 60, 5, time.Minute)
	heardAgo(p, 257000002, "SAME-PAGE          ", 60, 5, time.Minute)

	// Within the week, the rename waits and the backlog goes out; the unmoved page never comes back.
	if n := p.submitIndexNow(now.Add(time.Hour), url, 48); n != 2 {
		t.Fatalf("round within the week: %d", n)
	}
	if got := strings.Join(requests()[1].URLList, " "); strings.Contains(got, "old-renamed") || strings.Contains(got, "same-page") {
		t.Errorf("round within the week sent %s", got)
	}
	if sent, _ := p.store.indexNowSentSince(now.Add(-time.Minute)); sent != 4 {
		t.Errorf("budget spent %d, want the 4 URLs submitted", sent)
	}
	// After the week the moved page leads what is left of the backlog, and is sent once.
	if n := p.submitIndexNow(now.Add(8*24*time.Hour), url, 48); n != 2 {
		t.Fatalf("round after the week: %d", n)
	}
	got := requests()[2].URLList
	if got[0] != "https://openwaters.io/ais/vessels/257000001-old-renamed" || strings.Contains(strings.Join(got, " "), "same-page") {
		t.Errorf("round after the week = %v, want the moved page first and the unmoved one left out", got)
	}
}

// A refused submission records nothing, so the same vessels lead the next round. A 429 also pauses
// submission for a day, since the limit it reports is a daily one, and a restart keeps the pause.
func TestIndexNowFailureRetries(t *testing.T) {
	p := storePipeline(t)
	status := http.StatusInternalServerError
	url, requests := fakeIndexNow(t, &status)
	heardAgo(p, 257000001, "FIRST", 60, 5, time.Hour)
	now := time.Now()
	if n := p.submitIndexNow(now, url, 48); n != 0 {
		t.Errorf("a 500 counted %d as submitted", n)
	}
	status = http.StatusTooManyRequests
	if n := p.submitIndexNow(now.Add(time.Hour), url, 48); n != 0 {
		t.Errorf("a 429 counted %d as submitted", n)
	}
	if p.indexNow.failures.Load() != 2 {
		t.Errorf("failures %d, want 2", p.indexNow.failures.Load())
	}
	if until := p.store.indexNowPausedUntil(); !until.After(now.Add(24 * time.Hour)) {
		t.Errorf("pause recorded until %v, want a day after the 429", until)
	}
	status = http.StatusOK
	if n := p.submitIndexNow(now.Add(2*time.Hour), url, 48); n != 0 || len(requests()) != 2 {
		t.Errorf("a round inside the pause sent %d in %d requests", n, len(requests()))
	}
	if n := p.submitIndexNow(now.Add(26*time.Hour), url, 48); n != 1 {
		t.Errorf("retry after the pause submitted %d, want 1", n)
	}
	if reqs := requests(); len(reqs) != 3 || reqs[2].URLList[0] != reqs[0].URLList[0] {
		t.Errorf("requests = %+v, want the same URL each time", reqs)
	}
}

// The submitted check runs once per listed vessel, so it must probe indexnow by its key: a scan there is
// quadratic in the sitemap.
func TestIndexNowDuePlanUsesKey(t *testing.T) {
	p := storePipeline(t)
	rows, err := p.store.db.Query("EXPLAIN QUERY PLAN "+indexNowDueSQL, 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, aux int
		var detail string
		if err := rows.Scan(&id, &parent, &aux, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
		if strings.HasPrefix(detail, "SCAN i") {
			t.Errorf("plan scans indexnow: %s", detail)
		}
	}
	t.Log(strings.Join(steps, "; "))
}
