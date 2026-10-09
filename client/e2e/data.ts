import { createSocket } from "node:dgram";
import { e2eAuth } from "./auth";
import { e2ePorts } from "./ports";

/** The e2e server, from e2e/server.sh, and the port it hears volunteer receivers on. */
export const API = `http://127.0.0.1:${e2ePorts().api}`;
const UDP_PORT = e2ePorts().udp;

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

/**
 * A fishing-net buoy under an aid's 99 MMSI, as a volunteer receiver hears one: a vessel's
 * position report and its name, a serial and its battery level. Sends it over UDP and waits until
 * the e2e server holds it as gear.
 */
export async function heardGear(): Promise<VesselRef> {
  const mmsi = 994123456;
  const sentences = ["!AIVDM,1,1,,A,1>l4Nh?P051ivs0RCKl>4?v00000,0*15", "!AIVDM,1,1,,A,H>l4Nh0pEB09DuV3NoOFD000000,2*5A"];
  const socket = createSocket("udp4");
  await new Promise<void>((resolve, reject) =>
    socket.send(sentences.join("\n") + "\n", UDP_PORT, "127.0.0.1", (err) => (err ? reject(err) : resolve())),
  );
  socket.close();
  for (let i = 0; i < 50; i++) {
    const res = await fetch(`${API}/v1/vessels/${mmsi}`, { headers: { Authorization: `Bearer ${e2eAuth().token}` } });
    if (res.ok) {
      const { properties } = (await res.json()) as { properties: { kind: string; name?: string } };
      if (properties.kind === "gear" && properties.name) return { mmsi, name: properties.name };
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("the e2e server never held the buoy as gear");
}

// Two position reports from gpsd's sample log, of ships Digitraffic will not also be reporting.
const VOLUNTEER_HEARS = ["!AIVDM,1,1,,A,15RTgt0PAso;90TKcjM8h6g208CQ,0*4A", "!AIVDM,1,1,,A,16SteH0P00Jt63hHaa6SagvJ087r,0*42"];

/**
 * Plays a volunteer receiver: sends the e2e server what one heard, over UDP as a forwarder does, and
 * waits until the station list has it. Digitraffic is a feed, which the station list leaves out.
 * Returns the station's id.
 */
export async function heardFromVolunteer(): Promise<string> {
  const socket = createSocket("udp4");
  await new Promise<void>((resolve, reject) =>
    socket.send(VOLUNTEER_HEARS.join("\n") + "\n", UDP_PORT, "127.0.0.1", (err) => (err ? reject(err) : resolve())),
  );
  socket.close();
  for (let i = 0; i < 50; i++) {
    const stations = await api<Array<{ station: string; source: string }>>("/v1/stations");
    const volunteer = stations.find((s) => s.source.startsWith("udp:"));
    if (volunteer) return volunteer.station;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("the e2e server never listed the volunteer station");
}
