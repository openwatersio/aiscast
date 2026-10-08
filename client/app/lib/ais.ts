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

/**
 * An MMSI as the API's `/v1/vessels/{id}` path takes it: nine digits. The API reads seven digits as an IMO
 * number, and a coast station's MMSI (`00MIDxxxx`) is seven digits once its leading zeros are dropped.
 */
export function mmsiSegment(mmsi: number): string {
  return String(mmsi).padStart(9, "0");
}

/** A station's card, the PNG its page shares, with each segment of the id encoded. */
export function stationCardPath(id: string): string {
  return `/stations/${id.split("/").map(encodeURIComponent).join("/")}.png`;
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

/** Whether the vessel went unheard between positions `i - 1` and `i`. */
export type Unheard = (i: number) => boolean;

/** Unheard across a silence longer than `maxGap`, for positions that carry no breaks of their own. */
export function silences(times: number[], maxGap = TRACK_GAP_MS): Unheard {
  return (i) => i > 0 && times[i]! - times[i - 1]! > maxGap;
}

/**
 * Unheard along a track the server sent, then the stream's positions after it: the track's own
 * breaks before position `length`, and from there a silence longer than `gap`, the gap for the
 * step the track was thinned to, since its last position can sit up to a step before the
 * vessel's last report.
 */
export function trackThenStream(length: number, breaks: ReadonlySet<number> | undefined, times: number[], gap = TRACK_GAP_MS): Unheard {
  const silence = silences(times, gap);
  return (i) => (breaks && i < length ? breaks.has(i) : silence(i));
}

/**
 * Splits a track wherever the vessel went unheard, so the line is drawn only where there is
 * evidence. Joining across a gap invents a course and a speed: one real track here jumps
 * 208 km across 14 hours of silence, which as a single line reads as a passage that was
 * never reported.
 */
export function splitTrack(coords: Array<[number, number]>, unheard: Unheard): Array<Array<[number, number]>> {
  const segments: Array<Array<[number, number]>> = [];
  let current: Array<[number, number]> = [];
  for (let i = 0; i < coords.length; i++) {
    if (unheard(i)) {
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
 * where the vessel went unheard, or longer than `gap` after the last report, there is none, as
 * the chart's broken line shows.
 */
export function speedAt(
  track: { times: number[]; sog: Array<number | null | undefined> },
  at: number,
  unheard: Unheard = silences(track.times),
  gap = TRACK_GAP_MS,
): number | undefined {
  const i = indexAt(track.times, at);
  if (i < 0) return undefined;
  const a = track.sog[i] ?? undefined;
  const t = track.times[i]!;
  if (a === undefined || at === t) return a;
  if (i + 1 >= track.times.length) return at - t <= gap ? a : undefined;
  if (unheard(i + 1)) return undefined;
  const span = track.times[i + 1]! - t;
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
  unheard: Unheard = silences(times),
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
    inGap: unheard(index + 1),
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

/** Great-circle distance between two [lat, lon] positions, in nautical miles. */
export function distanceNM([lat1, lon1]: [number, number], [lat2, lon2]: [number, number]): number {
  const rad = Math.PI / 180;
  const a =
    Math.sin(((lat2 - lat1) * rad) / 2) ** 2 +
    Math.cos(lat1 * rad) * Math.cos(lat2 * rad) * Math.sin(((lon2 - lon1) * rad) / 2) ** 2;
  return 2 * 3440.065 * Math.asin(Math.sqrt(a));
}

/** "0.4 nm" close by, "37 nm" and "8,400 nm" further off. */
export function formatDistance(nm: number): string {
  return nm < 10 ? `${nm.toFixed(1)} nm` : `${Math.round(nm).toLocaleString("en-US")} nm`;
}

/** An age short enough for the edge of a list row: "45s", "12m", "18h", then days past two of them. */
export function shortAge(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 48 * 3600) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
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

/**
 * The largest box around a [lat, lon] that an area cap of `cap` square degrees allows, as the
 * boxes `viewBoxes` gives. It is square on the ground, so it widens in longitude toward the poles,
 * and is cut a little short so rounding never takes it over the cap.
 */
export function centerBoxes([lat, lon]: [number, number], cap: number): Array<[number, number, number, number]> {
  const area = cap * 0.98;
  const squeeze = Math.max(Math.cos((lat * Math.PI) / 180), 0.05);
  const height = Math.min(Math.sqrt(area * squeeze), 180);
  const width = Math.min(area / height, 360);
  const south = Math.max(-90, lat - height / 2);
  const north = Math.min(90, south + height);
  return viewBoxes(south, lon - width / 2, north, lon + width / 2);
}

// The source kinds people run: a receiver sending over UDP, HTTP or the stream with a token,
// or identified by its own vessel's MMSI. Everything else is a government feed or a partner
// aggregate, named by its upstream.
const VOLUNTEER_KINDS = new Set(["udp", "http", "v1", "mmsi", "station"]);

/** Whether a `source`, such as `udp:24dfc99708ff` or `aishub`, is a volunteer's receiver. */
export function isVolunteer(source: string | undefined): boolean {
  return source != null && VOLUNTEER_KINDS.has(source.split(":")[0]!);
}

// Feeds are stations named by their upstream's id, which reads as code. BarentsWatch splits into
// its networks as `barentswatch/terra` and the like, each a station of its own.
const FEED_NAMES: Record<string, string> = {
  aishub: "AISHub",
  aisstream: "aisstream.io",
  digitraffic: "Digitraffic (Finland)",
  kystverket: "Kystverket (Norway)",
  barentswatch: "BarentsWatch (Norway)",
  "barentswatch/terra": "BarentsWatch coastal",
  "barentswatch/offshore": "BarentsWatch offshore",
  "barentswatch/satellite": "BarentsWatch satellite",
};

/** A feed's name for people, or undefined for an id that is not a known feed. */
export function feedName(id: string): string | undefined {
  return FEED_NAMES[id];
}

/** A station's title, and the end of its id where the title alone could name more than one. */
export interface StationName {
  title: string;
  suffix?: string;
}

/**
 * What to call a station: its name, else the place nearest its traffic, else the feed it is, else
 * "Anonymous" and the end of its id. A receiver's full id is a key or a hash that reads as noise.
 */
export function stationName(st: { station: string; name?: string; near?: string }): StationName {
  if (st.name) return { title: st.name };
  if (st.near) return { title: `Near ${st.near}` };
  const feed = feedName(st.station);
  if (feed) return { title: feed };
  const base = st.station.split("/", 1)[0]!;
  return isVolunteer(base) ? { title: "Anonymous", suffix: `…${base.slice(-4)}` } : { title: st.station };
}

/**
 * A station page's description: its vessels and messages over the same 24 hours, as its share card
 * counts them. `vessels` alone is the last 30 minutes, the fallback for a server without the 24-hour count.
 */
export function stationDescription(
  title: string,
  st: { vessels: number; vessels_24h?: number; events: { last_24h: number }; last_age_s: number },
): string {
  const n = (v: number) => v.toLocaleString("en-US");
  return `AIS receiving station ${title}: ${n(st.vessels_24h ?? st.vessels)} vessels and ${n(st.events.last_24h)} messages in 24 hours, last message ${formatAge(st.last_age_s)}.`;
}

/** stationName as one line of text, for where it cannot be styled: "Anonymous …bCro". */
export function stationTitle(st: { station: string; name?: string; near?: string }): string {
  const { title, suffix } = stationName(st);
  return suffix ? `${title} ${suffix}` : title;
}

/**
 * Whether a station is sending: live within five minutes, quiet within the hour, offline after.
 * A satellite feed hears in passes, so it goes quiet between them while working as it should.
 */
export type StationStatus = "live" | "quiet" | "offline";

export function stationStatus(lastAgeS: number): StationStatus {
  if (lastAgeS < 300) return "live";
  if (lastAgeS < 3600) return "quiet";
  return "offline";
}

/**
 * Names for a list of stations, keyed by id. Where two would read the same, each gets its receiver's
 * tag (`n2k`) or the end of its id, so the list never shows two identical rows.
 */
export function stationTitles(sts: Array<{ station: string; name?: string; near?: string }>): Map<string, StationName> {
  const count = new Map<string, number>();
  for (const st of sts) count.set(stationTitle(st), (count.get(stationTitle(st)) ?? 0) + 1);
  return new Map(
    sts.map((st) => {
      const name = stationName(st);
      if (count.get(stationTitle(st))! < 2 || name.title === st.station) return [st.station, name];
      const [base, tag] = st.station.split("/", 2) as [string, string | undefined];
      const tell = tag ?? `…${base.slice(-4)}`;
      return [st.station, { title: name.title, suffix: name.suffix ? `${name.suffix} ${tell}` : tell }];
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
