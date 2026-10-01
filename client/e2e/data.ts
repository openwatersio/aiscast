import { e2eAuth } from "./auth";

/** The e2e server, from e2e/server.sh. */
export const API = "http://127.0.0.1:8787";

/**
 * The Gulf of Finland between Helsinki and Tallinn, the busiest water Digitraffic reports on,
 * as minLat,minLon,maxLat,maxLon. The map opens over it at MAP_HASH.
 */
export const AREA = "59.3,22.8,60.5,27.0";
export const MAP_HASH = "#map=8/59.85/24.9";

export interface VesselRef {
  mmsi: number;
  name: string;
}

export async function api<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { headers: { Authorization: `Bearer ${e2eAuth().token}` } });
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res.json() as Promise<T>;
}

/** Vessels heard in AREA that have sent their name. */
export async function namedVessels(): Promise<VesselRef[]> {
  const fc = await api<{ features: Array<{ properties: { mmsi: number; name?: string } }> }>(`/v1/vessels?bbox=${AREA}`);
  return fc.features.flatMap((f) => (f.properties.name ? [{ mmsi: f.properties.mmsi, name: f.properties.name }] : []));
}

/** One of them. The global setup waits until there are some. */
export async function namedVessel(): Promise<VesselRef> {
  const [vessel] = await namedVessels();
  if (!vessel) throw new Error("no named vessel in the test area");
  return vessel;
}
