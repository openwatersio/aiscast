package main

// Vessel particulars from Wikidata, found by IMO number (P458): builder, year built, tonnage, registered
// dimensions, country of registry, and former names. A bot import gave most IMO-registered ships an item,
// and Wikidata is CC0, so the particulars are served without a credit line.
//
// Once a week a sync reads every item with an IMO from the Wikidata Query Service and replaces the wikidata
// table in the vessel record. Requests read that table and never Wikidata. One query per field keeps each
// in the seconds; one query for everything takes half a minute, close to the service's 60-second limit.
// Builders and registries are named last, in queries over their items alone: joining labels onto every
// ship's statement runs past the limit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	wikidataSPARQL = "https://query.wikidata.org/sparql"
	// Wikimedia asks every client to name itself and give a contact.
	wikidataUserAgent = "aiscast/1.0 (https://openwaters.io/ais/; ais@openwaters.io)"
	wikidataEvery     = 7 * 24 * time.Hour
	wikidataLicense   = "CC0-1.0"
)

// wikidataPause spaces the queries of one sync. The Query Service allows each client 60 seconds of query
// time a minute, and each query here takes 1 to 20.
var wikidataPause = 10 * time.Second

// wikidataLabelBatch is the items named per label query.
const wikidataLabelBatch = 1000

var wikidataClient = &http.Client{Timeout: 3 * time.Minute}

// wikidataShip is what the API and MCP serve for one vessel.
type wikidataShip struct {
	ID           string   `json:"id" jsonschema:"Wikidata item ID"`
	URL          string   `json:"url" jsonschema:"the item's page on Wikidata"`
	License      string   `json:"license" jsonschema:"CC0-1.0: public domain, no credit required"`
	Builder      string   `json:"builder,omitempty" jsonschema:"shipyard or builder"`
	YearBuilt    int      `json:"year_built,omitempty" jsonschema:"year the vessel entered service"`
	GrossTonnage int      `json:"gross_tonnage,omitempty"`
	Deadweight   int      `json:"deadweight,omitempty" jsonschema:"deadweight, tonnes"`
	Length       float64  `json:"length,omitempty" jsonschema:"length as registered, metres; may differ from the AIS length"`
	Beam         float64  `json:"beam,omitempty" jsonschema:"beam as registered, metres"`
	Registry     string   `json:"registry,omitempty" jsonschema:"country of registry, in English"`
	FormerNames  []string `json:"former_names,omitempty" jsonschema:"names the vessel has carried before, oldest first"`
}

// wikidataStats is read by /metrics.
type wikidataStats struct {
	runs, failures, ships atomic.Int64
	lastSuccess           atomic.Int64 // unix seconds
}

// sparqlTerm is one value in a SPARQL JSON result.
type sparqlTerm struct {
	Value string `json:"value"`
	Lang  string `json:"xml:lang"`
}

// wikidataBinding is one result row. Every query names its variables from this set.
type wikidataBinding struct {
	Item  sparqlTerm `json:"item"`
	V     sparqlTerm `json:"v"`
	Label sparqlTerm `json:"label"`
	End   sparqlTerm `json:"end"`
}

// wikidataLabelQuery names the items in its VALUES list, in English or, failing that, in the multilingual
// label Wikidata uses for names that are the same in every language.
const wikidataLabelQuery = `SELECT ?v ?label WHERE { VALUES ?v { %s } ?v rdfs:label ?label FILTER(LANG(?label) IN ("en", "mul")) }`

// wikidataItem gathers one item's rows across the queries.
type wikidataItem struct {
	qid         int
	imos        []uint32
	ship        wikidataShip
	builder     wikidataRef
	registry    wikidataRef
	formerNames []formerName
}

// wikidataRef is a property whose value is another item, named later by its label.
type wikidataRef struct {
	qid   int
	ended bool
}

type formerName struct {
	name string
	end  string
}

// take keeps one value when an item has several: one without an end time (P582), which is still in force,
// then the lowest QID, so every sync picks the same.
func (r *wikidataRef) take(b wikidataBinding) {
	qid, ended := qidOf(b.V.Value), b.End.Value != ""
	if qid == 0 {
		return
	}
	if r.qid == 0 || (r.ended && !ended) || (r.ended == ended && qid < r.qid) {
		*r = wikidataRef{qid, ended}
	}
}

// wikidataQueries run in order; the first finds the items and their IMOs, and each after it adds one field.
// Quantities are read normalized (psn:), which converts feet to metres and tonnes to kilograms. Gross
// tonnage is unitless (Q199), and a quantity in gross register tons, an older measure, is left out. Where an
// item has several best-ranked values the largest is kept, which after a lengthening is the current one.
var wikidataQueries = []struct {
	field  string
	sparql string
	apply  func(*wikidataItem, wikidataBinding)
}{
	{"imo", `SELECT ?item ?v WHERE { ?item wdt:P458 ?v }`, func(it *wikidataItem, b wikidataBinding) {
		if n, err := strconv.ParseUint(strings.TrimSpace(b.V.Value), 10, 32); err == nil && validIMO(uint32(n)) {
			it.imos = append(it.imos, uint32(n))
		}
	}},
	{"year_built", `SELECT ?item ?v WHERE { ?item wdt:P458 []; wdt:P729 ?t . BIND(YEAR(?t) AS ?v) }`, func(it *wikidataItem, b wikidataBinding) {
		if n, err := strconv.Atoi(b.V.Value); err == nil && n > 0 && (it.ship.YearBuilt == 0 || n < it.ship.YearBuilt) {
			it.ship.YearBuilt = n
		}
	}},
	{"gross_tonnage", `SELECT ?item ?v WHERE { ?item wdt:P458 []; p:P1093 ?s . ?s a wikibase:BestRank; psv:P1093 [ wikibase:quantityAmount ?v; wikibase:quantityUnit wd:Q199 ] }`,
		func(it *wikidataItem, b wikidataBinding) {
			it.ship.GrossTonnage = max(it.ship.GrossTonnage, int(math.Round(number(b.V.Value))))
		}},
	{"deadweight", `SELECT ?item ?v WHERE { ?item wdt:P458 []; p:P4519 ?s . ?s a wikibase:BestRank; psn:P4519/wikibase:quantityAmount ?v }`,
		func(it *wikidataItem, b wikidataBinding) {
			it.ship.Deadweight = max(it.ship.Deadweight, int(math.Round(number(b.V.Value)/1000)))
		}},
	{"length", `SELECT ?item ?v WHERE { ?item wdt:P458 []; p:P2043 ?s . ?s a wikibase:BestRank; psn:P2043/wikibase:quantityAmount ?v }`,
		func(it *wikidataItem, b wikidataBinding) {
			it.ship.Length = max(it.ship.Length, math.Round(number(b.V.Value)*100)/100)
		}},
	{"beam", `SELECT ?item ?v WHERE { ?item wdt:P458 []; p:P2261 ?s . ?s a wikibase:BestRank; psn:P2261/wikibase:quantityAmount ?v }`,
		func(it *wikidataItem, b wikidataBinding) {
			it.ship.Beam = max(it.ship.Beam, math.Round(number(b.V.Value)*100)/100)
		}},
	{"builder", `SELECT ?item ?v WHERE { ?item wdt:P458 []; wdt:P176 ?v }`,
		func(it *wikidataItem, b wikidataBinding) { it.builder.take(b) }},
	// A registry statement with an end time is one the vessel has left.
	{"registry", `SELECT ?item ?v ?end WHERE { ?item wdt:P458 []; p:P8047 ?s . ?s a wikibase:BestRank; ps:P8047 ?v . OPTIONAL { ?s pq:P582 ?end } }`,
		func(it *wikidataItem, b wikidataBinding) { it.registry.take(b) }},
	// Official names (P1448) with an end time are the former ones.
	{"former_names", `SELECT ?item ?v ?end WHERE { ?item wdt:P458 []; p:P1448 ?s . ?s ps:P1448 ?v; pq:P582 ?end . MINUS { ?s wikibase:rank wikibase:DeprecatedRank } }`,
		func(it *wikidataItem, b wikidataBinding) {
			if name := strings.TrimSpace(b.V.Value); name != "" {
				it.formerNames = append(it.formerNames, formerName{name, b.End.Value})
			}
		}},
}

func number(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// qidOf is the number of an entity URI, 0 for anything else.
func qidOf(uri string) int {
	i := strings.LastIndex(uri, "/Q")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(uri[i+2:])
	return n
}

// validIMO checks an IMO number's check digit, so a mistyped IMO in AIS or on Wikidata does not attach
// another ship's particulars. The first six digits, weighted 7 down to 2, sum to the seventh modulo 10.
func validIMO(n uint32) bool {
	if n < 1_000_000 || n > 9_999_999 {
		return false
	}
	sum := uint32(0)
	for d, w := n/10, uint32(2); w <= 7; d, w = d/10, w+1 {
		sum += d % 10 * w
	}
	return sum%10 == n%10
}

// fetchWikidata reads every item with an IMO, keyed by IMO. An IMO on more than one item takes the lowest
// QID, usually the item made by hand rather than by the bulk import.
func fetchWikidata(ctx context.Context, endpoint string) (map[uint32]*wikidataShip, error) {
	pause := func() error {
		select {
		case <-time.After(wikidataPause):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	items := map[int]*wikidataItem{}
	for i, q := range wikidataQueries {
		if i > 0 {
			if err := pause(); err != nil {
				return nil, err
			}
		}
		rows, err := sparql(ctx, endpoint, q.sparql)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.field, err)
		}
		for _, b := range rows {
			qid := qidOf(b.Item.Value)
			it := items[qid]
			if it == nil {
				if i > 0 || qid == 0 {
					continue // an item added since the first query, or not an item
				}
				it = &wikidataItem{qid: qid}
				items[qid] = it
			}
			q.apply(it, b)
		}
	}
	byIMO := map[uint32]*wikidataItem{}
	named := map[int]bool{}
	for _, it := range items {
		for _, imo := range it.imos {
			if o := byIMO[imo]; o == nil || it.qid < o.qid {
				byIMO[imo] = it
			}
		}
		for _, r := range []wikidataRef{it.builder, it.registry} {
			if r.qid != 0 {
				named[r.qid] = true
			}
		}
	}
	refs := make([]int, 0, len(named))
	for qid := range named {
		refs = append(refs, qid)
	}
	sort.Ints(refs)
	labels, en := map[int]string{}, map[int]bool{}
	for len(refs) > 0 {
		batch := refs[:min(wikidataLabelBatch, len(refs))]
		refs = refs[len(batch):]
		ids := make([]string, len(batch))
		for i, qid := range batch {
			ids[i] = "wd:Q" + strconv.Itoa(qid)
		}
		if err := pause(); err != nil {
			return nil, err
		}
		rows, err := sparql(ctx, endpoint, fmt.Sprintf(wikidataLabelQuery, strings.Join(ids, " ")))
		if err != nil {
			return nil, fmt.Errorf("labels: %w", err)
		}
		for _, b := range rows {
			if qid := qidOf(b.V.Value); qid != 0 && !en[qid] {
				labels[qid], en[qid] = b.Label.Value, b.Label.Lang == "en"
			}
		}
	}
	ships := make(map[uint32]*wikidataShip, len(byIMO))
	for imo, it := range byIMO {
		s := it.ship
		s.ID = "Q" + strconv.Itoa(it.qid)
		s.Builder, s.Registry = labels[it.builder.qid], labels[it.registry.qid]
		sort.SliceStable(it.formerNames, func(i, j int) bool { return it.formerNames[i].end < it.formerNames[j].end })
		seen := map[string]bool{}
		for _, n := range it.formerNames {
			if !seen[n.name] {
				seen[n.name] = true
				s.FormerNames = append(s.FormerNames, n.name)
			}
		}
		ships[imo] = &s
	}
	return ships, nil
}

// sparql runs one query. A response cut short fails to decode, so a partial answer is never taken for a
// whole one.
func sparql(ctx context.Context, endpoint, query string) ([]wikidataBinding, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"query": {query}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/sparql-results+json")
	req.Header.Set("User-Agent", wikidataUserAgent)
	res, err := wikidataClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	var body struct {
		Results struct {
			Bindings []wikidataBinding `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Results.Bindings, nil
}

// wikidataFields names what wikidataCounts and wikidataCountSQL count, in their order.
var wikidataFields = [...]string{"ships", "builder", "year_built", "gross_tonnage", "deadweight", "length", "beam", "registry", "former_names"}

const wikidataCountSQL = `SELECT count(*), coalesce(sum(builder != ''), 0), coalesce(sum(year_built > 0), 0),
	coalesce(sum(gross_tonnage > 0), 0), coalesce(sum(deadweight > 0), 0), coalesce(sum(length > 0), 0),
	coalesce(sum(beam > 0), 0), coalesce(sum(registry != ''), 0), coalesce(sum(former_names != ''), 0) FROM wikidata`

// wikidataCounts is the ships in a sync and how many have each field.
func wikidataCounts(ships map[uint32]*wikidataShip) (n [len(wikidataFields)]int) {
	for _, w := range ships {
		for i, set := range [...]bool{true, w.Builder != "", w.YearBuilt > 0, w.GrossTonnage > 0, w.Deadweight > 0,
			w.Length > 0, w.Beam > 0, w.Registry != "", len(w.FormerNames) > 0} {
			if set {
				n[i]++
			}
		}
	}
	return n
}

// replaceWikidata swaps the stored particulars for ships in one transaction, so a reader sees one sync or
// the other. A sync is refused when it has fewer than half the ships already stored, or any field set on
// fewer than half as many: Wikidata does not lose half of anything in a week, so a query was cut short, and
// each field comes from its own query.
func (s *store) replaceWikidata(ships map[uint32]*wikidataShip) error {
	// Counted before the transaction, which then opens with a write: in WAL a transaction that reads first
	// fails to upgrade if the record's writer commits in between. Only the sync writes this table.
	var have [len(wikidataFields)]int
	dst := make([]any, len(have))
	for i := range have {
		dst[i] = &have[i]
	}
	if err := s.db.QueryRow(wikidataCountSQL).Scan(dst...); err != nil {
		return err
	}
	if len(ships) == 0 {
		return errors.New("found no ships; keeping the stored set")
	}
	for i, got := range wikidataCounts(ships) {
		if 2*got < have[i] {
			return fmt.Errorf("found %s on %d ships where %d are stored; keeping the stored set", wikidataFields[i], got, have[i])
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM wikidata`); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO wikidata (imo, qid, builder, year_built, gross_tonnage, deadweight, length, beam, registry, former_names)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for imo, w := range ships {
		names := ""
		if len(w.FormerNames) > 0 {
			b, _ := json.Marshal(w.FormerNames)
			names = string(b)
		}
		if _, err := st.Exec(imo, w.ID, w.Builder, w.YearBuilt, w.GrossTonnage, w.Deadweight, w.Length, w.Beam, w.Registry, names); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// wikidataShips is the stored particulars for each IMO that has them.
func (s *store) wikidataShips(imos []uint32) (map[uint32]*wikidataShip, error) {
	out := map[uint32]*wikidataShip{}
	if len(imos) == 0 {
		return out, nil
	}
	args := make([]any, len(imos))
	for i, n := range imos {
		args[i] = n
	}
	rows, err := s.db.Query(`SELECT imo, qid, builder, year_built, gross_tonnage, deadweight, length, beam, registry, former_names
		FROM wikidata WHERE imo IN (?`+strings.Repeat(",?", len(imos)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var imo uint32
		var names string
		w := &wikidataShip{License: wikidataLicense}
		if err := rows.Scan(&imo, &w.ID, &w.Builder, &w.YearBuilt, &w.GrossTonnage, &w.Deadweight, &w.Length, &w.Beam, &w.Registry, &names); err != nil {
			return nil, err
		}
		w.URL = "https://www.wikidata.org/wiki/" + w.ID
		if names != "" {
			json.Unmarshal([]byte(names), &w.FormerNames)
		}
		out[imo] = w
	}
	return out, rows.Err()
}

// wikidataOf is the particulars for each IMO with a valid check digit. A failed read costs the particulars,
// never the answer they ride on.
func (p *Pipeline) wikidataOf(imos ...uint32) map[uint32]*wikidataShip {
	if p.store == nil {
		return nil
	}
	var valid []uint32
	for _, n := range imos {
		if validIMO(n) {
			valid = append(valid, n)
		}
	}
	m, err := p.store.wikidataShips(valid)
	if err != nil {
		log.Printf("wikidata: %v", err)
	}
	return m
}

// runWikidata syncs the particulars once a week, checking hourly, so a failed sync is retried within the
// hour and a restart does not sync again.
func (p *Pipeline) runWikidata(endpoint string) {
	time.Sleep(time.Minute) // let the boot flush seed the record first
	p.syncWikidataIfDue(time.Now().UTC(), endpoint)
	for range time.Tick(time.Hour) {
		p.syncWikidataIfDue(time.Now().UTC(), endpoint)
	}
}

// syncWikidataIfDue replaces the particulars when the last sync is a week old or there has never been one,
// and reports whether it did.
func (p *Pipeline) syncWikidataIfDue(now time.Time, endpoint string) bool {
	last, err := p.store.meta("wikidata_sync")
	if err != nil {
		log.Printf("wikidata: %v", err)
		return false
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < wikidataEvery {
		p.wikidata.lastSuccess.Store(t.Unix())
		if p.wikidata.ships.Load() == 0 {
			var n int64
			p.store.db.QueryRow(`SELECT count(*) FROM wikidata`).Scan(&n)
			p.wikidata.ships.Store(n)
		}
		return false
	}
	p.wikidata.runs.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ships, err := fetchWikidata(ctx, endpoint)
	if err == nil {
		err = p.store.replaceWikidata(ships)
	}
	if err != nil {
		p.wikidata.failures.Add(1)
		log.Printf("wikidata: %v", err)
		return false
	}
	if err := p.store.setMeta("wikidata_sync", now.Format(time.RFC3339)); err != nil {
		log.Printf("wikidata: %v", err)
	}
	p.wikidata.ships.Store(int64(len(ships)))
	p.wikidata.lastSuccess.Store(now.Unix())
	log.Printf("wikidata: stored particulars for %d IMO numbers", len(ships))
	return true
}
