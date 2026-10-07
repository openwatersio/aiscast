package main

// The enrichment sources merged into one document. Each synced source keeps its own table and its own
// shape; this file folds them into a single vocabulary served on /v1/vessels/{mmsi} and the MCP
// get_vessels tool, so a client never reads per-source keys. provenance names the source of each field it
// serves, and sources carries the credit, license, and the source's page for the vessel, or its public
// search when it keeps no per-vessel pages. A new source
// adds fields to the vocabulary and a rank to the merge here, and no response key is named after it.

import "strconv"

// sourceRef is one source's entry in the sources map: how to credit it and where its record is.
type sourceRef struct {
	Credit  string `json:"credit" jsonschema:"display credit for the source"`
	License string `json:"license"`
	URL     string `json:"url,omitempty" jsonschema:"the source's page for this vessel, or its public search when the source keeps no per-vessel pages"`
}

// particulars is the merged document: one vocabulary, metres and tonnes, no source names.
type particulars struct {
	RegisteredName string   `json:"registered_name,omitempty" jsonschema:"name as documented with the flag state, when it differs from what AIS reports"`
	Identification string   `json:"identification,omitempty" jsonschema:"the flag state's number for the vessel: a documented vessel's official number, else its state registration"`
	Service        string   `json:"service,omitempty" jsonschema:"flag-state service type, e.g. Towing Vessel, Passenger (Inspected), Recreational"`
	CallSign       string   `json:"callsign,omitempty" jsonschema:"call sign as registered; the callsign in the vessel's own properties is the one AIS reports"`
	Status         string   `json:"status,omitempty" jsonschema:"flag-state status, e.g. Active, Laid Up"`
	ShipType       string   `json:"ship_type,omitempty" jsonschema:"kind of ship, e.g. bulk carrier, container ship, ferry"`
	Builder        string   `json:"builder,omitempty" jsonschema:"shipyard or builder"`
	YardNumber     string   `json:"yard_number,omitempty" jsonschema:"the builder's hull number"`
	YearBuilt      int      `json:"year_built,omitempty"`
	GrossTonnage   int      `json:"gross_tonnage,omitempty"`
	NetTonnage     int      `json:"net_tonnage,omitempty"`
	TonnageMeasure string   `json:"tonnage_measure,omitempty" jsonschema:"how a tonnage from the flag state was measured: Convention (the international system), Regulatory (the older US system, in register tons), or Simplified (small vessels)"`
	Deadweight     int      `json:"deadweight,omitempty" jsonschema:"deadweight, tonnes"`
	Length         float64  `json:"length,omitempty" jsonschema:"length as registered, metres; may differ from the AIS length"`
	Beam           float64  `json:"beam,omitempty" jsonschema:"beam as registered, metres"`
	Depth          float64  `json:"depth,omitempty" jsonschema:"registered depth, metres"`
	Draught        float64  `json:"draught,omitempty" jsonschema:"design draught, metres; AIS reports the draught on the current voyage"`
	Registry       string   `json:"registry,omitempty" jsonschema:"country of registry, in English"`
	HomePort       string   `json:"home_port,omitempty" jsonschema:"port of registry"`
	Owner          string   `json:"owner,omitempty"`
	Operator       string   `json:"operator,omitempty"`
	FormerNames    []string `json:"former_names,omitempty" jsonschema:"names the vessel has carried before, oldest first"`
	Wikipedia      string   `json:"wikipedia,omitempty" jsonschema:"the ship's English Wikipedia article"`
	CommonsCat     string   `json:"commons_category,omitempty" jsonschema:"the category of the ship's photos on Wikimedia Commons"`
	Image          string   `json:"image,omitempty" jsonschema:"the Commons page of a photo of the ship, which shows its license and credit"`
}

// enrichment is one vessel's rows from every synced source, handed to the merge together. A new source
// is one more field here rather than another parameter everywhere.
type enrichment struct {
	wd *wikidataShip
	cg *uscgVessel
	fd *fdirVessel
	fc *fccShip
	tc *tcVessel
	am *amsaVessel
	is *isedShip

	callsign string // the call sign the vessel's AIS reports
}

// mergeParticulars folds the sources into the served document. Per field, deterministically: a flag
// state outranks Wikidata for registered facts, an empty value never wins, and provenance records the
// winner by the field's JSON name. The flag states never meet: a vessel flies one flag at a time.
// An FCC license ranks below PSIX, the vessel registry proper, and above Wikidata for what it documents.
// AMSA's free-text ship type is the one flag-state field that yields to Wikidata's.
func mergeParticulars(e enrichment) (*particulars, map[string]string, map[string]sourceRef) {
	wd, cg, fd, fc, tcv, am, is := e.wd, e.cg, e.fd, e.fc, e.tc, e.am, e.is
	if wd == nil && cg == nil && fd == nil && fc == nil && tcv == nil && am == nil && is == nil {
		return nil, nil, nil
	}
	m := &particulars{}
	prov := map[string]string{}
	str := func(field, source, v string, dst *string) {
		if v != "" && *dst == "" {
			*dst, prov[field] = v, source
		}
	}
	num := func(field, source string, v, dst *int) {
		if *v > 0 && *dst == 0 {
			*dst, prov[field] = *v, source
		}
	}
	flt := func(field, source string, v, dst *float64) {
		if *v > 0 && *dst == 0 {
			*dst, prov[field] = *v, source
		}
	}
	if cg != nil {
		str("registered_name", "uscg", cg.Name, &m.RegisteredName)
		str("identification", "uscg", cg.Identification, &m.Identification)
		str("callsign", "uscg", cg.callsign, &m.CallSign)
		str("service", "uscg", cg.Service, &m.Service)
		str("status", "uscg", cg.Status, &m.Status)
		num("year_built", "uscg", &cg.YearBuilt, &m.YearBuilt)
		flt("length", "uscg", &cg.Length, &m.Length)
		flt("beam", "uscg", &cg.Beam, &m.Beam)
		flt("depth", "uscg", &cg.Depth, &m.Depth)
		num("net_tonnage", "uscg", &cg.NetTonnage, &m.NetTonnage)
		// Convention tonnage is the figure readers expect, so it wins; a Regulatory or Simplified figure
		// yields to Wikidata's and serves only when it is all there is, named by tonnage_measure.
		if cg.GrossTonnage > 0 && (cg.TonnageMeasure == "Convention" || wd == nil || wd.GrossTonnage == 0) {
			m.GrossTonnage, prov["gross_tonnage"] = cg.GrossTonnage, "uscg"
		}
		if prov["gross_tonnage"] == "uscg" || prov["net_tonnage"] == "uscg" {
			str("tonnage_measure", "uscg", cg.TonnageMeasure, &m.TonnageMeasure)
		}
		// The match itself is the registry fact: PSIX documents US-flag vessels, so a stale country on
		// the Wikidata item never wins over it.
		str("registry", "uscg", "United States", &m.Registry)
	}
	if fd != nil {
		str("registered_name", "fiskeridir", fd.Name, &m.RegisteredName)
		str("identification", "fiskeridir", fd.Registration, &m.Identification)
		str("callsign", "fiskeridir", fd.CallSign, &m.CallSign)
		num("year_built", "fiskeridir", &fd.YearBuilt, &m.YearBuilt)
		flt("length", "fiskeridir", &fd.Length, &m.Length)
		flt("beam", "fiskeridir", &fd.Beam, &m.Beam)
		// The register's London Convention figure; its 1947 measures were dropped at sync.
		num("gross_tonnage", "fiskeridir", &fd.GrossTonnage, &m.GrossTonnage)
		str("owner", "fiskeridir", fd.Owner, &m.Owner)
		// The match itself is the registry fact, as a PSIX match is.
		str("registry", "fiskeridir", "Norway", &m.Registry)
	}
	if fc != nil {
		str("registered_name", "fcc", fc.Name, &m.RegisteredName)
		str("identification", "fcc", fc.Official, &m.Identification)
		str("callsign", "fcc", fc.CallSign, &m.CallSign)
		// A ship station license is a US license, so the match is itself the registry fact.
		str("registry", "fcc", "United States", &m.Registry)
	}
	if tcv != nil {
		str("registered_name", "tc", tcv.Name, &m.RegisteredName)
		str("identification", "tc", tcv.Official, &m.Identification)
		str("service", "tc", tcv.Service, &m.Service)
		num("year_built", "tc", &tcv.YearBuilt, &m.YearBuilt)
		num("gross_tonnage", "tc", &tcv.GrossTonnage, &m.GrossTonnage)
		num("net_tonnage", "tc", &tcv.NetTonnage, &m.NetTonnage)
		flt("length", "tc", &tcv.Length, &m.Length)
		flt("beam", "tc", &tcv.Beam, &m.Beam)
		flt("depth", "tc", &tcv.Depth, &m.Depth)
		str("home_port", "tc", tcv.HomePort, &m.HomePort)
		str("registry", "tc", "Canada", &m.Registry)
	}
	if am != nil {
		str("registered_name", "amsa", am.Name, &m.RegisteredName)
		str("identification", "amsa", am.Official, &m.Identification)
		str("status", "amsa", am.Status, &m.Status)
		num("year_built", "amsa", &am.YearBuilt, &m.YearBuilt)
		flt("length", "amsa", &am.Length, &m.Length)
		str("home_port", "amsa", am.HomePort, &m.HomePort)
		str("registry", "amsa", "Australia", &m.Registry)
	}
	if is != nil {
		// The MMSI registry's answer: thin, but it is the flag state's own name and call sign for the
		// boat, and for most Canadian small craft the only registered facts any source holds.
		str("registered_name", "ised", is.Name, &m.RegisteredName)
		str("callsign", "ised", is.CallSign, &m.CallSign)
		str("registry", "ised", "Canada", &m.Registry)
	}
	if wd != nil {
		str("ship_type", "wikidata", wd.ShipType, &m.ShipType)
		str("builder", "wikidata", wd.Builder, &m.Builder)
		str("yard_number", "wikidata", wd.YardNumber, &m.YardNumber)
		num("year_built", "wikidata", &wd.YearBuilt, &m.YearBuilt)
		num("gross_tonnage", "wikidata", &wd.GrossTonnage, &m.GrossTonnage)
		num("deadweight", "wikidata", &wd.Deadweight, &m.Deadweight)
		flt("length", "wikidata", &wd.Length, &m.Length)
		flt("beam", "wikidata", &wd.Beam, &m.Beam)
		flt("draught", "wikidata", &wd.Draught, &m.Draught)
		str("registry", "wikidata", wd.Registry, &m.Registry)
		str("home_port", "wikidata", wd.HomePort, &m.HomePort)
		// The call sign on an item is often one the ship gave up with an earlier flag: 70 of 231 heard ships
		// in an October 2026 sample. Nothing on the item tells those apart, so it serves only to confirm AIS.
		if wd.CallSign == wikidataCallSign(e.callsign) {
			str("callsign", "wikidata", wd.CallSign, &m.CallSign)
		}
		str("owner", "wikidata", wd.Owner, &m.Owner)
		str("operator", "wikidata", wd.Operator, &m.Operator)
		if len(wd.FormerNames) > 0 {
			m.FormerNames, prov["former_names"] = wd.FormerNames, "wikidata"
		}
		str("wikipedia", "wikidata", wd.Wikipedia, &m.Wikipedia)
		str("commons_category", "wikidata", wd.CommonsCategory, &m.CommonsCat)
		str("image", "wikidata", wd.Image, &m.Image)
	}
	if am != nil {
		// The list's type is free text with uneven spellings, Tug beside Tug Boat, and a use such as
		// Passenger as often as a kind of ship, so it fills ship_type only when Wikidata has none.
		str("ship_type", "amsa", am.ShipType, &m.ShipType)
	}
	sources := map[string]sourceRef{}
	if wd != nil {
		sources["wikidata"] = sourceRef{Credit: "Wikidata", License: wikidataLicense, URL: wd.URL}
	}
	if cg != nil {
		sources["uscg"] = sourceRef{Credit: "U.S. Coast Guard PSIX", License: psixLicense, URL: psixSearchPage}
	}
	if fd != nil {
		sources["fiskeridir"] = sourceRef{Credit: "Norwegian Directorate of Fisheries", License: fdirLicense}
	}
	if fc != nil {
		sources["fcc"] = sourceRef{Credit: "FCC ship station license", License: psixLicense,
			URL: "https://wireless2.fcc.gov/UlsApp/UlsSearch/license.jsp?licKey=" + strconv.FormatInt(fc.USI, 10)}
	}
	if tcv != nil {
		sources["tc"] = sourceRef{Credit: "Transport Canada vessel registry", License: tcLicense}
	}
	if am != nil {
		sources["amsa"] = sourceRef{Credit: "© Australian Maritime Safety Authority", License: amsaLicense, URL: amsaSearch(am.IMO)}
	}
	if is != nil {
		sources["ised"] = sourceRef{Credit: "ISED Canadian MMSI registry", License: isedLicense, URL: isedSearchPage}
	}
	return m, prov, sources
}
