import type { BBox } from "./stream";

/** When a vessel was last heard, as a window the search keeps to. "any" sends nothing. */
export type Heard = "any" | "today" | "yesterday" | "week" | "month" | "year";

/**
 * Where a search looks: everywhere, only the map's view, or everywhere ordered from the reader's
 * own position. Anywhere and In this view are ordered from the middle of the map.
 */
export type Where = "anywhere" | "view" | "me";

/** What the search chips have chosen. */
export interface SearchFilters {
  where: Where;
  /** The chosen entry of SHIP_TYPES, by label, or "any". */
  type: string;
  heard: Heard;
}

export const NO_FILTERS: SearchFilters = { where: "anywhere", type: "any", heard: "any" };

/**
 * How results are ordered: nearest first, or most recently heard once a Heard window says time
 * is what matters.
 */
export function sortOf(f: SearchFilters): "nearest" | "recent" {
  return f.heard === "any" ? "nearest" : "recent";
}

/** The place the search is ordered from and the map it may keep to. */
export interface SearchView {
  /** [lat, lon] distances are measured from: the middle of the map, or the reader's position. */
  origin?: [number, number];
  /** The map's view as [south, west, north, east] boxes, two when it crosses the antimeridian. */
  boxes?: BBox[];
}

/** The Type menu's choices, each a set of ITU ship-type codes in the API's `type=` syntax. */
export const SHIP_TYPES: Array<{ label: string; types: string }> = [
  { label: "Cargo", types: "70-79" },
  { label: "Tanker", types: "80-89" },
  { label: "Passenger", types: "60-69" },
  { label: "Fishing", types: "30" },
  { label: "Sailing & pleasure", types: "36,37" },
  { label: "Tugs & pilots", types: "31,32,50,51,52" },
];

/** The Where menu's choices. With nothing typed the list is the map's, so Anywhere is left out. */
export const WHERE: Array<{ value: Where; label: string; chip: string }> = [
  { value: "anywhere", label: "Anywhere", chip: "Anywhere" },
  { value: "view", label: "In this view", chip: "In this view" },
  { value: "me", label: "Near me", chip: "Near me" },
];

/** The Heard menu's choices: the menu's label, and the chip's once chosen. */
export const HEARD: Array<{ value: Heard; label: string; chip: string }> = [
  { value: "any", label: "Any time", chip: "Any time" },
  { value: "today", label: "Today", chip: "Heard today" },
  { value: "yesterday", label: "Yesterday", chip: "Heard since yesterday" },
  { value: "week", label: "This week", chip: "Heard this week" },
  { value: "month", label: "This month", chip: "Heard this month" },
  { value: "year", label: "This year", chip: "Heard this year" },
];

/** Whether any chip narrows the results. Near me orders them and narrows nothing. */
export function hasFilters(f: SearchFilters): boolean {
  return f.type !== "any" || f.heard !== "any" || f.where === "view";
}

/** The day a local week starts on, 0 for Sunday, as the reader's locale has it; Monday if unknown. */
export function firstDayOfWeek(locale?: string): number {
  try {
    const l = new Intl.Locale(locale ?? navigator.language) as Intl.Locale & {
      getWeekInfo?(): { firstDay: number };
      weekInfo?: { firstDay: number };
    };
    const firstDay = (l.getWeekInfo?.() ?? l.weekInfo)?.firstDay;
    // Intl numbers the days 1 for Monday to 7 for Sunday.
    return firstDay ? firstDay % 7 : 1;
  } catch {
    return 1;
  }
}

/** The local-calendar moment a Heard window starts: midnight today, the start of the week, and so on. */
export function heardSince(heard: Heard, now: Date, weekStart = 1): Date | undefined {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  switch (heard) {
    case "any":
      return undefined;
    case "today":
      return d;
    case "yesterday":
      d.setDate(d.getDate() - 1);
      return d;
    case "week":
      d.setDate(d.getDate() - ((d.getDay() - weekStart + 7) % 7));
      return d;
    case "month":
      return new Date(now.getFullYear(), now.getMonth(), 1);
    case "year":
      return new Date(now.getFullYear(), 0, 1);
  }
}

/** Type codes as the API writes them, "70-79,36", as a test of one code. */
function typeTest(spec: string): (type: number | undefined) => boolean {
  const ranges = spec.split(",").map((part) => part.split("-").map(Number) as [number, number?]);
  return (type) => type != null && ranges.some(([lo, hi]) => type >= lo && type <= (hi ?? lo));
}

const typesOf = (f: SearchFilters) => SHIP_TYPES.find((t) => t.label === f.type)?.types;

/** The Heard window as seconds to now, rounded to the minute, and at least one. */
function heardSeconds(f: SearchFilters, now: Date, weekStart?: number): string | undefined {
  const since = heardSince(f.heard, now, weekStart);
  return since && String(Math.max(60, Math.round((now.getTime() - since.getTime()) / 60_000) * 60));
}

/** The filters as `/v1/vessels` search parameters, with no leading `&`. Without a view, there is no order by distance or area. */
export function filterParams(f: SearchFilters, now: Date, weekStart?: number, view?: SearchView): string {
  const params = new URLSearchParams();
  if (view?.origin && sortOf(f) === "nearest") params.set("around", view.origin.join(","));
  if (view?.boxes && f.where === "view") for (const b of view.boxes) params.append("bbox", b.join(","));
  const types = typesOf(f);
  if (types) params.set("type", types);
  const age = heardSeconds(f, now, weekStart);
  if (age) params.set("max_age", age);
  return params.toString();
}

/**
 * The filters as `/v1/vessels` parameters for the vessels in the view, with nothing typed.
 * An area keeps a vessel last heard under way for only 30 minutes, since it has moved on, so a
 * Heard window applies to those as well, as it does in a search.
 */
export function areaParams(f: SearchFilters, now: Date, weekStart: number | undefined, boxes: BBox[]): string {
  const params = new URLSearchParams();
  for (const b of boxes) params.append("bbox", b.join(","));
  const types = typesOf(f);
  if (types) params.set("type", types);
  const age = heardSeconds(f, now, weekStart);
  if (age) {
    params.set("max_age", age);
    params.set("max_age_moving", age);
  }
  return params.toString();
}

/**
 * The same filters as a test of one vessel, for the results shown from the stream before the
 * server answers, so that list agrees with the one that replaces it.
 */
export function filterTest(
  f: SearchFilters,
  now: Date,
  weekStart?: number,
  view?: SearchView,
): (v: { shipType?: number; seen: number; lat?: number; lon?: number }) => boolean {
  const types = typesOf(f);
  const typeOk = types ? typeTest(types) : () => true;
  const since = heardSince(f.heard, now, weekStart)?.getTime() ?? -Infinity;
  const boxes = f.where === "view" ? view?.boxes : undefined;
  const inView = (v: { lat?: number; lon?: number }) =>
    !boxes ||
    (v.lat != null && v.lon != null && boxes.some(([s, w, n, e]) => v.lat! >= s && v.lat! <= n && v.lon! >= w && v.lon! <= e));
  return (v) => typeOk(v.shipType) && v.seen >= since && inView(v);
}
