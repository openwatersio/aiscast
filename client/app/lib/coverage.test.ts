import { describe, expect, it } from "vitest";
import { COVERAGE_COLORS, COVERAGE_OPACITY, COVERAGE_STEPS, coverageColor, coverageDay, coverageOpacity, coverageSummary } from "./coverage";

describe("coverageOpacity", () => {
  it("draws a one-day window at full strength, since its cells were heard on every day there is", () => {
    expect(coverageOpacity(1)).toBe(COVERAGE_OPACITY.max);
    expect(coverageOpacity(0)).toBe(COVERAGE_OPACITY.max);
  });

  it("fades from one day heard to the whole window", () => {
    expect(coverageOpacity(7)).toEqual(["interpolate", ["linear"], ["get", "days"], 1, COVERAGE_OPACITY.min, 7, COVERAGE_OPACITY.max]);
  });
});

describe("coverageSummary", () => {
  it("rounds vessels a day and says how often the cell was heard", () => {
    expect(coverageSummary(12.4, 7, 7)).toEqual(["About 12 vessels a day", "Heard on all of the last 7 days"]);
    expect(coverageSummary(1.2, 3, 7)).toEqual(["About 1 vessel a day", "Heard on 3 of the last 7 days"]);
    expect(coverageSummary(0.3, 1, 6)).toEqual(["Fewer than one vessel a day", "Heard on 1 of the last 6 days"]);
    expect(coverageSummary(1234, 7, 7)[0]).toBe("About 1,234 vessels a day");
  });
});

describe("coverageDay", () => {
  it("reads a day in UTC", () => {
    expect(coverageDay("2026-09-29")).toBe("Sep 29");
  });
});

describe("coverageColor", () => {
  it("colors a cell by the first step whose bound it is at or under", () => {
    const [light] = [COVERAGE_COLORS.light];
    expect(coverageColor("light")).toEqual([
      "case",
      ["<=", ["get", "vessels"], 1],
      light[0],
      ["<=", ["get", "vessels"], 10],
      light[1],
      ["<=", ["get", "vessels"], 100],
      light[2],
      light[3],
    ]);
  });
});

it("has a color for every key step in both themes", () => {
  expect(COVERAGE_COLORS.light).toHaveLength(COVERAGE_STEPS.length);
  expect(COVERAGE_COLORS.dark).toHaveLength(COVERAGE_STEPS.length);
});
