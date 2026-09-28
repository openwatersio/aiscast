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
