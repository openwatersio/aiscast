import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { VesselMedia } from "./media";
import { mediaResponse } from "./media.server";

const IMO = "9728916";
const thumb = (title: string) => `https://upload.wikimedia.org/thumb/${encodeURIComponent(title)}/960px-x.jpg`;

/** Commons and Wikidata as far as the lookup asks them: category members, file info, and P18 by IMO. */
function fakeWikimedia({
  categories,
  taken,
  p18,
  wikidataDown = false,
}: {
  categories: Record<string, string[]>;
  taken: Record<string, string>;
  p18?: string;
  wikidataDown?: boolean;
}) {
  return vi.fn(async (input: string | URL) => {
    const url = new URL(input);
    const q = url.searchParams;
    if (url.host === "www.wikidata.org") {
      if (wikidataDown) return new Response("", { status: 503 });
      const pageprops = p18 ? { page_image_free: p18 } : undefined;
      return Response.json({ query: { pages: [{ title: "Q52380920", pageprops }] } });
    }
    if (q.get("list") === "categorymembers") {
      const members = (categories[q.get("cmtitle")!] ?? []).map((title) => ({ ns: title.startsWith("Category:") ? 14 : 6, title }));
      return Response.json({ query: { categorymembers: members } });
    }
    const titles = q.get("titles")!.split("|");
    // Commons spells a title its own way, as it does for Wikidata's underscores.
    const normalized = titles.filter((t) => t.includes("_")).map((t) => ({ from: t, to: t.replace(/_/g, " ") }));
    const pages = titles.map((t) => {
      const title = t.replace(/_/g, " ");
      return {
        title,
        imageinfo: [
          {
            thumburl: thumb(title),
            thumbwidth: 960,
            thumbheight: 540,
            descriptionurl: `https://commons.wikimedia.org/wiki/${title}`,
            mime: "image/jpeg",
            extmetadata: { Artist: { value: "someone" }, DateTimeOriginal: { value: taken[title] } },
          },
        ],
      };
    });
    return Response.json({ query: { normalized, pages } });
  });
}

let stored: Map<string, Response>;

beforeEach(() => {
  stored = new Map();
  vi.stubGlobal("caches", {
    default: {
      match: async (req: Request) => stored.get(req.url)?.clone(),
      put: async (req: Request, res: Response) => void stored.set(req.url, res),
    },
  });
});

afterEach(() => vi.unstubAllGlobals());

async function lookup(key = IMO) {
  const res = await mediaResponse(key, "https://openwaters.io/ais/vessels/media/" + key);
  return { media: (await res.json()) as VesselMedia, cacheControl: res.headers.get("cache-control") };
}

const pages = (media: VesselMedia) => media.photos.map((p) => p.page.replace("https://commons.wikimedia.org/wiki/", ""));

const triton = {
  [`Category:IMO ${IMO}`]: ["Category:Triton (ship, 2016)"],
  "Category:Triton (ship, 2016)": ["File:Yacht.jpg", "File:Rotterdam.jpg", "File:Hamburg.jpg"],
};
const taken = { "File:Yacht.jpg": "2020-08-14", "File:Rotterdam.jpg": "2018-05-01", "File:Hamburg.jpg": "2016-09-01" };

describe("vessel photos", () => {
  it("leads with Wikidata's photo, then the newest", async () => {
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken, p18: "Rotterdam.jpg" }));
    const { media, cacheControl } = await lookup();
    expect(pages(media)).toEqual(["File:Rotterdam.jpg", "File:Yacht.jpg", "File:Hamburg.jpg"]);
    expect(media.links.commonsCategory).toBe(`https://commons.wikimedia.org/wiki/Category:IMO_${IMO}`);
    expect(cacheControl).toMatch(/max-age=604800/);
  });

  it("shows Wikidata's photo when the category does not hold it", async () => {
    const p18 = "Triton_(ship,_2016)_pic4.JPG";
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken, p18 }));
    const { media } = await lookup();
    expect(pages(media)).toEqual(["File:Triton (ship, 2016) pic4.JPG", "File:Yacht.jpg", "File:Rotterdam.jpg", "File:Hamburg.jpg"]);
  });

  it("orders by date alone when Wikidata has no photo", async () => {
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken }));
    const { media, cacheControl } = await lookup();
    expect(pages(media)).toEqual(["File:Yacht.jpg", "File:Rotterdam.jpg", "File:Hamburg.jpg"]);
    expect(cacheControl).toMatch(/max-age=604800/);
  });

  it("serves the category's photos when Wikidata fails, and asks again soon", async () => {
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken, wikidataDown: true }));
    const { media, cacheControl } = await lookup();
    expect(pages(media)).toEqual(["File:Yacht.jpg", "File:Rotterdam.jpg", "File:Hamburg.jpg"]);
    expect(cacheControl).toBe("public, max-age=900");
  });

  it("shows Wikidata's photo alone when the category is empty, without linking the category", async () => {
    vi.stubGlobal("fetch", fakeWikimedia({ categories: {}, taken, p18: "Rotterdam.jpg" }));
    const { media } = await lookup();
    expect(pages(media)).toEqual(["File:Rotterdam.jpg"]);
    expect(media.links).toEqual({});
  });

  it("does not ask Wikidata for an MMSI", async () => {
    const fetch = fakeWikimedia({ categories: { "Category:MMSI 249515000": ["File:Yacht.jpg"] }, taken, p18: "Rotterdam.jpg" });
    vi.stubGlobal("fetch", fetch);
    const { media } = await lookup("249515000");
    expect(pages(media)).toEqual(["File:Yacht.jpg"]);
    expect(fetch.mock.calls.some(([u]) => new URL(u).host === "www.wikidata.org")).toBe(false);
  });
});
