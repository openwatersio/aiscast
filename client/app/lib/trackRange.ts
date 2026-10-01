const HOUR = 3600e3;
const DAY = 24 * HOUR;

/** The stretch of a vessel's history being shown: `hours` long, ending at `end`, or now when `end` is null. */
export interface TrackRange {
  hours: number;
  end: number | null;
}

/** The lengths offered. The server answers at most 7 days per request. */
export const TRACK_RANGES = [6, 12, 24, 48, 168];

export function rangeBounds(r: TrackRange, now = Date.now()): { from: number; to: number } {
  const to = r.end ?? now;
  return { from: to - r.hours * HOUR, to };
}

// Past pages end on a UTC boundary of their own length, or of a day for longer ones, so a page
// is a whole day or a whole quarter of one rather than whatever moment the reader started from.
const unit = (hours: number) => Math.min(hours, 24) * HOUR;

/** The page before this one. From a range ending now, that is the last whole period before its start. */
export function earlier(r: TrackRange, now = Date.now()): TrackRange {
  const { from } = rangeBounds(r, now);
  const u = unit(r.hours);
  return { hours: r.hours, end: Math.ceil(from / u) * u };
}

/** The page after this one, back to the range ending now once it would reach the present. */
export function later(r: TrackRange, now = Date.now()): TrackRange {
  if (r.end == null) return r;
  const end = r.end + r.hours * HOUR;
  return { hours: r.hours, end: end >= now ? null : end };
}

// Built once: the chart formats every tick and the readout on each pointer move.
const DAY_FORMAT = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", timeZone: "UTC" });
const DAY_YEAR_FORMAT = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const dayFormat = (withYear: boolean) => (withYear ? DAY_YEAR_FORMAT : DAY_FORMAT);

const hhmm = (t: number) => new Date(t).toISOString().slice(11, 16);

/** "Sep 29", or with the year when it is not this one. */
export function utcDay(t: number, now = Date.now()): string {
  return dayFormat(new Date(t).getUTCFullYear() !== new Date(now).getUTCFullYear()).format(t);
}

/** A moment on a track: the time, with the date when the range spans more than one recent day. */
export function utcMoment(t: number, withDate: boolean, now = Date.now()): string {
  return withDate ? `${utcDay(t, now)} ${hhmm(t)}` : hhmm(t);
}

/** "Last 24 hours", "Last 7 days", or the UTC period of a past page: "Sep 29", "Sep 23 – 29", "Sep 29, 18:00–24:00". */
export function rangeLabel(r: TrackRange, now = Date.now()): string {
  if (r.end == null) return r.hours > 48 && r.hours % 24 === 0 ? `Last ${r.hours / 24} days` : `Last ${r.hours} hours`;
  const { from, to } = rangeBounds(r, now);
  if (r.hours < 24) return `${utcDay(from, now)}, ${hhmm(from)}–${to % DAY === 0 ? "24:00" : hhmm(to)}`;
  if (r.hours === 24) return utcDay(from, now);
  const withYear = new Date(from).getUTCFullYear() !== new Date(now).getUTCFullYear();
  return dayFormat(withYear).formatRange(from, to - 1);
}

/**
 * Where to label the chart's time axis. Up to a day and a half, four evenly spaced times;
 * longer, UTC midnights labelled with the date, since a time alone no longer says which day.
 */
export function timeTicks(from: number, to: number, now = Date.now()): Array<{ t: number; label: string }> {
  const span = to - from;
  if (span <= 36 * HOUR) {
    return [0, 1, 2, 3].map((i) => {
      const t = from + (span * i) / 3;
      return { t, label: hhmm(t) };
    });
  }
  const step = Math.ceil(span / DAY / 4) * DAY;
  const ticks: Array<{ t: number; label: string }> = [];
  for (let t = Math.ceil(from / DAY) * DAY; t <= to; t += step) ticks.push({ t, label: utcDay(t, now) });
  return ticks;
}
