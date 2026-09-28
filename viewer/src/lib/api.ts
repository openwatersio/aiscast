// The aiscast API this app is a client of. Server renders talk to it over loopback in
// production; the browser talks to it across the origin, which is why every endpoint the
// app touches sends `Access-Control-Allow-Origin: *`.
//
// Two bases, deliberately. API_SERVER is where SSR fetches: loopback on the box, so it
// never leaves the machine. PUBLIC_AIS_API is what the browser is told to use. They differ
// in production and are usually the same in development.
export const API_SERVER =
  import.meta.env.API_SERVER ?? import.meta.env.PUBLIC_AIS_API ?? "https://ais.openwaters.io";

export const API_PUBLIC = import.meta.env.PUBLIC_AIS_API ?? "https://ais.openwaters.io";

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

async function get<T>(path: string, init?: RequestInit): Promise<T | undefined> {
  try {
    const res = await fetch(`${API_SERVER}${path}`, {
      ...init,
      headers: { accept: "application/json", ...init?.headers },
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
export async function getVessel(mmsi: number): Promise<VesselFeature | undefined> {
  return get<VesselFeature>(`/v1/vessels/${mmsi}`);
}

/** Name prefix, or MMSI prefix when the query is all digits. The server caps this at 50. */
export async function searchVessels(q: string): Promise<VesselFeature[]> {
  const fc = await get<FeatureCollection>(`/v1/vessels?q=${encodeURIComponent(q)}`);
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
export async function getTrack(mmsi: number, hours = 24): Promise<Track | undefined> {
  const from = new Date(Date.now() - hours * 3600e3).toISOString();
  return get<Track>(`/v1/vessels/${mmsi}/track?from=${from}`);
}

export async function getStations(): Promise<Station[] | undefined> {
  return get<Station[]>("/v1/stations");
}

export async function getStation(
  id: string,
): Promise<{ station: Station; vessels: FeatureCollection } | undefined> {
  return get(`/v1/stations/${id.split("/").map(encodeURIComponent).join("/")}`);
}
