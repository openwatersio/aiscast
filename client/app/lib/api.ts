import { data } from "react-router";

// The aiscast API this app is a client of. The Worker fetches from it to render a page, and
// the browser fetches from it directly after that, so in-app navigation never waits on the
// Worker. Both answer with the same functions and differ only in base and token.

export interface ApiAuth {
  api: string;
  /** Sent as a bearer header. The server's own for renders, the visitor's in the browser. */
  token?: string;
}

let publicApi = "https://ais.openwaters.io";

/** The server tells the browser which API it rendered against, so a local server is one setting. */
export function setPublicApi(api: string) {
  publicApi = api.replace(/\/+$/, "");
}

export function publicApiBase(): string {
  return publicApi;
}

/**
 * The token this browser minted on openwaters.io/ais/token, if it has one. Same origin as the
 * app, so the page that mints it and the app read the same storage.
 */
export function storedToken(): string | undefined {
  try {
    const raw = localStorage.getItem("aiscast.token");
    if (!raw) return undefined;
    const { token, claims } = JSON.parse(raw);
    if (claims?.exp && claims.exp * 1000 <= Date.now()) return undefined;
    return typeof token === "string" ? token : undefined;
  } catch {
    return undefined;
  }
}

export function browserAuth(): ApiAuth {
  return { api: publicApi, token: storedToken() };
}

export interface VesselProps {
  mmsi: number;
  kind: "vessel" | "aton" | "base" | "sar";
  name?: string;
  type?: number;
  cog?: number;
  sog?: number;
  heading?: number;
  nav_status?: number;
  flag?: string;
  imo?: number;
  callsign?: string;
  destination?: string;
  eta?: string;
  draught?: number;
  length?: number;
  beam?: number;
  /** Where the AIS antenna sits, in meters, as the vessel reports it. */
  to_bow?: number;
  to_stern?: number;
  to_port?: number;
  to_starboard?: number;
  first_seen?: string;
  /** The town or region nearest the position, on search results only. */
  near?: string;
  seen: string;
  source: string;
  station: string;
  msg_type: string;
  /** Registered facts merged from the enrichment sources into one vocabulary. On /v1/vessels/{mmsi} only. */
  particulars?: VesselParticulars;
  /** The source of each particulars field, by the field's name; values are keys of `sources`. */
  provenance?: Record<string, string>;
  /** Each contributing source's credit, license, and its page for this vessel, or its public search. */
  sources?: Record<string, SourceRef>;
}

/**
 * The vessel as registered, merged per field from the enrichment sources: a flag state outranks
 * Wikidata, an empty value never wins. Dimensions are in meters, deadweight in tonnes. Every field
 * is present only when a source has it.
 */
export interface VesselParticulars {
  /** Name as documented with the flag state, which can differ from the AIS name. */
  registered_name?: string;
  /** The official number of a documented vessel, else its state registration. */
  identification?: string;
  service?: string;
  status?: string;
  ship_type?: string;
  builder?: string;
  yard_number?: string;
  year_built?: number;
  gross_tonnage?: number;
  net_tonnage?: number;
  /** How a flag-state tonnage was measured; Convention is the international system. */
  tonnage_measure?: "Convention" | "Regulatory" | "Simplified";
  deadweight?: number;
  length?: number;
  beam?: number;
  depth?: number;
  /** Design draught; the vessel's own `draught` is the current voyage's. */
  draught?: number;
  registry?: string;
  home_port?: string;
  owner?: string;
  operator?: string;
  /** Oldest first. */
  former_names?: string[];
  wikipedia?: string;
  commons_category?: string;
  /** The Commons page of one photo, which carries its own license. */
  image?: string;
}

/** One enrichment source's credit and where its record of this vessel is. */
export interface SourceRef {
  credit: string;
  license: string;
  url?: string;
}

export interface VesselFeature {
  type: "Feature";
  id: number;
  attribution?: Record<string, string>;
  /** Null for a vessel the network has heard but never had a position from. */
  geometry: { type: "Point"; coordinates: [number, number] } | null;
  properties: VesselProps;
}

export interface FeatureCollection {
  type: "FeatureCollection";
  features: VesselFeature[];
  attribution: Record<string, string>;
}

export interface Station {
  station: string;
  source: string;
  events: { last_24h: number; last_7d: number };
  duplicates: number;
  vessels: number; // last 30 minutes
  vessels_24h?: number; // optional while a server without them is still deployed
  vessels_exclusive_24h?: number; // of vessels_24h, heard by no other station
  positions: number;
  first_seen: string;
  last_seen: string;
  last_age_s: number;
  bbox?: [number, number, number, number];
  name?: string;
  name_from?: "operator" | "vessel";
  mmsi?: number; // the station's own vessel
  near?: string; // the town or region nearest the traffic it hears
}

/** The API answered with neither the resource nor a 404, or did not answer. */
export class ApiUnavailable extends Error {}

/**
 * A resource, or undefined when the API says there is no such thing (404). Anything else
 * throws ApiUnavailable: an outage is not an answer, and a page that took it for "not
 * found" would tell crawlers a real vessel's page is gone.
 */
async function get<T>(auth: ApiAuth, path: string): Promise<T | undefined> {
  let res: Response;
  try {
    res = await fetch(`${auth.api}${path}`, {
      headers: {
        accept: "application/json",
        ...(auth.token ? { authorization: `Bearer ${auth.token}` } : {}),
      },
    });
  } catch (e) {
    throw new ApiUnavailable(`${path}: ${e}`);
  }
  if (res.status === 404) return undefined;
  if (!res.ok) throw new ApiUnavailable(`${path}: ${res.status}`);
  try {
    return (await res.json()) as T;
  } catch {
    throw new ApiUnavailable(`${path}: not JSON`);
  }
}

/** For what a page can do without, such as search or a chart: an outage reads as no answer. */
function soft<T>(answer: Promise<T>): Promise<T | undefined> {
  return answer.catch((e) => {
    if (e instanceof ApiUnavailable) return undefined;
    throw e;
  });
}

/**
 * For what decides whether a page exists: an outage becomes a 503 that says to come back,
 * which a crawler retries, rather than the 404 a missing record gets.
 */
export async function orUnavailable<T>(answer: Promise<T>): Promise<T> {
  try {
    return await answer;
  } catch (e) {
    if (e instanceof ApiUnavailable) throw data("The AIS API is unavailable", { status: 503, headers: { "retry-after": "60" } });
    throw e;
  }
}

/**
 * One vessel, however long ago it was last heard. `geometry` is null for a vessel whose
 * position the network has never heard, and an unknown MMSI is a 404.
 */
export async function getVessel(auth: ApiAuth, mmsi: number): Promise<VesselFeature | undefined> {
  return get<VesselFeature>(auth, `/v1/vessels/${mmsi}`);
}

/**
 * Name prefix, or MMSI prefix when the query is all digits. The server caps this at 50.
 * `filters` are further `/v1/vessels` parameters, as `filterParams` writes them.
 */
export async function searchVessels(auth: ApiAuth, q: string, filters = ""): Promise<VesselFeature[]> {
  const fc = await soft(get<FeatureCollection>(auth, `/v1/vessels?q=${encodeURIComponent(q)}${filters && `&${filters}`}`));
  return fc?.features ?? [];
}

/** The vessels in an area, as `areaParams` asks for them. Undefined when the API does not answer. */
export async function vesselsInArea(auth: ApiAuth, params: string): Promise<VesselFeature[] | undefined> {
  return (await soft(get<FeatureCollection>(auth, `/v1/vessels?${params}`)))?.features;
}

export interface Track {
  type: "Feature";
  geometry: { type: "LineString"; coordinates: Array<[number, number]> } | null;
  properties: {
    mmsi: number;
    name?: string;
    points: number;
    from: string;
    to: string;
    truncated: boolean;
    times: string[];
    sog?: Array<number | null>;
    cog?: Array<number | null>;
  };
}

/** Where a vessel has been. The server holds the last 48 hours and clamps to it. */
export async function getTrack(
  auth: ApiAuth,
  mmsi: number,
  range: { from: number; to: number; limit?: number; intervalSeconds?: number },
): Promise<Track | undefined> {
  const from = new Date(range.from).toISOString();
  const to = new Date(range.to).toISOString();
  const limit = range.limit ? `&limit=${range.limit}` : "";
  const interval = range.intervalSeconds ? `&interval=${range.intervalSeconds}` : "";
  return soft(get<Track>(auth, `/v1/vessels/${mmsi}/track?from=${from}&to=${to}${limit}${interval}`));
}

export async function getStations(auth: ApiAuth): Promise<Station[] | undefined> {
  return soft(get<Station[]>(auth, "/v1/stations"));
}

/** The pages of vessels the sitemap lists, each up to the 50,000 URLs a sitemap holds. */
export interface SitemapPages {
  page_size: number;
  max_age_s: number;
  pages: Array<{ vessels: number; lastmod: string }>;
}

/**
 * The list of pages has no "not found", so a 404 is unexpected and counts as an outage. Taken
 * for an empty index, it would tell crawlers there are no vessels.
 */
export async function getSitemapPages(auth: ApiAuth): Promise<SitemapPages> {
  const pages = await get<SitemapPages>(auth, "/sitemap/vessels");
  if (!pages) throw new ApiUnavailable("/sitemap/vessels: 404");
  return pages;
}

/** One page of the sitemap's vessels, from 1. Undefined past the last page. */
export async function getSitemapVessels(
  auth: ApiAuth,
  page: number,
): Promise<Array<{ mmsi: number; name: string; seen: string }> | undefined> {
  return (await get<{ vessels: Array<{ mmsi: number; name: string; seen: string }> }>(auth, `/sitemap/vessels?page=${page}`))?.vessels;
}

export async function getStation(
  auth: ApiAuth,
  id: string,
): Promise<{ station: Station; vessels: FeatureCollection } | undefined> {
  return get(auth, `/v1/stations/${id.split("/").map(encodeURIComponent).join("/")}`);
}

export interface Stats {
  time: string;
  stations: { total: number; active: number; by_source: Record<string, number> };
  vessels: {
    total: number; // every vessel the network has heard
    active: number; // heard in the last 30 minutes; with_position and by_kind describe these
    with_position: number;
    by_kind: Record<string, number>;
    last_24h?: number; // the windows are absent on a server running without the vessel record
    last_7d?: number;
    last_30d?: number;
    new?: { last_24h: number; last_7d: number; last_30d: number };
  };
  events: { per_second: number; last_24h: number; last_7d: number };
  sources: Record<
    string,
    {
      events: { last_24h: number };
      last_age_s: number;
      vessels: number;
      vessels_exclusive: number;
      delay?: { n: number; p50: number; p99: number };
    }
  >;
}

export async function getStats(auth: ApiAuth): Promise<Stats | undefined> {
  return soft(get<Stats>(auth, "/v1/stats"));
}

/** TileJSON for the coverage map, with the days it covers. */
export interface CoverageTiles {
  tiles: string[];
  minzoom?: number;
  maxzoom?: number;
  attribution?: string;
  window: { from: string; to: string; days: number };
}

/** Nothing when the server has no coverage loaded, which it answers with a 503. */
export async function getCoverage(auth: ApiAuth): Promise<CoverageTiles | undefined> {
  return soft(get<CoverageTiles>(auth, "/v1/coverage/tiles.json"));
}
