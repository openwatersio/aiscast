import type { Theme } from "./theme";

/**
 * The steps of vessels a day a coverage cell is colored by, each up to and including its `max`,
 * and how the key names them. A cell heard on some days of the window but averaging under one
 * vessel a day falls in the first; water nobody heard has no cell at all.
 */
export const COVERAGE_STEPS = [
  { max: 1, label: "1", spoken: "up to 1" },
  { max: 10, label: "10", spoken: "up to 10" },
  { max: 100, label: "100", spoken: "up to 100" },
  { max: Infinity, label: ">100", spoken: "more than 100" },
] as const;

/**
 * One hue per theme, in step order. Both start from the blue that clears either basemap's
 * water, then run darker over the light map and lighter over the dark one, so more vessels
 * always stands further from the water.
 */
export const COVERAGE_COLORS: Record<Theme, readonly string[]> = {
  light: ["#3987e5", "#256abf", "#184f95", "#0d366b"],
  dark: ["#3987e5", "#6da7ec", "#9ec5f4", "#cde2fb"],
};

/**
 * Fill opacity for a cell heard on one day of the window, and for one heard on every day. Even the
 * strongest lets the coastline and place names show through, and the legend draws at that strength.
 */
export const COVERAGE_OPACITY = { min: 0.2, max: 0.55 } as const;

/**
 * The fill-opacity expression for a window of this many days: faintest for a cell heard on one
 * day, strongest for one heard on all of them. A one-day window has nothing to fade between, and every
 * cell in it was heard on every day there is.
 */
export function coverageOpacity(windowDays: number): number | unknown[] {
  if (windowDays <= 1) return COVERAGE_OPACITY.max;
  return ["interpolate", ["linear"], ["get", "days"], 1, COVERAGE_OPACITY.min, windowDays, COVERAGE_OPACITY.max];
}

/** The fill-color expression: each step's color for a cell at or under its `max`. */
export function coverageColor(theme: Theme): unknown[] {
  const colors = COVERAGE_COLORS[theme];
  const bounded = COVERAGE_STEPS.slice(0, -1).flatMap((s, i) => [["<=", ["get", "vessels"], s.max], colors[i]]);
  return ["case", ...bounded, colors[COVERAGE_STEPS.length - 1]];
}

/** What the hovercard says about a cell. */
export function coverageSummary(vessels: number, days: number, windowDays: number): [string, string] {
  const n = Math.round(vessels);
  const count =
    vessels < 1 ? "Fewer than one vessel a day" : `About ${n.toLocaleString("en-US")} ${n === 1 ? "vessel" : "vessels"} a day`;
  const heard = days >= windowDays ? `Heard on all of the last ${windowDays} days` : `Heard on ${days} of the last ${windowDays} days`;
  return [count, heard];
}

/** A day of the window as the key shows it: "Sep 29". */
export function coverageDay(day: string): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
}
