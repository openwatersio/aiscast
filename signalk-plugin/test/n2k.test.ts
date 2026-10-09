import { setSupportsCamelCaseCacheEnabled } from "@canboat/ts-pgns";
import { Parser } from "@signalk/nmea0183-signalk";
import ggencoder from "ggencoder";
import { beforeAll, describe, expect, it } from "vitest";
import { n2kToSentence } from "../src/n2k.js";
import { fakeApp, type FakeApp } from "./fake-app.js";

// ts-pgns caches the first server-version check for the whole process; the old-server test needs it rechecked.
beforeAll(() => setSupportsCamelCaseCacheEnabled(false));

const MMSI = 244670316;
const deg = (d: number) => (d * Math.PI) / 180;

// The sentence as a Signal K consumer reads it: context plus each path's value, with the
// root-path objects (name, callsign, IMO) merged into one.
function decode(sentence: string | null) {
  expect(sentence).toMatch(/^!AIVD[MO],1,1,,A,/);
  const delta = new Parser().parse(sentence!)!;
  const values: Record<string, any> = {};
  const root: Record<string, any> = {};
  for (const { path, value } of delta.updates[0].values) {
    if (path === "") Object.assign(root, value);
    else values[path] = value;
  }
  return { context: delta.context, root, values };
}

function encode(app: FakeApp, pgn: number, fields: Record<string, unknown>) {
  return n2kToSentence(app, { pgn, fields });
}

const voyage = {
  userId: MMSI,
  imoNumber: 9074729,
  callsign: "PDZK",
  name: "NOORDERLICHT",
  typeOfShip: "Sailing",
  length: 46,
  beam: 7,
  positionReferenceFromStarboard: 3,
  positionReferenceFromBow: 10,
  draft: 3.2,
  destination: "TERSCHELLING",
};

describe("NMEA 2000 AIS re-encoding", () => {
  it("encodes a class A position report's status and rate of turn (129038), turning either way", () => {
    const report = (rateOfTurn: number) =>
      decode(encode(fakeApp(), 129038, { userId: MMSI, longitude: 4.4, latitude: 52.1, navStatus: "Under way using engine", rateOfTurn })).values;
    const port = report(-0.01); // about 34° a minute; AIS keeps it to a few percent
    expect(port["navigation.state"]).toBe("motoring");
    expect(port["navigation.rateOfTurn"]).toBeCloseTo(-0.01, 3);
    expect(report(0.01)["navigation.rateOfTurn"]).toBeCloseTo(0.01, 3);
    expect(report(0)["navigation.rateOfTurn"]).toBe(0);
    // Faster than the field can say: the most it holds, not a wrap into a turn the other way.
    const fast = encode(fakeApp(), 129038, { userId: MMSI, longitude: 4.4, latitude: 52.1, rateOfTurn: -0.5 });
    expect(new ggencoder.AisDecode(fast!)).toMatchObject({ rot: -127 });
  });

  it("leaves a class A position's status and rate of turn not available when NMEA 2000 lacks them", () => {
    const sentence = encode(fakeApp(), 129038, { userId: MMSI, longitude: 4.4, latitude: 52.1 });
    expect(new ggencoder.AisDecode(sentence!)).toMatchObject({ navstatus: 15, rot: -128 });
  });

  it("keeps a course and heading just short of north, at the tenth of a degree AIS carries", () => {
    const values = (cog: number, heading: number) =>
      decode(encode(fakeApp(), 129039, { userId: MMSI, longitude: 4.4, latitude: 52.1, sog: 3, cog: deg(cog), heading: deg(heading) })).values;
    const north = values(359.8, 359.8);
    expect(north["navigation.courseOverGroundTrue"]).toBeCloseTo(deg(359.8), 3);
    expect(north["navigation.headingTrue"]).toBe(0); // heading is whole degrees, and 360 is not one
    expect(values(68.76, 74)["navigation.courseOverGroundTrue"]).toBeCloseTo(deg(68.8), 3);
  });

  it("encodes a class B position report (129039) as message 18", () => {
    const { context, values } = decode(
      encode(fakeApp(), 129039, { userId: MMSI, longitude: 4.4, latitude: 52.1, sog: 3, cog: deg(69), heading: deg(74), positionAccuracy: "High" }),
    );
    expect(context).toBe(`vessels.urn:mrn:imo:mmsi:${MMSI}`);
    expect(values["sensors.ais.class"]).toBe("B");
    expect(values["navigation.position"]).toEqual({ longitude: 4.4, latitude: 52.1 });
    expect(values["navigation.speedOverGround"]).toBeCloseTo(3, 1);
    expect(values["navigation.courseOverGroundTrue"]).toBeCloseTo(deg(69), 3);
    expect(values["navigation.headingTrue"]).toBeCloseTo(deg(74), 3);
  });

  it("encodes class A static and voyage data (129794) as message 5", () => {
    const { root, values } = decode(encode(fakeApp(), 129794, voyage));
    expect(root.name).toBe("NOORDERLICHT");
    expect(root.communication.callsignVhf).toBe("PDZK");
    expect(root.registrations.imo).toBe("IMO 9074729");
    expect(values["design.aisShipType"]).toEqual({ id: 36, name: "Sailing" });
    expect(values["design.length"]).toEqual({ overall: 46 });
    expect(values["design.beam"]).toBe(7);
    expect(values["design.draft"]).toEqual({ current: 3.2 });
    expect(values["sensors.ais.fromBow"]).toBe(10);
    expect(values["sensors.ais.fromCenter"]).toBe(0.5); // 4 m to port, 3 m to starboard
    expect(values["navigation.destination.commonName"]).toBe("TERSCHELLING");
  });

  it("encodes class B static data part A (129809) as message 24 with the name", () => {
    const { root } = decode(encode(fakeApp(), 129809, { userId: MMSI, name: "NOORDERLICHT" }));
    expect(root).toEqual({ mmsi: String(MMSI), name: "NOORDERLICHT" });
  });

  it("encodes class B static data part B (129810) as message 24 with type, call sign, and size", () => {
    const { root, values } = decode(
      encode(fakeApp(), 129810, { userId: MMSI, typeOfShip: "Pleasure", callsign: "PDZK", length: 12, beam: 4, positionReferenceFromStarboard: 2, positionReferenceFromBow: 3 }),
    );
    expect(root.communication.callsignVhf).toBe("PDZK");
    expect(values["design.aisShipType"]).toEqual({ id: 37, name: "Pleasure" });
    expect(values["design.length"]).toEqual({ overall: 12 });
    expect(values["design.beam"]).toBe(4);
    expect(values["sensors.ais.fromBow"]).toBe(3);
    expect(values["sensors.ais.fromCenter"]).toBe(0);
  });

  it("encodes an aid to navigation report (129041) as message 21", () => {
    const sentence = encode(fakeApp(), 129041, {
      userId: 992446001,
      longitude: 4.4,
      latitude: 52.1,
      atonType: "Floating AtoN: cardinal N",
      atonName: "NOORD HINDER",
      positionAccuracy: "High",
      offPositionIndicator: "Yes",
      virtualAtonFlag: "No",
      assignedModeFlag: "Autonomous and continuous",
      lengthDiameter: 5,
      beamDiameter: 5,
      positionReferenceFromStarboardEdge: 2,
      positionReferenceFromTrueNorthFacingEdge: 2,
    });
    const { context, root, values } = decode(sentence);
    expect(context).toBe("atons.urn:mrn:imo:mmsi:992446001");
    expect(root.name).toBe("NOORD HINDER");
    expect(values["navigation.position"]).toEqual({ longitude: 4.4, latitude: 52.1 });
    // Signal K's parser leaves out the AtoN-specific fields, so read the bits directly.
    const raw = new ggencoder.AisDecode(sentence!) as unknown as Record<string, unknown>;
    expect(raw).toMatchObject({ aistype: 21, aidtype: 20, offpos: 1, virtual: 0, dimA: 2, dimB: 3, dimC: 3, dimD: 2 });
  });

  it("reads field names on Signal K servers before 2.15, which do not send camelCase", () => {
    const app = fakeApp();
    (app as unknown as { config: { version: string } }).config.version = "2.14.0";
    const sentence = encode(app, 129794, {
      "User ID": MMSI,
      "IMO number": 9074729,
      Callsign: "PDZK",
      Name: "NOORDERLICHT",
      "Type of ship": "Sailing",
      Length: 46,
      Beam: 7,
      "Position reference from Starboard": 3,
      "Position reference from Bow": 10,
      Draft: 3.2,
      Destination: "TERSCHELLING",
    });
    expect(sentence).toBe(encode(fakeApp(), 129794, voyage));
  });

  it("encodes own ship as !AIVDO, by transceiver information or by our MMSI", () => {
    const app = fakeApp(); // our MMSI is 123456789
    expect(encode(app, 129039, { userId: MMSI, longitude: 4.4, latitude: 52.1, aisTransceiverInformation: "Own information not broadcast" })).toMatch(/^!AIVDO/);
    expect(encode(app, 129810, { userId: 123456789, typeOfShip: "Sailing" })).toMatch(/^!AIVDO/);
    expect(encode(app, 129809, { userId: MMSI, name: "NOORDERLICHT" })).toMatch(/^!AIVDM/);
  });

  it("drops a report without an MMSI, and PGNs it does not encode", () => {
    const app = fakeApp();
    expect(encode(app, 129039, { longitude: 4.4, latitude: 52.1 })).toBeNull();
    expect(encode(app, 129809, { userId: 0, name: "NOBODY" })).toBeNull();
    expect(encode(app, 127250, { heading: 1 })).toBeNull();
  });
});
