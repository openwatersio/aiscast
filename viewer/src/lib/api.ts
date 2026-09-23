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
  seen: string;
  source: string;
  station: string;
  msg_type: string;
}

export interface VesselFeature {
  type: "Feature";
  id: number;
  geometry: { type: "Point"; coordinates: [number, number] };
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
 * One vessel's current state. `/v1/vessels/{mmsi}` does not exist yet, so this asks the
 * MMSI-filtered collection, which is anonymous-safe and unaffected by the area cap.
 */
export async function getVessel(mmsi: number): Promise<VesselFeature | undefined> {
  const fc = await get<FeatureCollection>(`/v1/vessels?mmsi=${mmsi}`);
  return fc?.features?.find((f) => f.properties.mmsi === mmsi);
}

export async function getStations(): Promise<Station[] | undefined> {
  return get<Station[]>("/v1/stations");
}

export async function getStation(
  id: string,
): Promise<{ station: Station; vessels: FeatureCollection } | undefined> {
  return get(`/v1/stations/${id.split("/").map(encodeURIComponent).join("/")}`);
}
