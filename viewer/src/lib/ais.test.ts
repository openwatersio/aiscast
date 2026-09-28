import { describe, expect, it } from "vitest";
import {
  mergeTrack,
  parseDestination,
  parseEta,
  parsePlace,
  parseVesselParam,
  shipClass,
  vesselPath,
  vesselSlug,
} from "./ais";

describe("vesselSlug", () => {
  it("lowercases and hyphenates", () => {
    expect(vesselSlug("GLOVIS CAPTAIN")).toBe("glovis-captain");
  });

  it("strips diacritics rather than dropping the letters", () => {
    expect(vesselSlug("CÔTE D'IVOIRE")).toBe("cote-d-ivoire");
    expect(vesselSlug("MÅLØY")).toBe("maloy");
  });

  // NFD leaves these alone because they are letters, not accented bases, so without
  // transliteration they vanish and the slug becomes unreadable.
  it("transliterates Nordic letters instead of dropping them", () => {
    expect(vesselSlug("Ærø Færgen")).toBe("aero-faergen");
    expect(vesselSlug("BJØRN")).toBe("bjorn");
    expect(vesselSlug("Þór")).toBe("thor");
  });

  it("collapses runs and trims edges, so no slug ends in a hyphen", () => {
    expect(vesselSlug("  M/V  ***SEA*** BREEZE  ")).toBe("m-v-sea-breeze");
  });

  it("is empty for a nameless or unusable name, which yields a bare MMSI URL", () => {
    expect(vesselSlug(undefined)).toBe("");
    expect(vesselSlug("")).toBe("");
    expect(vesselSlug("***")).toBe("");
  });

  it("caps length without leaving a trailing hyphen", () => {
    const slug = vesselSlug("A".repeat(40) + " " + "B".repeat(40));
    expect(slug.length).toBeLessThanOrEqual(60);
    expect(slug.endsWith("-")).toBe(false);
  });
});

describe("vesselPath", () => {
  it("appends the slug when the vessel has a name", () => {
    expect(vesselPath(440468000, "GLOVIS CAPTAIN")).toBe("/vessels/440468000-glovis-captain");
  });

  it("omits the separator entirely when there is no name", () => {
    expect(vesselPath(440468000)).toBe("/vessels/440468000");
    expect(vesselPath(440468000, "***")).toBe("/vessels/440468000");
  });
});

describe("parseVesselParam", () => {
  it("reads a bare MMSI", () => {
    expect(parseVesselParam("440468000")).toEqual({ mmsi: 440468000, slug: "" });
  });

  it("reads an MMSI with a slug", () => {
    expect(parseVesselParam("440468000-glovis-captain")).toEqual({
      mmsi: 440468000,
      slug: "glovis-captain",
    });
  });

  it("keeps a wrong slug so the route can redirect to the canonical one", () => {
    expect(parseVesselParam("440468000-old-name")?.slug).toBe("old-name");
  });

  it("rejects anything that is not an MMSI", () => {
    expect(parseVesselParam("")).toBeUndefined();
    expect(parseVesselParam("abc")).toBeUndefined();
    expect(parseVesselParam("-glovis")).toBeUndefined();
    expect(parseVesselParam("1234567890")).toBeUndefined();
    expect(parseVesselParam("0")).toBeUndefined();
  });

  // The pair has to round-trip, or every vessel URL redirects forever.
  it("round-trips with vesselPath", () => {
    for (const name of ["GLOVIS CAPTAIN", "Ærø Færgen", undefined, "***"]) {
      const path = vesselPath(440468000, name);
      const parsed = parseVesselParam(path.replace("/vessels/", ""));
      expect(parsed?.mmsi).toBe(440468000);
      expect(parsed?.slug).toBe(vesselSlug(name));
    }
  });
});

describe("shipClass", () => {
  it("prefers the AIS kind over the ship type", () => {
    expect(shipClass("aton", 70)).toBe("aton");
    expect(shipClass("base", undefined)).toBe("base");
    expect(shipClass("sar", 0)).toBe("sar");
  });

  it("maps ITU type ranges", () => {
    expect(shipClass("vessel", 30)).toBe("fishing");
    expect(shipClass("vessel", 36)).toBe("pleasure");
    expect(shipClass("vessel", 52)).toBe("special");
    expect(shipClass("vessel", 60)).toBe("passenger");
    expect(shipClass("vessel", 70)).toBe("cargo");
    expect(shipClass("vessel", 89)).toBe("tanker");
  });

  it("falls back to other for unknown or absent types", () => {
    expect(shipClass("vessel", undefined)).toBe("other");
    expect(shipClass("vessel", 0)).toBe("other");
    expect(shipClass("vessel", 99)).toBe("other");
  });
});

describe("parseEta", () => {
  const now = new Date("2026-09-21T12:00:00Z");

  it("reads the transmitted form", () => {
    expect(parseEta("09-25 16:00", now)?.toISOString()).toBe("2026-09-25T16:00:00.000Z");
  });

  it("defaults a missing time to midnight", () => {
    expect(parseEta("09-25", now)?.toISOString()).toBe("2026-09-25T00:00:00.000Z");
  });

  // The whole reason the year is inferred rather than assumed to be the current one.
  it("rolls into next year when that is nearer", () => {
    const newYear = new Date("2026-12-28T12:00:00Z");
    expect(parseEta("01-03 08:00", newYear)?.toISOString()).toBe("2027-01-03T08:00:00.000Z");
  });

  it("rolls back a year when that is nearer", () => {
    const january = new Date("2026-01-03T12:00:00Z");
    expect(parseEta("12-28 08:00", january)?.toISOString()).toBe("2025-12-28T08:00:00.000Z");
  });

  it("rejects the sentinel and impossible dates senders transmit", () => {
    for (const bad of ["00-00 24:60", "", undefined, "13-01 00:00", "02-30 00:00", "garbage"]) {
      expect(parseEta(bad, now)).toBeUndefined();
    }
  });
});

describe("parsePlace", () => {
  it("decodes the country half of a UN/LOCODE", () => {
    expect(parsePlace("USCHS")).toMatchObject({ country: "United States", code: "CHS" });
    expect(parsePlace("NL RTM")).toMatchObject({ country: "Netherlands", code: "RTM" });
  });

  // US inland towing units emit a country and a facility code joined by a caret.
  it("decodes the country half of a caret facility code", () => {
    expect(parsePlace("US^0XG5")).toMatchObject({ country: "United States", code: "0XG5" });
    expect(parsePlace("US^0NP9")).toMatchObject({ code: "0NP9" });
  });

  it("keeps free text as sent", () => {
    expect(parsePlace("FOURCHON")).toEqual({ raw: "FOURCHON" });
    expect(parsePlace("CH 16")).toEqual({ raw: "CH 16" });
  });

  it("treats a placeholder as nothing reported", () => {
    for (const p of ["XX XXX", "?? ???", "-", "  ", "", undefined, "...."]) {
      expect(parsePlace(p)).toBeUndefined();
    }
  });
});

describe("parseDestination", () => {
  it("splits an origin and a destination on the separator", () => {
    expect(parseDestination("USSAV>USORF")).toEqual({
      from: { raw: "USSAV", flag: "🇺🇸", country: "United States", code: "SAV" },
      to: { raw: "USORF", flag: "🇺🇸", country: "United States", code: "ORF" },
    });
  });

  it("tolerates spaces around the separator", () => {
    const v = parseDestination("US ORF > MY PKG");
    expect(v?.from).toMatchObject({ code: "ORF", country: "United States" });
    expect(v?.to).toMatchObject({ code: "PKG", country: "Malaysia" });
  });

  it("drops a placeholder half and keeps the real one", () => {
    expect(parseDestination("XX XXX>US^0G7C")?.from).toBeUndefined();
    expect(parseDestination("XX XXX>US^0G7C")?.to).toMatchObject({ code: "0G7C" });
    expect(parseDestination("US^0GR0>?? ???")?.to).toBeUndefined();
    expect(parseDestination("US^0GR0>?? ???")?.from).toMatchObject({ code: "0GR0" });
  });

  it("is nothing when both halves are placeholders", () => {
    expect(parseDestination("XX XXX>?? ???")).toBeUndefined();
    expect(parseDestination("")).toBeUndefined();
  });

  it("reads a lone destination with no separator", () => {
    expect(parseDestination("NANTUCKET ROUTE")).toEqual({ to: { raw: "NANTUCKET ROUTE" } });
    expect(parseDestination("USNYC")?.to).toMatchObject({ code: "NYC" });
  });
});

describe("mergeTrack", () => {
  const history: Array<[number, number]> = [
    [-76.1, 37.1],
    [-76.2, 37.0],
  ];
  const end = Date.parse("2026-09-28T18:00:00Z");

  it("drops live positions the history already covers", () => {
    const live: Array<[number, number, number]> = [
      [-76.15, 37.05, end - 600_000],
      [-76.18, 37.02, end - 60_000],
    ];
    expect(mergeTrack(history, end, live)).toEqual(history);
  });

  it("extends the history with newer positions only", () => {
    const live: Array<[number, number, number]> = [
      [-76.15, 37.05, end - 60_000],
      [-76.3, 36.9, end + 60_000],
    ];
    expect(mergeTrack(history, end, live)).toEqual([...history, [-76.3, 36.9]]);
  });

  it("uses every live position when there is no history", () => {
    const live: Array<[number, number, number]> = [[-76.15, 37.05, 1]];
    expect(mergeTrack([], 0, live)).toEqual([[-76.15, 37.05]]);
  });

  it("never reorders the history it was given", () => {
    expect(mergeTrack(history, end, [])).toEqual(history);
  });
});
