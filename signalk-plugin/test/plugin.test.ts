import { mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Plugin } from "@signalk/server-api";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import createPlugin, { type Config } from "../src/index.js";
import { fakeApp, type FakeApp } from "./fake-app.js";
import { startFakeServer, type FakeServer } from "./fake-server.js";

const VDM = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"; // MMSI 227006760, position report
const VDM2 = "!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"; // MMSI 258857000, position report
const VDO = "!AIVDO,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
async function until(cond: () => boolean | Promise<boolean>, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await cond())) {
    if (Date.now() > end) throw new Error("condition not met");
    await sleep(20);
  }
}

let server: FakeServer;
let app: FakeApp;
let plugin: Plugin;

function start(config: Partial<Config> = {}): Promise<void> {
  plugin = createPlugin(app);
  plugin.start({ advanced: { server: server.url }, ...config }, () => {});
  return until(() => server.clients.length > 0);
}

beforeEach(async () => {
  server = await startFakeServer();
  app = fakeApp({ "navigation.position": { value: { latitude: 59.9, longitude: 10.7 } } });
});

afterEach(async () => {
  await plugin?.stop?.();
  await server.close();
});

describe("identity", () => {
  it("creates a keypair once and mints a personal token for it", async () => {
    await start();
    const jwk = JSON.parse(await readFile(join(app.dataDir, "identity.json"), "utf8"));
    expect(jwk.crv).toBe("Ed25519");
    expect(server.keyRequests).toEqual([{ pubkey: jwk.x }]);
    expect(Buffer.from(jwk.x, "base64url")).toHaveLength(32);
    await plugin.stop!();

    await start(); // same key, cached token: no second /v1/keys call
    expect(server.keyRequests).toHaveLength(1);
    expect(JSON.parse(await readFile(join(app.dataDir, "identity.json"), "utf8")).x).toBe(jwk.x);
  });

  it("keeps receiving without a token when the server cannot mint one", async () => {
    server.keysStatus = 501;
    await start();
    expect(app.errors[0]).toMatch(/No token/);
    await server.waitForFrame((f) => f.type === "subscribe");
  });
});

describe("uplink", () => {
  it("publishes targets and own ship as received with a c: TAG block, ignoring non-AIS sentences", async () => {
    await start();
    app.emit("nmea0183", VDM);
    app.emit("nmea0183", VDO);
    app.emit("nmea0183", "$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230394,003.1,W*6A");
    const f = await server.waitForFrame((f) => f.type === "publish");
    expect(f.nmea).toHaveLength(2);
    expect((f.nmea as string[])[0]).toMatch(new RegExp(`^\\\\c:\\d{13}\\*[0-9A-F]{2}\\\\${VDM.replace(/[*?]/g, "\\$&")}$`));
    expect((f.nmea as string[])[1]).toContain(VDO);
    await sleep(50);
    expect(server.frames.filter((f) => f.type === "publish")).toHaveLength(1);
  });

  it("keeps own ship private when that switch is off", async () => {
    await start({ share: { ownShip: false } });
    app.emit("nmea0183", VDO);
    app.emit("nmea0183", VDM);
    const f = await server.waitForFrame((f) => f.type === "publish");
    expect((f.nmea as string[]).map((s) => s.replace(/^\\[^\\]*\\/, ""))).toEqual([VDM]);
  });

  it("shares a position report built from Signal K when asked, tagged s:self, independent of the transponder switch", async () => {
    await start({ share: { ownShip: false, position: true } });
    const f = await server.waitForFrame((f) => f.type === "publish");
    expect((f.nmea as string[])[0]).toMatch(/^\\s:self,c:\d{13}\*[0-9A-F]{2}\\!AIVDO,/);
  });

  it("queues to disk while offline and replays oldest-first on reconnect", async () => {
    await start();
    const port = Number(new URL(server.url).port);
    await server.close();
    await until(() => server.clients.length === 0);
    app.emit("nmea0183", VDM);
    app.emit("nmea0183", VDM2);
    await sleep(100);
    // queue files are written on a timer; force the backlog through by restarting the plugin (stop flushes)
    await plugin.stop!();
    const files = await readdir(join(app.dataDir, "queue"));
    expect(files).toHaveLength(1);
    expect(JSON.parse(await readFile(join(app.dataDir, "queue", files[0]), "utf8"))).toHaveLength(2);

    server = await startFakeServer(port);
    await start();
    const f = await server.waitForFrame((f) => f.type === "publish");
    expect((f.nmea as string[]).map((s) => s.replace(/^\\[^\\]*\\/, ""))).toEqual([VDM, VDM2]);
    expect(f.replay).toBe(true);
    await until(async () => (await readdir(join(app.dataDir, "queue"))).length === 0);
  });

  it("drains a pre-existing backlog before live sentences", async () => {
    await mkdir(join(app.dataDir, "queue"), { recursive: true });
    await writeFile(join(app.dataDir, "queue", "1.json"), JSON.stringify([VDM2]));
    await start();
    app.emit("nmea0183", VDM);
    await until(() => server.frames.filter((f) => f.type === "publish").length === 2);
    const [first, second] = server.frames.filter((f) => f.type === "publish");
    expect((first.nmea as string[])[0]).toBe(VDM2);
    expect((second.nmea as string[])[0]).toContain(VDM);
  });

  it("keeps a file on disk when the socket drops mid-drain, without duplicating it", async () => {
    await mkdir(join(app.dataDir, "queue"), { recursive: true });
    await writeFile(join(app.dataDir, "queue", "1.json"), JSON.stringify([VDM2]));
    server.ack = false;
    await start();
    await server.waitForFrame((f) => f.type === "publish" && f.replay === true);
    for (const c of server.clients) c.terminate();
    await until(() => server.clients.length === 0);
    await sleep(100);
    await plugin.stop!(); // flushes anything the drain wrongly copied into memory
    const files = await readdir(join(app.dataDir, "queue"));
    expect(files).toEqual(["1.json"]);
    expect(JSON.parse(await readFile(join(app.dataDir, "queue", "1.json"), "utf8"))).toEqual([VDM2]);
  });

  it("re-encodes NMEA 2000 AIS PGNs as sentences tagged s:n2k", async () => {
    await start();
    app.emit("N2KAnalyzerOut", {
      pgn: 129038,
      fields: { userId: 244670316, longitude: 4.4, latitude: 52.1, sog: 5.1, cog: 1.2, heading: 1.3, navStatus: "Under way using engine", aisTransceiverInformation: "Channel A VDL reception" },
    });
    app.emit("N2KAnalyzerOut", { pgn: 129038, fields: { userId: 123456789, longitude: 4.4, latitude: 52.1, aisTransceiverInformation: "Own information not broadcast" } });
    // own MMSI while transmitting: the transponder stamps "VDL transmission", still own ship
    app.emit("N2KAnalyzerOut", { pgn: 129038, fields: { userId: 123456789, longitude: 4.4, latitude: 52.1, aisTransceiverInformation: "Channel A VDL transmission" } });
    app.emit("N2KAnalyzerOut", { pgn: 127250, fields: { heading: 1 } });
    const f = await server.waitForFrame((f) => f.type === "publish");
    const nmea = f.nmea as string[];
    expect(nmea).toHaveLength(3);
    expect(nmea[0]).toMatch(/^\\s:n2k,c:\d{13}\*[0-9A-F]{2}\\!AIVDM,1,1,,A,/);
    expect(nmea[1]).toMatch(/\\!AIVDO,1,1,,A,/);
    expect(nmea[2]).toMatch(/\\!AIVDO,1,1,,A,/);
    const { Parser } = await import("@signalk/nmea0183-signalk");
    const d = new Parser().parse(nmea[0].replace(/^\\[^\\]*\\/, ""));
    expect(d?.context).toBe("vessels.urn:mrn:imo:mmsi:244670316");
    const pos = d?.updates[0].values.find((v) => v.path === "navigation.position")?.value as { latitude: number; longitude: number };
    expect(pos.latitude).toBeCloseTo(52.1, 3);
    expect(pos.longitude).toBeCloseTo(4.4, 3);
  });

  it("never republishes a payload that came from aiscast", async () => {
    await start({ receive: { mode: "always" } });
    await server.waitForFrame((f) => f.type === "subscribe");
    server.send({ type: "event", source: "kystverket", nmea: [VDM], mmsi: 227006760, msg_type: "PositionReport", lat: 1, lon: 1 });
    await until(() => app.deltas.length === 1);
    app.emit("nmea0183", VDM); // e.g. signalk-n2kais-to-nmea0183 re-emitting our injected target
    app.emit("nmea0183", VDM2);
    const f = await server.waitForFrame((f) => f.type === "publish");
    expect((f.nmea as string[]).map((s) => s.replace(/^\\[^\\]*\\/, ""))).toEqual([VDM2]);
  });
});

describe("buddy boats", () => {
  const buddylist = (urns: string[], enabled = true) =>
    writeFile(
      join(app.dataDir, "..", "signalk-buddylist-plugin.json"),
      JSON.stringify({ configuration: { buddies: urns.map((urn) => ({ urn })) }, enabled }),
    );

  it("follows buddies from the buddylist plugin, and keeps them when local AIS silences the radius", async () => {
    await buddylist(["urn:mrn:imo:mmsi:258857000", "urn:mrn:imo:mmsi:227006760"]);
    await start();
    const sub = await server.waitForFrame((f) => f.type === "subscribe" && f.mmsi != null && f.bbox != null);
    expect(sub.mmsi).toEqual([227006760, 258857000]);
    app.emit("nmea0183", VDM); // auto mode drops the box but keeps the buddies
    const only = await server.waitForFrame((f) => f.type === "subscribe" && f.bbox == null);
    expect(only.mmsi).toEqual([227006760, 258857000]);
    expect(server.frames.find((f) => f.type === "unsubscribe")).toBeUndefined();
  });

  it("follows buddies and injects them even with receive off", async () => {
    await buddylist(["urn:mrn:imo:mmsi:258857000"]);
    await start({ receive: { mode: "off" } });
    const sub = await server.waitForFrame((f) => f.type === "subscribe");
    expect(sub).toEqual({ type: "subscribe", snapshot: true, mmsi: [258857000] });
    server.send({ type: "event", source: "kystverket", nmea: [VDM2], mmsi: 258857000, msg_type: "PositionReport", lat: 1, lon: 1 });
    await until(() => app.deltas.length === 1);
    expect(app.deltas[0].context).toBe("vessels.urn:mrn:imo:mmsi:258857000");
  });

  it("trims the list to the welcome frame's mmsi cap instead of losing the whole subscription", async () => {
    await buddylist(["urn:mrn:imo:mmsi:258857000", "urn:mrn:imo:mmsi:227006760"]);
    await start({ receive: { mode: "off" } });
    await server.waitForFrame((f) => f.type === "subscribe" && (f.mmsi as number[]).length === 2);
    server.send({ type: "welcome", limits: { mmsis: 1 } });
    const trimmed = await server.waitForFrame((f) => f.type === "subscribe" && (f.mmsi as number[]).length === 1);
    expect(trimmed.mmsi).toEqual([227006760]);
  });

  it("ignores itself, malformed URNs, and a disabled buddylist plugin", async () => {
    await buddylist(["urn:mrn:imo:mmsi:123456789", "urn:mrn:imo:mmsi:12345", "bogus"]);
    await start({ receive: { mode: "off" } });
    await sleep(100);
    expect(server.frames.find((f) => f.type === "subscribe")).toBeUndefined();
  });
});

describe("config UI", () => {
  it("disables the GPS position option without an MMSI and says why", () => {
    type Ui = { share: { position: Record<string, unknown> } };
    const ui = (createPlugin(app).uiSchema as () => Ui)();
    expect(ui.share.position["ui:disabled"]).toBeUndefined();
    const bare = fakeApp({ mmsi: undefined });
    const noMmsi = (createPlugin(bare).uiSchema as () => Ui)();
    expect(noMmsi.share.position["ui:disabled"]).toBe(true);
    expect(noMmsi.share.position["ui:help"]).toMatch(/MMSI/);
  });
});

describe("downlink", () => {
  it("auto mode subscribes around the vessel while no AIS is heard locally, and unsubscribes when it is", async () => {
    await start();
    const sub = await server.waitForFrame((f) => f.type === "subscribe");
    const [minLat, minLon, maxLat, maxLon] = (sub.bbox as number[][])[0];
    expect(minLat).toBeLessThan(59.9);
    expect(maxLat).toBeGreaterThan(59.9);
    expect(minLon).toBeLessThan(10.7);
    expect(maxLon).toBeGreaterThan(10.7);
    app.emit("nmea0183", VDM);
    await server.waitForFrame((f) => f.type === "unsubscribe");
  });

  it("does not subscribe without a position", async () => {
    delete app.self["navigation.position"];
    await start({ receive: { mode: "always" } });
    await sleep(100);
    expect(server.frames.find((f) => f.type === "subscribe")).toBeUndefined();
  });

  it("injects events through the server parser with its own $source and the event time, dropping self and echoes", async () => {
    await start({ receive: { mode: "always" } });
    await server.waitForFrame((f) => f.type === "subscribe");
    const time = "2026-08-20T15:25:54.342871Z";
    server.send({ type: "event", time, source: "kystverket", nmea: [VDM], mmsi: 227006760, msg_type: "PositionReport", lat: 1, lon: 1 });
    server.send({ type: "event", time, source: "kystverket", nmea: [VDM2], mmsi: 123456789, msg_type: "PositionReport", lat: 1, lon: 1 }); // self MMSI
    const pub = server.keyRequests[0].pubkey;
    server.send({ type: "event", time, source: `v1:ed25519:${pub}`, nmea: [VDM2], mmsi: 258857000, msg_type: "PositionReport", lat: 1, lon: 1 }); // our echo
    await until(() => app.deltas.length >= 1);
    await sleep(50);
    expect(app.deltas).toHaveLength(1);
    const d = app.deltas[0];
    expect(d.context).toBe("vessels.urn:mrn:imo:mmsi:227006760");
    expect(d.updates[0].$source).toBe("signalk-aiscast.net");
    expect(d.updates[0].timestamp).toBe(time);
    expect((d.updates[0] as { source?: unknown }).source).toBeUndefined();
    const paths = d.updates.flatMap((u) => ("values" in u ? u.values.map((v) => v.path) : []));
    expect(paths).toContain("navigation.position");
  });

  it("in always mode leaves a target alone that the local receiver updated recently", async () => {
    await start({ receive: { mode: "always" } });
    await server.waitForFrame((f) => f.type === "subscribe");
    app.model["vessels.urn:mrn:imo:mmsi:227006760.navigation.position"] = {
      $source: "ais-receiver.AI",
      timestamp: new Date().toISOString(),
    };
    server.send({ type: "event", source: "kystverket", nmea: [VDM], mmsi: 227006760, msg_type: "PositionReport", lat: 1, lon: 1 });
    server.send({ type: "event", source: "kystverket", nmea: [VDM2], mmsi: 258857000, msg_type: "PositionReport", lat: 1, lon: 1 });
    await until(() => app.deltas.length === 1);
    expect(app.deltas[0].context).toBe("vessels.urn:mrn:imo:mmsi:258857000");
  });
});
