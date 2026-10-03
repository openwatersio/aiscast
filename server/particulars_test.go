package main

import (
	"reflect"
	"testing"
)

func TestMergeParticulars(t *testing.T) {
	if m, prov, src := mergeParticulars(nil, nil, nil, nil); m != nil || prov != nil || src != nil {
		t.Errorf("no sources: %v %v %v", m, prov, src)
	}

	wd := &wikidataShip{ID: "Q1", URL: "https://www.wikidata.org/wiki/Q1", ShipType: "container ship",
		Builder: "Meyer Werft", YearBuilt: 2010, GrossTonnage: 100_000, Length: 300, Beam: 40, Registry: "Malta"}
	cg := &uscgVessel{ID: 42, Name: "EXAMPLE", Identification: "1234567", Service: "Freight Ship", Status: "Active",
		YearBuilt: 2011, Length: 299.5, Beam: 39.9, Depth: 20, GrossTonnage: 99_000, NetTonnage: 50_000, TonnageMeasure: "Convention"}

	// Both sources: the flag state wins the shared registered facts, Wikidata keeps its own.
	m, prov, src := mergeParticulars(wd, cg, nil, nil)
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
	m, prov, _ = mergeParticulars(wd, &reg, nil, nil)
	if m.GrossTonnage != 100_000 || prov["gross_tonnage"] != "wikidata" || m.TonnageMeasure != "Regulatory" {
		t.Errorf("regulatory tonnage: %+v %v", m, prov)
	}
	// With no Wikidata figure, the Regulatory one is all there is.
	bare := *wd
	bare.GrossTonnage = 0
	m, prov, _ = mergeParticulars(&bare, &reg, nil, nil)
	if m.GrossTonnage != 99_000 || prov["gross_tonnage"] != "uscg" {
		t.Errorf("regulatory fallback: %+v %v", m, prov)
	}

	// One source alone serves what it has; a PSIX match is itself the registry fact.
	m, prov, src = mergeParticulars(nil, cg, nil, nil)
	if m.Registry != "United States" || prov["registry"] != "uscg" || m.ShipType != "" || src["wikidata"].Credit != "" {
		t.Errorf("uscg alone: %+v %v %v", m, prov, src)
	}
	if m, _, src = mergeParticulars(wd, nil, nil, nil); m.Registry != "Malta" || m.Depth != 0 || src["uscg"].Credit != "" {
		t.Errorf("wikidata alone: %+v %v", m, src)
	}
	// The Norwegian register serves its facts and outranks Wikidata for them; Wikidata fills the rest.
	fdv := &fdirVessel{ID: "2026127741", CallSign: "3YPL", Name: "H \u00d8STERVOLD", Registration: "VL0148AV",
		YearBuilt: 2020, Length: 80, Beam: 16, GrossTonnage: 3439, Owner: "H \u00d8STERVOLD AS"}
	m, prov, src = mergeParticulars(wd, nil, fdv, nil)
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
	if m, _, _ = mergeParticulars(nil, nil, fdv, nil); m.Registry != "Norway" {
		t.Errorf("fiskeridir alone: %+v", m)
	}
}
