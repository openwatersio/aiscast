import { describe, expect, it } from "vitest";
import { earlier, later, rangeBounds, rangeLabel, timeTicks, utcMoment } from "./trackRange";

const now = Date.UTC(2026, 8, 30, 14, 20);
const utc = (d: number, h = 0) => Date.UTC(2026, 8, d, h);

describe("earlier and later", () => {
  it("steps from the last day to yesterday, then a day at a time", () => {
    const yesterday = earlier({ span: 24, end: null }, now);
    expect(yesterday).toEqual({ span: 24, end: utc(30) });
    expect(rangeBounds(yesterday, now)).toEqual({ from: utc(29), to: utc(30) });
    expect(earlier(yesterday, now)).toEqual({ span: 24, end: utc(29) });
  });
  it("snaps shorter pages to their own boundary and longer ones to midnight", () => {
    expect(earlier({ span: 6, end: null }, now)).toEqual({ span: 6, end: utc(30, 12) });
    expect(earlier({ span: 168, end: null }, now)).toEqual({ span: 168, end: utc(24) });
  });
  it("comes back to the present once a page would reach it", () => {
    expect(later({ span: 24, end: utc(29) }, now)).toEqual({ span: 24, end: utc(30) });
    expect(later({ span: 24, end: utc(30) }, now)).toEqual({ span: 24, end: null });
    expect(later({ span: 24, end: null }, now)).toEqual({ span: 24, end: null });
  });
});

describe("rangeLabel", () => {
  it("names a range ending now by its length", () => {
    expect(rangeLabel({ span: 48, end: null }, now)).toBe("Last 48 hours");
    expect(rangeLabel({ span: 168, end: null }, now)).toBe("Last 7 days");
  });
  it("names a past page by its UTC period", () => {
    expect(rangeLabel({ span: 24, end: utc(30) }, now)).toBe("Sep 29");
    expect(rangeLabel({ span: 168, end: utc(24) }, now)).toMatch(/^Sep 17\s–\s23$/);
    expect(rangeLabel({ span: 168, end: utc(3) }, now)).toMatch(/^Aug 27\s–\sSep 2$/);
    expect(rangeLabel({ span: 6, end: utc(30) }, now)).toBe("Sep 29, 18:00–24:00");
    expect(rangeLabel({ span: 24, end: Date.UTC(2025, 11, 31) }, now)).toBe("Dec 30, 2025");
  });
});

describe("timeTicks", () => {
  it("labels a day with times", () => {
    expect(timeTicks(utc(29), utc(30), now).map((t) => t.label)).toEqual(["00:00", "08:00", "16:00", "00:00"]);
  });
  it("labels longer spans with dates at midnight", () => {
    expect(timeTicks(utc(23, 14), utc(30, 14), now).map((t) => t.label)).toEqual(["Sep 24", "Sep 26", "Sep 28", "Sep 30"]);
  });
});

it("dates a moment when asked", () => {
  expect(utcMoment(utc(29, 7), false, now)).toBe("07:00");
  expect(utcMoment(utc(29, 7), true, now)).toBe("Sep 29 07:00");
});

describe("calendar ranges", () => {
  it("names the last 30 days and 12 months, and past months and years by their names", () => {
    expect(rangeLabel({ span: "month", end: null }, now)).toBe("Last 30 days");
    expect(rangeLabel({ span: "year", end: null }, now)).toBe("Last 12 months");
    expect(rangeLabel({ span: "month", end: utc(1) }, now)).toBe("August 2026");
    expect(rangeLabel({ span: "year", end: Date.UTC(2026, 0, 1) }, now)).toBe("2025");
  });
  it("steps back to the last whole month or year before the range, then one at a time", () => {
    const august = earlier({ span: "month", end: null }, now);
    expect(august).toEqual({ span: "month", end: utc(1) });
    expect(rangeBounds(august, now)).toEqual({ from: Date.UTC(2026, 7, 1), to: utc(1) });
    expect(earlier(august, now)).toEqual({ span: "month", end: Date.UTC(2026, 7, 1) });
    expect(earlier({ span: "year", end: null }, now)).toEqual({ span: "year", end: Date.UTC(2026, 0, 1) });
    expect(earlier({ span: "year", end: Date.UTC(2026, 0, 1) }, now)).toEqual({ span: "year", end: Date.UTC(2025, 0, 1) });
  });
  it("comes forward a month at a time and back to the present", () => {
    expect(later({ span: "month", end: Date.UTC(2026, 7, 1) }, now)).toEqual({ span: "month", end: utc(1) });
    expect(later({ span: "month", end: utc(1) }, now)).toEqual({ span: "month", end: null });
    expect(later({ span: "year", end: Date.UTC(2026, 0, 1) }, now)).toEqual({ span: "year", end: null });
  });
  it("reaches back a calendar year for the last 12 months", () => {
    expect(rangeBounds({ span: "year", end: null }, now)).toEqual({ from: Date.UTC(2025, 8, 30, 14, 20), to: now });
  });
  it("labels a year's chart by month, with the year where it turns", () => {
    expect(timeTicks(Date.UTC(2025, 0, 1), Date.UTC(2026, 0, 1), now).map((t) => t.label)).toEqual(["2025", "Apr", "Jul", "Oct", "2026"]);
    expect(timeTicks(Date.UTC(2025, 8, 30, 14), now, now).map((t) => t.label)).toEqual(["Oct 2025", "2026", "Apr", "Jul"]);
  });
});
