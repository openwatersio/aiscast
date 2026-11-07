import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { forgetStationIndex, nearestStations, slackwaterStations, type StationIndex } from "./slackwater.server";

const INDEX: StationIndex = {
  origin: "https://slackwater.xyz",
  stations: [
    { kind: "current", path: "/currents/ca/bc/active-pass/", name: "Active Pass", latitude: 48.8617, longitude: -123.3133 },
    { kind: "current", path: "/currents/ca/bc/porlier-pass/", name: "Porlier Pass", latitude: 49.0133, longitude: -123.585 },
    { kind: "tide", path: "/tides/ca/bc/fulford-harbour/", name: "Fulford Harbour", latitude: 48.7694, longitude: -123.45 },
    { kind: "tide", path: "/tides/ca/bc/crofton/", name: "Crofton", latitude: 48.8667, longitude: -123.6333 },
    { kind: "tide", path: "/tides/us/wa/seattle/", name: "Seattle", latitude: 47.6026, longitude: -122.3393 },
  ],
};

describe("nearestStations", () => {
  it("picks the nearest tide station and the nearest current station, tide first", () => {
    // Quinsam's crossing between Crofton and Vesuvius Bay.
    const near = nearestStations(INDEX, 48.8657, -123.63606);
    expect(near.map((s) => [s.kind, s.name, s.url])).toEqual([
      ["tide", "Crofton", "https://slackwater.xyz/tides/ca/bc/crofton/"],
      ["current", "Porlier Pass", "https://slackwater.xyz/currents/ca/bc/porlier-pass/"],
    ]);
  });

  it("leaves out a kind with nothing within reach", () => {
    // Seattle's waterfront: a tide station on the doorstep, no current station within 50 km.
    expect(nearestStations(INDEX, 47.6, -122.34).map((s) => s.name)).toEqual(["Seattle"]);
  });

  it("offers nothing in open water", () => {
    expect(nearestStations(INDEX, 40, -150)).toEqual([]);
  });

  it("links only station paths, never whatever else an index might carry", () => {
    const index: StationIndex = {
      origin: "https://evil.example",
      stations: [
        { kind: "tide", path: "//evil.example/tides/", name: "Elsewhere", latitude: 48.8657, longitude: -123.63606 },
        { kind: "tide", path: "/tides/ca/bc/crofton/", name: "Crofton", latitude: 48.8667, longitude: -123.6333 },
      ],
    };
    expect(nearestStations(index, 48.8657, -123.63606).map((s) => s.url)).toEqual(["https://slackwater.xyz/tides/ca/bc/crofton/"]);
  });
});

describe("slackwaterStations", () => {
  beforeEach(() => forgetStationIndex());
  afterEach(() => vi.unstubAllGlobals());

  it("fetches the index once and answers from it after that", async () => {
    const fetch = vi.fn(async () => Response.json(INDEX));
    vi.stubGlobal("fetch", fetch);
    expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
    expect((await slackwaterStations(47.6, -122.34))?.map((s) => s.name)).toEqual(["Seattle"]);
    expect(fetch).toHaveBeenCalledOnce();
    expect(String((fetch.mock.calls[0] as unknown[])[0])).toBe("https://slackwater.xyz/stations.json");
  });

  it.each([
    ["is missing", () => new Response("Not found", { status: 404 })],
    ["is not JSON", () => new Response("<html>", { status: 200 })],
    ["has no stations", () => Response.json({ origin: "https://slackwater.xyz" })],
  ])("says it has no index, warns, and does not throw, when the index %s", async (_, answer) => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.stubGlobal("fetch", vi.fn(async () => answer()));
    await expect(slackwaterStations(48.8657, -123.63606)).resolves.toBeUndefined();
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/^slackwater: /));
    warn.mockRestore();
  });

  it("says it has no index, and warns, when Slackwater cannot be reached", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("network"); }));
    await expect(slackwaterStations(48.8657, -123.63606)).resolves.toBeUndefined();
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/^slackwater: /));
    warn.mockRestore();
  });

  it("tells no station within reach apart from no index", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Response.json(INDEX)));
    await expect(slackwaterStations(40, -150)).resolves.toEqual([]);
  });

  it("warns when an index loses entries to a format it doesn't read", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const renamed = { kind: "tide", path: "/tides/ca/bc/sidney/", name: "Sidney", lat: 48.65, lon: -123.4 };
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ ...INDEX, stations: [renamed, ...INDEX.stations] })));
    await slackwaterStations(48.8657, -123.63606);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/^slackwater: .*1 of 6/));
    warn.mockRestore();
  });

  it("drops a bad entry rather than the whole index", async () => {
    const bad = { kind: "wave", path: "/tides/x/", name: "Wave", latitude: null, longitude: 1 };
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ ...INDEX, stations: [bad, ...INDEX.stations] })));
    expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
  });

  it("reads the index slackwater.xyz publishes, attribution table and all, and links without it", async () => {
    // The live shape: each station points into a list of notices, which the station page displays.
    const published = {
      origin: "https://slackwater.xyz",
      license: "https://github.com/openwatersio/slackwater-database#license",
      attributions: ["Slackwater database (https://github.com/openwatersio/slackwater-database)"],
      stations: [
        ...INDEX.stations.map((s) => ({ ...s, attribution: 0 })),
        // A station whose notice index points nowhere is still linked: the vessel page shows none.
        { kind: "tide", path: "/tides/ca/bc/vesuvius/", name: "Vesuvius", latitude: 48.8657, longitude: -123.63606, attribution: 9 },
      ],
    };
    vi.stubGlobal("fetch", vi.fn(async () => Response.json(published)));
    expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Vesuvius", "Porlier Pass"]);
  });

  it("drops an entry whose kind and path disagree, or that has no name", async () => {
    const nearby = { latitude: 48.8657, longitude: -123.63606 };
    const misfiled = { ...nearby, kind: "tide", path: "/currents/ca/bc/stuart-channel/", name: "Stuart Channel" };
    const nameless = { ...nearby, kind: "current", path: "/currents/ca/bc/nameless/", name: " " };
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ ...INDEX, stations: [misfiled, nameless, ...INDEX.stations] })));
    expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
  });

  it("leaves caching to slackwater.xyz's own headers, and names itself", async () => {
    // Overriding the edge's TTL would keep a bad 200 for as long as a good one.
    const fetch = vi.fn(async () => Response.json(INDEX));
    vi.stubGlobal("fetch", fetch);
    await slackwaterStations(48.8657, -123.63606);
    const init = (fetch.mock.calls[0] as unknown[])[1] as { cf?: unknown; headers?: Record<string, string> };
    expect(init.cf).toBeUndefined();
    expect(init.headers?.["user-agent"]).toMatch(/^aiscast-web\//);
  });

  describe("over time", () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());

    it("keeps no page waiting more than two seconds, and shares one fetch between pages", async () => {
      const fetch = vi.fn(() => new Promise<Response>(() => {}));
      vi.stubGlobal("fetch", fetch);
      const first = slackwaterStations(48.8657, -123.63606);
      const second = slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(2000);
      await expect(first).resolves.toBeUndefined();
      await expect(second).resolves.toBeUndefined();
      expect(fetch).toHaveBeenCalledOnce();
    });

    it("gives up on a fetch that never settles, as one whose request was canceled can be", async () => {
      const fetch = vi.fn(() => new Promise<Response>(() => {}));
      vi.stubGlobal("fetch", fetch);
      void slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(10_001);
      void slackwaterStations(48.8657, -123.63606);
      expect(fetch).toHaveBeenCalledTimes(2);
    });

    it("hands its fetch to the Worker to keep alive past the page that started it", async () => {
      vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
      const keepAlive = vi.fn();
      void slackwaterStations(48.8657, -123.63606, keepAlive);
      void slackwaterStations(48.8657, -123.63606, keepAlive);
      // One fetch, kept alive once.
      expect(keepAlive).toHaveBeenCalledOnce();
      expect(keepAlive.mock.calls[0]?.[0]).toBeInstanceOf(Promise);
    });

    it("answers from a stale copy at once while it refreshes, rather than make the page wait", async () => {
      vi.stubGlobal("fetch", vi.fn(async () => Response.json(INDEX)));
      await slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(6 * 3_600_000 + 1);
      const refresh = vi.fn(() => new Promise<Response>(() => {}));
      vi.stubGlobal("fetch", refresh);
      const settled = vi.fn();
      void slackwaterStations(48.8657, -123.63606).then(settled);
      // No timer advanced: the answer must not be waiting on the refresh.
      await vi.advanceTimersByTimeAsync(0);
      expect(settled.mock.calls[0]?.[0].map((s: { name: string }) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
      expect(refresh).toHaveBeenCalledOnce();
    });

    it("lets an index too slow for one page arrive for the next", async () => {
      const fetch = vi.fn(() => new Promise<Response>((resolve) => setTimeout(() => resolve(Response.json(INDEX)), 3000)));
      vi.stubGlobal("fetch", fetch);
      const first = slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(2000);
      await expect(first).resolves.toBeUndefined();
      await vi.advanceTimersByTimeAsync(1000);
      expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
      expect(fetch).toHaveBeenCalledOnce();
    });

    it("waits five minutes after a failure before asking again", async () => {
      const fetch = vi.fn(async () => new Response("Not found", { status: 404 }));
      vi.stubGlobal("fetch", fetch);
      await slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(4 * 60_000);
      await slackwaterStations(48.8657, -123.63606);
      expect(fetch).toHaveBeenCalledOnce();
      await vi.advanceTimersByTimeAsync(60_000);
      await slackwaterStations(48.8657, -123.63606);
      expect(fetch).toHaveBeenCalledTimes(2);
    });

    it("keeps a good index through a failed refresh", async () => {
      vi.stubGlobal("fetch", vi.fn(async () => Response.json(INDEX)));
      await slackwaterStations(48.8657, -123.63606);
      await vi.advanceTimersByTimeAsync(6 * 3_600_000 + 1);
      const down = vi.fn(async () => new Response("", { status: 503 }));
      vi.stubGlobal("fetch", down);
      expect((await slackwaterStations(48.8657, -123.63606))?.map((s) => s.name)).toEqual(["Crofton", "Porlier Pass"]);
      expect(down).toHaveBeenCalledOnce();
    });

    it("stops linking a stale copy a day after it was fetched", async () => {
      vi.stubGlobal("fetch", vi.fn(async () => Response.json(INDEX)));
      await slackwaterStations(48.8657, -123.63606);
      vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 503 })));
      await vi.advanceTimersByTimeAsync(24 * 3_600_000 + 1);
      await expect(slackwaterStations(48.8657, -123.63606)).resolves.toBeUndefined();
    });
  });
});
