package main

// Particulars of Australian vessels from AMSA's list of registered ships, the General and International
// Shipping Registers in one spreadsheet, under CC BY 4.0: official number, length, year of completion,
// type, home port, and registration status. No tonnage and no beam.
//
// The list has no MMSI and no call sign, so the IMO number is the one join, as with Transport Canada's
// register, and only AU-flag vessels look here: about 700 of its 7,500 vessels carry an IMO. The
// spreadsheet's file name carries its date, so each sync finds the link on the list's page first.

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	amsaPage    = "https://www.amsa.gov.au/vessels-operators/ship-registration/list-registered-ships"
	amsaEvery   = 7 * 24 * time.Hour
	amsaLicense = "CC-BY-4.0"
	// amsaMaxPage bounds the page read for the spreadsheet's link; the real page is about 200 KB.
	amsaMaxPage = 8 << 20
)

// amsaMinVessels is the fewest IMO-carrying vessels a sync may store. It stands in for the stored set on
// the first sync: 714 registered vessels carried a valid IMO in October 2026.
var amsaMinVessels = 300

// amsaXlsxLink is a spreadsheet link on the list's page, such as
// /sites/default/files/copy-of-list-of-registered-ships-06.10.26.xlsx, perhaps with a query a CMS adds
// to bust caches.
var amsaXlsxLink = regexp.MustCompile(`(?i)href=["']([^"']+\.xlsx(?:\?[^"']*)?)["']`)

// amsaVessel is one registered vessel with an IMO, as stored and merged.
type amsaVessel struct {
	IMO       uint32
	Official  string // AMSA's official number
	Name      string
	ShipType  string // the list's type, such as Tug, Fishing Vessel, or Oil Tanker
	Status    string // Registered, or Provisional for a provisional registration
	YearBuilt int    // year of completion
	Length    float64
	HomePort  string
}

// amsaStats is read by /metrics.
type amsaStats struct {
	enabled                 atomic.Bool // set when the sync's env flag is on; gates the last-success metric
	runs, failures, vessels atomic.Int64
	lastSuccess             atomic.Int64 // unix seconds
}

// amsaOfficial is the official number as written. The list serves it as text, and the older numbers
// keep a leading zero, 074784, that reading it as a number would drop; only a numeric cell is
// normalized.
func amsaOfficial(s string) string {
	s = strings.TrimSpace(s)
	if strings.Trim(s, "0123456789") == "" {
		return s
	}
	return tcOfficial(s)
}

// amsaYear is the year of completion, which a numeric cell may serve as 2013 or 2013.0.
func amsaYear(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f != math.Trunc(f) || f < 1800 || f > float64(time.Now().Year()+1) {
		return 0
	}
	return int(f)
}

// amsaSheetURL finds the spreadsheet's link on the list's page: the one naming the list, so no other
// spreadsheet, such as a list of deregistered ships, is ever read in its place. A list renamed past
// recognition fails the sync, which the failure alert reports, rather than store another sheet.
func amsaSheetURL(ctx context.Context, page string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := tcClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("page: %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, amsaMaxPage))
	if err != nil {
		return "", err
	}
	matches := amsaXlsxLink.FindAllSubmatch(body, -1)
	link := ""
	for _, m := range matches {
		if l := string(m[1]); strings.Contains(strings.ToLower(l), "list-of-registered-ships") {
			link = l
			break
		}
	}
	if link == "" {
		return "", fmt.Errorf("no link to the list among %d spreadsheet links on the page", len(matches))
	}
	base, err := url.Parse(page)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(html.UnescapeString(link))
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// fetchAMSA downloads the list and keeps the vessels whose IMO has a valid check digit, keyed by IMO.
func fetchAMSA(ctx context.Context, page, dir string) (map[uint32]*amsaVessel, error) {
	sheet, err := amsaSheetURL(ctx, page)
	if err != nil {
		return nil, err
	}
	rows, err := fetchXlsx(ctx, sheet, dir, "amsa-registry-*.xlsx")
	if err != nil {
		return nil, err
	}
	col := xlsxColumns(rows[0])
	get := func(row map[string]string, name string) string {
		if col[name] == "" {
			return ""
		}
		return strings.TrimSpace(row[col[name]])
	}
	for _, name := range []string{"ship name", "official number", "imo number"} {
		if col[name] == "" {
			return nil, fmt.Errorf("no %q column", name)
		}
	}
	for _, name := range []string{"length", "year of completion", "type", "home port", "status"} {
		if col[name] == "" {
			log.Printf("amsa: no %q column; the field will be empty this sync", name)
		}
	}
	out := map[uint32]*amsaVessel{}
	for _, row := range rows[1:] {
		imo, err := strconv.ParseUint(tcOfficial(get(row, "imo number")), 10, 32)
		if err != nil || !validIMO(uint32(imo)) {
			continue
		}
		out[uint32(imo)] = &amsaVessel{IMO: uint32(imo),
			Official:  amsaOfficial(get(row, "official number")),
			Name:      get(row, "ship name"),
			ShipType:  get(row, "type"),
			Status:    get(row, "status"),
			YearBuilt: amsaYear(get(row, "year of completion")),
			Length:    tcFloat(get(row, "length")),
			HomePort:  get(row, "home port")}
	}
	return out, nil
}

// ---- the store ----

const amsaCols = `imo, official, name, ship_type, status, year_built, length, home_port`

// replaceAMSA stores a sync and its time in one transaction, refused when it collapses, as the other
// listings are.
func (s *store) replaceAMSA(vessels map[uint32]*amsaVessel, at time.Time) error {
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM amsa`).Scan(&have); err != nil {
		return err
	}
	if len(vessels) < amsaMinVessels || 2*len(vessels) < have {
		return fmt.Errorf("read %d vessels where %d are stored and at least %d are expected; keeping the stored set",
			len(vessels), have, amsaMinVessels)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM amsa`); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO amsa (` + amsaCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, v := range vessels {
		if _, err := st.Exec(v.IMO, v.Official, v.Name, v.ShipType, v.Status, v.YearBuilt, v.Length, v.HomePort); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('amsa_sync', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// amsaByIMO is the registered vessels among the given IMOs.
func (s *store) amsaByIMO(imos []uint32) (map[uint32]*amsaVessel, error) {
	out := map[uint32]*amsaVessel{}
	if len(imos) == 0 {
		return out, nil
	}
	args := make([]any, len(imos))
	for i, n := range imos {
		args[i] = n
	}
	rows, err := s.db.Query(`SELECT `+amsaCols+` FROM amsa WHERE imo IN (?`+strings.Repeat(",?", len(imos)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := &amsaVessel{}
		if err := rows.Scan(&v.IMO, &v.Official, &v.Name, &v.ShipType, &v.Status, &v.YearBuilt, &v.Length, &v.HomePort); err != nil {
			return nil, err
		}
		out[v.IMO] = v
	}
	return out, rows.Err()
}

// ---- lookup and sync ----

// amsaOf is the list's particulars for each IMO, for callers that have already checked the flag: the list
// speaks for AU-flag vessels, and an IMO another flag carries now may have left it. A failed read costs
// the particulars, never the answer they ride on.
func (p *Pipeline) amsaOf(imos ...uint32) map[uint32]*amsaVessel {
	if p.store == nil {
		return nil
	}
	var valid []uint32
	for _, n := range imos {
		if validIMO(n) {
			valid = append(valid, n)
		}
	}
	out, err := p.store.amsaByIMO(valid)
	if err != nil {
		log.Printf("amsa: %v", err)
		return nil
	}
	return out
}

// amsaSearch is the list's page narrowed to one vessel. The list's own vessel pages are named by a slug
// of the ship's name that the spreadsheet does not carry, so the search is the link that can be built.
func amsaSearch(imo uint32) string {
	return amsaPage + "?combine=" + strconv.FormatUint(uint64(imo), 10)
}

// loadAMSAStats reads what the last sync stored, so /metrics reports it from boot and with the sync off.
func (p *Pipeline) loadAMSAStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM amsa`).Scan(&n); err != nil {
		return err
	}
	p.amsa.vessels.Store(n)
	last, err := p.store.meta("amsa_sync")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.amsa.lastSuccess.Store(t.Unix())
	}
	return nil
}

// runAMSA syncs the list once a week, checking hourly, so a failed sync is retried within the hour and a
// restart does not sync again.
func (p *Pipeline) runAMSA(page string) {
	time.Sleep(6 * time.Minute) // after the boot flush and the other syncs' first requests
	for {
		p.syncAMSAIfDue(time.Now().UTC(), page)
		time.Sleep(time.Hour)
	}
}

// syncAMSAIfDue replaces the stored list when the last sync is a week old or there has never been one,
// and reports whether it did.
func (p *Pipeline) syncAMSAIfDue(now time.Time, page string) bool {
	p.amsa.enabled.Store(true) // a sync that runs is enabled, whoever called it
	last, err := p.store.meta("amsa_sync")
	if err != nil {
		log.Printf("amsa: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < amsaEvery {
		return false
	}
	p.amsa.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	vessels, err := fetchAMSA(ctx, page, filepath.Dir(p.store.path))
	if err == nil {
		err = p.store.replaceAMSA(vessels, now)
	}
	if err != nil {
		p.amsa.failures.Add(1)
		log.Printf("amsa: %v", err)
		return false
	}
	p.amsa.vessels.Store(int64(len(vessels)))
	p.amsa.lastSuccess.Store(now.Unix())
	log.Printf("amsa: stored %d registered vessels with an IMO", len(vessels))
	return true
}
