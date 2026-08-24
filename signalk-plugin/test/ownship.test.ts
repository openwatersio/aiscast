import { Parser } from "@signalk/nmea0183-signalk";
import { describe, expect, it } from "vitest";
import { OwnShip } from "../src/ownship.js";
import { fakeApp } from "./fake-app.js";

const T = 1_700_000_000_000;
const MIN = 60_000;
const POS = { "navigation.position": { value: { latitude: 59.9, longitude: 10.7 } } };

// Decoded through the server's own AIS parser: { path: value }, with the path-less mmsi/name objects merged in.
function decode(sentence: string): Record<string, unknown> {
  const d = new Parser().parse(sentence) as { updates: { values: { path: string; value: unknown }[] }[] };
  const out: Record<string, unknown> = {};
  for (const { path, value } of d.updates[0].values) {
    if (path === "") Object.assign(out, value);
    else out[path] = value;
  }
  return out;
}

function setup(self: Record<string, unknown>) {
  const app = fakeApp(self);
  const out: string[] = [];
  const own = new OwnShip(app, (s) => out.push(s));
  return { app, out, own };
}

describe("own ship", () => {
  it("builds a class B position report and name from Signal K, leaving unknown fields as not available", () => {
    const { out, own } = setup({ ...POS, name: "TEST" });
    own.tick(T);
    expect(out).toHaveLength(2);
    expect(out[0]).toMatch(/^!AIVDO,/);
    const pos = decode(out[0]);
    expect(pos.mmsi).toBe("123456789");
    expect(pos["navigation.position"]).toEqual({ latitude: 59.9, longitude: 10.7 });
    expect(pos["sensors.ais.class"]).toBe("B");
    expect(pos["navigation.speedOverGround"]).toBeUndefined();
    expect(pos["navigation.courseOverGroundTrue"]).toBeUndefined();
    expect(pos["navigation.headingTrue"]).toBeUndefined();
    expect(decode(out[1]).name).toBe("TEST");
  });

  it("converts units and derives true heading from magnetic heading and variation", () => {
    const { out, own } = setup({
      ...POS,
      "navigation.speedOverGround.value": 2.6751,
      "navigation.courseOverGroundTrue.value": Math.PI / 2,
      "navigation.headingMagnetic.value": 1.5,
      "navigation.magneticVariation.value": 0.15,
    });
    own.tick(T);
    const pos = decode(out[0]);
    expect(pos["navigation.speedOverGround"]).toBeCloseTo(2.6751, 1);
    expect(pos["navigation.courseOverGroundTrue"]).toBeCloseTo(Math.PI / 2, 2);
    expect(pos["navigation.headingTrue"]).toBeCloseTo(1.65, 1);
  });

  it("sends static part B with callsign, type, and dimensions when the vessel is described", () => {
    const { out, own } = setup({
      ...POS,
      "communication.callsignVhf": "WDK1234",
      "design.aisShipType.value.id": 36,
      "design.length.value.overall": 11,
      "design.beam.value": 4,
      "sensors.gps.fromBow.value": 5,
      "sensors.gps.fromCenter.value": 1, // Signal K: 1 m to port → dimC (to port) 1, dimD (to starboard) 3
    });
    own.tick(T);
    expect(out).toHaveLength(2); // no name: part A skipped
    const b = decode(out[1]);
    expect(b.communication).toEqual({ callsignVhf: "WDK1234" });
    expect(b["design.aisShipType"]).toMatchObject({ id: 36 });
    expect(b["design.length"]).toEqual({ overall: 11 });
    expect(b["design.beam"]).toBe(4);
    expect(b["sensors.ais.fromBow"]).toBe(5);
    // the parser derives fromCenter as beam/2 - dimD, signed opposite to the schema: -1 here means dimD is 3
    expect(b["sensors.ais.fromCenter"]).toBe(-1);
  });

  it("repeats only when the position changes, with static data every six minutes", () => {
    const { app, out, own } = setup({ ...POS, name: "TEST" });
    own.tick(T);
    own.tick(T + MIN);
    expect(out).toHaveLength(2);
    app.self["navigation.position"] = { value: { latitude: 59.91, longitude: 10.7 } };
    own.tick(T + 2 * MIN);
    expect(out).toHaveLength(3);
    app.self["navigation.position"] = { value: { latitude: 59.92, longitude: 10.7 } };
    own.tick(T + 3 * MIN);
    expect(out).toHaveLength(4);
    own.tick(T + 6 * MIN); // position unchanged: skipped, but static is still due
    expect(out).toHaveLength(5);
    expect(decode(out[4]).name).toBe("TEST");
  });

  it("wraps a course near due north to 0 instead of the 360 not-available sentinel", () => {
    const rad = (deg: number) => (deg * Math.PI) / 180;
    const { out, own } = setup({
      ...POS,
      "navigation.courseOverGroundTrue.value": rad(359.97),
      "navigation.headingTrue.value": rad(359.7),
    });
    own.tick(T);
    const pos = decode(out[0]);
    expect(pos["navigation.courseOverGroundTrue"]).toBeCloseTo(0, 5);
    expect(pos["navigation.headingTrue"]).toBeCloseTo(0, 5);
  });

  it("stays quiet while a transponder is heard, without an MMSI, or without a fix", () => {
    const { out, own } = setup(POS);
    own.heard(T);
    own.tick(T + MIN);
    expect(out).toHaveLength(0);
    own.tick(T + 5 * MIN);
    expect(out).toHaveLength(1);

    const noMmsi = setup({ ...POS, mmsi: undefined });
    noMmsi.own.tick(T);
    expect(noMmsi.out).toHaveLength(0);

    const nullIsland = setup({ "navigation.position": { value: { latitude: 0, longitude: 0 } } });
    nullIsland.own.tick(T);
    expect(nullIsland.out).toHaveLength(0);
  });
});
