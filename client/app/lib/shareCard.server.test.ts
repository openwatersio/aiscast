import { afterEach, describe, expect, it, vi } from "vitest";
import type { Station } from "./api";
import { stationCard, stationCardId, stationCardProps } from "./shareCard.server";

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
});
