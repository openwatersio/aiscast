import { describe, expect, it } from "vitest";
import { isValidImo } from "./ais";
import { coverKey, coverPhoto, fleetCard, FLEETS, fleetVessels, getFleet, parentPath, type Fleet } from "./fleets.server";

const FLEET_KEYS = ["id", "title", "summary", "description", "cover", "photos", "source", "sections", "children"];
const SECTION_KEYS = ["title", "subtitle", "avatar", "links", "vessels"];
const VESSEL_KEYS = ["name", "mmsi", "imo", "subtitle", "note", "photos", "links"];

describe("fleets", () => {
  it("nests fleets in groups by folder, the most vessels first", () => {
    const size = (id: string): number => {
      const f = getFleet(id)!;
      return f.sections ? fleetVessels(f).length : f.children.reduce((n, c) => n + size(c), 0);
    };
    for (const group of FLEETS.filter((f) => !f.sections)) {
      const sizes = group.children.map(size);
      expect(sizes, group.id).toEqual([...sizes].sort((a, b) => b - a));
    }
    expect(getFleet("")!.children[0]).toBe("cruise-ships");
    expect(getFleet("youtube")!.children).toEqual(["youtube/sailors", "youtube/cruisers"]);
    expect(getFleet("youtube")!.sections).toBeUndefined();
    expect(getFleet("cruise-ships/royal-caribbean")!.sections).toBeDefined();
    expect(parentPath("cruise-ships/royal-caribbean")).toBe("/fleets/cruise-ships");
    expect(parentPath("cruise-ships")).toBe("/fleets");
    expect(parentPath("")).toBe("/vessels");
  });

  for (const fleet of FLEETS) {
    it(`${fleet.id || "the top group"} is well formed`, () => {
      expect(fleet.id).toMatch(/^([a-z0-9-]+(\/[a-z0-9-]+)*)?$/);
      // YAML reads a bare `2024`, `yes` or date as something other than text, which a page cannot show.
      const text = (v: unknown, what: string) => expect(typeof v === "string" && v.length > 0, `${fleet.id}: ${what} ${String(v)}`).toBe(true);
      const optional = (v: unknown, what: string) => v === undefined || text(v, what);
      text(fleet.title, "title");
      text(fleet.summary, "summary");
      optional(fleet.description, "description");
      const links = (l: Record<string, unknown> | undefined) => Object.entries(l ?? {}).forEach(([k, v]) => (text(k, "link label"), text(v, `link ${k}`)));
      // A typo'd key is otherwise silently ignored.
      for (const key of Object.keys(fleet)) expect(FLEET_KEYS, `${fleet.id}: ${key}`).toContain(key);
      // Commons file names, which a typo or a pasted URL would make into a lookup for nothing.
      const photos = [...(fleet.photos ?? []), ...fleetVessels(fleet).flatMap((v) => v.photos ?? [])];
      for (const name of photos) expect(name, `${fleet.id}: photo`).toMatch(/^(File:)?[^/|#:]+\.(jpe?g|png|webp|tiff?)$/i);
      if (!fleet.sections) {
        expect(fleet.children.length, "a group with no fleets").toBeGreaterThan(0);
        return;
      }
      links(fleet.source);
      const vessels = fleetVessels(fleet);
      expect(vessels.length).toBeGreaterThan(0);
      for (const s of fleet.sections) {
        for (const key of Object.keys(s)) expect(SECTION_KEYS, `${fleet.id}: ${key}`).toContain(key);
        optional(s.title, "section title");
        optional(s.subtitle, "section subtitle");
        // Only YouTube's own image hosts, which youtube-avatars.mjs writes.
        if (s.avatar !== undefined) expect(s.avatar, `${fleet.id}: avatar`).toMatch(/^https:\/\/yt3\.(ggpht|googleusercontent)\.com\//);
        links(s.links);
      }
      if (fleet.cover) expect(vessels.filter((v) => v.mmsi).map((v) => v.name), "cover needs an MMSI").toContain(fleet.cover);
      const mmsis = vessels.flatMap((v) => (v.mmsi ? [v.mmsi] : []));
      expect(new Set(mmsis).size, "an MMSI listed twice").toBe(mmsis.length);
      for (const v of vessels) {
        // YAML reads a bare `1914` or `null` as something other than a name.
        expect(typeof v.name === "string" && v.name.length > 0, JSON.stringify(v)).toBe(true);
        for (const key of Object.keys(v)) expect(VESSEL_KEYS, `${v.name}: ${key}`).toContain(key);
        optional(v.subtitle, `${v.name} subtitle`);
        optional(v.note, `${v.name} note`);
        links(v.links);
        // Numbers, not strings: the map's features are keyed by number.
        if (v.mmsi != null) expect(typeof v.mmsi === "number" && /^[2-7]\d{8}$/.test(String(v.mmsi)), `${v.name} MMSI ${v.mmsi}`).toBe(true);
        if (v.imo != null) expect(typeof v.imo === "number" && isValidImo(v.imo), `${v.name} IMO ${v.imo}`).toBe(true);
      }
      const urls = [...Object.values(fleet.source ?? {}), ...fleet.sections.flatMap((s) => [s, ...s.vessels]).flatMap((x) => Object.values(x.links ?? {}))];
      for (const url of urls) expect(url).toMatch(/^https:\/\//);
    });
  }
});

it("covers a fleet with its named vessel, or the first it can find on the map, and a group with its first fleet's", () => {
  const fleet = (cover?: string): Fleet => ({
    id: "t",
    title: "T",
    summary: "S",
    cover,
    children: [],
    sections: [{ vessels: [{ name: "Unconfirmed" }, { name: "First", mmsi: 368478440 }, { name: "Named", mmsi: 319225400, imo: 9857298 }] }],
  });
  expect(coverKey(fleet("Named"))).toBe("9857298");
  expect(coverKey(fleet())).toBe("368478440");
  expect(coverKey(fleet("Unconfirmed"))).toBe("368478440");
  const cruise = getFleet("cruise-ships")!;
  expect(coverKey(cruise)).toBe(coverKey(getFleet(cruise.children[0]!)!));
  // A group's card shows the first photo its fleets name, here Royal Caribbean's, the only one that names any.
  expect(coverPhoto(cruise)).toBe(getFleet("cruise-ships/royal-caribbean")!.photos![0]);
  expect(coverPhoto(getFleet("youtube")!)).toBeUndefined();
});

it("covers a fleet without photos with its channels' avatars, and a group with all of its fleets'", () => {
  const sailors = getFleet("youtube/sailors")!;
  expect(fleetCard(sailors).avatars).toEqual(sailors.sections!.flatMap((s) => s.avatar ?? []).slice(0, 8));
  expect(fleetCard(getFleet("youtube")!).avatars![0]).toBe(fleetCard(getFleet(getFleet("youtube")!.children[0]!)!).avatars![0]);
  expect(fleetCard(getFleet("cruise-ships/royal-caribbean")!).avatars).toEqual([]);
});
