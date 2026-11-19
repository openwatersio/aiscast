import { afterEach, describe, expect, it, vi } from "vitest";
import { askNearbyStations, isStationId, nearbyPath, slackwaterHandoff } from "./slackwater";

afterEach(() => vi.unstubAllGlobals());

describe("nearbyPath", () => {
  it("rounds a position to about a kilometre, so nearby vessels share one answer", () => {
    expect(nearbyPath(47.555233, -122.232281)).toBe("/ais/vessels/slackwater/47.56,-122.23");
    expect(nearbyPath(47.5549, -122.2318)).toBe("/ais/vessels/slackwater/47.55,-122.23");
  });

  it("asks nothing without a position", () => {
    expect(nearbyPath(undefined, -122.2)).toBeUndefined();
    expect(nearbyPath(47.5, Number.NaN)).toBeUndefined();
  });
});

describe("askNearbyStations", () => {
  const crofton = { kind: "tide", name: "Crofton", url: "https://slackwater.xyz/tides/ca/bc/crofton/" };

  it("asks the Worker once per position, and answers from what it heard after that", async () => {
    const fetch = vi.fn(async () => Response.json({ stations: [crofton] }));
    vi.stubGlobal("fetch", fetch);
    const path = "/ais/vessels/slackwater/48.87,-123.64";
    await expect(askNearbyStations(path)).resolves.toEqual([crofton]);
    await expect(askNearbyStations(path)).resolves.toEqual([crofton]);
    expect(fetch).toHaveBeenCalledExactlyOnceWith(path);
  });

  it("shares one request between pages asking at once", async () => {
    const fetch = vi.fn(async () => Response.json({ stations: [crofton] }));
    vi.stubGlobal("fetch", fetch);
    const path = "/ais/vessels/slackwater/48.12,-123.43";
    const [a, b] = await Promise.all([askNearbyStations(path), askNearbyStations(path)]);
    expect(a).toEqual([crofton]);
    expect(b).toEqual([crofton]);
    expect(fetch).toHaveBeenCalledOnce();
  });

  it("offers no stations when the Worker can't say, and asks again next time", async () => {
    const fetch = vi.fn(async () => new Response("", { status: 503 }));
    vi.stubGlobal("fetch", fetch);
    const path = "/ais/vessels/slackwater/10.00,10.00";
    await expect(askNearbyStations(path)).resolves.toEqual([]);
    await askNearbyStations(path);
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("keeps only links to slackwater.xyz station pages, whatever the answer holds", async () => {
    const odd = [crofton, { kind: "tide", name: "Elsewhere", url: "https://evil.example/tides/" }, { kind: "wave", name: "W", url: "https://slackwater.xyz/tides/x/" }];
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ stations: odd })));
    await expect(askNearbyStations("/ais/vessels/slackwater/1.00,1.00")).resolves.toEqual([crofton]);
  });

  it("keeps a tide station's API ID for the widget, and drops a station whose ID could reach another path", async () => {
    const drawn = { ...crofton, id: "ticon/crofton_bc-7450-can-meds" };
    const odd = [
      drawn,
      { ...crofton, url: "https://slackwater.xyz/tides/ca/bc/a/", id: "noaa/../../admin" },
      { ...crofton, url: "https://slackwater.xyz/tides/ca/bc/b/", id: 9447130 },
      { kind: "current", name: "Porlier Pass", url: "https://slackwater.xyz/currents/ca/bc/porlier-pass/", id: "noaa/PUG1701" },
    ];
    vi.stubGlobal("fetch", vi.fn(async () => Response.json({ stations: odd })));
    await expect(askNearbyStations("/ais/vessels/slackwater/2.00,2.00")).resolves.toEqual([drawn]);
  });
});

describe("isStationId", () => {
  it.each(["noaa/9447130", "kartverket/ANX", "ticon/victoria_bc-543a-can-uhslc_rq", "ticon/a.b"])("takes %s", (id) => {
    expect(isStationId(id)).toBe(true);
  });

  // The widget asks for predictions only by `source/id`; a CHS ID has no source segment.
  it.each(["", "chs-dodd-narrows", "/noaa/1", "noaa/../x", "noaa/..", "noaa/.", "ticon/a/b", "noaa/1?x=1", "noaa/1#a", "noaa 1", "noaa/"])("refuses %j", (id) => {
    expect(isStationId(id)).toBe(false);
  });
});

describe("Slackwater handoff", () => {
  it.each([
    { type: "click", button: 0, detail: 1 },
    { type: "click", button: 0, detail: 0 },
    { type: "auxclick", button: 1, detail: 1 },
  ])("sends only the event name and page context for $type (detail $detail, button $button)", (event) => {
    for (const context of ["ferry", "vessel"] as const) {
      const plausible = vi.fn();
      vi.stubGlobal("window", { plausible });
      slackwaterHandoff(context)(event);
      expect(plausible).toHaveBeenCalledExactlyOnceWith("slackwater_handoff", { props: { context } });
    }
  });

  it("ignores right-clicks and does not require analytics for the link to work", () => {
    const plausible = vi.fn();
    vi.stubGlobal("window", { plausible });
    slackwaterHandoff("ferry")({ type: "auxclick", button: 2 });
    expect(plausible).not.toHaveBeenCalled();
    vi.stubGlobal("window", {});
    expect(() => slackwaterHandoff("vessel")({ type: "click", button: 0 })).not.toThrow();
  });
});
