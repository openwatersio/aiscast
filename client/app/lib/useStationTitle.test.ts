import { describe, expect, it } from "vitest";
import { resolveStation } from "./useStationTitle";

describe("resolveStation", () => {
  const titles = new Map([
    ["station:mmsi:368168720", "CERULEAN"],
    ["station:a/b", "Pier"],
    ["station:a", "Other"],
  ]);
  it("names a listed station as it is, even with a slash in its subject", () => {
    expect(resolveStation("station:a/b", titles)).toEqual({ id: "station:a/b", title: "Pier" });
  });
  it("names a volunteer's tagged id as its receiver when only the receiver is listed", () => {
    expect(resolveStation("station:mmsi:368168720/n2k", titles)).toEqual({ id: "station:mmsi:368168720", title: "CERULEAN" });
  });
  it("leaves an id it cannot place, or any id before the list arrives, as itself", () => {
    expect(resolveStation("aisstream", titles)).toEqual({ id: "aisstream", title: "aisstream" });
    expect(resolveStation("station:mmsi:368168720/n2k", undefined)).toEqual({ id: "station:mmsi:368168720/n2k", title: "station:mmsi:368168720/n2k" });
  });
});
