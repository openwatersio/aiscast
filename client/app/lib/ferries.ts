import type { VesselParticulars } from "./api";
import type { FleetVessel } from "./fleets.server";
import bcFerries from "../fleets/ferries/bc-ferries.json" with { type: "json" };
import blackBall from "../fleets/ferries/black-ball.json" with { type: "json" };
import hyLine from "../fleets/ferries/hy-line.json" with { type: "json" };
import wsf from "../fleets/ferries/wsf.json" with { type: "json" };

export interface FerryIdentity {
  operator?: string;
  service?: string;
  url?: string;
}

export interface FerryVessel {
  mmsi: number;
  imo?: number;
  name?: string;
  kind?: string;
  particulars?: Pick<VesselParticulars, "ship_type" | "service" | "operator">;
}

interface FerryRoster {
  title: string;
  source: Record<string, string>;
  vessels: FleetVessel[];
}

// Shared with fleet pages; direct JSON imports work in both Vite and Node without a YAML parser.
const rosters: FerryRoster[] = [bcFerries, blackBall, hyLine, wsf];
const byMmsi = new Map<number, FleetVessel & FerryIdentity>();
const byImo = new Map<number, FleetVessel & FerryIdentity>();
for (const roster of rosters) {
  for (const vessel of roster.vessels) {
    const identity = { ...vessel, operator: roster.title, url: vessel.links?.["Official vessel"] ?? roster.source[roster.title] };
    if (vessel.mmsi) byMmsi.set(vessel.mmsi, identity);
    if (vessel.imo) byImo.set(vessel.imo, identity);
  }
}

const FERRY_TYPES = new Set([
  "ferry",
  "passenger ferry",
  "car ferry",
  "vehicle ferry",
  "train ferry",
  "railway ferry",
  "high-speed ferry",
  "double-ended ferry",
  "ro-ro ferry",
  "ro-ro passenger ferry",
  "roll-on/roll-off passenger ship",
  "cruise ferry",
]);

export function matchesFerryRoster(verified: FleetVessel, v: FerryVessel): boolean {
  if (v.kind && v.kind !== "vessel") return false;
  const name = v.name?.trim().toUpperCase();
  // MMSIs are reissued; a name and any reported IMO must still agree with the roster.
  return Boolean((v.mmsi === verified.mmsi || (v.imo && v.imo === verified.imo)) && name &&
    !(v.imo && verified.imo && v.imo !== verified.imo) &&
    [verified.name, verified.ais_name].some((alias) => alias?.trim().toUpperCase() === name));
}

export function ferryIdentity(v: FerryVessel): FerryIdentity | undefined {
  if (v.kind && v.kind !== "vessel") return undefined;
  const verified = (v.imo && byImo.get(v.imo)) || byMmsi.get(v.mmsi);
  if (verified && matchesFerryRoster(verified, v)) return verified;
  for (const type of [v.particulars?.ship_type, v.particulars?.service]) {
    if (type && FERRY_TYPES.has(type.trim().toLowerCase().replace(/[‐‑‒–—]/g, "-"))) {
      // Enrichment can retain a former operator; current service copy needs a verified entry.
      return {};
    }
  }
  return undefined;
}
