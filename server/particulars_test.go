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
	if src["wikidata"].URL != wd.URL || src["uscg"].URL != "https://cgmix.uscg.mil/PSIX/PSIXDetails.aspx?VesselID=42" ||
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
}
