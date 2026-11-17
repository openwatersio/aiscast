package main

// IndexNow tells Bing and the other participating engines about vessel pages as they enter the sitemap,
// rather than leaving them to the engines' next read of a sitemap of hundreds of thousands of URLs. One
// submission to api.indexnow.org is shared with every participating engine
// (https://www.indexnow.org/documentation).
//
// A vessel's page is news when it first qualifies for the sitemap, or when a rename moves it to a new
// address. Its position is not: that changes every few seconds, and the sitemap's lastmod covers the rest.
// The indexnow table remembers the slug each vessel was submitted under, so a round sends only vessels
// never submitted or whose page has moved, within a daily budget: moved pages first, then the rest newest
// to the record first. A moved page waits a week from its last submission, so an MMSI two ships share,
// flipping between their names, cannot crowd out the rest. The history import backdates first_seen to a
// vessel's first archive report, so a vessel new to the live feeds but in an archive waits its turn by that
// date. The vessels already listed when this first ran go out at the same pace.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	indexNowEndpoint = "https://api.indexnow.org/indexnow"
	// indexNowKey is public by design: an engine verifies a submission by fetching the key file, which the
	// web client serves from client/public/ais/vessels/. A key file in a directory admits URLs under it
	// only, so this one admits vessel pages and nothing else on openwaters.io.
	indexNowKey         = "1f83ecc30f1fef381b5d2d8d8cbfb597"
	indexNowHost        = "openwaters.io"
	indexNowKeyLocation = "https://" + indexNowHost + "/ais/vessels/" + indexNowKey + ".txt"
	// indexNowBatch is the protocol's limit of URLs per request.
	indexNowBatch = 10000
	// indexNowDaily is the default budget of URLs a day. slackwater.xyz met a 429 at about 9,300 a day
	// from the same endpoint.
	indexNowDaily = 5000
	// indexNowPause is how long a 429 stops submission: the limit it reports is a daily one.
	indexNowPause = 24 * time.Hour
	// indexNowResend is how long a submitted vessel waits before a rename sends its page again.
	indexNowResend = 7 * 24 * time.Hour
	// indexNowPausedKey holds, in the meta table, the unix second a 429's pause ends, so a restart keeps it.
	indexNowPausedKey = "indexnow_paused_until"
)

var indexNowClient = &http.Client{Timeout: time.Minute}

// indexNowKeyURL is where a round checks the key file before it submits: the keyLocation the engines are
// told, a variable so tests can serve their own.
var indexNowKeyURL = indexNowKeyLocation

// indexNowStats is read by /metrics.
type indexNowStats struct {
	enabled             atomic.Bool
	submitted, failures atomic.Int64
	lastSuccess         atomic.Int64 // unix seconds
}

type indexNowVessel struct {
	mmsi uint32
	name string
}

// ---- the store ----

// indexNowDueSQL is the vessels the sitemap lists whose page has not been submitted at its address: never
// submitted, or moved by a rename at least indexNowResend after the last submission. Moved pages come
// first, then newest to the record. It shares sitemapWhere, so a vessel is submitted exactly when it is
// listed, and compares slugs, so a name that changes only in what the address drops is not a move.
const indexNowDueSQL = `SELECT mmsi, name FROM vessels` + sitemapWhere + `
	AND NOT EXISTS (SELECT 1 FROM indexnow i WHERE i.mmsi = vessels.mmsi AND (i.slug = vessel_slug(vessels.name) OR i.sent_at >= ?))
	ORDER BY EXISTS (SELECT 1 FROM indexnow i WHERE i.mmsi = vessels.mmsi) DESC, first_seen DESC, mmsi LIMIT ?`

func (s *store) indexNowDue(ctx context.Context, cutoff, resend time.Time, limit int) ([]indexNowVessel, error) {
	rows, err := s.db.QueryContext(ctx, indexNowDueSQL, unixMs(cutoff), unixMs(resend), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []indexNowVessel
	for rows.Next() {
		var v indexNowVessel
		if err := rows.Scan(&v.mmsi, &v.name); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// indexNowSentSince is the URLs submitted since t, which the daily budget is spent against. It is summed
// from a log of rounds rather than from the vessels, which keep only their latest submission, and is read
// from the record rather than memory, so neither a resubmission nor a restart refills the budget.
func (s *store) indexNowSentSince(t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT coalesce(sum(urls), 0) FROM indexnow_rounds WHERE sent_at >= ?`, unixMs(t)).Scan(&n)
	return n, err
}

// logIndexNowRound records that a round sent urls URLs, and drops rounds too old to count against any budget.
func (s *store) logIndexNowRound(urls int, at time.Time) error {
	if _, err := s.db.Exec(`INSERT INTO indexnow_rounds (sent_at, urls) VALUES (?, ?)`, unixMs(at), urls); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM indexnow_rounds WHERE sent_at < ?`, unixMs(at.Add(-2*indexNowPause)))
	return err
}

// setIndexNowSent records the vessels as submitted at their current addresses.
func (s *store) setIndexNowSent(vs []indexNowVessel, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO indexnow (mmsi, slug, sent_at) VALUES (?, ?, ?)
		ON CONFLICT (mmsi) DO UPDATE SET slug = excluded.slug, sent_at = excluded.sent_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range vs {
		if _, err := stmt.Exec(v.mmsi, vesselSlug(v.name), unixMs(at)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// indexNowPausedUntil is when a 429's pause ends, or the zero time.
func (s *store) indexNowPausedUntil() time.Time {
	v, err := s.meta(indexNowPausedKey)
	if n, perr := strconv.ParseInt(v, 10, 64); err == nil && perr == nil {
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// ---- submission ----

// indexNowError is a refused submission, with the status the endpoint answered.
type indexNowError struct {
	status int
	detail string
}

func (e *indexNowError) Error() string { return fmt.Sprintf("%d: %s", e.status, e.detail) }

// checkIndexNowKey fetches the key file as an engine would. The endpoint answers 202 before any engine has
// fetched it and drops the URLs later if the file is missing, and the round records them as sent on that
// 202, so a key file that is not being served must stop the round before it submits.
func checkIndexNowKey(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := indexNowClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	if res.StatusCode != http.StatusOK || string(bytes.TrimSpace(body)) != indexNowKey {
		return fmt.Errorf("key file %s answers %s, not the key", url, res.Status)
	}
	return nil
}

// postIndexNow sends one batch of URLs. 200 and 202 are success; 202 means the engine has yet to fetch
// the key file, and it keeps the URLs while it does.
func postIndexNow(ctx context.Context, endpoint string, urls []string) error {
	body, err := json.Marshal(map[string]any{
		"host":        indexNowHost,
		"key":         indexNowKey,
		"keyLocation": indexNowKeyLocation,
		"urlList":     urls,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := indexNowClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusAccepted {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return &indexNowError{status: res.StatusCode, detail: string(bytes.TrimSpace(detail))}
}

// runIndexNow submits a round every hour, each at most a twenty-fourth of the daily budget, so the budget
// is spread over the day and a vessel that qualifies now is not held for tomorrow's.
func (p *Pipeline) runIndexNow(endpoint string, daily int) {
	if daily <= 0 {
		log.Printf("indexnow: INDEXNOW_DAILY=%d leaves no budget; not submitting", daily)
		return
	}
	time.Sleep(9 * time.Minute) // after the boot flush, so the round reads the record the feeds have refilled
	for {
		p.submitIndexNow(time.Now(), endpoint, daily)
		time.Sleep(time.Hour)
	}
}

// submitIndexNow sends one round and returns how many URLs it submitted. A refused submission records
// nothing, so its vessels are sent in a later round; a 429 pauses submission for indexNowPause.
func (p *Pipeline) submitIndexNow(now time.Time, endpoint string, daily int) int {
	if now.Before(p.store.indexNowPausedUntil()) {
		return 0
	}
	sent, err := p.store.indexNowSentSince(now.Add(-24 * time.Hour))
	if err != nil {
		log.Printf("indexnow: %v", err)
		return 0
	}
	room := min(daily-sent, (daily+23)/24, indexNowBatch)
	if room <= 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	due, err := p.store.indexNowDue(ctx, now.Add(-sitemapAge), now.Add(-indexNowResend), room)
	if err != nil {
		log.Printf("indexnow: %v", err)
		return 0
	}
	if len(due) == 0 {
		return 0
	}
	urls := make([]string, len(due))
	for i, v := range due {
		urls[i] = "https://" + indexNowHost + vesselPagePath(v.mmsi, v.name)
	}
	if err := checkIndexNowKey(ctx, indexNowKeyURL); err != nil {
		p.indexNow.failures.Add(1)
		log.Printf("indexnow: not submitting: %v", err)
		return 0
	}
	if err := postIndexNow(ctx, endpoint, urls); err != nil {
		p.indexNow.failures.Add(1)
		var refused *indexNowError
		if errors.As(err, &refused) && refused.status == http.StatusTooManyRequests {
			if err := p.store.setMeta(indexNowPausedKey, strconv.FormatInt(now.Add(indexNowPause).Unix(), 10)); err != nil {
				log.Printf("indexnow: %v", err)
			}
			log.Printf("indexnow: %d URLs: %v; pausing until %s", len(urls), err, now.Add(indexNowPause).UTC().Format(time.RFC3339))
		} else {
			log.Printf("indexnow: %d URLs: %v", len(urls), err)
		}
		return 0
	}
	// The round is logged before the vessels, so the budget counts it even if recording them fails and
	// the next round sends them again.
	if err := p.store.logIndexNowRound(len(urls), now); err != nil {
		log.Printf("indexnow: %v", err)
	}
	if err := p.store.setIndexNowSent(due, now); err != nil {
		log.Printf("indexnow: submitted %d URLs but could not record them: %v", len(urls), err)
	}
	p.indexNow.submitted.Add(int64(len(urls)))
	p.indexNow.lastSuccess.Store(now.Unix())
	log.Printf("indexnow: submitted %d vessel pages", len(urls))
	return len(urls)
}
