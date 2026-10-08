package main

// Particulars of Canadian vessels from Transport Canada's Register of Large Vessels, published weekly as
// a spreadsheet under the Open Government Licence - Canada: official number, build year, registered
// dimensions and tonnage, service, and port of registry.
//
// The register has no MMSI and no call sign, so the IMO number is the one join, and only CA-flag vessels
// look here: about 1,200 of its 27,000 vessels carry an IMO. The spreadsheet is a zip of XML the standard
// library reads; nothing here needs a spreadsheet dependency.

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	tcEndpoint = "https://opendatatc.tc.canada.ca/large-vessel-registry_dataset_en.xlsx"
	tcEvery    = 7 * 24 * time.Hour
	tcLicense  = "OGL-Canada-2.0"
	// tcMaxXlsx bounds the download; the real file is about 3 MB.
	tcMaxXlsx = 64 << 20
)

// tcMinVessels is the fewest IMO-carrying vessels a sync may store. It stands in for the stored set on
// the first sync: 1,165 registered vessels carried an IMO in October 2026.
var tcMinVessels = 500

var tcClient = &http.Client{Timeout: 5 * time.Minute}

// tcVessel is one registered vessel with an IMO, as stored and merged.
type tcVessel struct {
	IMO          uint32
	Official     string // Transport Canada's official number
	Name         string
	Service      string // the register's vessel descriptor, such as NON-COMMERCIAL or FISHING
	YearBuilt    int
	GrossTonnage int
	NetTonnage   int
	Length       float64 // metres
	Beam         float64
	Depth        float64
	HomePort     string
}

// tcStats is read by /metrics.
type tcStats struct {
	enabled                 atomic.Bool // set when the sync's env flag is on; gates the last-success metric
	runs, failures, vessels atomic.Int64
	lastSuccess             atomic.Int64 // unix seconds
}

// tcYear undoes the register's year encoding, the year with two implied decimals (196000 is 1960), and
// rejects the junk the column also holds (0, 719, 1121).
func tcYear(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e7 {
		return 0
	}
	y := int(math.Round(f))
	if y >= 100_000 {
		y /= 100
	}
	if y < 1800 || y > time.Now().Year()+1 {
		return 0
	}
	return y
}

func tcFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	// NaN compares false with everything and Inf breaks JSON encoding; no real registered value
	// reaches seven digits.
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > 1e7 {
		return 0
	}
	return f
}

// tcOfficial is the official number as displayed: the spreadsheet serves it as a number, 152527.0 or
// 313730.00, while some older marks are alphanumeric and pass through as written.
func tcOfficial(s string) string {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && f >= 0 && f < 1e12 {
		return strconv.FormatInt(int64(f), 10)
	}
	return s
}

// xlsxSheet reads one worksheet of a spreadsheet as rows of cell values by column letter, resolving
// shared strings. Only what the register needs: values and shared strings, no styles or formulas.
func xlsxSheet(path string) ([]map[string]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	// The download cap bounds the compressed bytes; a hostile entry could still inflate far past it,
	// so each entry is bounded uncompressed too.
	const maxEntry = 256 << 20
	read := func(name string) ([]byte, error) {
		for _, f := range zr.File {
			if f.Name == name {
				if f.UncompressedSize64 > maxEntry {
					return nil, fmt.Errorf("%s inflates to %d bytes", name, f.UncompressedSize64)
				}
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				b, err := io.ReadAll(io.LimitReader(rc, maxEntry+1))
				if err == nil && len(b) > maxEntry {
					return nil, fmt.Errorf("%s inflates past %d bytes", name, int64(maxEntry))
				}
				return b, err
			}
		}
		return nil, fmt.Errorf("%s not in the archive", name)
	}
	var shared []string
	if raw, err := read("xl/sharedStrings.xml"); err == nil {
		var ss struct {
			SI []struct {
				T []string `xml:"t"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(raw, &ss); err != nil {
			return nil, err
		}
		for _, si := range ss.SI {
			shared = append(shared, strings.Join(si.T, ""))
		}
	}
	raw, err := read("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	var sheet struct {
		Rows []struct {
			Cells []struct {
				R string `xml:"r,attr"`
				T string `xml:"t,attr"`
				V string `xml:"v"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(raw, &sheet); err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(sheet.Rows))
	for _, r := range sheet.Rows {
		row := map[string]string{}
		for _, c := range r.Cells {
			letter := strings.TrimRight(c.R, "0123456789")
			if letter == "" {
				continue // a cell with no address could pollute whatever reads a missing column
			}
			v := c.V
			if c.T == "s" {
				i, err := strconv.Atoi(v)
				if err != nil || i < 0 || i >= len(shared) {
					continue
				}
				v = shared[i]
			}
			row[letter] = v
		}
		out = append(out, row)
	}
	return out, nil
}

// fetchXlsx downloads a spreadsheet to a temporary file in dir, the vessel record's directory in
// production, where ProtectSystem=strict leaves the one writable path, and reads its first worksheet.
// pattern names the temporary file, as os.CreateTemp takes it; a file a crash left behind is removed
// first.
func fetchXlsx(ctx context.Context, endpoint, dir, pattern string) ([]map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := tcClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", res.Status)
	}
	staleDir := dir
	if staleDir == "" {
		staleDir = os.TempDir()
	}
	if stale, _ := filepath.Glob(filepath.Join(staleDir, pattern)); stale != nil {
		for _, f := range stale {
			os.Remove(f)
		}
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, err := io.Copy(tmp, io.LimitReader(res.Body, tcMaxXlsx))
	if err != nil {
		return nil, err
	}
	if size == tcMaxXlsx {
		return nil, fmt.Errorf("download larger than %d bytes", int64(tcMaxXlsx))
	}
	rows, err := xlsxSheet(tmp.Name())
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty sheet")
	}
	return rows, nil
}

// xlsxColumns maps each lowercased header in a sheet's first row to its column letter. Registers have
// reordered their columns before, so a sync reads by header. Letters iterate sorted, so a duplicated
// header keeps the first column deterministically.
func xlsxColumns(header map[string]string) map[string]string {
	col := map[string]string{}
	letters := make([]string, 0, len(header))
	for letter := range header {
		letters = append(letters, letter)
	}
	sort.Strings(letters)
	for _, letter := range letters {
		name := strings.ToLower(strings.TrimSpace(header[letter]))
		if col[name] == "" {
			col[name] = letter
		}
	}
	return col
}

// fetchTC downloads the register and keeps the vessels whose IMO has a valid check digit, keyed by IMO.
func fetchTC(ctx context.Context, endpoint, dir string) (map[uint32]*tcVessel, error) {
	rows, err := fetchXlsx(ctx, endpoint, dir, "tc-registry-*.xlsx")
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
	for _, name := range []string{"official number", "vessel name", "imo vessel number", "year of build", "gross tonnage"} {
		if col[name] == "" {
			return nil, fmt.Errorf("no %q column", name)
		}
	}
	for _, name := range []string{"net tonnage", "length", "breadth", "depth", "vessel descriptor", "port of registry"} {
		if col[name] == "" {
			log.Printf("tc: no %q column; the field will be empty this sync", name)
		}
	}
	out := map[uint32]*tcVessel{}
	for _, row := range rows[1:] {
		imo, err := strconv.ParseUint(tcOfficial(get(row, "imo vessel number")), 10, 32)
		if err != nil || !validIMO(uint32(imo)) {
			continue
		}
		out[uint32(imo)] = &tcVessel{IMO: uint32(imo),
			Official:     tcOfficial(get(row, "official number")),
			Name:         get(row, "vessel name"),
			Service:      get(row, "vessel descriptor"),
			YearBuilt:    tcYear(get(row, "year of build")),
			GrossTonnage: int(math.Round(tcFloat(get(row, "gross tonnage")))),
			NetTonnage:   int(math.Round(tcFloat(get(row, "net tonnage")))),
			Length:       tcFloat(get(row, "length")),
			Beam:         tcFloat(get(row, "breadth")),
			Depth:        tcFloat(get(row, "depth")),
			HomePort:     get(row, "port of registry")}
	}
	return out, nil
}

// ---- the store ----

const tcCols = `imo, official, name, service, year_built, gross_tonnage, net_tonnage, length, beam, depth, home_port`

// replaceTC stores a sync and its time in one transaction, refused when it collapses, as the other
// listings are.
func (s *store) replaceTC(vessels map[uint32]*tcVessel, at time.Time) error {
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM tc`).Scan(&have); err != nil {
		return err
	}
	if len(vessels) < tcMinVessels || 2*len(vessels) < have {
		return fmt.Errorf("read %d vessels where %d are stored and at least %d are expected; keeping the stored set",
			len(vessels), have, tcMinVessels)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tc`); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO tc (` + tcCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, v := range vessels {
		if _, err := st.Exec(v.IMO, v.Official, v.Name, v.Service, v.YearBuilt, v.GrossTonnage, v.NetTonnage,
			v.Length, v.Beam, v.Depth, v.HomePort); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('tc_sync', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// tcByIMO is the registered vessels among the given IMOs.
func (s *store) tcByIMO(imos []uint32) (map[uint32]*tcVessel, error) {
	out := map[uint32]*tcVessel{}
	if len(imos) == 0 {
		return out, nil
	}
	args := make([]any, len(imos))
	for i, n := range imos {
		args[i] = n
	}
	rows, err := s.db.Query(`SELECT `+tcCols+` FROM tc WHERE imo IN (?`+strings.Repeat(",?", len(imos)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := &tcVessel{}
		if err := rows.Scan(&v.IMO, &v.Official, &v.Name, &v.Service, &v.YearBuilt, &v.GrossTonnage, &v.NetTonnage,
			&v.Length, &v.Beam, &v.Depth, &v.HomePort); err != nil {
			return nil, err
		}
		out[v.IMO] = v
	}
	return out, rows.Err()
}

// ---- lookup and sync ----

// tcOf is the register's particulars for each IMO, for callers that have already checked the flag: the
// register speaks for CA-flag vessels, and an IMO another flag carries now may have left it. A failed
// read costs the particulars, never the answer they ride on.
func (p *Pipeline) tcOf(imos ...uint32) map[uint32]*tcVessel {
	if p.store == nil {
		return nil
	}
	var valid []uint32
	for _, n := range imos {
		if validIMO(n) {
			valid = append(valid, n)
		}
	}
	out, err := p.store.tcByIMO(valid)
	if err != nil {
		log.Printf("tc: %v", err)
		return nil
	}
	return out
}

// loadTCStats reads what the last sync stored, so /metrics reports it from boot and with the sync off.
func (p *Pipeline) loadTCStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM tc`).Scan(&n); err != nil {
		return err
	}
	p.tc.vessels.Store(n)
	last, err := p.store.meta("tc_sync")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.tc.lastSuccess.Store(t.Unix())
	}
	return nil
}

// runTC syncs the register once a week, checking hourly, so a failed sync is retried within the hour and
// a restart does not sync again.
func (p *Pipeline) runTC(endpoint string) {
	time.Sleep(5 * time.Minute) // after the boot flush and the other syncs' first requests
	for {
		p.syncTCIfDue(time.Now().UTC(), endpoint)
		time.Sleep(time.Hour)
	}
}

// syncTCIfDue replaces the stored register when the last sync is a week old or there has never been one,
// and reports whether it did.
func (p *Pipeline) syncTCIfDue(now time.Time, endpoint string) bool {
	p.tc.enabled.Store(true) // a sync that runs is enabled, whoever called it
	last, err := p.store.meta("tc_sync")
	if err != nil {
		log.Printf("tc: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < tcEvery {
		return false
	}
	p.tc.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	dir := ""
	if p.store != nil {
		dir = filepath.Dir(p.store.path)
	}
	vessels, err := fetchTC(ctx, endpoint, dir)
	if err == nil {
		err = p.store.replaceTC(vessels, now)
	}
	if err != nil {
		p.tc.failures.Add(1)
		log.Printf("tc: %v", err)
		return false
	}
	p.tc.vessels.Store(int64(len(vessels)))
	p.tc.lastSuccess.Store(now.Unix())
	log.Printf("tc: stored %d registered vessels with an IMO", len(vessels))
	return true
}
