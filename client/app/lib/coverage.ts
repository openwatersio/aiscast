import type { Theme } from "./theme";

/** What a coverage map colors its cells by: vessels heard a day, or the stations that heard them. */
export type CoverageMeasure = "vessels" | "stations";

/**
 * The steps each measure's cells are colored by, each up to and including its `max`, and how
 * the key names them. A cell heard on some days of the window but averaging under one vessel a
 * day falls in the first; water nobody heard has no cell at all.
 */
export const COVERAGE_STEPS = {
  vessels: [
    { max: 1, label: "1", spoken: "up to 1" },
    { max: 10, label: "10", spoken: "up to 10" },
    { max: 100, label: "100", spoken: "up to 100" },
    { max: Infinity, label: ">100", spoken: "more than 100" },
  ],
  stations: [
    { max: 1, label: "1", spoken: "1" },
    { max: 2, label: "2", spoken: "2" },
    { max: 3, label: "3", spoken: "3" },
    { max: Infinity, label: "4+", spoken: "4 or more" },
  ],
} as const satisfies Record<CoverageMeasure, readonly { max: number; label: string; spoken: string }[]>;

/** The key's title for each measure, and how a screen reader hears it. */
export const COVERAGE_KEY = {
  vessels: { title: "Vessels / day", spoken: "vessels heard a day" },
  stations: { title: "Stations", spoken: "stations that heard each cell" },
} as const satisfies Record<CoverageMeasure, { title: string; spoken: string }>;

/**
 * A magma ramp per theme, in step order, so coverage stands apart from the app's blue and the
 * water. It runs from yellow to purple over the light map and from purple to yellow over the
 * dark one, so more always stands further from the basemap.
 */
export const COVERAGE_COLORS: Record<Theme, readonly string[]> = {
  light: ["#f6c445", "#ee7b30", "#c23a6b", "#5b1e78"],
  dark: ["#7a3596", "#cf3f6c", "#f07a30", "#f8cf55"],
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

/** The fill-color expression: each step's color for a cell at or under its `max` of the measure. */
export function coverageColor(theme: Theme, measure: CoverageMeasure): unknown[] {
  const colors = COVERAGE_COLORS[theme];
  const steps = COVERAGE_STEPS[measure];
  const bounded = steps.slice(0, -1).flatMap((s, i) => [["<=", ["get", measure], s.max], colors[i]]);
  return ["case", ...bounded, colors[steps.length - 1]];
}

/** What the hovercard says about a cell, a line each: the measure first, then how often it was heard. */
export function coverageSummary(
  measure: CoverageMeasure,
  cell: { vessels: number; days: number; stations: number },
  windowDays: number,
): string[] {
  const n = Math.round(cell.vessels);
  const count =
    cell.vessels < 1 ? "Fewer than one vessel a day" : `About ${n.toLocaleString("en-US")} ${n === 1 ? "vessel" : "vessels"} a day`;
  const heard =
    cell.days >= windowDays ? `Heard on all of the last ${windowDays} days` : `Heard on ${cell.days} of the last ${windowDays} days`;
  if (measure === "vessels") return [count, heard];
  return [`Heard by ${cell.stations} ${cell.stations === 1 ? "station" : "stations"}`, count, heard];
}

/** A day of the window as the key shows it: "Sep 29". */
export function coverageDay(day: string): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
}
