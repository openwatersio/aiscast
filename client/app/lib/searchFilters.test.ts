import { describe, expect, it } from "vitest";
import { filterParams, filterTest, heardSince, NO_FILTERS } from "./searchFilters";

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
    const params = new URLSearchParams(filterParams({ type: "Sailing & pleasure", heard: "today" }, now));
    expect(params.get("type")).toBe("36,37");
    // 15:20:30 since midnight, to the minute.
    expect(params.get("max_age")).toBe(String((15 * 60 + 21) * 60));
  });
});

describe("filterTest", () => {
  const seen = now.getTime();
  it("matches type ranges and the heard window", () => {
    const test = filterTest({ type: "Tugs & pilots", heard: "today" }, now);
    expect(test({ shipType: 52, seen })).toBe(true);
    expect(test({ shipType: 31, seen })).toBe(true);
    expect(test({ shipType: 80, seen })).toBe(false);
    expect(test({ shipType: 50, seen: new Date(2026, 8, 29, 23).getTime() })).toBe(false);
    expect(test({ seen })).toBe(false);
  });
  it("passes everything without filters", () => {
    expect(filterTest(NO_FILTERS, now)({ seen: 0 })).toBe(true);
  });
});
