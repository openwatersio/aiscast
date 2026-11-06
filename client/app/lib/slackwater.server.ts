/**
 * The Slackwater station pages near a vessel, from the index slackwater.xyz publishes beside
 * its sitemaps. Linking them from vessel pages gives crawlers paths into station pages that
 * nothing else links to.
 */

import type { NearbyStation } from "./slackwater";

export type { NearbyStation };

const ORIGIN = "https://slackwater.xyz";
const INDEX_URL = `${ORIGIN}/stations.json`;

/** Past this, a station describes some other water than the vessel's. */
const REACH_KM = 50;

// The index changes when the station database does, a few times a month. slackwater.xyz serves it
// as a static asset in about a tenth of a second and asks for revalidation on every fetch, so the
// edge keeps no copy of its own; an isolate keeps its parsed copy for six hours. After a failure
// an isolate goes five minutes without asking and keeps linking its last copy, for a day at most,
// since past that its paths may have moved. A fetch gets ten seconds and a page waits two at most,
// so an index too slow for one page still arrives for the next.
const KEEP_MS = 6 * 3_600_000;
const STALE_MS = 24 * 3_600_000;
const RETRY_MS = 5 * 60_000;
const FETCH_MS = 10_000;
const WAIT_MS = 2_000;

// As the Worker's other outbound requests name themselves.
const USER_AGENT = "aiscast-web/1.0 (https://openwaters.io/ais/; hello@openwaters.io)";

export type StationKind = NearbyStation["kind"];

/** One entry of slackwater.xyz's `/stations.json`. */
export interface IndexedStation {
  kind: StationKind;
  path: string;
  name: string;
  latitude: number;
  longitude: number;
  /** Its notice's position in `attributions`. */
  attribution: number;
}

export interface StationIndex {
  origin: string;
  /** The notices the stations' licences ask a page showing them to display, each listed once. */
  attributions: string[];
  stations: IndexedStation[];
}

// The site's own station URLs. The origin is ours, never the index's, so a bad index can only
// ever point at a slackwater.xyz path.
const STATION_PATH = /^\/(tides|currents)\/[a-z0-9-]+(\/[a-z0-9-]+)*\/$/;

function km(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const rad = Math.PI / 180;
  const dLat = (lat2 - lat1) * rad;
  const dLon = (lon2 - lon1) * rad;
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(lat1 * rad) * Math.cos(lat2 * rad) * Math.sin(dLon / 2) ** 2;
  return 12_742 * Math.asin(Math.min(1, Math.sqrt(a)));
}

/**
 * The nearest tide station and the nearest current station within reach, tide first. A station
 * whose notice the index doesn't carry is passed over: its licence asks for the notice wherever
 * it is shown, so without one it can't be shown.
 */
export function nearestStations(index: StationIndex, lat: number, lon: number): NearbyStation[] {
  const best = new Map<StationKind, { station: IndexedStation; notice: string; km: number }>();
  for (const station of index.stations) {
    const notice = index.attributions[station.attribution];
    if (!STATION_PATH.test(station.path) || typeof notice !== "string" || !notice.trim()) continue;
    const d = km(lat, lon, station.latitude, station.longitude);
    if (d > REACH_KM) continue;
    const held = best.get(station.kind);
    if (!held || d < held.km) best.set(station.kind, { station, notice, km: d });
  }
  return (["tide", "current"] as const).flatMap((kind) => {
    const found = best.get(kind);
    return found ? [{ kind, name: found.station.name, url: `${ORIGIN}${found.station.path}`, attribution: found.notice }] : [];
  });
}

const isStation = (s: unknown): s is IndexedStation => {
  const e = s as Partial<IndexedStation> | null;
  return (e?.kind === "tide" || e?.kind === "current") && typeof e.path === "string"
    // A tide station's page is under /tides/, a current station's under /currents/.
    && e.path.startsWith(e.kind === "tide" ? "/tides/" : "/currents/")
    && typeof e.name === "string" && e.name.trim() !== ""
    && Number.isFinite(e.latitude) && Number.isFinite(e.longitude) && Number.isInteger(e.attribution);
};

/** The usable entries of whatever the index answered, or nothing when none are. */
function readIndex(body: unknown): StationIndex | undefined {
  const { stations: listed, attributions: notices } = (body ?? {}) as { stations?: unknown; attributions?: unknown };
  const stations = Array.isArray(listed) ? listed.filter(isStation) : [];
  const attributions = Array.isArray(notices) ? notices.map((n) => (typeof n === "string" ? n : "")) : [];
  // Entries this can't read mean slackwater.xyz changed its format, which nothing else would show.
  const unread = (Array.isArray(listed) ? listed.length : 0) - stations.length;
  if (unread) console.warn(`slackwater: stations.json has ${unread} of ${Array.isArray(listed) ? listed.length : 0} entries this can't read`);
  if (!stations.length || !attributions.length) {
    console.warn("slackwater: stations.json has no usable stations or attributions");
    return undefined;
  }
  return { origin: ORIGIN, attributions, stations };
}

async function fetchIndex(): Promise<StationIndex | undefined> {
  try {
    const res = await fetch(INDEX_URL, { headers: { "user-agent": USER_AGENT }, signal: AbortSignal.timeout(FETCH_MS) });
    if (!res.ok) {
      console.warn(`slackwater: stations.json answered ${res.status}`);
      return undefined;
    }
    return readIndex(await res.json());
  } catch (e) {
    // Slackwater down, slow, or answering with something else: the vessel page goes on without it.
    console.warn(`slackwater: stations.json failed: ${e instanceof Error ? e.message : String(e)}`);
    return undefined;
  }
}

/** Gives up waiting on `work` after `ms`, without needing it to settle. */
function within<T>(work: Promise<T>, ms: number): Promise<T | undefined> {
  return new Promise<T | undefined>((resolve, reject) => {
    const timer = setTimeout(() => resolve(undefined), ms);
    work.then(resolve, reject).finally(() => clearTimeout(timer));
  });
}

let ready: { index: StationIndex; fetchedAt: number } | undefined;
let pending: { started: number; index: Promise<StationIndex | undefined> } | undefined;
let quietUntil = 0;

/** The last copy, while it is young enough to link from. */
const lastCopy = (now: number) => (ready && now - ready.fetchedAt < STALE_MS ? ready.index : undefined);

/**
 * The index, and the fetch that refreshes it, which `keepAlive` (the Worker's `waitUntil`) carries
 * past the response of the page that started it, so a fetch too slow for one page lands for the next.
 */
async function stationIndex(keepAlive: (work: Promise<unknown>) => void): Promise<StationIndex | undefined> {
  const now = Date.now();
  if (ready && now - ready.fetchedAt < KEEP_MS) return ready.index;
  if (now < quietUntil) return lastCopy(now);
  // Pages share one fetch, each behind its own two-second wait. Should the Worker cancel one
  // anyway, it never settles, so past its own ten seconds it is given up on and the next page
  // starts another.
  if (!pending || now - pending.started > FETCH_MS) {
    const index = fetchIndex();
    const entry = { started: now, index };
    pending = entry;
    keepAlive(index.then((found) => {
      if (pending === entry) pending = undefined;
      if (found) {
        ready = { index: found, fetchedAt: Date.now() };
        quietUntil = 0;
      } else {
        quietUntil = Date.now() + RETRY_MS;
      }
    }));
  }
  // A copy past six hours still links well while it refreshes, so a page waits only when it has none.
  return lastCopy(now) ?? (await within(pending.index, WAIT_MS));
}

/**
 * The Slackwater station pages near a position: none when none is within reach, and undefined
 * when the index couldn't be had in time, so a page can ask to be kept for less. Never throws.
 */
export async function slackwaterStations(
  lat: number,
  lon: number,
  keepAlive: (work: Promise<unknown>) => void = () => {},
): Promise<NearbyStation[] | undefined> {
  const index = await stationIndex(keepAlive);
  return index ? nearestStations(index, lat, lon) : undefined;
}

/** Drops the isolate's copy of the index. For tests. */
export function forgetStationIndex() {
  ready = undefined;
  pending = undefined;
  quietUntil = 0;
}
