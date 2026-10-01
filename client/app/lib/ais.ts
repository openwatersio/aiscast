export const NAV_STATUS: Record<number, string> = {
  0: "Under way using engine",
  1: "At anchor",
  2: "Not under command",
  3: "Restricted manoeuvrability",
  4: "Constrained by draught",
  5: "Moored",
  6: "Aground",
  7: "Engaged in fishing",
  8: "Under way sailing",
  9: "Carrying dangerous goods (HSC)",
  10: "Carrying dangerous goods (WIG)",
  14: "AIS-SART, MOB or EPIRB",
};

export type ShipClass =
  | "cargo"
  | "tanker"
  | "passenger"
  | "fishing"
  | "pleasure"
  | "special"
  | "aton"
  | "base"
  | "sar"
  | "other";

/** Matches the palette on openwaters.io/ais, tuned for the dark basemap. */
export const CLASS_COLORS: Record<ShipClass, string> = {
  cargo: "#2e7d32",
  tanker: "#c62828",
  passenger: "#1565c0",
  fishing: "#ef6c00",
  pleasure: "#8e24aa",
  special: "#00838f",
  aton: "#6d4c41",
  base: "#37474f",
  sar: "#d81b60",
  other: "#f9a825",
};

export const CLASS_LABELS: Record<ShipClass, string> = {
  cargo: "Cargo",
  tanker: "Tanker",
  passenger: "Passenger",
  fishing: "Fishing",
  pleasure: "Pleasure craft",
  special: "Special craft",
  aton: "Aid to navigation",
  base: "Base station",
  sar: "Search and rescue",
  other: "Other",
};

export function shipClass(kind?: string, type?: number): ShipClass {
  if (kind === "aton" || kind === "base" || kind === "sar") return kind;
  if (!type) return "other";
  if (type === 30) return "fishing";
  if (type === 36 || type === 37) return "pleasure";
  if (type >= 50 && type <= 59) return "special";
  if (type >= 60 && type <= 69) return "passenger";
  if (type >= 70 && type <= 79) return "cargo";
  if (type >= 80 && type <= 89) return "tanker";
  return "other";
}

/**
 * The cosmetic half of a vessel URL. The MMSI is canonical; this only has to be stable
 * for a given name and safe in a path segment.
 */
// Letters NFD cannot help with. `\u00e5` decomposes to a + ring and needs no entry, but `\u00e6`, `\u00f8`
// and friends are distinct letters rather than an accented base, so normalising drops them
// and `\u00c6r\u00f8 F\u00e6rgen` slugs as `r-f-rgen`. Norwegian and Finnish feeds make that common here.
const TRANSLITERATE: Record<string, string> = {
  \u00e6: "ae",
  \u00f8: "o",
  \u0153: "oe",
  \u00f0: "d",
  \u0111: "d",
  \u00fe: "th",
  \u00df: "ss",
  \u0142: "l",
  \u014b: "n",
  \u0167: "t",
};

export function vesselSlug(name?: string): string {
  if (!name) return "";
  const s = name
    .toLowerCase()
    .replace(/[\u00e6\u00f8\u0153\u00f0\u0111\u00fe\u00df\u0142\u014b\u0167]/g, (c) => TRANSLITERATE[c] ?? c)
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s.slice(0, 60).replace(/-+$/, "");
}

export function vesselPath(mmsi: number, name?: string): string {
  const slug = vesselSlug(name);
  return slug ? `/vessels/${mmsi}-${slug}` : `/vessels/${mmsi}`;
}

/**
 * Splits a `/vessels/...` segment back into its parts. Anything after the MMSI is ignored
 * for lookup and only compared to decide whether to redirect to the canonical form.
 */
export function parseVesselParam(param: string): { mmsi: number; slug: string } | undefined {
  const m = /^(\d{1,9})(?:-(.*))?$/.exec(param);
  if (!m) return undefined;
  const mmsi = Number(m[1]);
  if (!Number.isInteger(mmsi) || mmsi <= 0) return undefined;
  return { mmsi, slug: m[2] ?? "" };
}

export function formatAge(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))}s ago`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;
  return `${Math.round(seconds / 86400)} days ago`;
}

export function formatCoord(lat: number, lon: number): string {
  const d = (v: number, pos: string, neg: string) => {
    const deg = Math.floor(Math.abs(v));
    const min = (Math.abs(v) - deg) * 60;
    return `${deg}°${min.toFixed(3)}' ${v >= 0 ? pos : neg}`;
  };
  return `${d(lat, "N", "S")} ${d(lon, "E", "W")}`;
}

// The server resolves the MMSI's MID to an ISO 3166-1 alpha-2 code, so the only work left
// is turning that into something readable. Intl does both; a lookup table would be 250
// rows of what the platform already ships.
// fallback "none" makes this a validity check as well as a lookup: an unassigned code comes
// back undefined instead of echoing itself, which is what lets a destination be tested for
// being a UN/LOCODE. ZZ is assigned, and means "unknown", so it is excluded by hand.
const REGIONS = new Intl.DisplayNames(["en"], { type: "region", fallback: "none" });

export function flagName(code?: string): string | undefined {
  if (!code || code.length !== 2) return undefined;
  const upper = code.toUpperCase();
  if (upper === "ZZ") return undefined;
  try {
    return REGIONS.of(upper);
  } catch {
    return undefined;
  }
}

/** One end of a voyage, as far as the destination field can be read. */
export interface Place {
  /** Exactly what was sent, for the cases the rules below cannot improve on. */
  raw: string;
  flag?: string;
  country?: string;
  /** Port or facility code, when the text carried one. */
  code?: string;
}

export interface Voyage {
  from?: Place;
  to?: Place;
}

// Crews leave the field at whatever their unit shipped with, and these are the placeholders
// it ships with. A half made only of them says nothing.
const PLACEHOLDER = /^[X?\-.\s]*$/i;

/**
 * Reads one end of a voyage. Two forms carry more than their own text:
 *
 * - a UN/LOCODE, `USCHS` or `US CHS`, whose first two letters are the country;
 * - `US^0XG5`, a country and a short facility code, which US inland towing units emit.
 *
 * Naming the port or the facility would need a table this does not carry, so the code is
 * shown as sent and only the country is resolved.
 */
export function parsePlace(text?: string): Place | undefined {
  const raw = (text ?? "").trim();
  if (!raw || PLACEHOLDER.test(raw)) return undefined;

  const m = /^([A-Za-z]{2})(?:\s*\^\s*([A-Za-z0-9]{2,4})|[\s-]?([A-Za-z0-9]{3}))$/.exec(raw);
  const country = m ? flagName(m[1]) : undefined;
  if (!m || !country) return { raw };
  return { raw, flag: flagEmoji(m[1]), country, code: (m[2] ?? m[3])!.toUpperCase() };
}

/**
 * Splits the destination field into where a vessel came from and where it is going. About
 * one vessel in eight separates the two with `>`, and that is the only place the origin of
 * a voyage is ever reported.
 */
export function parseDestination(text?: string): Voyage | undefined {
  const raw = (text ?? "").trim();
  if (!raw) return undefined;
  const at = raw.indexOf(">");
  if (at < 0) {
    const to = parsePlace(raw);
    return to ? { to } : undefined;
  }
  const from = parsePlace(raw.slice(0, at));
  const to = parsePlace(raw.slice(at + 1));
  return from || to ? { from, to } : undefined;
}

export function flagEmoji(code?: string): string | undefined {
  if (!code || code.length !== 2) return undefined;
  return String.fromCodePoint(
    ...[...code.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65),
  );
}

/**
 * AIS carries an ETA as month, day, hour and minute with no year, so the year is inferred
 * as whichever one puts the arrival nearest to now. That is right for a voyage in progress
 * and it is what makes a December ETA read on 2 January work.
 */
export function parseEta(eta: string | undefined, now = new Date()): Date | undefined {
  const m = /^(\d{2})-(\d{2})(?:\s+(\d{2}):(\d{2}))?$/.exec((eta ?? "").trim());
  if (!m) return undefined;
  const [month, day, hour, minute] = [m[1], m[2], m[3] ?? "0", m[4] ?? "0"].map(Number);
  if (month! < 1 || month! > 12 || day! < 1 || day! > 31) return undefined;
  if (hour! > 23 || minute! > 59) return undefined;

  let best: Date | undefined;
  for (const year of [now.getUTCFullYear() - 1, now.getUTCFullYear(), now.getUTCFullYear() + 1]) {
    const d = new Date(Date.UTC(year, month! - 1, day!, hour!, minute!));
    // Date rolls 02-30 into March; AIS senders do transmit impossible dates.
    if (d.getUTCMonth() !== month! - 1) continue;
    if (!best || Math.abs(+d - +now) < Math.abs(+best - +now)) best = d;
  }
  return best;
}

/** "in 3 days", "in 5 h", "2 days ago". */
export function relativeTime(to: Date, now = new Date()): string {
  const mins = Math.round((+to - +now) / 60000);
  const abs = Math.abs(mins);
  const [value, unit]: [number, Intl.RelativeTimeFormatUnit] =
    abs < 60 ? [mins, "minute"] : abs < 60 * 24 ? [Math.round(mins / 60), "hour"] : [Math.round(mins / 1440), "day"];
  return new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(value, unit);
}

/**
 * Joins the track the server returned to the positions this session has collected.
 *
 * The two overlap: the session has been collecting since the page opened, which is inside
 * the window the server already covered. Appending all of it draws the line back to where
 * the vessel was minutes ago and forward again, a loop over its own path. Only positions
 * newer than the history's last point extend it.
 */
export function mergeTrack(
  history: Array<[number, number]>,
  historyEnd: number,
  live: Array<[number, number, number]>,
): Array<[number, number]> {
  const tail = live
    .filter(([, , t]) => t > historyEnd)
    .map(([lon, lat]) => [lon, lat] as [number, number]);
  return [...history, ...tail];
}

/**
 * Gaps longer than this end a segment. A vessel reports every few seconds to a few minutes,
 * so a longer silence means it was out of range of every receiver, not that it sailed a
 * straight line. It matches the server's own vessel cache window.
 */
export const TRACK_GAP_MS = 30 * 60 * 1000;

/**
 * The gap for a track thinned to one position per `intervalMs`. The first position in each
 * interval can sit up to two intervals after the one before while the vessel reports all along,
 * so only a silence past that and TRACK_GAP_MS means it went unheard.
 */
export function trackGap(intervalMs: number): number {
  return TRACK_GAP_MS + 2 * intervalMs;
}

/**
 * Splits a track wherever the vessel went unheard, so the line is drawn only where there is
 * evidence. Joining across a gap invents a course and a speed: one real track here jumps
 * 208 km across 14 hours of silence, which as a single line reads as a passage that was
 * never reported.
 */
export function splitTrack(
  coords: Array<[number, number]>,
  times: number[],
  maxGap = TRACK_GAP_MS,
): Array<Array<[number, number]>> {
  const segments: Array<Array<[number, number]>> = [];
  let current: Array<[number, number]> = [];
  for (let i = 0; i < coords.length; i++) {
    if (i > 0 && times[i]! - times[i - 1]! > maxGap) {
      if (current.length > 1) segments.push(current);
      current = [];
    }
    current.push(coords[i]!);
  }
  if (current.length > 1) segments.push(current);
  return segments;
}

/** The last position at or before this time, or -1 when the time precedes the track. */
export function indexAt(times: number[], at: number): number {
  let lo = 0;
  let hi = times.length - 1;
  let found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (times[mid]! <= at) {
      found = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return found;
}

/**
 * Speed at a moment, between the reports either side. A report's own moment has its speed;
 * inside a gap longer than `maxGap`, or that long after the last report, the vessel
 * went unheard and there is none, as the chart's broken line shows.
 */
export function speedAt(
  track: { times: number[]; sog: Array<number | null | undefined> },
  at: number,
  maxGap = TRACK_GAP_MS,
): number | undefined {
  const i = indexAt(track.times, at);
  if (i < 0) return undefined;
  const a = track.sog[i] ?? undefined;
  const t = track.times[i]!;
  if (a === undefined || at === t) return a;
  if (i + 1 >= track.times.length) return at - t <= maxGap ? a : undefined;
  const span = track.times[i + 1]! - t;
  if (span > maxGap) return undefined;
  const b = track.sog[i + 1];
  if (b == null || span <= 0) return a;
  return a + (b - a) * ((at - t) / span);
}

export interface TrackPoint {
  /** Where the vessel was, or where it must have passed when inside a gap. */
  point: [number, number];
  /** Last position at or before the time. */
  index: number;
  /** True when the time falls in a stretch the vessel was not heard. */
  inGap: boolean;
}

/**
 * Where a vessel was at a moment, interpolated between the two positions either side.
 *
 * Inside a gap this is a guess and the caller must draw it as one: the vessel was unheard,
 * and the straight line between the two ends is the only thing available. Between ordinary
 * reports it is close enough to right, and it is what stops playback stepping from fix to
 * fix.
 */
export function interpolateAt(
  coords: Array<[number, number]>,
  times: number[],
  at: number,
  maxGap = TRACK_GAP_MS,
): TrackPoint | undefined {
  if (!coords.length) return undefined;
  const index = indexAt(times, at);
  if (index < 0) return { point: coords[0]!, index: 0, inGap: false };
  if (index >= coords.length - 1) return { point: coords[coords.length - 1]!, index: coords.length - 1, inGap: false };

  const span = times[index + 1]! - times[index]!;
  const fraction = span > 0 ? (at - times[index]!) / span : 0;
  const [x1, y1] = coords[index]!;
  const [x2, y2] = coords[index + 1]!;
  return {
    point: [x1 + (x2 - x1) * fraction, y1 + (y2 - y1) * fraction],
    index,
    inGap: span > maxGap,
  };
}

/** Initial great-circle bearing from one position to the next, in degrees from north. */
export function bearing([lon1, lat1]: [number, number], [lon2, lat2]: [number, number]): number {
  const rad = Math.PI / 180;
  const y = Math.sin((lon2 - lon1) * rad) * Math.cos(lat2 * rad);
  const x =
    Math.cos(lat1 * rad) * Math.sin(lat2 * rad) -
    Math.sin(lat1 * rad) * Math.cos(lat2 * rad) * Math.cos((lon2 - lon1) * rad);
  return (Math.atan2(y, x) / rad + 360) % 360;
}

/**
 * Whether a number passes the IMO check: the seventh digit is the sum of the first six
 * weighted 7 down to 2, mod 10. AIS static data carries a fair share of mistyped IMOs, and
 * looking one up finds another ship or nothing. Numbers starting with 1 are the yacht series
 * and valid.
 */
export function isValidImo(imo: number | undefined): imo is number {
  if (imo == null || !Number.isInteger(imo) || imo < 1_000_000 || imo > 9_999_999) return false;
  const digits = String(imo).split("").map(Number);
  const sum = digits.slice(0, 6).reduce((acc, d, i) => acc + d * (7 - i), 0);
  return sum % 10 === digits[6];
}

/** Where a vessel's AIS antenna sits, in meters from its bow, stern, port and starboard sides. */
export interface Offsets {
  toBow: number;
  toStern: number;
  toPort: number;
  toStarboard: number;
}

/**
 * What a vessel's reported dimensions can draw, read by ITU-R M.1371's rules for them: the
 * hull's length and beam, and the antenna when the vessel says where it is. `length` and
 * `beam` stand in when there are no usable offsets. Undefined when there is nothing to draw.
 */
export function vesselDimensions(
  offsets: Offsets | undefined,
  length?: number,
  beam?: number,
): { length: number; beam: number; antenna?: Offsets } | undefined {
  if (offsets) {
    const { toBow: a, toStern: b, toPort: c, toStarboard: d } = offsets;
    // 511 and 63 are the most the fields hold, meaning "that or more": the size is unknown.
    if (a >= 511 || b >= 511 || c >= 63 || d >= 63) return undefined;
    // A and C both zero is the encoding for a reference point that is not available, and some
    // transponders zero only A, sending the length as B. An antenna on the hull's very edge is
    // that, not a position, so the dot needs all four.
    const known = a > 0 && b > 0 && c > 0 && d > 0;
    if (a + b >= 2 && c + d > 0) return { length: a + b, beam: c + d, antenna: known ? offsets : undefined };
  }
  return length && length >= 2 && beam ? { length, beam } : undefined;
}

/**
 * A map view as [south, west, north, east] boxes the stream can take. MapLibre's longitudes
 * run past ±180 when the view crosses the antimeridian, so such a view is two boxes, one
 * either side of it, and a view wider than the world is the whole of it.
 */
export function viewBoxes(south: number, west: number, north: number, east: number): Array<[number, number, number, number]> {
  const s = Math.max(-90, south);
  const n = Math.min(90, north);
  if (east - west >= 360) return [[s, -180, n, 180]];
  const w = ((((west + 180) % 360) + 360) % 360) - 180;
  const e = w + (east - west);
  return e <= 180 ? [[s, w, n, e]] : [[s, w, n, 180], [s, -180, n, e - 360]];
}

// The source kinds people run: a receiver sending over UDP, HTTP or the stream with a token,
// or identified by its own vessel's MMSI. Everything else is a government feed or a partner
// aggregate, named by its upstream.
const VOLUNTEER_KINDS = new Set(["udp", "http", "v1", "mmsi", "station"]);

/** Whether a `source`, such as `udp:24dfc99708ff` or `aishub`, is a volunteer's receiver. */
export function isVolunteer(source: string | undefined): boolean {
  return source != null && VOLUNTEER_KINDS.has(source.split(":")[0]!);
}

/** What to call a station: its name, else the place nearest its traffic, else its id. */
export function stationTitle(st: { station: string; name?: string; near?: string }): string {
  return st.name ?? (st.near ? `Near ${st.near}` : st.station);
}

/**
 * Titles for a list of stations, keyed by id. Where two would read the same, each gets its receiver's
 * tag (`n2k`) or the end of its id, so the list never shows two identical rows.
 */
export function stationTitles(sts: Array<{ station: string; name?: string; near?: string }>): Map<string, string> {
  const count = new Map<string, number>();
  for (const st of sts) count.set(stationTitle(st), (count.get(stationTitle(st)) ?? 0) + 1);
  return new Map(
    sts.map((st) => {
      const title = stationTitle(st);
      if (count.get(title)! < 2 || title === st.station) return [st.station, title];
      const [base, tag] = st.station.split("/", 2) as [string, string | undefined];
      return [st.station, `${title} (${tag ?? `…${base.slice(-4)}`})`];
    }),
  );
}

/**
 * Returns the receiver for a volunteer station id that ends in a TAG path: `station:mmsi:368168720`
 * for `station:mmsi:368168720/n2k`. A volunteer receiver is one station, but links to its paths are
 * still shared. Returns undefined for any other id, because a feed's path, such as
 * `barentswatch/terra`, is a station of its own.
 */
export function volunteerReceiver(id: string): string | undefined {
  const [base, path] = id.split("/", 2);
  return path != null && isVolunteer(base) ? base : undefined;
}
