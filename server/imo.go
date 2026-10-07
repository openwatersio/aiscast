package main

// Lookup by IMO number. Everything else is keyed by MMSI, so an IMO resolves to the MMSIs whose static data
// reported it, through the record mirror, or the cache without a store.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxMMSIsPerIMO caps the vessels one IMO answers with. Placeholder numbers such as 1234567 and 9999999 are
// reported by several unrelated vessels at once.
const maxMMSIsPerIMO = 10

// imoActiveWindow is how recently a vessel must have been heard to count when an IMO has to name one vessel.
// A ship that changed flag keeps its IMO under a new MMSI, and its old MMSI goes quiet.
const imoActiveWindow = 30 * 24 * time.Hour

const limitsURL = "https://github.com/openwatersio/aiscast/blob/main/docs/limits.md"

// errIMOTier refuses lookup by IMO to the tiers that do not get the raw feed. Every tier still sees imo in the
// vessels it gets back.
var errIMOTier = errors.New("looking vessels up by IMO number needs a feeder or partner token; see " + limitsURL)

func imoGate(cl *Claims) error {
	if cl.mayRaw() {
		return nil
	}
	return errIMOTier
}

const imoRange = "imo must be a number from 1 to 9999999"

// parseIMO reads one IMO number. The check digit is not required: the API serves IMOs as vessels broadcast
// them, so a client must be able to look up any number it was given. 0 is "not available" on the wire.
func parseIMO(s string) (uint32, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return uint32(n), err == nil && n >= 1 && n <= 9_999_999
}

// parseIMOs reads imo=<imo>,<imo>,...
func parseIMOs(q string) ([]uint32, string) {
	var out []uint32
	for _, f := range strings.Split(q, ",") {
		n, ok := parseIMO(f)
		if !ok {
			return nil, imoRange
		}
		out = append(out, n)
	}
	return out, ""
}

// checkIMOs is parseIMO's range for a list already decoded from JSON.
func checkIMOs(imos []uint32) string {
	for _, n := range imos {
		if n < 1 || n > 9_999_999 {
			return imoRange
		}
	}
	return ""
}

// resolveIMOs is the vessels reporting each IMO, at most maxMMSIsPerIMO each, most recently heard first. An
// IMO no vessel reports is absent. cut reports that the cap left vessels out.
func (p *Pipeline) resolveIMOs(imos []uint32) (_ map[uint32][]record, cut bool, _ error) {
	out := map[uint32][]record{}
	if len(imos) == 0 {
		return out, false, nil
	}
	var recs []record
	if p.store != nil {
		var err error
		if recs, err = p.store.find(recordQuery{imos: imos}); err != nil {
			return nil, false, err
		}
	} else {
		want := map[uint32]bool{}
		for _, n := range imos {
			want[n] = true
		}
		p.vmu.RLock()
		for mmsi, v := range p.vessels {
			if v.IMO != 0 && want[v.IMO] {
				recs = append(recs, record{mmsi: mmsi, v: v.state()})
			}
		}
		p.vmu.RUnlock()
		slices.SortFunc(recs, func(a, b record) int {
			if c := b.v.Seen.Compare(a.v.Seen); c != 0 {
				return c
			}
			return cmp.Compare(a.mmsi, b.mmsi)
		})
	}
	for _, r := range recs {
		if len(out[r.v.IMO]) < maxMMSIsPerIMO {
			out[r.v.IMO] = append(out[r.v.IMO], r)
		} else {
			cut = true
		}
	}
	return out, cut, nil
}

// soleVessel is the one vessel an IMO names: its only MMSI, or the only one heard within imoActiveWindow. recs
// is resolveIMOs' list for the IMO.
func soleVessel(recs []record, now time.Time) (record, bool) {
	if len(recs) == 1 {
		return recs[0], true
	}
	var active []record
	for _, r := range recs {
		if now.Sub(r.v.Seen) <= imoActiveWindow {
			active = append(active, r)
		}
	}
	if len(active) == 1 {
		return active[0], true
	}
	return record{}, false
}

// followIMOs adds the vessels reporting imos to s, and keeps the IMOs so the stream can follow a vessel that
// first reports one after the subscription is made.
func (p *Pipeline) followIMOs(s *v1Sub, imos []uint32) error {
	if len(imos) == 0 {
		return nil
	}
	byIMO, cut, err := p.resolveIMOs(imos)
	if err != nil {
		return err
	}
	s.cut = cut
	if s.mmsi == nil {
		s.mmsi = map[uint32]bool{}
	}
	s.imo = map[uint32]int{}
	for _, n := range imos {
		s.imo[n] = len(byIMO[n])
		for _, r := range byIMO[n] {
			s.mmsi[r.mmsi] = true
		}
	}
	return nil
}

var imoQueryPattern = regexp.MustCompile(`(?i)^IMO\s*(\d{1,7})$`)

// imoQuery reads a search for an IMO number: "IMO 9241061" or "IMO9241061", any case, is explicit, and a bare
// seven-digit number may be one. Text that is no IMO, such as IMO 0, stays a name search.
func imoQuery(text string) (imo uint32, explicit bool) {
	if m := imoQueryPattern.FindStringSubmatch(text); m != nil {
		if n, ok := parseIMO(m[1]); ok {
			return n, true
		}
		return 0, false
	}
	if len(text) == 7 && strings.Trim(text, "0123456789") == "" {
		if n, ok := parseIMO(text); ok {
			return n, false
		}
	}
	return 0, false
}

// parseVesselPath reads the {mmsi} segment of /v1/vessels/{mmsi}: an MMSI, or IMO<n> for the vessel an IMO
// names. MMSIs are all digits, so the two cannot collide.
func parseVesselPath(seg string) (mmsi, imo uint32, msg string) {
	if len(seg) > 3 && strings.EqualFold(seg[:3], "IMO") {
		n, ok := parseIMO(seg[3:])
		if !ok {
			return 0, 0, imoRange
		}
		return 0, n, ""
	}
	n, err := strconv.ParseUint(seg, 10, 32)
	if err != nil {
		return 0, 0, "mmsi must be a number, or IMO followed by an IMO number"
	}
	return uint32(n), 0, ""
}

// vesselPath resolves the {mmsi} segment of a single-vessel request to one MMSI, answering the request itself
// when it cannot: 400, 403, 404 for an IMO no vessel reports, and 300 with the candidates for an IMO that
// names no one vessel. suffix is the rest of the path after the segment. location is the request's address by
// MMSI when it named an IMO, for the handler to send with a successful answer (setContentLocation).
func (p *Pipeline) vesselPath(w http.ResponseWriter, r *http.Request, cl *Claims, suffix string) (mmsi uint32, location string, ok bool) {
	mmsi, imo, msg := parseVesselPath(r.PathValue("mmsi"))
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return 0, "", false
	}
	if imo == 0 {
		return mmsi, "", true
	}
	if err := imoGate(cl); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return 0, "", false
	}
	byIMO, cut, err := p.resolveIMOs([]uint32{imo})
	if err != nil {
		log.Printf("store: %v", err)
		http.Error(w, "vessel record unavailable", http.StatusInternalServerError)
		return 0, "", false
	}
	recs := byIMO[imo]
	if len(recs) == 0 {
		unknownVessel(w)
		return 0, "", false
	}
	// The links keep the request's parameters, so a candidate's track covers the range asked for, but never the
	// token, which a client following them sends as it sent this one.
	vals := r.URL.Query()
	vals.Del("key")
	query := ""
	if len(vals) > 0 {
		query = "?" + vals.Encode()
	}
	href := func(mmsi uint32) string { return fmt.Sprintf("/v1/vessels/%d%s%s", mmsi, suffix, query) }
	if rec, ok := soleVessel(recs, time.Now()); ok {
		return rec.mmsi, href(rec.mmsi), true
	}
	features := make([][]byte, 0, len(recs))
	attribution := map[string]string{}
	p.vmu.RLock()
	for _, rec := range recs {
		v, _ := p.newestState(rec)
		f := v.feature(rec.mmsi)
		c := struct {
			Geometry   *pointGeometry `json:"geometry"`
			Href       string         `json:"href"`
			ID         uint32         `json:"id"`
			Properties vesselProps    `json:"properties"`
			Type       string         `json:"type"`
		}{Href: href(rec.mmsi), ID: f.ID, Properties: f.Properties, Type: f.Type}
		if v.HasPos {
			c.Geometry = &f.Geometry
		}
		b, _ := json.Marshal(c)
		features = append(features, b)
		noteAttribution(attribution, v.Source)
	}
	p.vmu.RUnlock()
	w.Header().Set("Content-Type", "application/geo+json")
	w.WriteHeader(http.StatusMultipleChoices)
	w.Write(append(featureCollection(features, attribution, cut), '\n'))
	return 0, "", false
}

// setContentLocation names the answer's address by MMSI on a request made by IMO. Not a redirect, which would
// cost every client a round trip. Exposed, since CORS hides it from scripts otherwise.
func setContentLocation(w http.ResponseWriter, location string) {
	if location != "" {
		w.Header().Set("Content-Location", location)
		w.Header().Set("Access-Control-Expose-Headers", "Content-Location")
	}
}

func unknownVessel(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound) // not http.Error: that would override the JSON Content-Type
	json.NewEncoder(w).Encode(map[string]string{"error": "unknown vessel"})
}
