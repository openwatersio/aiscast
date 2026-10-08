import { afterEach, describe, expect, it, vi } from "vitest";

// workers-og answers at once and renders into the body. This one renders a few bytes, or fails
// partway, as a render can.
const og = vi.hoisted(() => ({ fail: false }));
vi.mock("workers-og", () => ({
  ImageResponse: class extends Response {
    constructor() {
      super(
        new ReadableStream({
          pull: (c) => {
            if (og.fail) return c.error(new Error("render failed"));
            c.enqueue(new Uint8Array([0x89, 0x50, 0x4e, 0x47]));
            c.close();
          },
        }),
        { headers: { "content-type": "image/png" } },
      );
    }
  },
}));
import { stationCardPath } from "./ais";
import { getStation, type Station } from "./api";
import { serveStationCard, shareCard, stationCard, stationCardId, stationCardProps } from "./shareCard.server";

const station = (over: Partial<Station>): Station => ({
  station: "station:abc",
  source: "station:abc",
  events: { last_24h: 0, last_7d: 0 },
  duplicates: 0,
  vessels: 3,
  positions: 0,
  first_seen: "",
  last_seen: "",
  last_age_s: 0,
  ...over,
});

afterEach(() => {
  vi.unstubAllGlobals();
  og.fail = false;
});

describe("station cards", () => {
  it("are a station's address with .png, whatever its id holds", () => {
    expect(stationCardId("/ais/stations/digitraffic.png")).toBe("digitraffic");
    expect(stationCardId("/ais/stations/kystverket/2573010.png")).toBe("kystverket/2573010");
    expect(stationCardId("/ais/stations/station%3Aed25519%3Aabc.png")).toBe("station:ed25519:abc");
    expect(stationCardId("/ais/stations/digitraffic")).toBeUndefined();
    expect(stationCardId("/ais/stations/.png")).toBeUndefined();
    expect(stationCardId("/ais/stations/%E0.png")).toBeUndefined();
  });

  it("say what the station is, where, and what it heard in 24 hours", () => {
    expect(stationCardProps(station({ name: "Harbor Light", name_from: "operator", near: "Oslo", vessels_24h: 1234, vessels_exclusive_24h: 87 }))).toEqual({
      title: "Harbor Light",
      subtitle: "Volunteer receiver near Oslo",
      period: "Last 24 hours",
      stats: [
        { value: "1,234", label: "vessels" },
        { value: "87", label: "unique vessels" },
      ],
    });
    expect(stationCardProps(station({ name: "AZURIS", name_from: "vessel", mmsi: 368168720, near: "Andalusia, Spain" })).subtitle).toBe(
      "Volunteer receiver near Andalusia, Spain",
    );
    // A title that is the place already says where.
    expect(stationCardProps(station({ near: "Oslo" })).subtitle).toBe("Volunteer receiver");
    const feed = stationCardProps(station({ station: "digitraffic", source: "digitraffic" }));
    expect([feed.title, feed.subtitle, feed.stats[0]!.value, feed.stats[1]!.value]).toEqual(["Digitraffic (Finland)", "Data feed", "3", "0"]);
  });

  it("are a 404 for a station the API does not know, and a 503 when it cannot say", async () => {
    const auth = { api: "https://api.test" };
    vi.stubGlobal("fetch", vi.fn(async () => Response.json([station({ station: "digitraffic" })])));
    expect((await stationCard(auth, "nowhere/0")).status).toBe(404);
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 502 })));
    expect((await stationCard(auth, "digitraffic")).status).toBe(503);
  });

  it("read the station from the list, not its own answer with every vessel it last heard", async () => {
    const api = vi.fn(async (_url: string) => Response.json([station({ station: "aishub", source: "aishub" })]));
    vi.stubGlobal("fetch", api);
    expect((await stationCard({ api: "https://api.test" }, "aishub")).status).toBe(200);
    expect(api.mock.calls.map(([url]) => url)).toEqual(["https://api.test/v1/stations"]);
  });

  it("send a receiver's tagged path to the receiver's card, as its page does", async () => {
    const res = await stationCard({ api: "https://api.test" }, "station:mmsi:368168720/n2k");
    expect(res.status).toBe(301);
    expect(res.headers.get("location")).toBe("/ais/stations/station%3Ammsi%3A368168720.png");
    // A receiver's id is encoded, so a "#" or "?" in it stays in the path.
    const odd = await stationCard({ api: "https://api.test" }, "station:harbor#1/n2k");
    expect(odd.headers.get("location")).toBe("/ais/stations/station%3Aharbor%231.png");
  });
  it("share one path, and one cache entry, whatever spelling of the id asked for it", () => {
    for (const path of ["/ais/stations/digitraffic.png", "/ais/stations/%64igitraffic.png"]) {
      expect(stationCardPath(stationCardId(path)!)).toBe("/stations/digitraffic.png");
    }
    expect(stationCardPath("station:ed25519:a/b c")).toBe("/stations/station%3Aed25519%3Aa/b%20c.png");
  });

  it("never ask the API for a path that leaves the station's", async () => {
    const fetch = vi.fn(async () => Response.json({}));
    vi.stubGlobal("fetch", fetch);
    const id = stationCardId("/ais/stations/..%2F..%2Fv1%2Fvessels.png")!;
    expect(id).toBe("../../v1/vessels");
    expect(await getStation({ api: "https://api.test" }, id)).toBeUndefined();
    expect(fetch).not.toHaveBeenCalled();
  });
  it("fail when the render fails, rather than answer 200 with a broken image", async () => {
    og.fail = true;
    await expect(shareCard({ title: "Harbor Light", stats: [] })).rejects.toThrow("render failed");
  });
});

describe("the Worker's station cards", () => {
  const auth = { api: "https://api.test" };
  const list = [station({ station: "digitraffic", source: "digitraffic" })];

  /** The edge cache, and the puts waiting on waitUntil. */
  function edge() {
    const store = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    const cache = {
      match: async (key: string) => store.get(key)?.clone(),
      put: async (key: string, res: Response) => void store.set(key, res),
    } as unknown as Cache;
    return { store, cache, waitUntil: (p: Promise<unknown>) => void pending.push(p), settle: () => Promise.all(pending) };
  }
  const ask = (e: ReturnType<typeof edge>, path: string, method = "GET") => {
    const url = new URL(`https://openwaters.io${path}`);
    return serveStationCard(new Request(url, { method }), url, auth, e.cache, e.waitUntil);
  };

  it("answer only card paths", () => {
    expect(ask(edge(), "/ais/stations/digitraffic")).toBeUndefined();
    expect(ask(edge(), "/ais/vessels/230000000.png")).toBeUndefined();
  });

  it("keep one card per station, whichever spelling of its path asked", async () => {
    const api = vi.fn(async () => Response.json(list));
    vi.stubGlobal("fetch", api);
    const e = edge();
    const first = await ask(e, "/ais/stations/digitraffic.png")!;
    await e.settle();
    const again = await ask(e, "/ais/stations/%64igitraffic.png")!;
    expect([first.status, again.status]).toEqual([200, 200]);
    expect(api).toHaveBeenCalledTimes(1);
    expect([...e.store.keys()]).toEqual(["https://openwaters.io/ais/stations/digitraffic.png"]);
    expect(first.headers.get("cache-control")).toBe("public, max-age=3600");
  });

  it("keep a station the API does not know for five minutes, and an outage not at all", async () => {
    const e = edge();
    vi.stubGlobal("fetch", vi.fn(async () => Response.json(list)));
    expect((await ask(e, "/ais/stations/nowhere.png")!).headers.get("cache-control")).toBe("public, max-age=300");
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 502 })));
    expect((await ask(e, "/ais/stations/digitraffic.png")!).status).toBe(503);
    await e.settle();
    expect([...e.store.keys()]).toEqual(["https://openwaters.io/ais/stations/nowhere.png"]);
  });

  it("answer GET and HEAD, and refuse other methods", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Response.json(list)));
    expect((await ask(edge(), "/ais/stations/digitraffic.png", "HEAD")!).status).toBe(200);
    const res = await ask(edge(), "/ais/stations/digitraffic.png", "POST")!;
    expect([res.status, res.headers.get("allow")]).toEqual([405, "GET, HEAD"]);
  });
});
