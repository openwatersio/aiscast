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
  seen: string;
  source: string;
  station: string;
  msg_type: string;
}

export interface VesselFeature {
  type: "Feature";
  id: number;
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
  vessels: number;
  positions: number;
  first_seen: string;
  last_seen: string;
  last_age_s: number;
  bbox?: [number, number, number, number];
}

async function get<T>(auth: ApiAuth, path: string): Promise<T | undefined> {
  try {
    const res = await fetch(`${auth.api}${path}`, {
      headers: {
        accept: "application/json",
        ...(auth.token ? { authorization: `Bearer ${auth.token}` } : {}),
      },
    });
    if (!res.ok) return undefined;
    return (await res.json()) as T;
  } catch {
    // A page that cannot reach the API still renders, saying so. Throwing here would turn
    // a data outage into a 500 on every vessel URL a crawler holds.
    return undefined;
  }
}

/**
 * One vessel, however long ago it was last heard. `geometry` is null for a vessel whose
 * position the network has never heard, and an unknown MMSI is a 404.
 */
export async function getVessel(auth: ApiAuth, mmsi: number): Promise<VesselFeature | undefined> {
  return get<VesselFeature>(auth, `/v1/vessels/${mmsi}`);
}

/** Name prefix, or MMSI prefix when the query is all digits. The server caps this at 50. */
export async function searchVessels(auth: ApiAuth, q: string): Promise<VesselFeature[]> {
  const fc = await get<FeatureCollection>(auth, `/v1/vessels?q=${encodeURIComponent(q)}`);
  return fc?.features ?? [];
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
  return get<Track>(auth, `/v1/vessels/${mmsi}/track?from=${from}&to=${to}${limit}${interval}`);
}

export async function getStations(auth: ApiAuth): Promise<Station[] | undefined> {
  return get<Station[]>(auth, "/v1/stations");
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
  vessels: { total: number; with_position: number; by_kind: Record<string, number> };
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
  return get<Stats>(auth, "/v1/stats");
}
