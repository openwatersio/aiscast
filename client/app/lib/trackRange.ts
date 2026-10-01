const HOUR = 3600e3;
const DAY = 24 * HOUR;

/** A range's length: hours, or a calendar month or year, whose past pages are whole months or years. */
export type TrackSpan = number | "month" | "year";

/** The stretch of a vessel's history being shown: `span` long, ending at `end`, or now when `end` is null. */
export interface TrackRange {
  span: TrackSpan;
  end: number | null;
}

/** The lengths offered. The server answers at most 366 days per request. */
export const TRACK_RANGES: TrackSpan[] = [6, 12, 24, 48, 168, "month", "year"];

/** The start of the UTC month or year `n` after the one holding t. */
function calendar(t: number, span: "month" | "year", n = 0): number {
  const d = new Date(t);
  return span === "month" ? Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + n, 1) : Date.UTC(d.getUTCFullYear() + n, 0, 1);
}

export function rangeBounds(r: TrackRange, now = Date.now()): { from: number; to: number } {
  const to = r.end ?? now;
  if (typeof r.span === "number") return { from: to - r.span * HOUR, to };
  if (r.end != null) return { from: calendar(to, r.span, -1), to };
  if (r.span === "month") return { from: to - 30 * DAY, to };
  const d = new Date(to);
  d.setUTCFullYear(d.getUTCFullYear() - 1);
  return { from: d.getTime(), to };
}

// Past pages end on a UTC boundary of their own length, or of a day for longer ones, so a page
// is a whole day or a whole quarter of one rather than whatever moment the reader started from.
const unit = (hours: number) => Math.min(hours, 24) * HOUR;

/** The page before this one. From a range ending now, that is the last whole period before its start. */
export function earlier(r: TrackRange, now = Date.now()): TrackRange {
  const { from } = rangeBounds(r, now);
  if (typeof r.span === "number") {
    const u = unit(r.span);
    return { span: r.span, end: Math.ceil(from / u) * u };
  }
  const start = calendar(from, r.span);
  return { span: r.span, end: start < from ? calendar(from, r.span, 1) : start };
}

/** The page after this one, back to the range ending now once it would reach the present. */
export function later(r: TrackRange, now = Date.now()): TrackRange {
  if (r.end == null) return r;
  const end = typeof r.span === "number" ? r.end + r.span * HOUR : calendar(r.end, r.span, 1);
  return { span: r.span, end: end >= now ? null : end };
}

// Built once: the chart formats every tick and the readout on each pointer move.
const DAY_FORMAT = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", timeZone: "UTC" });
const DAY_YEAR_FORMAT = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const dayFormat = (withYear: boolean) => (withYear ? DAY_YEAR_FORMAT : DAY_FORMAT);
const MONTH_FORMAT = new Intl.DateTimeFormat("en", { month: "short", timeZone: "UTC" });
const MONTH_YEAR_FORMAT = new Intl.DateTimeFormat("en", { month: "long", year: "numeric", timeZone: "UTC" });

const hhmm = (t: number) => new Date(t).toISOString().slice(11, 16);

/** "Sep 29", or with the year when it is not this one. */
export function utcDay(t: number, now = Date.now()): string {
  return dayFormat(new Date(t).getUTCFullYear() !== new Date(now).getUTCFullYear()).format(t);
}

/** A moment on a track: the time, with the date when the range spans more than one recent day. */
export function utcMoment(t: number, withDate: boolean, now = Date.now()): string {
  return withDate ? `${utcDay(t, now)} ${hhmm(t)}` : hhmm(t);
}

/**
 * "Last 24 hours", "Last 7 days", "Last 12 months", or the UTC period of a past page: "Sep 29",
 * "Sep 23 – 29", "Sep 29, 18:00–24:00", "September 2026", "2025".
 */
export function rangeLabel(r: TrackRange, now = Date.now()): string {
  if (r.end == null) {
    if (r.span === "month") return "Last 30 days";
    if (r.span === "year") return "Last 12 months";
    return r.span > 48 && r.span % 24 === 0 ? `Last ${r.span / 24} days` : `Last ${r.span} hours`;
  }
  const { from, to } = rangeBounds(r, now);
  if (r.span === "month") return MONTH_YEAR_FORMAT.format(from);
  if (r.span === "year") return String(new Date(from).getUTCFullYear());
  if (r.span < 24) return `${utcDay(from, now)}, ${hhmm(from)}–${to % DAY === 0 ? "24:00" : hhmm(to)}`;
  if (r.span === 24) return utcDay(from, now);
  const withYear = new Date(from).getUTCFullYear() !== new Date(now).getUTCFullYear();
  return dayFormat(withYear).formatRange(from, to - 1);
}

/**
 * Where to label the chart's time axis. Up to a day and a half, four evenly spaced times;
 * longer, UTC midnights labelled with the date, since a time alone no longer says which day;
 * past three months, the starts of months, with the year on January's and on the first.
 */
export function timeTicks(from: number, to: number, now = Date.now()): Array<{ t: number; label: string }> {
  const span = to - from;
  const ticks: Array<{ t: number; label: string }> = [];
  if (span <= 36 * HOUR) {
    for (const i of [0, 1, 2, 3]) {
      const t = from + (span * i) / 3;
      ticks.push({ t, label: hhmm(t) });
    }
    return ticks;
  }
  if (span <= 92 * DAY) {
    const step = Math.ceil(span / DAY / 4) * DAY;
    for (let t = Math.ceil(from / DAY) * DAY; t <= to; t += step) ticks.push({ t, label: utcDay(t, now) });
    return ticks;
  }
  const first = calendar(from, "month") < from ? calendar(from, "month", 1) : calendar(from, "month");
  const step = Math.ceil(Math.round(span / (30.44 * DAY)) / 4);
  for (let t = first; t <= to; t = calendar(t, "month", step)) {
    const d = new Date(t);
    const label = d.getUTCMonth() === 0 ? String(d.getUTCFullYear()) : ticks.length === 0 ? `${MONTH_FORMAT.format(t)} ${d.getUTCFullYear()}` : MONTH_FORMAT.format(t);
    ticks.push({ t, label });
  }
  return ticks;
}
