package main

// Particulars of US-flag vessels from the Coast Guard's Port State Information Exchange (PSIX), a weekly
// public snapshot of its MISLE database: service, status, year built, official number, registered
// dimensions, and tonnage. It is a US government work, in the public domain.
//
// PSIX has no MMSI, and no bulk download. Its SOAP service lists vessels by flag and service type, and
// answers dimensions and tonnage one vessel at a time. So the sync runs in two parts. Once a week a listing
// reads every US-flag vessel with a call sign or an official number into the uscg table, by service type, and recreational
// vessels by build year, since all of them at once fails. Then a backfill reads dimensions and tonnage, a
// request a second, for the vessels heard on AIS that match a listed one, and again every 90 days.
//
// A vessel matches by call sign and name together. AIS carries junk call signs and PSIX keeps vessels long
// scrapped, whose call signs have since gone to other vessels, so a call sign alone attaches the wrong
// vessel; on a sample of US traffic the name check turned away 30 of 1,804 call-sign matches, nearly all
// of them rightly.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	psixEndpoint     = "https://cgmix.uscg.mil/xml/PSIXData.asmx"
	psixEvery        = 7 * 24 * time.Hour
	psixDetailsEvery = 90 * 24 * time.Hour
	psixLicense      = "public domain (U.S. government work)"
	psixFirstYear    = 1800 // the oldest recreational build year PSIX holds
)

var (
	// psixPause spaces the listing's requests and psixDetailPause the backfill's. The service states no
	// limit; a request a second or slower keeps us a small share of its traffic.
	psixPause       = 2 * time.Second
	psixDetailPause = time.Second
	// psixMinVessels is the fewest vessels a listing may store. It stands in for the stored set on the first
	// sync: 61,634 US-flag vessels outside the recreational service had a call sign in September 2026, and
	// the ident-only rows kept for the FCC join add at most the ~66,000 licensed official numbers.
	psixMinVessels = 50_000
)

// psixServices are the service types the listing asks for one at a time. Recreational is asked for by
// build year instead.
var psixServices = []string{"Commercial Fishing Vessel", "Fish Processing Vessel", "Freight Barge", "Freight Ship",
	"Industrial Vessel", "Mobile Offshore Drilling Unit", "Offshore Supply Vessel", "Oil Recovery", "Passenger (Inspected)",
	"Passenger (Uninspected)", "Passenger Barge (Inspected)", "Passenger Barge (Uninspected)", "Public Freight",
	"Public Tankship/Barge", "Public Vessel, Unclassified", "Research Vessel", "School Ship", "Tank Barge", "Tank Ship",
	"Towing Vessel", "Unclassified", "Unknown"}

var psixClient = &http.Client{Timeout: 5 * time.Minute}

// uscgVessel is what the API and MCP serve for one vessel.
type uscgVessel struct {
	ID             int     `json:"id" jsonschema:"the vessel's ID in PSIX"`
	License        string  `json:"license" jsonschema:"public domain (U.S. government work), no credit required"`
	Name           string  `json:"name" jsonschema:"name as documented with the Coast Guard"`
	Identification string  `json:"identification,omitempty" jsonschema:"the official number of a documented vessel, else its state registration"`
	Service        string  `json:"service,omitempty" jsonschema:"service type, e.g. Towing Vessel, Passenger (Inspected), Commercial Fishing Vessel, Recreational"`
	Status         string  `json:"status,omitempty" jsonschema:"Coast Guard status, e.g. Active, Laid Up"`
	YearBuilt      int     `json:"year_built,omitempty"`
	Length         float64 `json:"length,omitempty" jsonschema:"registered length, metres"`
	Beam           float64 `json:"beam,omitempty" jsonschema:"registered breadth, metres"`
	Depth          float64 `json:"depth,omitempty" jsonschema:"registered depth, metres"`
	GrossTonnage   int     `json:"gross_tonnage,omitempty"`
	NetTonnage     int     `json:"net_tonnage,omitempty"`
	TonnageMeasure string  `json:"tonnage_measure,omitempty" jsonschema:"how the tonnage was measured: Convention (the international system), Regulatory (the older US system, in register tons), or Simplified (small vessels)"`

	callsign    string
	officialKey string // Identification through normOfficial, the key the FCC licenses join; "" without one
	detailsAt   int64  // unix ms of the last dimensions and tonnage read; 0 before
}

// uscgStats is read by /metrics.
type uscgStats struct {
	runs, failures, vessels atomic.Int64
	details, detailFailures atomic.Int64
	lastSuccess             atomic.Int64 // unix seconds
}

// ---- matching ----

// psixJunkCallSigns are values crews and clerks type where there is no call sign.
var psixJunkCallSigns = map[string]bool{"NONE": true, "NA": true, "UNKNOWN": true, "TBA": true, "TBD": true, "NIL": true}

// normCallSign is a call sign as both sides are compared, or "" for one that cannot be a US call sign:
// those start with A, K, N, or W and run four to seven letters and digits.
func normCallSign(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 4 || len(s) > 7 || psixJunkCallSigns[s] || !strings.ContainsAny(s[:1], "AKNW") {
		return ""
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return s
}

// vesselNamePrefix is the type a name is often written with on one side and not the other: F/V MELISSA K
// on AIS is MELISSA K in PSIX.
var vesselNamePrefix = regexp.MustCompile(`^(F/?V|M/?V|R/?V|M/?Y|S/?V|USCGC|CG|USNS|USS|NOAA|DREDGE|TUG)\s+`)

func vesselNameKey(s string) string {
	s = vesselNamePrefix.ReplaceAllString(strings.ToUpper(strings.TrimSpace(s)), "")
	if i := strings.IndexByte(s, '('); i > 0 {
		s = s[:i] // USCGC PENOBSCOT BAY (WTGB 107)
	}
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, s)
}

// namesAgree reports whether an AIS name and a documented name are the same vessel's. AIS names are cut at
// 20 characters, abbreviated, and misspelled (GOV THOMAS H KEAN, BR00KLYN MCALLISTER), so beyond equal,
// a prefix, or one inside the other, two names agree when their longest common subsequence is 80% of their
// lengths. That still tells 2502 from 2504 and WEEKS 551 from BREAKWATER 551.
func namesAgree(a, b string) bool {
	a, b = vesselNameKey(a), vesselNameKey(b)
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	if min(len(a), len(b)) >= 5 && (strings.Contains(a, b) || strings.Contains(b, a)) {
		return true
	}
	return 20*lcs(a, b) >= 8*(len(a)+len(b))
}

// lcs is the length of the longest common subsequence of two short ASCII strings.
func lcs(a, b string) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range len(a) {
		for j := range len(b) {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(cur[j], prev[j+1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// uscgPick is the listed vessel an AIS vessel named name is, among those with its call sign: one whose name
// agrees, an active record over others, then the newest record.
func uscgPick(name string, cands []*uscgVessel) *uscgVessel {
	var best *uscgVessel
	for _, c := range cands {
		if !namesAgree(name, c.Name) {
			continue
		}
		if best == nil {
			best = c
		} else if active, bestActive := c.Status == "Active", best.Status == "Active"; active != bestActive {
			if active {
				best = c
			}
		} else if c.ID > best.ID {
			best = c
		}
	}
	return best
}

// ---- the SOAP service ----

// psixCall runs one operation and returns the XML document its ...XMLString result carries. A response
// without that result is the service failing, as it does for a listing too large to build.
func psixCall(ctx context.Context, endpoint, op string, params [][2]string) ([]byte, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><`)
	body.WriteString(op + ` xmlns="https://cgmix.uscg.mil">`)
	for _, p := range params {
		body.WriteString("<" + p[0] + ">")
		xml.EscapeText(&body, []byte(p[1]))
		body.WriteString("</" + p[0] + ">")
	}
	body.WriteString("</" + op + "></soap:Body></soap:Envelope>")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"https://cgmix.uscg.mil/`+op+`"`)
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := psixClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", op, res.Status)
	}
	var env struct {
		Body struct {
			Response struct {
				Result *struct {
					Text string `xml:",chardata"`
				} `xml:",any"`
			} `xml:",any"`
		} `xml:"Body"`
	}
	if err := xml.NewDecoder(res.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if env.Body.Response.Result == nil {
		return nil, fmt.Errorf("%s: no result", op)
	}
	return []byte(env.Body.Response.Result.Text), nil
}

// psixControlRef is a character reference to a control character, which XML forbids and PSIX holds now
// and then: one recreational vessel is named VA&#x12;08BP.
var psixControlRef = regexp.MustCompile(`&#(x0*[0-9a-fA-F]{1,2}|0*[0-9]{1,2});`)

// psixRows decodes the elements named name in a result document into a slice of T.
func psixRows[T any](doc []byte, name string) ([]T, error) {
	doc = psixControlRef.ReplaceAllFunc(doc, func(ref []byte) []byte {
		num, base := string(ref[2:len(ref)-1]), 10
		if num[0] == 'x' {
			num, base = num[1:], 16
		}
		n, err := strconv.ParseUint(num, base, 32)
		if err == nil && n < 0x20 && n != '\t' && n != '\n' && n != '\r' {
			return nil
		}
		return ref
	})
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var out []T
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == name {
			var row T
			if err := dec.DecodeElement(&row, &se); err != nil {
				return nil, err
			}
			out = append(out, row)
		}
	}
}

type psixSummary struct {
	ID             int    `xml:"VesselId"`
	Name           string `xml:"VesselName"`
	CallSign       string `xml:"VesselCallSign"`
	Service        string `xml:"ServiceType"`
	Year           string `xml:"ConstructionCompletedYear"`
	Status         string `xml:"StatusLookupName"`
	Identification string `xml:"Identification"`
}

func psixListing(ctx context.Context, endpoint, service, year string) ([]psixSummary, error) {
	doc, err := psixCall(ctx, endpoint, "getVesselSummaryXMLString", [][2]string{{"VesselID", ""}, {"VesselName", ""},
		{"CallSign", ""}, {"VIN", ""}, {"HIN", ""}, {"Flag", "UNITED STATES"}, {"Service", service}, {"BuildYear", year}})
	if err != nil {
		return nil, err
	}
	return psixRows[psixSummary](doc, "VesselSummary")
}

// fetchPSIX lists every US-flag vessel with a call sign, or an official number an FCC license carries,
// keyed by PSIX vessel ID. licensed is the set of normalized official numbers from the fcc table: an
// ident-only row only that join could ever reach is dead weight unless its number is licensed, and
// keeping all of them made the weekly replace long enough to stall the ingest writer.
func fetchPSIX(ctx context.Context, endpoint string, now time.Time, licensed map[string]bool) (map[int]*uscgVessel, error) {
	type ask struct{ service, year string }
	var asks []ask
	for _, s := range psixServices {
		asks = append(asks, ask{s, ""})
	}
	for y := psixFirstYear; y <= now.Year()+1; y++ {
		asks = append(asks, ask{"Recreational", strconv.Itoa(y)})
	}
	out := map[int]*uscgVessel{}
	for i := 0; i < len(asks); i++ {
		a := asks[i]
		if i > 0 {
			select {
			case <-time.After(psixPause):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		rows, err := psixListing(ctx, endpoint, a.service, a.year)
		if err != nil {
			// A service the service answers with no result has outgrown what PSIX can build whole, as
			// Recreational did first and Commercial Fishing Vessel did in October 2026. Its vessels are
			// still there a build year at a time, so the service's years join the queue instead of
			// failing the sync. A year that fails stays fatal: one bad ask must not quietly thin a
			// listing the halving guard would accept.
			if a.year == "" && strings.Contains(err.Error(), "no result") {
				log.Printf("uscg: %s: listing by build year", a.service)
				for y := psixFirstYear; y <= now.Year()+1; y++ {
					asks = append(asks, ask{a.service, strconv.Itoa(y)})
				}
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", a.service, a.year, err)
		}
		for _, r := range rows {
			cs, name := normCallSign(r.CallSign), strings.TrimSpace(r.Name)
			ident := strings.TrimSpace(r.Identification)
			// A merged record keeps its call sign under a name that says so. A vessel without a call
			// sign is kept when its official number or state registration survives normOfficial, which
			// is how the FCC licenses join: most documented recreational vessels are listed that way.
			// With neither key, or a placeholder where the number should be, nothing could ever find it.
			key := normOfficial(ident)
			if r.ID == 0 || (cs == "" && !licensed[key]) || strings.HasPrefix(name, "DUPLICATE OF") {
				continue
			}
			year, _ := strconv.Atoi(strings.TrimSpace(r.Year))
			out[r.ID] = &uscgVessel{ID: r.ID, callsign: cs, Name: name, Identification: ident, officialKey: key,
				Service: strings.TrimSpace(r.Service), Status: strings.TrimSpace(r.Status), YearBuilt: year}
		}
	}
	return out, nil
}

type psixDimension struct {
	Type    string `xml:"DimensionTypeLookupName"`
	Length  string `xml:"LengthInFeet"`
	Breadth string `xml:"BreadthInFeet"`
	Depth   string `xml:"DepthInFeet"`
}

type psixTonnage struct {
	Weight string `xml:"MeasureOfWeight"`
	Type   string `xml:"TonnageTypeLookupName"`
	Unit   string `xml:"TonnageUnitOfMeasurementFilterLookupId"`
}

// psixDimensionRank orders the measurements a vessel's breadth and depth are taken from.
var psixDimensionRank = map[string]int{"Convention": 1, "Hull(Overall) Formal": 2, "Hull(Overall) Simplified": 3, "Pre-1990/Other": 4}

// psixTonnageRank orders the measurement systems when a vessel has been measured under more than one.
var psixTonnageRank = map[string]int{"Convention": 1, "Regulatory": 2, "Simplified": 3}

func feetToMetres(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return math.Round(f*0.3048*100) / 100
}

// psixDetails fills v's dimensions and tonnage. The Length row is the registered length; breadth and depth
// come from the hull measurement. Tonnage rows are labelled Long Ton and Short Ton, but the unit filter
// tells them apart: 64 is gross tonnage and 111 net, which every vessel sampled bore out.
func psixDetails(ctx context.Context, endpoint string, v *uscgVessel) error {
	id := [][2]string{{"VesselID", strconv.Itoa(v.ID)}}
	doc, err := psixCall(ctx, endpoint, "getVesselDimensionsXMLString", id)
	if err != nil {
		return err
	}
	dims, err := psixRows[psixDimension](doc, "VesselDimensions")
	if err != nil {
		return err
	}
	select {
	case <-time.After(psixDetailPause):
	case <-ctx.Done():
		return ctx.Err()
	}
	doc, err = psixCall(ctx, endpoint, "getVesselTonnageXMLString", id)
	if err != nil {
		return err
	}
	tons, err := psixRows[psixTonnage](doc, "VesselTonnage")
	if err != nil {
		return err
	}
	v.Length, v.Beam, v.Depth, v.GrossTonnage, v.NetTonnage, v.TonnageMeasure = 0, 0, 0, 0, 0, ""
	hull := 0
	for _, d := range dims {
		if d.Type == "Length" {
			v.Length = feetToMetres(d.Length)
		}
		if r := psixDimensionRank[d.Type]; r > 0 && feetToMetres(d.Breadth) > 0 && (hull == 0 || r < hull) {
			hull, v.Beam, v.Depth = r, feetToMetres(d.Breadth), feetToMetres(d.Depth)
			if v.Length == 0 {
				v.Length = feetToMetres(d.Length)
			}
		}
	}
	measure := 0
	for _, t := range tons {
		system, _, _ := strings.Cut(strings.TrimSpace(t.Type), " (") // Convention (Subpart B)
		r := psixTonnageRank[system]
		n, err := strconv.Atoi(strings.TrimSpace(t.Weight))
		if r == 0 || err != nil || n <= 0 || (measure != 0 && r > measure) {
			continue
		}
		if r < measure {
			v.GrossTonnage, v.NetTonnage = 0, 0
		}
		measure, v.TonnageMeasure = r, system
		switch strings.TrimSpace(t.Unit) {
		case "64":
			v.GrossTonnage = n
		case "111":
			v.NetTonnage = n
		}
	}
	return nil
}

// ---- storage ----

const uscgCols = `vessel_id, callsign, name, identification, official_key, service, status, year_built, length, beam, depth,
	gross_tonnage, net_tonnage, tonnage_measure, details_at`

func scanUSCG(rows *sql.Rows) (*uscgVessel, error) {
	v := &uscgVessel{License: psixLicense}
	err := rows.Scan(&v.ID, &v.callsign, &v.Name, &v.Identification, &v.officialKey, &v.Service, &v.Status, &v.YearBuilt, &v.Length, &v.Beam,
		&v.Depth, &v.GrossTonnage, &v.NetTonnage, &v.TonnageMeasure, &v.detailsAt)
	return v, err
}

// replaceUSCGListing stores a listing and the sync's time in one transaction. A listed vessel keeps the
// dimensions and tonnage already read for it; a vessel the listing no longer has is dropped. The listing is
// refused when it has fewer than psixMinVessels or half the vessels already stored.
func (s *store) replaceUSCGListing(vessels map[int]*uscgVessel, at time.Time) error {
	// Counted before the transaction, which then opens with a write, as replaceWikidata does.
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM uscg`).Scan(&have); err != nil {
		return err
	}
	if len(vessels) == 0 || len(vessels) < psixMinVessels || 2*len(vessels) < have {
		return fmt.Errorf("listed %d vessels where %d are stored and at least %d are expected; keeping the stored set",
			len(vessels), have, psixMinVessels)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO uscg (vessel_id, callsign, name, identification, official_key, service, status, year_built, listed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vessel_id) DO UPDATE SET callsign = excluded.callsign, name = excluded.name,
			identification = excluded.identification, official_key = excluded.official_key, service = excluded.service,
			status = excluded.status, year_built = excluded.year_built, listed = excluded.listed`)
	if err != nil {
		return err
	}
	defer st.Close()
	listed := at.UnixMilli()
	for _, v := range vessels {
		if _, err := st.Exec(v.ID, v.callsign, v.Name, v.Identification, v.officialKey, v.Service, v.Status, v.YearBuilt, listed); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM uscg WHERE listed != ?`, listed); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('uscg_sync', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) setUSCGDetails(v *uscgVessel, at time.Time) error {
	_, err := s.db.Exec(`UPDATE uscg SET length = ?, beam = ?, depth = ?, gross_tonnage = ?, net_tonnage = ?, tonnage_measure = ?,
		details_at = ? WHERE vessel_id = ?`, v.Length, v.Beam, v.Depth, v.GrossTonnage, v.NetTonnage, v.TonnageMeasure, at.UnixMilli(), v.ID)
	return err
}

// uscgByCallSign is the listed vessels with each call sign.
func (s *store) uscgByCallSign(callsigns []string) (map[string][]*uscgVessel, error) {
	out := map[string][]*uscgVessel{}
	if len(callsigns) == 0 {
		return out, nil
	}
	args := make([]any, len(callsigns))
	for i, c := range callsigns {
		args[i] = c
	}
	rows, err := s.db.Query(`SELECT `+uscgCols+` FROM uscg WHERE callsign IN (?`+strings.Repeat(",?", len(callsigns)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanUSCG(rows)
		if err != nil {
			return nil, err
		}
		out[v.callsign] = append(out[v.callsign], v)
	}
	return out, rows.Err()
}

// uscgDue is the listed vessels matched to a US-flag vessel in the record whose dimensions and tonnage were
// read before cutoff or never, most recently heard first, so the backfill reaches the vessels on the water
// before those long gone.
// uscgDueSQL is the backfill's due query, a named constant so the plan test can hold it to its
// indexes.
const uscgDueSQL = `SELECT * FROM (
		SELECT v.mmsi, v.name, v.seen, 0 AS via, u.vessel_id, u.callsign, u.name AS uname, u.identification, u.service, u.status,
			u.year_built, u.length, u.beam, u.depth, u.gross_tonnage, u.net_tonnage, u.tonnage_measure, u.details_at
			FROM vessels v JOIN uscg u ON u.callsign = upper(trim(v.callsign))
			WHERE v.flag = 'US' AND u.callsign != ''
		UNION ALL
		SELECT v.mmsi, v.name, v.seen, 1 AS via, u.vessel_id, u.callsign, u.name AS uname, u.identification, u.service, u.status,
			u.year_built, u.length, u.beam, u.depth, u.gross_tonnage, u.net_tonnage, u.tonnage_measure, u.details_at
			FROM vessels v JOIN fcc f ON f.mmsi = v.mmsi JOIN uscg u ON u.official_key = f.official
			WHERE v.flag = 'US' AND f.official != '' AND u.official_key != ''
		) ORDER BY seen DESC, mmsi`

func (s *store) uscgDue(ctx context.Context, cutoff time.Time) ([]*uscgVessel, error) {
	rows, err := s.db.QueryContext(ctx, uscgDueSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type heard struct {
		mmsi   uint32
		name   string
		cands  []*uscgVessel // matched through the call sign
		fromID []*uscgVessel // matched through the FCC license's official number
	}
	var order []*heard
	byMMSI := map[uint32]*heard{}
	for rows.Next() {
		var mmsi uint32
		var name string
		var seen int64
		var via int
		v := &uscgVessel{License: psixLicense}
		if err := rows.Scan(&mmsi, &name, &seen, &via, &v.ID, &v.callsign, &v.Name, &v.Identification, &v.Service, &v.Status, &v.YearBuilt,
			&v.Length, &v.Beam, &v.Depth, &v.GrossTonnage, &v.NetTonnage, &v.TonnageMeasure, &v.detailsAt); err != nil {
			return nil, err
		}
		h := byMMSI[mmsi]
		if h == nil {
			h = &heard{mmsi: mmsi, name: name}
			byMMSI[mmsi] = h
			order = append(order, h)
		}
		if via == 1 {
			h.fromID = append(h.fromID, v)
		} else {
			h.cands = append(h.cands, v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var due []*uscgVessel
	taken := map[int]bool{}
	for _, h := range order {
		v := uscgChoose(h.name, uscgNewest(h.fromID), uscgPick(h.name, h.cands))
		if v != nil && !taken[v.ID] && v.detailsAt < cutoff.UnixMilli() {
			taken[v.ID] = true
			due = append(due, v)
		}
	}
	return due, nil
}

// ---- the pipeline side ----

// uscgKey is what a vessel is matched by.
type uscgKey struct {
	mmsi           uint32
	callsign, name string
	official       string // from the vessel's FCC license (fcc.go), "" without one
}

// uscgOf is the PSIX particulars for each US-flag vessel that matches a listed one. An official number
// from the vessel's FCC license matches a record exactly and wins; without one, or when the number finds
// nothing, the vessel matches by call sign and name together. A failed read costs the particulars, never
// the answer they ride on.
func (p *Pipeline) uscgOf(keys ...uscgKey) map[uint32]*uscgVessel {
	if p.store == nil {
		return nil
	}
	callsign := map[uint32]string{} // the US-flag keys, by their normalized call sign
	official := map[uint32]string{} // the US-flag keys' official numbers from their FCC licenses
	var callsigns, officials []string
	for _, k := range keys {
		if flagOf(k.mmsi) != "US" {
			continue
		}
		if cs := normCallSign(k.callsign); cs != "" {
			callsign[k.mmsi] = cs
			callsigns = append(callsigns, cs)
		}
		if k.official != "" {
			official[k.mmsi] = k.official
			officials = append(officials, k.official)
		}
	}
	if len(callsigns) == 0 && len(officials) == 0 {
		return nil
	}
	byCS, err := p.store.uscgByCallSign(callsigns)
	if err != nil {
		log.Printf("uscg: %v", err)
		return nil
	}
	byID, err := p.store.uscgByIdentification(officials)
	if err != nil {
		log.Printf("uscg: %v", err)
		return nil
	}
	out := map[uint32]*uscgVessel{}
	for _, k := range keys {
		var fromID, fuzzy *uscgVessel
		if o, ok := official[k.mmsi]; ok {
			fromID = uscgNewest(byID[o])
		}
		if cs, ok := callsign[k.mmsi]; ok {
			fuzzy = uscgPick(k.name, byCS[cs])
		}
		if v := uscgChoose(k.name, fromID, fuzzy); v != nil {
			out[k.mmsi] = v
		}
	}
	return out
}

// uscgChoose is the one record a vessel gets, agreed between the serve path and the backfill so they
// never pick differently: an official-number match wins when the fuzzy path found nothing, found the
// same record, or the documented names agree; a license carrying a stale hull's number never overrides
// a call-sign and name match that disagrees with it. A record only its official number reached, one
// with no call sign at all, also needs the names to agree: with no second key, a stale or mistyped
// number on the license would otherwise serve another hull's facts uncorroborated.
func uscgChoose(name string, fromID, fuzzy *uscgVessel) *uscgVessel {
	if fromID != nil && fuzzy == nil && fromID.callsign == "" && !namesAgree(name, fromID.Name) {
		fromID = nil
	}
	if fromID != nil && (fuzzy == nil || fuzzy.ID == fromID.ID || namesAgree(name, fromID.Name)) {
		return fromID
	}
	return fuzzy
}

// uscgNewest is the record to serve among those sharing an official number: active over not, then newest.
func uscgNewest(cands []*uscgVessel) *uscgVessel {
	var best *uscgVessel
	for _, c := range cands {
		if best == nil {
			best = c
		} else if active, bestActive := c.Status == "Active", best.Status == "Active"; active != bestActive {
			if active {
				best = c
			}
		} else if c.ID > best.ID {
			best = c
		}
	}
	return best
}

// uscgByIdentification is the listed vessels with each official number or state registration, keyed and
// queried by the normalized form both sides of the FCC join are written in.
func (s *store) uscgByIdentification(ids []string) (map[string][]*uscgVessel, error) {
	out := map[string][]*uscgVessel{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, c := range ids {
		args[i] = c
	}
	rows, err := s.db.Query(`SELECT `+uscgCols+` FROM uscg WHERE official_key != '' AND official_key IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanUSCG(rows)
		if err != nil {
			return nil, err
		}
		out[v.officialKey] = append(out[v.officialKey], v)
	}
	return out, rows.Err()
}

// loadUSCGStats reads what the last sync stored, so /metrics reports it from boot and with the sync off.
func (p *Pipeline) loadUSCGStats() error {
	var n int64
	if err := p.store.db.QueryRow(`SELECT count(*) FROM uscg`).Scan(&n); err != nil {
		return err
	}
	p.uscg.vessels.Store(n)
	last, err := p.store.meta("uscg_sync")
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.uscg.lastSuccess.Store(t.Unix())
	}
	return nil
}

// runUSCG lists the vessels once a week, checking hourly, and after each check reads the dimensions and
// tonnage that are due.
func (p *Pipeline) runUSCG(endpoint string) {
	time.Sleep(2 * time.Minute) // after the boot flush and the Wikidata sync's first query
	for {
		now := time.Now().UTC()
		p.syncUSCGIfDue(now, endpoint)
		p.backfillUSCG(now, endpoint, time.Hour)
		time.Sleep(time.Hour)
	}
}

// syncUSCGIfDue replaces the listing when the last one is a week old or there has never been one, and
// reports whether it did.
func (p *Pipeline) syncUSCGIfDue(now time.Time, endpoint string) bool {
	last, err := p.store.meta("uscg_sync")
	if err != nil {
		log.Printf("uscg: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < psixEvery {
		// A license sync newer than the listing can make more ident-only rows joinable, so the listing
		// re-runs early. Comparing stamps, rather than one sync clearing the other's, stays correct
		// when the license sync lands while a listing is already in flight: the fresh listing writes a
		// newer stamp, and the comparison still fires at the next check.
		fccLast, ferr := p.store.meta("fcc_sync")
		if ferr != nil {
			log.Printf("uscg: %v", ferr)
			return false
		}
		if f, perr := time.Parse(time.RFC3339, fccLast); perr != nil || !f.After(t) {
			return false
		}
	}
	p.uscg.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	licensed, err := p.store.fccOfficials()
	if err != nil {
		p.uscg.failures.Add(1)
		log.Printf("uscg: %v", err)
		return false
	}
	vessels, err := fetchPSIX(ctx, endpoint, now, licensed)
	if err == nil {
		err = p.store.replaceUSCGListing(vessels, now)
	}
	if err != nil {
		p.uscg.failures.Add(1)
		log.Printf("uscg: %v", err)
		return false
	}
	p.uscg.vessels.Store(int64(len(vessels)))
	p.uscg.lastSuccess.Store(now.Unix())
	log.Printf("uscg: listed %d US-flag vessels", len(vessels))
	return true
}

// psixFailuresInARow ends a backfill round: the service is down, not one vessel's record.
const psixFailuresInARow = 3

// backfillUSCG reads dimensions and tonnage for the matched vessels that are due, a vessel at a time, until
// none are left or budget runs out. A vessel whose read fails is passed over until the next round, so one
// bad record cannot hold up the rest; psixFailuresInARow failures end the round.
func (p *Pipeline) backfillUSCG(now time.Time, endpoint string, budget time.Duration) int {
	// The budget bounds the due query too: a query gone quadratic once pinned a CPU for hours here,
	// unbounded, because the context used to start after it.
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	due, err := p.store.uscgDue(ctx, now.Add(-psixDetailsEvery))
	if err != nil {
		log.Printf("uscg: %v", err)
		return 0
	}
	n, failed := 0, 0
	for i, v := range due {
		if i > 0 {
			select {
			case <-time.After(psixDetailPause):
			case <-ctx.Done():
				return n
			}
		}
		if err := psixDetails(ctx, endpoint, v); err != nil {
			if ctx.Err() != nil {
				return n
			}
			p.uscg.detailFailures.Add(1)
			log.Printf("uscg: vessel %d: %v", v.ID, err)
			if failed++; failed == psixFailuresInARow {
				return n
			}
			continue
		}
		failed = 0
		if err := p.store.setUSCGDetails(v, time.Now()); err != nil {
			log.Printf("uscg: %v", err)
			return n
		}
		p.uscg.details.Add(1)
		n++
	}
	return n
}
