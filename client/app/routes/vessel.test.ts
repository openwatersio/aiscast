import { RouterContextProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { VesselFeature } from "../lib/api";
import { serverEnv, serverWaitUntil } from "../lib/context";

const lookup = vi.hoisted(() => ({ stations: vi.fn() }));
vi.mock("../lib/api", async (real) => ({
  ...(await real<typeof import("../lib/api")>()),
  getVessel: vi.fn(async (): Promise<VesselFeature> => ({
    type: "Feature",
    id: 230000003,
    geometry: { type: "Point", coordinates: [-123.63606, 48.8657] },
    properties: { mmsi: 230000003, name: "TEST SHIP", kind: "vessel", type: 70, seen: "2026-11-06T12:00:00Z", source: "aishub", station: "aishub", msg_type: "ShipStaticData" },
  }) as VesselFeature),
}));
vi.mock("../lib/media.server", () => ({ firstPhoto: vi.fn(async () => undefined) }));
vi.mock("../lib/slackwater.server", () => ({ slackwaterStations: lookup.stations }));

const { headers, loader } = await import("./vessel");

type Loaded = { data: { stations: unknown }; init?: ResponseInit };

async function load(): Promise<Loaded> {
  const context = new RouterContextProvider();
  context.set(serverEnv, { api: "http://127.0.0.1:0" });
  const keepAlive = vi.fn();
  context.set(serverWaitUntil, keepAlive);
  const args = { params: { param: "230000003-test-ship" }, context, request: new Request("https://openwaters.io/ais/vessels/230000003-test-ship") };
  return (await loader(args as unknown as Parameters<typeof loader>[0])) as unknown as Loaded;
}

describe("vessel loader", () => {
  beforeEach(() => lookup.stations.mockReset());

  it("looks the stations up near the vessel's position, keeping the fetch alive with the Worker's waitUntil", async () => {
    const near = [{ kind: "tide", name: "Crofton", url: "https://slackwater.xyz/tides/ca/bc/crofton/", attribution: "Slackwater database" }];
    lookup.stations.mockResolvedValue(near);
    const { data, init } = await load();
    expect(lookup.stations).toHaveBeenCalledWith(48.8657, -123.63606, expect.any(Function));
    expect(data.stations).toEqual(near);
    // A complete page keeps the edge's usual minute.
    expect(new Headers(init?.headers).get("Cache-Control")).toBeNull();
  });

  it("asks the edge to keep a page rendered without the index for seconds, not the usual minute", async () => {
    lookup.stations.mockResolvedValue(undefined);
    const { data, init } = await load();
    expect(data.stations).toEqual([]);
    const sent = headers({ loaderHeaders: new Headers(init?.headers) } as unknown as Parameters<typeof headers>[0]);
    expect(new Headers(sent).get("Cache-Control")).toBe("public, max-age=10");
  });
});
