import { describe, expect, it } from "vitest";
import { ferryIdentity } from "./ferries";
import { fleetVessels, fleetsWith, getFleet } from "./fleets.server";

describe("operator ferry rosters", () => {
  it("shares the full BC Ferries roster with fleet pages", () => {
    expect(fleetVessels(getFleet("ferries/bc-ferries")!)).toHaveLength(39);
    expect(fleetsWith(316011409).map((f) => f.id)).toContain("ferries/bc-ferries");
    expect(ferryIdentity({ mmsi: 316011409, name: "COASTAL CELEBRATION", particulars: { service: "PASSENGER" } })).toMatchObject({ operator: "BC Ferries" });
  });

  it("shares the full Washington State Ferries roster with fleet pages", () => {
    expect(fleetVessels(getFleet("ferries/wsf")!)).toHaveLength(21);
    expect(fleetsWith(366749710).map((f) => f.id)).toContain("ferries/wsf");
    // WSF radios carry a fleet prefix; the roster name is accepted too, but not another vessel's name.
    expect(ferryIdentity({ mmsi: 366749710, imo: 9137351, name: "WSF WENATCHEE", particulars: { service: "PASSENGER" } })).toMatchObject({ operator: "Washington State Ferries" });
    expect(ferryIdentity({ mmsi: 366749710, name: "WENATCHEE" })).toMatchObject({ operator: "Washington State Ferries" });
    expect(ferryIdentity({ mmsi: 366749710, name: "WENATCHEE EXPRESS" })).toBeUndefined();
  });

  it("identifies existing ferries and observed AIS aliases", () => {
    expect(ferryIdentity({ mmsi: 366929710, name: "COHO" })).toMatchObject({ operator: "Black Ball Ferry Line", service: "Victoria–Port Angeles" });
    expect(ferryIdentity({ mmsi: 367720690, name: "GREY LADY IV" })).toMatchObject({ operator: "Hy-Line Cruises", service: "Hyannis–Nantucket" });
    expect(ferryIdentity({ mmsi: 316001244, name: "QQ II" })).toMatchObject({ operator: "BC Ferries" });
    expect(ferryIdentity({ mmsi: 316059023, name: "ISLAND XWSALUXUL" })).toMatchObject({ operator: "BC Ferries" });
  });

  it("allows a confirmed hull to change radios, but requires its reported name", () => {
    expect(ferryIdentity({ mmsi: 316999999, imo: 9332779, name: "COASTAL CELEBRATION" })).toMatchObject({ operator: "BC Ferries" });
    expect(ferryIdentity({ mmsi: 316999999, imo: 9332779, name: "RENAMED SHIP" })).toBeUndefined();
  });

  it("does not inherit a roster identity with conflicting IMO, absent name or a non-vessel kind", () => {
    expect(ferryIdentity({ mmsi: 316011409, imo: 5076949, name: "COASTAL CELEBRATION" })).toBeUndefined();
    expect(ferryIdentity({ mmsi: 316011409, imo: 9876543, name: "COASTAL CELEBRATION" })).toBeUndefined();
    expect(ferryIdentity({ mmsi: 316011409 })).toBeUndefined();
    expect(ferryIdentity({ mmsi: 316011409, name: "COASTAL CELEBRATION", kind: "gear" })).toBeUndefined();
    expect(ferryIdentity({ mmsi: 316999999, name: "COASTAL CELEBRATION", particulars: { operator: "BC Ferries" } })).toBeUndefined();
  });

  it("requires dated official sources and unique identities across ferry operators", () => {
    const mmsis: number[] = [], imos: number[] = [];
    for (const id of getFleet("ferries")!.children) {
      const fleet = getFleet(id)!;
      expect(fleet.reviewed).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(fleet.source?.[fleet.title]).toMatch(/^https:\/\//);
      for (const v of fleetVessels(fleet)) {
        if (v.mmsi) {
          mmsis.push(v.mmsi);
          expect(v.links?.["AIS identity"]).toBe(`https://ais.openwaters.io/v1/vessels/${v.mmsi}`);
          expect(ferryIdentity({ mmsi: v.mmsi, imo: v.imo, name: v.ais_name ?? v.name })?.operator).toBe(fleet.title);
        }
        if (v.imo) imos.push(v.imo);
      }
    }
    expect(new Set(mmsis).size).toBe(mmsis.length);
    expect(new Set(imos).size).toBe(imos.length);
  });
});
