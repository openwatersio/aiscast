import { afterEach, describe, expect, it, vi } from "vitest";

// workers-og answers at once and renders into the body. This one takes a few milliseconds and
// counts the renders in progress. It can fail partway, as a render can, or never finish, as one
// whose request was canceled does not.
const og = vi.hoisted(() => ({ fail: false, hang: false, active: 0, most: 0 }));
vi.mock("workers-og", () => ({
  ImageResponse: class extends Response {
    constructor() {
      const hang = og.hang;
      og.hang = false;
      og.most = Math.max(og.most, ++og.active);
      super(
        new ReadableStream({
          pull: async (c) => {
            if (hang) return new Promise(() => {});
            await new Promise((r) => setTimeout(r, 5));
            og.active--;
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
import { getStation, type Station, type Stats, type VesselProps } from "./api";
import {
  networkCard,
  networkCardProps,
  RENDER_WAIT_MS,
  serveCard,
  shareCard,
  stationCard,
  stationCardId,
  stationCardProps,
  stationsCard,
  stationsCardProps,
  vesselCard,
  vesselCardMmsi,
  vesselCardProps,
} from "./shareCard.server";

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
  vi.useRealTimers();
  Object.assign(og, { fail: false, hang: false, active: 0, most: 0 });
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
    expect(await getStation({ api: "https://api.test" }, "../../v1/vessels")).toBeUndefined();
    expect(await getStation({ api: "https://api.test" }, "station:x/./y")).toBeUndefined();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("are not a path whose id holds a dot segment, which would key another station's card", () => {
    expect(stationCardId("/ais/stations/station%3Ammsi%3A1%2F..%2Fdigitraffic.png")).toBeUndefined();
    expect(stationCardId("/ais/stations/x%2F..%2F..%2F__edge%2Fdark%2Fvessels%2F1.png")).toBeUndefined();
    expect(stationCardId("/ais/stations/.%2Fdigitraffic.png")).toBeUndefined();
    // A dot inside a segment is not one.
    expect(stationCardId("/ais/stations/a..b.png")).toBe("a..b");
  });
  it("fail when the render fails, rather than answer 200 with a broken image", async () => {
    og.fail = true;
    await expect(shareCard({ title: "Harbor Light", stats: [] })).rejects.toThrow("render failed");
  });

  it("render one at a time, since overlapping renders can corrupt each other's layout", async () => {
    const first = shareCard({ title: "a", stats: [] });
    // Started a moment apart, as requests are, rather than in the same tick.
    await new Promise((r) => setTimeout(r, 1));
    const cards = await Promise.all([first, shareCard({ title: "b", stats: [] }), shareCard({ title: "c", stats: [] })]);
    expect(cards.map((c) => c.status)).toEqual([200, 200, 200]);
    expect(og.most).toBe(1);
  });

  it("wait for a render that never finishes only so long", async () => {
    vi.useFakeTimers();
    og.hang = true;
    void shareCard({ title: "canceled", stats: [] });
    await vi.advanceTimersByTimeAsync(1);
    const next = shareCard({ title: "next", stats: [] });
    await vi.advanceTimersByTimeAsync(RENDER_WAIT_MS - 100);
    expect(og.active).toBe(1); // still waiting its turn
    await vi.advanceTimersByTimeAsync(200);
    expect((await next).status).toBe(200);
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
    return serveCard(new Request(url, { method }), url, auth, e.cache, e.waitUntil);
  };

  it("answer only card paths", () => {
    expect(ask(edge(), "/ais/stations/digitraffic")).toBeUndefined();
    expect(ask(edge(), "/ais/vessels/230000000-viking-grace.png")).toBeUndefined();
    expect(ask(edge(), "/ais/network")).toBeUndefined();
    // Its key would resolve to Digitraffic's card, so it is the page's to 404.
    expect(ask(edge(), "/ais/stations/station%3Ammsi%3A1%2F..%2Fdigitraffic.png")).toBeUndefined();
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

describe("vessel cards", () => {
  const vessel = (over: Partial<VesselProps>): VesselProps => ({
    mmsi: 230000000,
    kind: "vessel",
    seen: "2026-10-08T12:00:00Z",
    source: "digitraffic",
    station: "digitraffic",
    msg_type: "PositionReport",
    ...over,
  });

  it("are a vessel's MMSI with .png, and nothing else under /vessels", () => {
    expect(vesselCardMmsi("/ais/vessels/230000000.png")).toBe(230000000);
    expect(vesselCardMmsi("/ais/vessels/023000000.png")).toBe(23000000);
    expect(vesselCardMmsi("/ais/vessels/230000000-viking-grace.png")).toBeUndefined();
    expect(vesselCardMmsi("/ais/vessels/0.png")).toBeUndefined();
    expect(vesselCardMmsi("/ais/vessels/1234567890.png")).toBeUndefined();
    expect(vesselCardMmsi("/ais/vessels/media/9241061.png")).toBeUndefined();
  });

  it("say what the vessel is and how big, with nothing that goes stale in an hour", () => {
    const props = vesselCardProps(
      vessel({ name: "VIKING GRACE", type: 60, flag: "FI", to_bow: 150, to_stern: 68, to_port: 16, to_starboard: 16, draught: 6.8, particulars: { year_built: 2013 } }),
    );
    expect(props).toEqual({
      title: "VIKING GRACE",
      subtitle: "Passenger · Finland",
      stats: [
        { value: "218 m", label: "length" },
        { value: "32 m", label: "beam" },
        { value: "2013", label: "built" },
      ],
    });
  });

  it("fill out with the vessel's numbers when it reports no size", () => {
    // 511 is the most the field holds: "that or more", a size nobody knows.
    const props = vesselCardProps(vessel({ to_bow: 511, to_stern: 0, to_port: 0, to_starboard: 0, imo: 9606900 }));
    expect([props.title, props.subtitle]).toEqual(["MMSI 230000000", undefined]);
    expect(props.stats).toEqual([
      { value: "230000000", label: "MMSI" },
      { value: "9606900", label: "IMO" },
    ]);
  });

  it("are a 404 for an MMSI the network has never heard, and a 503 when the API cannot say", async () => {
    const auth = { api: "https://api.test" };
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 404 })));
    expect((await vesselCard(auth, 230000000)).status).toBe(404);
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 502 })));
    expect((await vesselCard(auth, 230000000)).status).toBe(503);
  });
});

describe("network and station list cards", () => {
  const stats = {
    vessels: { total: 0, active: 0, with_position: 0, by_kind: {}, last_24h: 41234 },
    events: { per_second: 0, last_24h: 12_345_678, last_7d: 0 },
  } as unknown as Stats;
  const stations = [
    station({ station: "digitraffic", source: "digitraffic", vessels_exclusive_24h: 300 }),
    station({ station: "station:a", source: "station:a", vessels_exclusive_24h: 12 }),
    station({ station: "udp:b", source: "udp:b", vessels_exclusive_24h: 3 }),
    // Heard two days ago: not one of the last 24 hours'.
    station({ station: "station:old", source: "station:old", last_age_s: 2 * 86400, vessels_exclusive_24h: 50 }),
  ];

  it("count the network's last 24 hours", () => {
    expect(networkCardProps(stats, stations).stats).toEqual([
      { value: "41,234", label: "vessels" },
      { value: "12.3M", label: "messages" },
      // The volunteer receivers, as the station list's card counts them.
      { value: "2", label: "stations" },
    ]);
    // A server without the vessel record has no 24-hour count, so the card leaves it out.
    const bare = { ...stats, vessels: { ...stats.vessels, last_24h: undefined } } as Stats;
    expect(networkCardProps(bare, stations).stats.map((s) => s.label)).toEqual(["messages", "stations"]);
  });

  it("count the feeds and volunteer stations heard in the last 24 hours, each once, and what only one heard", () => {
    expect(stationsCardProps(stations).stats).toEqual([
      { value: "1", label: "feeds" },
      { value: "2", label: "stations" },
      { value: "315", label: "vessels" },
    ]);
  });
});

describe("the Worker's other cards", () => {
  const auth = { api: "https://api.test" };
  function edge() {
    const store = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    const cache = {
      match: async (key: string) => store.get(key)?.clone(),
      put: async (key: string, res: Response) => void store.set(key, res),
    } as unknown as Cache;
    return { store, cache, waitUntil: (p: Promise<unknown>) => void pending.push(p), settle: () => Promise.all(pending) };
  }
  const ask = (e: ReturnType<typeof edge>, path: string) => {
    const url = new URL(`https://openwaters.io${path}`);
    return serveCard(new Request(url), url, auth, e.cache, e.waitUntil);
  };

  it("keep one card per vessel, whichever spelling of its MMSI asked", async () => {
    const api = vi.fn(async () => Response.json({ type: "Feature", geometry: null, properties: { mmsi: 23000000, kind: "vessel", seen: "", source: "", station: "", msg_type: "" } }));
    vi.stubGlobal("fetch", api);
    const e = edge();
    expect((await ask(e, "/ais/vessels/23000000.png")!).status).toBe(200);
    await e.settle();
    expect((await ask(e, "/ais/vessels/023000000.png")!).status).toBe(200);
    expect(api).toHaveBeenCalledTimes(1);
    expect([...e.store.keys()]).toEqual(["https://openwaters.io/ais/vessels/23000000.png"]);
  });

  it("draw the network and station list from the API, kept under their own paths", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        Response.json(url.endsWith("/v1/stats") ? { vessels: { last_24h: 1 }, events: { last_24h: 1 } } : [station({})]),
      ),
    );
    const e = edge();
    expect((await ask(e, "/ais/network.png")!).status).toBe(200);
    expect((await ask(e, "/ais/stations.png")!).status).toBe(200);
    await e.settle();
    expect([...e.store.keys()].sort()).toEqual(["https://openwaters.io/ais/network.png", "https://openwaters.io/ais/stations.png"]);
  });
});

describe("cards drawn from the station list", () => {
  const auth = { api: "https://api.test" };
  const answer = (list: Station[]) =>
    vi.stubGlobal("fetch", vi.fn(async (url: string) => Response.json(url.endsWith("/v1/stats") ? { vessels: { last_24h: 9 }, events: { last_24h: 9 } } : list)));
  // Just after the server starts: messages counted, every vessel count still 0.
  const unloaded = [
    station({ station: "digitraffic", source: "digitraffic", events: { last_24h: 5000, last_7d: 5000 }, vessels: 0, vessels_24h: 0 }),
    station({ station: "station:a", source: "station:a", events: { last_24h: 40, last_7d: 40 }, vessels: 0, vessels_24h: 0 }),
  ];

  it("are an outage, which is not kept, while the list's counts are missing", async () => {
    answer(unloaded);
    expect((await stationsCard(auth)).status).toBe(503);
    expect((await stationCard(auth, "station:a")).status).toBe(503);
    // The network card's numbers don't come from the stations' vessel counts.
    expect((await networkCard(auth)).status).toBe(200);
  });

  it("draw a station that hears only its own vessel once the rest are counted", async () => {
    answer([unloaded[0]!, { ...unloaded[0]!, station: "aishub", source: "aishub", vessels_24h: 900 }, unloaded[1]!]);
    expect((await stationCard(auth, "station:a")).status).toBe(200);
    expect((await stationsCard(auth)).status).toBe(200);
  });
});
