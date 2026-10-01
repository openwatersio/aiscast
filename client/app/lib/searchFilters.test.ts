import { describe, expect, it } from "vitest";
import { areaParams, filterParams, filterTest, heardSince, NO_FILTERS } from "./searchFilters";

// A Wednesday afternoon, local time.
const now = new Date(2026, 8, 30, 15, 20, 30);

describe("heardSince", () => {
  it("anchors each window to the local calendar", () => {
    expect(heardSince("any", now)).toBeUndefined();
    expect(heardSince("today", now)).toEqual(new Date(2026, 8, 30));
    expect(heardSince("yesterday", now)).toEqual(new Date(2026, 8, 29));
    expect(heardSince("week", now, 1)).toEqual(new Date(2026, 8, 28));
    expect(heardSince("week", now, 0)).toEqual(new Date(2026, 8, 27));
    expect(heardSince("month", now)).toEqual(new Date(2026, 8, 1));
    expect(heardSince("year", now)).toEqual(new Date(2026, 0, 1));
  });
  it("starts a week today when today is its first day", () => {
    expect(heardSince("week", new Date(2026, 8, 28, 9), 1)).toEqual(new Date(2026, 8, 28));
  });
});

describe("filterParams", () => {
  it("sends nothing without filters", () => {
    expect(filterParams(NO_FILTERS, now)).toBe("");
  });
  it("sends the type's codes and the age", () => {
    const params = new URLSearchParams(filterParams({ ...NO_FILTERS, type: "Sailing & pleasure", heard: "today" }, now));
    expect(params.get("type")).toBe("36,37");
    // 15:20:30 since midnight, to the minute.
    expect(params.get("max_age")).toBe(String((15 * 60 + 21) * 60));
  });
  it("orders from the middle of the map and keeps to its boxes", () => {
    const view = { center: [38.97, -76.49] as [number, number], boxes: [[38, 179, 39, 180], [38, -180, 39, -179]] as Array<[number, number, number, number]> };
    expect(filterParams(NO_FILTERS, now, 1, view)).toBe("around=38.97%2C-76.49");
    const params = new URLSearchParams(filterParams({ ...NO_FILTERS, sort: "recent", inView: true }, now, 1, view));
    expect(params.has("around")).toBe(false);
    expect(params.getAll("bbox")).toEqual(["38,179,39,180", "38,-180,39,-179"]);
  });
  it("sends no order or box without a map", () => {
    expect(filterParams({ ...NO_FILTERS, inView: true }, now)).toBe("");
  });
});

describe("areaParams", () => {
  const view = { center: [0, 0] as [number, number], boxes: [[38, -77, 39, -76]] as Array<[number, number, number, number]> };
  it("always sends the view, and no order", () => {
    expect(areaParams(NO_FILTERS, now, 1, view)).toBe("bbox=38%2C-77%2C39%2C-76");
  });
  it("holds vessels last heard under way to the Heard window too", () => {
    const params = new URLSearchParams(areaParams({ ...NO_FILTERS, heard: "today" }, now, 1, view));
    expect(params.get("max_age")).toBe(params.get("max_age_moving"));
  });
});

describe("filterTest", () => {
  const seen = now.getTime();
  it("matches type ranges and the heard window", () => {
    const test = filterTest({ ...NO_FILTERS, type: "Tugs & pilots", heard: "today" }, now);
    expect(test({ shipType: 52, seen })).toBe(true);
    expect(test({ shipType: 31, seen })).toBe(true);
    expect(test({ shipType: 80, seen })).toBe(false);
    expect(test({ shipType: 50, seen: new Date(2026, 8, 29, 23).getTime() })).toBe(false);
    expect(test({ seen })).toBe(false);
  });
  it("keeps to the view's boxes", () => {
    const view = { center: [0, 0] as [number, number], boxes: [[38, -77, 39, -76]] as Array<[number, number, number, number]> };
    const test = filterTest({ ...NO_FILTERS, inView: true }, now, 1, view);
    expect(test({ seen, lat: 38.5, lon: -76.5 })).toBe(true);
    expect(test({ seen, lat: 40, lon: -76.5 })).toBe(false);
    expect(test({ seen })).toBe(false);
  });
  it("passes everything without filters", () => {
    expect(filterTest(NO_FILTERS, now)({ seen: 0 })).toBe(true);
  });
});
