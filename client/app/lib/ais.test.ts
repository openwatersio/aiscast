import { describe, expect, it } from "vitest";
import {
  bearing,
  isValidImo,
  isVolunteer,
  stationTitle,
  stationTitles,
  indexAt,
  interpolateAt,
  mergeTrack,
  parseDestination,
  parseEta,
  parsePlace,
  parseVesselParam,
  shipClass,
  speedAt,
  splitTrack,
  vesselPath,
  vesselDimensions,
  viewBoxes,
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

describe("splitTrack", () => {
  const coords: Array<[number, number]> = [
    [0, 0],
    [1, 1],
    [2, 2],
    [3, 3],
  ];
  const minute = 60_000;

  it("keeps a continuous track whole", () => {
    const times = [0, minute, 2 * minute, 3 * minute];
    expect(splitTrack(coords, times)).toEqual([coords]);
  });

  // The case that drew a 208 km line across 14 hours of silence.
  it("breaks where the vessel went unheard", () => {
    const times = [0, minute, 14 * 60 * minute, 14 * 60 * minute + minute];
    expect(splitTrack(coords, times)).toEqual([
      [
        [0, 0],
        [1, 1],
      ],
      [
        [2, 2],
        [3, 3],
      ],
    ]);
  });

  it("drops a segment that is a single point, since a line needs two", () => {
    const times = [0, 31 * minute, 62 * minute, 62 * minute + minute];
    expect(splitTrack(coords, times)).toEqual([
      [
        [2, 2],
        [3, 3],
      ],
    ]);
  });

  it("is empty for nothing to draw", () => {
    expect(splitTrack([], [])).toEqual([]);
    expect(splitTrack([[0, 0]], [0])).toEqual([]);
  });
});

describe("indexAt", () => {
  const times = [10, 20, 30, 40];

  it("finds the last position at or before the time", () => {
    expect(indexAt(times, 10)).toBe(0);
    expect(indexAt(times, 25)).toBe(1);
    expect(indexAt(times, 40)).toBe(3);
    expect(indexAt(times, 99)).toBe(3);
  });

  // Scrubbing into a gap should hold the last known position, not jump ahead.
  it("is -1 before the track starts", () => {
    expect(indexAt(times, 9)).toBe(-1);
    expect(indexAt([], 5)).toBe(-1);
  });
});

describe("interpolateAt", () => {
  const coords: Array<[number, number]> = [
    [0, 0],
    [10, 10],
  ];

  it("interpolates between two reports", () => {
    const r = interpolateAt(coords, [0, 100], 50);
    expect(r?.point).toEqual([5, 5]);
    expect(r?.inGap).toBe(false);
  });

  // Inside a gap the position is a guess, and the caller has to show it as one.
  it("marks a position inside a gap", () => {
    const hour = 3_600_000;
    const r = interpolateAt(coords, [0, 14 * hour], 7 * hour);
    expect(r?.point).toEqual([5, 5]);
    expect(r?.inGap).toBe(true);
  });

  it("clamps to the ends", () => {
    expect(interpolateAt(coords, [10, 20], 5)?.point).toEqual([0, 0]);
    expect(interpolateAt(coords, [10, 20], 99)?.point).toEqual([10, 10]);
    expect(interpolateAt([], [], 1)).toBeUndefined();
  });
});

describe("bearing", () => {
  it("reads the cardinal directions", () => {
    expect(Math.round(bearing([0, 0], [0, 1]))).toBe(0);
    expect(Math.round(bearing([0, 0], [1, 0]))).toBe(90);
    expect(Math.round(bearing([0, 0], [0, -1]))).toBe(180);
    expect(Math.round(bearing([0, 0], [-1, 0]))).toBe(270);
  });

  it("stays within a single turn", () => {
    for (const to of [[1, 1], [-1, -1], [179, 10], [-179, -10]] as Array<[number, number]>) {
      const b = bearing([0, 0], to);
      expect(b).toBeGreaterThanOrEqual(0);
      expect(b).toBeLessThan(360);
    }
  });
});

describe("isValidImo", () => {
  it("accepts numbers whose seventh digit is the weighted check", () => {
    expect(isValidImo(9551973)).toBe(true); // Happy Dynamic
    expect(isValidImo(9074729)).toBe(true);
  });

  it("rejects a wrong check digit and a transposition that breaks it", () => {
    expect(isValidImo(9551974)).toBe(false);
    expect(isValidImo(9559173)).toBe(false);
  });

  it("rejects anything that is not seven digits", () => {
    expect(isValidImo(undefined)).toBe(false);
    expect(isValidImo(0)).toBe(false);
    expect(isValidImo(955197)).toBe(false);
    expect(isValidImo(95519730)).toBe(false);
  });
});

describe("vesselDimensions", () => {
  const at = (toBow: number, toStern: number, toPort: number, toStarboard: number) => ({ toBow, toStern, toPort, toStarboard });

  it("draws the hull and antenna from full offsets", () => {
    expect(vesselDimensions(at(150, 50, 12, 20))).toEqual({ length: 200, beam: 32, antenna: at(150, 50, 12, 20) });
  });
  it("draws no antenna when A and C are zero, the reference point being unavailable", () => {
    expect(vesselDimensions(at(0, 200, 0, 32))).toEqual({ length: 200, beam: 32, antenna: undefined });
    expect(vesselDimensions(at(0, 200, 12, 20))).toEqual({ length: 200, beam: 32, antenna: undefined });
  });
  it("falls back to length and beam when a pair of offsets is absent", () => {
    expect(vesselDimensions(at(150, 50, 0, 0), 200, undefined)).toBeUndefined();
    expect(vesselDimensions(at(0, 0, 12, 20), 200, 32)).toEqual({ length: 200, beam: 32 });
  });
  it("falls back to length and beam without offsets", () => {
    expect(vesselDimensions(undefined, 90, 14)).toEqual({ length: 90, beam: 14 });
    expect(vesselDimensions(at(0, 0, 0, 0), 90, 14)).toEqual({ length: 90, beam: 14 });
  });
  it("draws nothing for all zeros, saturated fields, or absurd sizes", () => {
    expect(vesselDimensions(at(0, 0, 0, 0))).toBeUndefined();
    expect(vesselDimensions(at(511, 20, 10, 10), 531, 20)).toBeUndefined();
    expect(vesselDimensions(at(100, 20, 63, 10))).toBeUndefined();
    expect(vesselDimensions(undefined, 1, 1)).toBeUndefined();
    expect(vesselDimensions(undefined, 90, 0)).toBeUndefined();
  });
});

describe("speedAt", () => {
  const min = 60_000;
  // Reports at 0 and 10 minutes, then silence for an hour, then one at 70 minutes.
  const track = { times: [0, 10 * min, 70 * min], sog: [4, 8, 12] };

  it("interpolates between close reports and keeps each report's own speed", () => {
    expect(speedAt(track, 0)).toBe(4);
    expect(speedAt(track, 5 * min)).toBe(6);
    expect(speedAt(track, 10 * min)).toBe(8);
    expect(speedAt(track, 70 * min)).toBe(12);
  });
  it("has no speed inside a gap, or long after the last report", () => {
    expect(speedAt(track, 11 * min)).toBeUndefined();
    expect(speedAt(track, 69 * min)).toBeUndefined();
    expect(speedAt(track, 80 * min)).toBe(12);
    expect(speedAt(track, 101 * min)).toBeUndefined();
    expect(speedAt(track, -1)).toBeUndefined();
  });
});

describe("viewBoxes", () => {
  it("keeps a view inside the world as one box", () => {
    expect(viewBoxes(50, 2, 60, 12)).toEqual([[50, 2, 60, 12]]);
  });
  it("splits a view across the antimeridian into a box either side", () => {
    expect(viewBoxes(-20, 170, -10, 188)).toEqual([
      [-20, 170, -10, 180],
      [-20, -180, -10, -172],
    ]);
    expect(viewBoxes(-20, -190, -10, -170)).toEqual([
      [-20, 170, -10, 180],
      [-20, -180, -10, -170],
    ]);
  });
  it("takes a view wider than the world, or a wrapped copy of it, as the world or its place in it", () => {
    expect(viewBoxes(-80, -300, 80, 300)).toEqual([[-80, -180, 80, 180]]);
    expect(viewBoxes(50, 362, 60, 372)).toEqual([[50, 2, 60, 12]]);
  });
});

describe("isVolunteer", () => {
  it("counts the kinds people run", () => {
    for (const s of ["udp:24dfc99708ff", "station:ed25519:abc", "http:ed25519:abc", "v1:abc", "mmsi:367430440"]) {
      expect(isVolunteer(s)).toBe(true);
    }
  });
  it("leaves out feeds and aggregates", () => {
    for (const s of ["aishub", "aisstream", "kystverket", "barentswatch", "digitraffic"]) expect(isVolunteer(s)).toBe(false);
    expect(isVolunteer(undefined)).toBe(false);
  });
});

describe("stationTitles", () => {
  it("prefers the name, then the place, then the id", () => {
    expect(stationTitle({ station: "udp:1", name: "Pier", near: "Falmouth, MA" })).toBe("Pier");
    expect(stationTitle({ station: "udp:1", near: "Falmouth, MA" })).toBe("Near Falmouth, MA");
    expect(stationTitle({ station: "udp:1" })).toBe("udp:1");
  });
  it("tells apart stations that would read the same", () => {
    const titles = stationTitles([
      { station: "udp:aaaac34f", near: "Santa Monica, CA" },
      { station: "udp:bbbb9d1e", near: "Santa Monica, CA" },
      { station: "station:ed25519:xyz", name: "CERULEAN" },
      { station: "station:ed25519:xyz/n2k", name: "CERULEAN" },
      { station: "aishub" },
    ]);
    expect([...titles.values()]).toEqual([
      "Near Santa Monica, CA (…c34f)",
      "Near Santa Monica, CA (…9d1e)",
      "CERULEAN (…:xyz)",
      "CERULEAN (n2k)",
      "aishub",
    ]);
  });
});
