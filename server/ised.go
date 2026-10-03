package main

// Registered names and call signs of Canadian vessels from ISED's National Maritime Information
// Database, the registry of the MMSIs Canada assigns. ISED serves it publicly one vessel at a time and
// offers bulk only to the Canadian Coast Guard, so this source loads on demand, as the PSIX dimension
// backfill does: a background round asks about the CA-flag vessels the network has actually heard, a
// request a second, and stores the answer either way, so no vessel is asked about twice in a season.
// Serving reads only the table. Crown copyright; the per-vessel credit names the database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	isedEndpoint = "https://ised-isde.canada.ca/mmsi-ismm/api/ship"
	isedLicense  = "Crown copyright (Canada)"
	// isedRecheck is how long an answer stands, found or not: registrations change on the timescale of
	// boat sales, and a vessel with no record may get one.
	isedRecheck = 90 * 24 * time.Hour
)

var (
	// isedPause spaces the round's requests. The endpoint states no limit; a request a second keeps us
	// a polite share of a public search tool.
	isedPause = time.Second
	// isedFailuresInARow ends a round: the service is down, not one vessel's record.
	isedFailuresInARow = 3
)

var isedClient = &http.Client{Timeout: time.Minute}

// isedShip is one answer from the database, which can be that there is no record: a row with an empty
// name is a vessel ISED was asked about and had nothing for.
type isedShip struct {
	MMSI      uint32
	Name      string
	CallSign  string
	checkedAt int64 // unix ms of the last ask
}

// isedStats is read by /metrics.
type isedStats struct {
	checked, failures, ships atomic.Int64
}

// fetchISED asks the database about one MMSI. A vessel with no record answers an empty ship, which is
// still an answer worth keeping.
func fetchISED(ctx context.Context, endpoint string, mmsi uint32) (*isedShip, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?mmsi="+strconv.FormatUint(uint64(mmsi), 10), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", wikidataUserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := isedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%d: %s", mmsi, res.Status)
	}
	var answer struct {
		Records []struct {
			MMSI     string `json:"mmsi"` // served with stray spaces: " 316061185"
			Name     string `json:"vesselName"`
			CallSign string `json:"callSign"`
		} `json:"records"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%d: %w", mmsi, err)
	}
	out := &isedShip{MMSI: mmsi}
	for _, r := range answer.Records {
		if strings.TrimSpace(r.MMSI) == strconv.FormatUint(uint64(mmsi), 10) {
			out.Name, out.CallSign = strings.TrimSpace(r.Name), strings.TrimSpace(r.CallSign)
			break
		}
	}
	return out, nil
}

// ---- the store ----

// setISED stores one answer, found or empty.
func (s *store) setISED(v *isedShip, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ised (mmsi, name, callsign, checked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (mmsi) DO UPDATE SET name = excluded.name, callsign = excluded.callsign, checked_at = excluded.checked_at`,
		v.MMSI, v.Name, v.CallSign, at.UnixMilli())
	return err
}

// isedByMMSI is the stored answers with a record, among the given MMSIs.
func (s *store) isedByMMSI(mmsis []uint32) (map[uint32]*isedShip, error) {
	out := map[uint32]*isedShip{}
	if len(mmsis) == 0 {
		return out, nil
	}
	args := make([]any, len(mmsis))
	for i, m := range mmsis {
		args[i] = m
	}
	rows, err := s.db.Query(`SELECT mmsi, name, callsign, checked_at FROM ised WHERE name != '' AND mmsi IN (?`+
		strings.Repeat(",?", len(mmsis)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := &isedShip{}
		if err := rows.Scan(&v.MMSI, &v.Name, &v.CallSign, &v.checkedAt); err != nil {
			return nil, err
		}
		out[v.MMSI] = v
	}
	return out, rows.Err()
}

// isedDueSQL is the round's due query, a named constant so the plan test can hold it to its indexes:
// the CA-flag vessels the network has heard that were never asked about, or not in a season, most
// recently heard first.
const isedDueSQL = `SELECT v.mmsi FROM vessels v LEFT JOIN ised i ON i.mmsi = v.mmsi
	WHERE v.flag = 'CA' AND v.kind = 'vessel' AND (i.mmsi IS NULL OR i.checked_at < ?)
	ORDER BY v.seen DESC, v.mmsi LIMIT 2000`

func (s *store) isedDue(ctx context.Context, cutoff time.Time) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, isedDueSQL, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var m uint32
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- lookup and rounds ----

// isedOf is the stored registry answer for each MMSI that has one, for callers that have already
// checked the flag. A failed read costs the particulars, never the answer they ride on.
func (p *Pipeline) isedOf(mmsis ...uint32) map[uint32]*isedShip {
	if p.store == nil {
		return nil
	}
	out, err := p.store.isedByMMSI(mmsis)
	if err != nil {
		log.Printf("ised: %v", err)
		return nil
	}
	return out
}

// loadISEDStats reads what the rounds have stored, so /metrics reports it from boot and with the
// rounds off.
func (p *Pipeline) loadISEDStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM ised WHERE name != ''`).Scan(&n); err != nil {
		return err
	}
	p.ised.ships.Store(n)
	return nil
}

// runISED asks about the due vessels after each hourly check, as the PSIX backfill does.
func (p *Pipeline) runISED(endpoint string) {
	time.Sleep(6 * time.Minute) // after the boot flush and the other syncs' first requests
	for {
		p.backfillISED(time.Now().UTC(), endpoint, time.Hour)
		time.Sleep(time.Hour)
	}
}

// backfillISED asks about the vessels that are due, one at a time, until none are left or budget runs
// out. A vessel whose ask fails is passed over until the next round; isedFailuresInARow failures end
// the round.
func (p *Pipeline) backfillISED(now time.Time, endpoint string, budget time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	due, err := p.store.isedDue(ctx, now.Add(-isedRecheck))
	if err != nil {
		log.Printf("ised: %v", err)
		return 0
	}
	n, failed := 0, 0
	for i, mmsi := range due {
		if i > 0 {
			select {
			case <-time.After(isedPause):
			case <-ctx.Done():
				return n
			}
		}
		v, err := fetchISED(ctx, endpoint, mmsi)
		if err == nil {
			err = p.store.setISED(v, now)
		}
		if err != nil {
			p.ised.failures.Add(1)
			log.Printf("ised: %v", err)
			if failed++; failed >= isedFailuresInARow {
				return n
			}
			continue
		}
		failed = 0
		n++
		p.ised.checked.Add(1)
		if v.Name != "" {
			p.ised.ships.Add(1)
		}
	}
	if n > 0 {
		log.Printf("ised: checked %d Canadian vessels", n)
	}
	return n
}

// isedSearchURL is the public search the data comes from, for the per-vessel credit.
func isedSearchURL(mmsi uint32) string {
	return "https://ised-isde.canada.ca/mmsi-ismm/eng/shipSearch.html?" + url.Values{"mmsi": {strconv.FormatUint(uint64(mmsi), 10)}}.Encode()
}
