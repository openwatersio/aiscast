import { afterEach, describe, expect, it, vi } from "vitest";

// workers-og answers at once and renders into the body. This one fails partway, as a render can.
vi.mock("workers-og", () => ({
  ImageResponse: class extends Response {
    constructor() {
      super(new ReadableStream({ pull: (c) => c.error(new Error("render failed")) }), { headers: { "content-type": "image/png" } });
    }
  },
}));
import { stationCardPath } from "./ais";
import type { Station } from "./api";
import { shareCard, stationCard, stationCardId, stationCardProps } from "./shareCard.server";

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

afterEach(() => vi.unstubAllGlobals());

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
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 404 })));
    expect((await stationCard(auth, "nowhere/0")).status).toBe(404);
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 502 })));
    expect((await stationCard(auth, "digitraffic")).status).toBe(503);
  });

  it("send a receiver's tagged path to the receiver's card, as its page does", async () => {
    const res = await stationCard({ api: "https://api.test" }, "station:mmsi:368168720/n2k");
    expect(res.status).toBe(301);
    expect(res.headers.get("location")).toBe("/ais/stations/station:mmsi:368168720.png");
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
    expect((await stationCard({ api: "https://api.test" }, id)).status).toBe(404);
    expect(fetch).not.toHaveBeenCalled();
  });
  it("fail when the render fails, rather than answer 200 with a broken image", async () => {
    await expect(shareCard({ title: "Harbor Light", stats: [] })).rejects.toThrow("render failed");
  });
});
