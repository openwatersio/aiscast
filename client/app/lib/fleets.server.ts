import { load } from "js-yaml";
import { mediaKey } from "./media";

/** One vessel in a fleet. Without an MMSI it is listed but cannot be found on the map. */
export interface FleetVessel {
  name: string;
  mmsi?: number;
  imo?: number;
  subtitle?: string;
  note?: string;
  /** Wikimedia Commons file names, such as interiors, shown with the vessel. */
  photos?: string[];
  /** Label to URL, such as where the vessel's identity or ownership was checked. */
  links?: Record<string, string>;
}

export interface FleetSection {
  title?: string;
  subtitle?: string;
  /** The image of who the section is, such as a YouTube channel's avatar, from scripts/youtube-avatars.mjs. */
  avatar?: string;
  links?: Record<string, string>;
  vessels: FleetVessel[];
}

/**
 * A fleet, which lists vessels, or a group of fleets, which is a folder with an index.yaml.
 * Its id is its path under fleets/, which is its URL under /fleets: `cruise-ships/royal-caribbean`.
 * The root group, the Fleets page itself, has the id "".
 */
export interface Fleet {
  id: string;
  title: string;
  summary: string;
  description?: string;
  /** The name of the vessel whose photo covers the fleet; the first vessel with an MMSI otherwise. */
  cover?: string;
  /** Wikimedia Commons file names that head the fleet's page and cover its card, ahead of `cover`. */
  photos?: string[];
  /** Where the fleet's members come from, label to URL: `Wikidata: https://www.wikidata.org/wiki/Q929872`. */
  source?: Record<string, string>;
  /** A fleet's vessels. A group has none. */
  sections?: FleetSection[];
  /** A group's fleets and groups, the most vessels first. */
  children: string[];
}

// Parsed here, on the server, so the YAML parser stays out of the browser's bundle.
const files = import.meta.glob<string>("../fleets/**/*.yaml", { query: "?raw", import: "default", eager: true });

const parentOf = (id: string) => id.slice(0, Math.max(0, id.lastIndexOf("/")));

const byId = new Map<string, Fleet>([["", { id: "", title: "Fleets", summary: "Collections of notable vessels, on the map.", children: [] }]]);
for (const [path, text] of Object.entries(files).sort(([a], [b]) => a.localeCompare(b))) {
  const id = path.slice("../fleets/".length, -".yaml".length).replace(/(^|\/)index$/, "");
  const parsed = load(text) as Omit<Fleet, "id" | "children"> & { vessels?: FleetVessel[] };
  // Both come from where the file is, not from what it says.
  if ("id" in parsed || "children" in parsed) throw new Error(`fleets/${id}: id and children are not keys a fleet sets`);
  const { vessels, sections, ...rest } = parsed;
  if (vessels && sections) throw new Error(`fleets/${id}.yaml: list vessels or sections, not both`);
  const group = path.endsWith("/index.yaml");
  // A group's cover and photos are its first fleet's.
  if (group && (vessels || sections || "cover" in parsed || "photos" in parsed || "source" in parsed))
    throw new Error(`fleets/${id}/index.yaml: a group gives only a title, summary and description`);
  if (byId.has(id)) throw new Error(`fleets/${id}: both ${id}.yaml and ${id}/index.yaml`);
  byId.set(id, { ...rest, id, children: [], ...(group ? {} : { sections: sections ?? [{ vessels: vessels ?? [] }] }) });
}
for (const f of byId.values()) {
  if (!f.id) continue;
  const parent = byId.get(parentOf(f.id));
  if (!parent) throw new Error(`fleets/${parentOf(f.id)}/index.yaml is missing`);
  parent.children.push(f.id);
}

// Each group's fleets, the biggest first: a group counts every vessel in the fleets under it.
const size = (f: Fleet): number => (f.sections ? fleetVessels(f).length : f.children.reduce((n, id) => n + size(byId.get(id)!), 0));
for (const f of byId.values()) f.children.sort((a, b) => size(byId.get(b)!) - size(byId.get(a)!) || a.localeCompare(b));

export const FLEETS: Fleet[] = [...byId.values()];

// The fleets each vessel is in, by MMSI, for its page's cards.
const byMmsi = new Map<number, string[]>();
for (const f of FLEETS)
  for (const v of fleetVessels(f))
    if (v.mmsi && !byMmsi.get(v.mmsi)?.includes(f.id)) byMmsi.set(v.mmsi, [...(byMmsi.get(v.mmsi) ?? []), f.id]);

export function fleetsWith(mmsi: number): Fleet[] {
  return (byMmsi.get(mmsi) ?? []).map((id) => byId.get(id)!);
}

/** The avatars of a fleet's sections, or of every fleet in a group, in order. */
function avatars(fleet: Fleet): string[] {
  if (fleet.sections) return fleet.sections.flatMap((s) => s.avatar ?? []);
  return fleet.children.flatMap((id) => avatars(byId.get(id)!));
}

/** What a fleet's card shows: FleetCardData. */
export function fleetCard(fleet: Fleet) {
  const count = fleet.sections ? fleetVessels(fleet).length : fleet.children.length;
  return {
    path: fleetPath(fleet.id),
    title: fleet.title,
    summary: fleet.summary,
    cover: coverKey(fleet),
    photo: coverPhoto(fleet),
    avatars: avatars(fleet).slice(0, 8),
    count: `${count} ${fleet.sections ? "vessel" : "fleet"}${count === 1 ? "" : "s"}`,
  };
}

export function getFleet(id: string): Fleet | undefined {
  return byId.get(id);
}

/** The fleet's URL path, under the /ais/ basename. */
export function fleetPath(id: string): string {
  return id ? `/fleets/${id}` : "/fleets";
}

/** Where the fleet's back button goes: the group it is in, or the map for the Fleets page. */
export function parentPath(id: string): string {
  return id ? fleetPath(parentOf(id)) : "/vessels";
}

export function fleetVessels(fleet: Fleet): FleetVessel[] {
  return fleet.sections?.flatMap((s) => s.vessels) ?? [];
}

/** The media key of the fleet's cover photo, as useMedia takes it. A group takes its first fleet's. */
export function coverKey(fleet: Fleet): string | undefined {
  if (!fleet.sections) {
    for (const child of fleet.children) {
      const key = coverKey(byId.get(child)!);
      if (key) return key;
    }
    return undefined;
  }
  const tracked = fleetVessels(fleet).filter((v) => v.mmsi);
  const v = tracked.find((v) => v.name === fleet.cover) ?? tracked[0];
  return v?.mmsi ? mediaKey(v.imo, v.mmsi) : undefined;
}

/**
 * Every Commons file a fleet's page shows: its own photos and its vessels', or for a group, the
 * first photo of each fleet it shows as a card.
 */
export function photoNames(fleet: Fleet): string[] {
  if (!fleet.sections) return fleet.children.flatMap((id) => coverPhoto(byId.get(id)!) ?? []);
  return [...(fleet.photos ?? []), ...fleetVessels(fleet).flatMap((v) => v.photos ?? [])];
}

/** The Commons file that covers a fleet's card: its first photo, or for a group, its first fleet's. */
export function coverPhoto(fleet: Fleet): string | undefined {
  if (fleet.sections) return fleet.photos?.[0];
  for (const child of fleet.children) {
    const name = coverPhoto(byId.get(child)!);
    if (name) return name;
  }
  return undefined;
}
