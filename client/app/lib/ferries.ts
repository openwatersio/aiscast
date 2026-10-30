import type { VesselParticulars } from "./api";

export interface FerryIdentity {
  operator?: string;
  service?: string;
  url?: string;
}

export interface FerryVessel {
  mmsi: number;
  name?: string;
  kind?: string;
  particulars?: Pick<VesselParticulars, "ship_type" | "service" | "operator">;
}

const VERIFIED_FERRIES: Record<number, FerryIdentity & { name: string }> = {
  366929710: {
    name: "COHO",
    operator: "Black Ball Ferry Line",
    service: "Victoria–Port Angeles",
    url: "https://www.cohoferry.com/about-us",
  },
  367720690: {
    name: "GREY LADY IV",
    operator: "Hy-Line Cruises",
    service: "Hyannis–Nantucket",
    url: "https://www.hylinecruises.com/nantucket-ferry/",
  },
};

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

export function ferryIdentity(v: FerryVessel): FerryIdentity | undefined {
  if (v.kind && v.kind !== "vessel") return undefined;
  const verified = VERIFIED_FERRIES[v.mmsi];
  // An MMSI follows the radio, so a different reported name must not inherit the curated service.
  if (verified && v.name?.trim().toUpperCase() === verified.name) return verified;
  for (const type of [v.particulars?.ship_type, v.particulars?.service]) {
    if (type && FERRY_TYPES.has(type.trim().toLowerCase().replace(/[‐‑‒–—]/g, "-"))) {
      // Enrichment can retain a former operator; current service copy needs a verified entry.
      return {};
    }
  }
  return undefined;
}
