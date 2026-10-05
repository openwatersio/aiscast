package main

// Particulars of Norwegian fishing vessels from the Directorate of Fisheries' open vessel register:
// registration mark, build year, registered length and beam, London Convention tonnage, and the owning
// company. The register is published under the Norwegian Licence for Open Government Data (NLOD) 2.0.
//
// The register holds about 5,000 vessels and its API answers 30 a page, so once a week a sync reads every
// page into the fiskeridir table, a couple of hundred requests. Every row comes complete, so there is no
// backfill. A vessel matches as PSIX's do, by call sign and name together: the register has no MMSI, a
// third of its rows have no call sign at all, and Norwegian call signs are reassigned. Owners who are
// people rather than companies are dropped at sync, so no individual's name is stored or served.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	fdirEndpoint = "https://api.fiskeridir.no/vessel-api/api/v1/vessels"
	fdirEvery    = 7 * 24 * time.Hour
	fdirLicense  = "NLOD-2.0"
)

var (
	// fdirPause spaces the page reads. The service states no limit; a few requests a second over a few
	// minutes a week keeps us a small share of its traffic.
	fdirPause = 300 * time.Millisecond
	// fdirMinVessels is the fewest vessels a sync may store. It stands in for the stored set on the first
	// sync: 4,982 vessels were registered in October 2026, two thirds of them with a call sign.
	fdirMinVessels = 2_000
	// fdirMaxPages bounds a sync against an API that pages forever.
	fdirMaxPages = 2_000
)

var fdirClient = &http.Client{Timeout: time.Minute}

// fdirVessel is one register row as stored and merged.
type fdirVessel struct {
	ID           string // the register's id
	CallSign     string // normalized
	Name         string
	Registration string // the registration mark, such as VL0148AV
	YearBuilt    int
	Length       float64 // metres
	Beam         float64
	GrossTonnage int    // London Convention tonnage only; the register's 1947 measures are left out
	Owner        string // the first owning company; owners who are people are dropped at sync
}

// fdirStats is read by /metrics.
type fdirStats struct {
	enabled                 atomic.Bool // set when the sync's env flag is on; gates the last-success metric
	runs, failures, vessels atomic.Int64
	lastSuccess             atomic.Int64 // unix seconds
}

// normCallSignNO is a call sign as both sides are compared, or "" for one that cannot be a Norwegian
// one: those start with LA through LN, JW or JX (Svalbard), or 3Y.
func normCallSignNO(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 4 || len(s) > 7 {
		return ""
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	if s[0] == 'L' && s[1] >= 'A' && s[1] <= 'N' || strings.HasPrefix(s, "JW") || strings.HasPrefix(s, "JX") || strings.HasPrefix(s, "3Y") {
		return s
	}
	return ""
}

// fdirRow is a vessel as the API serves it.
type fdirRow struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	RegistrationMark string  `json:"registrationMark"`
	RadioCallSign    string  `json:"radioCallSign"`
	Width            float64 `json:"width"`
	Length           float64 `json:"length"`
	BuildYear        int     `json:"buildYear"`
	Tonnage          float64 `json:"tonnage"`
	TonnageType      string  `json:"tonnageType"` // LC is the 1969 London Convention; OC the 1947 Oslo one
	Owners           []struct {
		EntityType string `json:"entityType"`
		Name       string `json:"name"`
	} `json:"owners"`
}

// fetchFiskeridir reads the whole register, a page at a time. Vessels without a usable call sign are left
// out: nothing could ever match them.
func fetchFiskeridir(ctx context.Context, endpoint string) (map[string]*fdirVessel, error) {
	out := map[string]*fdirVessel{}
	for page := 1; ; page++ {
		// A feed that pages forever must fail the sync, never silently store a truncated register.
		if page > fdirMaxPages {
			return nil, fmt.Errorf("still paging after %d pages; refusing a truncated register", fdirMaxPages)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?page=%d", endpoint, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", wikidataUserAgent)
		res, err := fdirClient.Do(req)
		if err != nil {
			return nil, err
		}
		var rows []fdirRow
		err = json.NewDecoder(res.Body).Decode(&rows)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("page %d: %s", page, res.Status)
		}
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		for _, r := range rows {
			cs := normCallSignNO(r.RadioCallSign)
			if cs == "" || r.ID == "" {
				continue
			}
			v := &fdirVessel{ID: r.ID, CallSign: cs, Name: r.Name, Registration: r.RegistrationMark,
				YearBuilt: r.BuildYear, Length: r.Length, Beam: r.Width}
			if r.TonnageType == "LC" {
				v.GrossTonnage = int(math.Round(r.Tonnage))
			}
			for _, o := range r.Owners {
				if o.EntityType == "COMPANY" && o.Name != "" {
					v.Owner = o.Name
					break
				}
			}
			out[v.ID] = v
		}
		// The last page is the first short one; link headers say the same, this needs no parsing.
		if len(rows) == 0 || len(rows) < 30 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fdirPause):
		}
	}
	return out, nil
}

// ---- the store ----

const fdirCols = `vessel_id, callsign, name, registration, year_built, length, beam, gross_tonnage, owner`

// replaceFiskeridir stores a sync and its time in one transaction, refused when it has fewer than
// fdirMinVessels or half the vessels already stored, as the other listings are.
func (s *store) replaceFiskeridir(vessels map[string]*fdirVessel, at time.Time) error {
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM fiskeridir`).Scan(&have); err != nil {
		return err
	}
	if len(vessels) < fdirMinVessels || 2*len(vessels) < have {
		return fmt.Errorf("read %d vessels where %d are stored and at least %d are expected; keeping the stored set",
			len(vessels), have, fdirMinVessels)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM fiskeridir`); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO fiskeridir (` + fdirCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, v := range vessels {
		if _, err := st.Exec(v.ID, v.CallSign, v.Name, v.Registration, v.YearBuilt, v.Length, v.Beam, v.GrossTonnage, v.Owner); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('fiskeridir_sync', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// fiskeridirByCallSign is the registered vessels with each call sign.
func (s *store) fiskeridirByCallSign(callsigns []string) (map[string][]*fdirVessel, error) {
	out := map[string][]*fdirVessel{}
	if len(callsigns) == 0 {
		return out, nil
	}
	args := make([]any, len(callsigns))
	for i, c := range callsigns {
		args[i] = c
	}
	rows, err := s.db.Query(`SELECT `+fdirCols+` FROM fiskeridir WHERE callsign IN (?`+strings.Repeat(",?", len(callsigns)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := &fdirVessel{}
		if err := rows.Scan(&v.ID, &v.CallSign, &v.Name, &v.Registration, &v.YearBuilt, &v.Length, &v.Beam, &v.GrossTonnage, &v.Owner); err != nil {
			return nil, err
		}
		out[v.CallSign] = append(out[v.CallSign], v)
	}
	return out, rows.Err()
}

// ---- lookup and sync ----

type fdirKey struct {
	mmsi     uint32
	callsign string
	name     string
}

// fiskeridirOf is the register's particulars for each Norwegian vessel that matches a registered one, by
// call sign and name together, as uscgOf matches. A failed read costs the particulars, never the answer
// they ride on.
func (p *Pipeline) fiskeridirOf(keys ...fdirKey) map[uint32]*fdirVessel {
	if p.store == nil {
		return nil
	}
	callsign := map[uint32]string{}
	var callsigns []string
	for _, k := range keys {
		if cs := normCallSignNO(k.callsign); cs != "" && flagOf(k.mmsi) == "NO" {
			callsign[k.mmsi] = cs
			callsigns = append(callsigns, cs)
		}
	}
	if len(callsigns) == 0 {
		return nil
	}
	byCS, err := p.store.fiskeridirByCallSign(callsigns)
	if err != nil {
		log.Printf("fiskeridir: %v", err)
		return nil
	}
	out := map[uint32]*fdirVessel{}
	for _, k := range keys {
		if cs, ok := callsign[k.mmsi]; ok {
			if v := fdirPick(k.name, byCS[cs]); v != nil {
				out[k.mmsi] = v
			}
		}
	}
	return out
}

// fdirPick is the registered vessel an AIS vessel named name is, among those with its call sign: one whose
// name agrees, then the highest id, which the register hands out in order, so the newest registration.
func fdirPick(name string, cands []*fdirVessel) *fdirVessel {
	var best *fdirVessel
	for _, c := range cands {
		if namesAgree(name, c.Name) && (best == nil || c.ID > best.ID) {
			best = c
		}
	}
	return best
}

// loadFiskeridirStats reads what the last sync stored, so /metrics reports it from boot and with the sync off.
func (p *Pipeline) loadFiskeridirStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM fiskeridir`).Scan(&n); err != nil {
		return err
	}
	p.fiskeridir.vessels.Store(n)
	last, err := p.store.meta("fiskeridir_sync")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.fiskeridir.lastSuccess.Store(t.Unix())
	}
	return nil
}

// runFiskeridir syncs the register once a week, checking hourly, so a failed sync is retried within the
// hour and a restart does not sync again.
func (p *Pipeline) runFiskeridir(endpoint string) {
	time.Sleep(3 * time.Minute) // after the boot flush and the other syncs' first requests
	for {
		p.syncFiskeridirIfDue(time.Now().UTC(), endpoint)
		time.Sleep(time.Hour)
	}
}

// syncFiskeridirIfDue replaces the stored register when the last sync is a week old or there has never
// been one, and reports whether it did.
func (p *Pipeline) syncFiskeridirIfDue(now time.Time, endpoint string) bool {
	p.fiskeridir.enabled.Store(true) // a sync that runs is enabled, whoever called it
	last, err := p.store.meta("fiskeridir_sync")
	if err != nil {
		log.Printf("fiskeridir: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < fdirEvery {
		return false
	}
	p.fiskeridir.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	vessels, err := fetchFiskeridir(ctx, endpoint)
	if err == nil {
		err = p.store.replaceFiskeridir(vessels, now)
	}
	if err != nil {
		p.fiskeridir.failures.Add(1)
		log.Printf("fiskeridir: %v", err)
		return false
	}
	p.fiskeridir.vessels.Store(int64(len(vessels)))
	p.fiskeridir.lastSuccess.Store(now.Unix())
	log.Printf("fiskeridir: stored %d registered vessels with a call sign", len(vessels))
	return true
}
