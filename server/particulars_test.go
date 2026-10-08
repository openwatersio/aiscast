package main

import (
	"reflect"
	"testing"
)

func TestMergeParticulars(t *testing.T) {
	if m, prov, src := mergeParticulars(enrichment{}); m != nil || prov != nil || src != nil {
		t.Errorf("no sources: %v %v %v", m, prov, src)
	}

	wd := &wikidataShip{ID: "Q1", URL: "https://www.wikidata.org/wiki/Q1", ShipType: "container ship",
		Builder: "Meyer Werft", YearBuilt: 2010, GrossTonnage: 100_000, Length: 300, Beam: 40, Registry: "Malta"}
	cg := &uscgVessel{ID: 42, Name: "EXAMPLE", Identification: "1234567", Service: "Freight Ship", Status: "Active",
		YearBuilt: 2011, Length: 299.5, Beam: 39.9, Depth: 20, GrossTonnage: 99_000, NetTonnage: 50_000, TonnageMeasure: "Convention"}

	// Both sources: the flag state wins the shared registered facts, Wikidata keeps its own.
	m, prov, src := mergeParticulars(enrichment{wd: wd, cg: cg})
	want := &particulars{RegisteredName: "EXAMPLE", Identification: "1234567", Service: "Freight Ship", Status: "Active",
		ShipType: "container ship", Builder: "Meyer Werft",
		YearBuilt: 2011, Length: 299.5, Beam: 39.9, Depth: 20,
		GrossTonnage: 99_000, NetTonnage: 50_000, TonnageMeasure: "Convention", Registry: "United States"}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("merged: %+v, want %+v", m, want)
	}
	if prov["year_built"] != "uscg" || prov["builder"] != "wikidata" || prov["registry"] != "uscg" || prov["gross_tonnage"] != "uscg" {
		t.Errorf("provenance: %v", prov)
	}
	if src["wikidata"].URL != wd.URL || src["uscg"].URL != "https://cgmix.uscg.mil/PSIX/PSIXSearch.aspx" ||
		src["uscg"].License != psixLicense {
		t.Errorf("sources: %+v", src)
	}

	// A Regulatory tonnage yields to Wikidata's Convention figure and tonnage_measure stays, naming the
	// net tonnage's measure.
	reg := *cg
	reg.TonnageMeasure = "Regulatory"
	m, prov, _ = mergeParticulars(enrichment{wd: wd, cg: &reg})
	if m.GrossTonnage != 100_000 || prov["gross_tonnage"] != "wikidata" || m.TonnageMeasure != "Regulatory" {
		t.Errorf("regulatory tonnage: %+v %v", m, prov)
	}
	// With no Wikidata figure, the Regulatory one is all there is.
	bare := *wd
	bare.GrossTonnage = 0
	m, prov, _ = mergeParticulars(enrichment{wd: &bare, cg: &reg})
	if m.GrossTonnage != 99_000 || prov["gross_tonnage"] != "uscg" {
		t.Errorf("regulatory fallback: %+v %v", m, prov)
	}

	// One source alone serves what it has; a PSIX match is itself the registry fact.
	m, prov, src = mergeParticulars(enrichment{cg: cg})
	if m.Registry != "United States" || prov["registry"] != "uscg" || m.ShipType != "" || src["wikidata"].Credit != "" {
		t.Errorf("uscg alone: %+v %v %v", m, prov, src)
	}
	if m, _, src = mergeParticulars(enrichment{wd: wd}); m.Registry != "Malta" || m.Depth != 0 || src["uscg"].Credit != "" {
		t.Errorf("wikidata alone: %+v %v", m, src)
	}
	// The Norwegian register serves its facts and outranks Wikidata for them; Wikidata fills the rest.
	fdv := &fdirVessel{ID: "2026127741", CallSign: "3YPL", Name: "H \u00d8STERVOLD", Registration: "VL0148AV",
		YearBuilt: 2020, Length: 80, Beam: 16, GrossTonnage: 3439, Owner: "H \u00d8STERVOLD AS"}
	m, prov, src = mergeParticulars(enrichment{wd: wd, fd: fdv})
	if m.YearBuilt != 2020 || m.Length != 80 || m.GrossTonnage != 3439 || m.Owner != "H \u00d8STERVOLD AS" ||
		m.Identification != "VL0148AV" || m.ShipType != "container ship" || m.Registry != "Norway" {
		t.Errorf("fiskeridir with wikidata: %+v", m)
	}
	if prov["year_built"] != "fiskeridir" || prov["ship_type"] != "wikidata" {
		t.Errorf("fiskeridir provenance: %v", prov)
	}
	if src["fiskeridir"].Credit != "Norwegian Directorate of Fisheries" || src["fiskeridir"].License != "NLOD-2.0" {
		t.Errorf("fiskeridir source: %+v", src["fiskeridir"])
	}
	if m, _, _ = mergeParticulars(enrichment{fd: fdv}); m.Registry != "Norway" {
		t.Errorf("fiskeridir alone: %+v", m)
	}
	// The Canadian register serves its facts at the flag-state rank; Wikidata fills the rest.
	tcv := &tcVessel{IMO: 5260423, Official: "313730", Name: "OCEAN MASTER", Service: "FISHING",
		YearBuilt: 1960, GrossTonnage: 501, Length: 36.48, Beam: 9.48, Depth: 4.2, HomePort: "VANCOUVER"}
	m, prov, src = mergeParticulars(enrichment{wd: wd, tc: tcv})
	if m.YearBuilt != 1960 || m.Length != 36.48 || m.GrossTonnage != 501 || m.HomePort != "VANCOUVER" ||
		m.Identification != "313730" || m.Registry != "Canada" || m.ShipType != "container ship" {
		t.Errorf("tc with wikidata: %+v", m)
	}
	if prov["year_built"] != "tc" || prov["home_port"] != "tc" || prov["ship_type"] != "wikidata" {
		t.Errorf("tc provenance: %v", prov)
	}
	if src["tc"].Credit != "Transport Canada vessel registry" || src["tc"].License != "OGL-Canada-2.0" {
		t.Errorf("tc source: %+v", src["tc"])
	}
	// The Australian list serves its facts at the flag-state rank; Wikidata's ship type and the tonnage
	// and beam the list lacks fill the rest, and the list's type serves only without Wikidata's.
	amv := &amsaVessel{IMO: 9869447, Official: "862874", Name: "ABSOLUTE", ShipType: "Oil Tanker", Status: "Registered",
		YearBuilt: 2019, Length: 114.99, HomePort: "Fremantle"}
	m, prov, src = mergeParticulars(enrichment{wd: wd, am: amv})
	if m.ShipType != "container ship" || m.Status != "Registered" || m.YearBuilt != 2019 || m.Length != 114.99 ||
		m.Registry != "Australia" || m.GrossTonnage != wd.GrossTonnage || m.Beam != wd.Beam {
		t.Errorf("amsa with wikidata: %+v", m)
	}
	if prov["ship_type"] != "wikidata" || prov["registry"] != "amsa" || prov["gross_tonnage"] != "wikidata" {
		t.Errorf("amsa provenance: %v", prov)
	}
	if m, prov, _ = mergeParticulars(enrichment{am: amv}); m.ShipType != "Oil Tanker" || prov["ship_type"] != "amsa" {
		t.Errorf("amsa alone: %+v %v", m, prov)
	}
	if src["amsa"].Credit != "© Australian Maritime Safety Authority" || src["amsa"].License != "CC-BY-4.0" {
		t.Errorf("amsa source: %+v", src["amsa"])
	}
	// The call sign on a Wikidata item serves unless AIS reports a different one, and the flag state's
	// outranks it.
	signed := *wd
	signed.CallSign = "9HXC9"
	for _, c := range []struct{ ais, want string }{{"9HXC9", "9HXC9"}, {"9hxc9 ", "9HXC9"}, {"V7A3493", ""}, {"", "9HXC9"}, {"@@@@@@@", "9HXC9"}, {"0", "9HXC9"}} {
		m, prov, _ = mergeParticulars(enrichment{wd: &signed, callsign: c.ais})
		if m.CallSign != c.want || (c.want != "") != (prov["callsign"] == "wikidata") {
			t.Errorf("wikidata call sign with %q on AIS: %q from %q", c.ais, m.CallSign, prov["callsign"])
		}
	}
	for source, e := range map[string]enrichment{
		"ised":       {is: &isedShip{MMSI: 316001234, Name: "OCEAN MASTER", CallSign: "CFA1234"}},
		"uscg":       {cg: &uscgVessel{ID: 42, Name: "EXAMPLE", callsign: "CFA1234"}},
		"fcc":        {fc: &fccShip{MMSI: 366000001, USI: 1, CallSign: "CFA1234"}},
		"fiskeridir": {fd: &fdirVessel{ID: "1", Name: "EXAMPLE", CallSign: "CFA1234"}},
	} {
		e.wd, e.callsign = &signed, "9HXC9"
		if m, prov, _ = mergeParticulars(e); m.CallSign != "CFA1234" || prov["callsign"] != source {
			t.Errorf("%s with wikidata call sign: %q from %q", source, m.CallSign, prov["callsign"])
		}
	}
}
