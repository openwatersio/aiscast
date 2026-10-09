import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fileTitle, type VesselMedia } from "./media";
import { mediaResponse, namedPhotos } from "./media.server";

const IMO = "9728916";
const thumb = (title: string) => `https://upload.wikimedia.org/thumb/${encodeURIComponent(title)}/960px-x.jpg`;

/** Commons and Wikidata as far as the lookup asks them: category members, file info, and P18 by IMO. */
function fakeWikimedia({
  categories,
  taken,
  p18,
  items,
  wikidataDown = false,
}: {
  categories: Record<string, string[]>;
  taken: Record<string, string>;
  p18?: string;
  /** The items search finds for the IMO, in the order it answers; one carrying `p18` when absent. */
  items?: Array<{ qid: string; p18?: string }>;
  wikidataDown?: boolean;
}) {
  return vi.fn(async (input: string | URL) => {
    const url = new URL(input);
    const q = url.searchParams;
    if (url.host === "www.wikidata.org") {
      if (wikidataDown) return new Response("", { status: 503 });
      const found = items ?? [{ qid: "Q52380920", p18 }];
      const pages = found.map((it) => ({ title: it.qid, pageprops: it.p18 ? { page_image_free: it.p18 } : undefined }));
      return Response.json({ query: { pages } });
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

  it("takes the photo of the lowest QID when an IMO is on more than one item", async () => {
    const items = [
      { qid: "Q107073444", p18: "Yacht.jpg" },
      { qid: "Q52380920", p18: "Hamburg.jpg" },
    ];
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken, items }));
    expect(pages((await lookup()).media)[0]).toBe("File:Hamburg.jpg");
  });

  it("takes no other item's photo when the lowest QID has none", async () => {
    const items = [{ qid: "Q107073444", p18: "Hamburg.jpg" }, { qid: "Q52380920" }];
    vi.stubGlobal("fetch", fakeWikimedia({ categories: triton, taken, items }));
    expect(pages((await lookup()).media)).toEqual(["File:Yacht.jpg", "File:Rotterdam.jpg", "File:Hamburg.jpg"]);
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

describe("named photos", () => {
  it("keys each photo by fileTitle of the name as a fleet writes it, and answers again from the cache", async () => {
    const fetch = fakeWikimedia({ categories: {}, taken: {} });
    vi.stubGlobal("fetch", fetch);
    const names = ["Central_Park.jpg", "File:Bridge, Radiance 03.jpg"];
    const photos = await namedPhotos(names, "https://openwaters.io/ais/fleets/x");
    for (const name of names) expect(photos[fileTitle(name)]?.page, name).toContain(fileTitle(name).replace(/^File:/, ""));
    expect(await namedPhotos([...names].reverse(), "https://openwaters.io/ais/fleets/y")).toEqual(photos);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("answers with nothing, not an error, when Commons fails or answers with something other than JSON", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 503 })));
    expect(await namedPhotos(["A.jpg"], "https://openwaters.io/ais/fleets/x")).toEqual({});
    vi.stubGlobal("fetch", vi.fn(async () => new Response("<html>busy</html>", { status: 200 })));
    expect(await namedPhotos(["B.jpg"], "https://openwaters.io/ais/fleets/x")).toEqual({});
  });

  it("reads a name however its File: prefix is written", () => {
    expect(fileTitle("file:Central_Park.jpg")).toBe("File:Central Park.jpg");
    expect(fileTitle("Central Park.jpg")).toBe("File:Central Park.jpg");
  });
});
