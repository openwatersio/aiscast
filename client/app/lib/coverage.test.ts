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
    const cell = (vessels: number, days: number) => ({ vessels, days, stations: 1 });
    expect(coverageSummary("vessels", cell(12.4, 7), 7)).toEqual(["About 12 vessels a day", "Heard on all of the last 7 days"]);
    expect(coverageSummary("vessels", cell(1.2, 3), 7)).toEqual(["About 1 vessel a day", "Heard on 3 of the last 7 days"]);
    expect(coverageSummary("vessels", cell(0.3, 1), 6)).toEqual(["Fewer than one vessel a day", "Heard on 1 of the last 6 days"]);
    expect(coverageSummary("vessels", cell(1234, 7), 7)[0]).toBe("About 1,234 vessels a day");
  });

  it("leads with the stations that heard the cell when the map counts them", () => {
    expect(coverageSummary("stations", { vessels: 40, days: 7, stations: 2 }, 7)).toEqual([
      "Heard by 2 stations",
      "About 40 vessels a day",
      "Heard on all of the last 7 days",
    ]);
    expect(coverageSummary("stations", { vessels: 3, days: 2, stations: 1 }, 7)[0]).toBe("Heard by 1 station");
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
    expect(coverageColor("light", "vessels")).toEqual([
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

  it("colors by stations on the stations map, with 4 or more in the last step", () => {
    const dark = COVERAGE_COLORS.dark;
    expect(coverageColor("dark", "stations")).toEqual([
      "case",
      ["<=", ["get", "stations"], 1],
      dark[0],
      ["<=", ["get", "stations"], 2],
      dark[1],
      ["<=", ["get", "stations"], 3],
      dark[2],
      dark[3],
    ]);
  });
});

it("has a color for every key step of every measure in both themes", () => {
  for (const steps of Object.values(COVERAGE_STEPS)) {
    expect(COVERAGE_COLORS.light).toHaveLength(steps.length);
    expect(COVERAGE_COLORS.dark).toHaveLength(steps.length);
  }
});
