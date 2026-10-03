package main

// US ship station licenses from the FCC's Universal Licensing System: the first enrichment source keyed
// by MMSI itself. The FCC publishes its full licensing database weekly as bulk files, a US government
// work in the public domain. One 44 MB zip holds every ship station license; the sync keeps the active
// ones that carry an MMSI, about 66,000, with the licensed call sign, ship name, and official number.
//
// The official number is the join PSIX lacks: PSIX has no MMSI, so vessels match it by call sign and
// name, which junk call signs and renames defeat. A license's official number matches a PSIX record
// exactly, so a vessel the fuzzy match misses is still found. Licensee names are never read, so no
// individual's name is stored or served; recreational MMSIs issued by BoatUS and the like are not FCC
// licenses and are not here.

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	fccEndpoint = "https://data.fcc.gov/download/pub/uls/complete/l_ship.zip"
	fccEvery    = 7 * 24 * time.Hour
)

// fccMinShips is the fewest ships a sync may store. It stands in for the stored set on the first sync:
// 66,441 active ship licenses carried an MMSI in September 2026.
var fccMinShips = 30_000

var fccClient = &http.Client{Timeout: 15 * time.Minute}

// fccMaxZip bounds the download; the real file is 44 MB.
const fccMaxZip = 512 << 20

// normOfficial is an official number or state registration as both sides of a match are written:
// uppercase with separators dropped, or "" for a placeholder that would match the wrong vessel, such as
// 0000000, "In process", or a bare state code.
func normOfficial(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	v := b.String()
	if len(v) < 5 || !strings.ContainsAny(v, "123456789") || strings.Contains(v, "PROCESS") {
		return ""
	}
	return v
}

// fccShip is one active ship station license with an MMSI.
type fccShip struct {
	MMSI     uint32
	USI      int64 // the license's unique system identifier, which its ULS page is keyed by
	CallSign string
	Name     string
	Official string // the official number of a documented vessel, else its state registration
}

// cp1252 is the Windows-1252 table for 0x80 through 0x9F; the rest of the high half is Latin-1. The five
// unassigned codes decode to the replacement character.
var cp1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

// fromCP1252 decodes ULS text, which is Windows-1252, into UTF-8. ASCII, which is nearly every line,
// passes through untouched.
func fromCP1252(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x80:
			b.WriteByte(c)
		case c < 0xA0:
			b.WriteRune(cp1252[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// fccStats is read by /metrics.
type fccStats struct {
	runs, failures, ships atomic.Int64
	lastSuccess           atomic.Int64 // unix seconds
}

// fetchFCC downloads the weekly zip to a temporary file in dir and reads two of its tables: HD for each
// license's status and SH for the ship. Only an active license with an MMSI is kept; where two licenses
// name one MMSI, the newest wins. dir is the vessel record's directory in production: the service runs
// under ProtectSystem=strict, where /tmp is read-only and the record's directory is the one writable
// place. Empty falls back to the system temp directory.
func fetchFCC(ctx context.Context, endpoint, dir string) (map[uint32]*fccShip, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := fccClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", res.Status)
	}
	// A crash mid-sync leaves the previous download behind, and the zip holds licensee names this
	// source promises never to keep, so stale copies go first.
	staleDir := dir
	if staleDir == "" {
		staleDir = os.TempDir()
	}
	if stale, _ := filepath.Glob(filepath.Join(staleDir, "fcc-ship-*.zip")); stale != nil {
		for _, f := range stale {
			os.Remove(f)
		}
	}
	tmp, err := os.CreateTemp(dir, "fcc-ship-*.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, err := io.Copy(tmp, io.LimitReader(res.Body, fccMaxZip))
	if err != nil {
		return nil, err
	}
	if size == fccMaxZip {
		return nil, fmt.Errorf("download larger than %d bytes", fccMaxZip)
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return nil, err
	}
	lines := func(name string, field func([]string)) error {
		for _, f := range zr.File {
			if !strings.EqualFold(f.Name, name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			sc := bufio.NewScanner(rc)
			sc.Buffer(make([]byte, 1<<16), 1<<20)
			for sc.Scan() {
				field(strings.Split(sc.Text(), "|"))
			}
			return sc.Err()
		}
		return fmt.Errorf("%s not in the archive", name)
	}
	// HD: [5] is the license status, A for active.
	active := map[string]bool{}
	if err := lines("HD.dat", func(p []string) {
		if len(p) > 5 && p[5] == "A" {
			active[p[1]] = true
		}
	}); err != nil {
		return nil, err
	}
	// SH: [4] call sign, [9] ship name, [10] official number, [21] MMSI.
	out := map[uint32]*fccShip{}
	if err := lines("SH.dat", func(p []string) {
		if len(p) < 22 || !active[p[1]] || len(p[21]) != 9 {
			return
		}
		mmsi, err := strconv.ParseUint(p[21], 10, 32)
		// ULS holds a few mistyped MMSIs under foreign identification digits; a junk row must never
		// attach US facts to another flag's vessel.
		if err != nil || flagOf(uint32(mmsi)) != "US" {
			return
		}
		usi, _ := strconv.ParseInt(p[1], 10, 64)
		if prev, ok := out[uint32(mmsi)]; ok && prev.USI > usi {
			return
		}
		out[uint32(mmsi)] = &fccShip{MMSI: uint32(mmsi), USI: usi,
			CallSign: strings.TrimSpace(p[4]),
			Name:     fromCP1252(strings.TrimSpace(p[9])),
			Official: normOfficial(p[10])}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- the store ----

const fccCols = `mmsi, usi, callsign, name, official`

// replaceFCC stores a sync and its time in one transaction, refused when it collapses, as the other
// listings are.
func (s *store) replaceFCC(ships map[uint32]*fccShip, at time.Time) error {
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM fcc`).Scan(&have); err != nil {
		return err
	}
	if len(ships) < fccMinShips || 2*len(ships) < have {
		return fmt.Errorf("read %d ships where %d are stored and at least %d are expected; keeping the stored set",
			len(ships), have, fccMinShips)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM fcc`); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO fcc (` + fccCols + `) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, v := range ships {
		if _, err := st.Exec(v.MMSI, v.USI, v.CallSign, v.Name, v.Official); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('fcc_sync', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// fccByMMSI is the licensed ships among the given MMSIs.
func (s *store) fccByMMSI(mmsis []uint32) (map[uint32]*fccShip, error) {
	out := map[uint32]*fccShip{}
	if len(mmsis) == 0 {
		return out, nil
	}
	args := make([]any, len(mmsis))
	for i, m := range mmsis {
		args[i] = m
	}
	rows, err := s.db.Query(`SELECT `+fccCols+` FROM fcc WHERE mmsi IN (?`+strings.Repeat(",?", len(mmsis)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := &fccShip{}
		if err := rows.Scan(&v.MMSI, &v.USI, &v.CallSign, &v.Name, &v.Official); err != nil {
			return nil, err
		}
		out[v.MMSI] = v
	}
	return out, rows.Err()
}

// ---- lookup and sync ----

// fccOf is the active license for each MMSI that has one. Only FCC-licensed MMSIs are stored, so there is
// nothing to filter by flag. A failed read costs the particulars, never the answer they ride on.
func (p *Pipeline) fccOf(mmsis ...uint32) map[uint32]*fccShip {
	if p.store == nil {
		return nil
	}
	out, err := p.store.fccByMMSI(mmsis)
	if err != nil {
		log.Printf("fcc: %v", err)
		return nil
	}
	return out
}

// loadFCCStats reads what the last sync stored, so /metrics reports it from boot and with the sync off.
func (p *Pipeline) loadFCCStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM fcc`).Scan(&n); err != nil {
		return err
	}
	p.fcc.ships.Store(n)
	last, err := p.store.meta("fcc_sync")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.fcc.lastSuccess.Store(t.Unix())
	}
	return nil
}

// runFCC syncs the licenses once a week, checking hourly, so a failed sync is retried within the hour
// and a restart does not sync again.
func (p *Pipeline) runFCC(endpoint string) {
	time.Sleep(4 * time.Minute) // after the boot flush and the other syncs' first requests
	for {
		p.syncFCCIfDue(time.Now().UTC(), endpoint)
		time.Sleep(time.Hour)
	}
}

// syncFCCIfDue replaces the stored licenses when the last sync is a week old or there has never been
// one, and reports whether it did.
func (p *Pipeline) syncFCCIfDue(now time.Time, endpoint string) bool {
	last, err := p.store.meta("fcc_sync")
	if err != nil {
		log.Printf("fcc: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < fccEvery {
		return false
	}
	p.fcc.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	dir := ""
	if p.store != nil {
		dir = filepath.Dir(p.store.path)
	}
	ships, err := fetchFCC(ctx, endpoint, dir)
	if err == nil {
		err = p.store.replaceFCC(ships, now)
	}
	if err != nil {
		p.fcc.failures.Add(1)
		log.Printf("fcc: %v", err)
		return false
	}
	p.fcc.ships.Store(int64(len(ships)))
	p.fcc.lastSuccess.Store(now.Unix())
	log.Printf("fcc: stored %d active ship licenses with an MMSI", len(ships))
	return true
}
