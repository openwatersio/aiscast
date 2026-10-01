import { describe, expect, it } from "vitest";
import { earlier, later, rangeBounds, rangeLabel, timeTicks, utcMoment } from "./trackRange";

const now = Date.UTC(2026, 8, 30, 14, 20);
const utc = (d: number, h = 0) => Date.UTC(2026, 8, d, h);

describe("earlier and later", () => {
  it("steps from the last day to yesterday, then a day at a time", () => {
    const yesterday = earlier({ hours: 24, end: null }, now);
    expect(yesterday).toEqual({ hours: 24, end: utc(30) });
    expect(rangeBounds(yesterday, now)).toEqual({ from: utc(29), to: utc(30) });
    expect(earlier(yesterday, now)).toEqual({ hours: 24, end: utc(29) });
  });
  it("snaps shorter pages to their own boundary and longer ones to midnight", () => {
    expect(earlier({ hours: 6, end: null }, now)).toEqual({ hours: 6, end: utc(30, 12) });
    expect(earlier({ hours: 168, end: null }, now)).toEqual({ hours: 168, end: utc(24) });
  });
  it("comes back to the present once a page would reach it", () => {
    expect(later({ hours: 24, end: utc(29) }, now)).toEqual({ hours: 24, end: utc(30) });
    expect(later({ hours: 24, end: utc(30) }, now)).toEqual({ hours: 24, end: null });
    expect(later({ hours: 24, end: null }, now)).toEqual({ hours: 24, end: null });
  });
});

describe("rangeLabel", () => {
  it("names a range ending now by its length", () => {
    expect(rangeLabel({ hours: 48, end: null }, now)).toBe("Last 48 hours");
    expect(rangeLabel({ hours: 168, end: null }, now)).toBe("Last 7 days");
  });
  it("names a past page by its UTC period", () => {
    expect(rangeLabel({ hours: 24, end: utc(30) }, now)).toBe("Sep 29");
    expect(rangeLabel({ hours: 168, end: utc(24) }, now)).toMatch(/^Sep 17\s–\s23$/);
    expect(rangeLabel({ hours: 168, end: utc(3) }, now)).toMatch(/^Aug 27\s–\sSep 2$/);
    expect(rangeLabel({ hours: 6, end: utc(30) }, now)).toBe("Sep 29, 18:00–24:00");
    expect(rangeLabel({ hours: 24, end: Date.UTC(2025, 11, 31) }, now)).toBe("Dec 30, 2025");
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
