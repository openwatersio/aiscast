// NMEA 2000 AIS PGNs → !AIVDM/!AIVDO. N2K carries the decoded fields, not the VHF bits, so this is a re-encode;
// aiscast sees these with TAG `s:n2k`. Adapted from signalk-n2kais-to-nmea0183 (Apache-2.0, Scott Bender):
// https://github.com/SignalK/signalk-n2kais-to-nmea0183
import type { ServerAPI } from "@signalk/server-api";
import {
  AisAssignedModeValues,
  AisTransceiver,
  AtonTypeValues,
  convertNamesToCamel,
  NavStatusValues,
  PositionAccuracy,
  RaimFlag,
  ShipTypeValues,
  YesNo,
} from "@canboat/ts-pgns";
import ggencoder from "ggencoder";

const { AisEncode } = ggencoder; // CommonJS without static named exports

export const AIS_PGNS = new Set([129038, 129039, 129041, 129794, 129809, 129810]);

interface Fields {
  [k: string]: unknown;
}

// One canboatjs message (app.on('N2KAnalyzerOut')) → one sentence, or null when it is not an AIS PGN we encode.
export function n2kToSentence(app: ServerAPI, msg: { pgn: number; fields?: Fields }): string | null {
  if (!AIS_PGNS.has(msg.pgn)) return null;
  const pgn = convertNamesToCamel(app, msg as never) as { fields: Fields };
  const f = pgn.fields ?? {};
  const own = f.aisTransceiverInformation === AisTransceiver.OwnInformationNotBroadcast;
  let enc: Record<string, unknown> | null = null;
  switch (msg.pgn) {
    case 129038: // class A position
      enc = {
        aistype: 3,
        mmsi: f.userId,
        navstatus: f.navStatus !== undefined ? NavStatusValues[f.navStatus as string] : undefined,
        sog: mpsToKn(f.sog as number | undefined),
        lon: f.longitude,
        lat: f.latitude,
        cog: radToDeg(f.cog as number | undefined),
        hdg: radToDeg(f.heading as number | undefined),
        rot: rotToAis(f.rateOfTurn as number | undefined),
      };
      break;
    case 129039: // class B position
      enc = {
        aistype: 18,
        mmsi: f.userId,
        sog: mpsToKn(f.sog as number | undefined),
        accuracy: f.positionAccuracy === PositionAccuracy.Low ? 0 : 1,
        lon: f.longitude,
        lat: f.latitude,
        cog: radToDeg(f.cog as number | undefined),
        hdg: radToDeg(f.heading as number | undefined),
      };
      break;
    case 129794: // class A static and voyage
      enc = {
        aistype: 5,
        mmsi: f.userId,
        imo: f.imoNumber,
        cargo: f.typeOfShip !== undefined ? ShipTypeValues[f.typeOfShip as string] : undefined,
        callsign: f.callsign,
        shipname: f.name,
        destination: f.destination,
        draught: f.draft,
        ...dimensions(f.positionReferenceFromBow, f.length, f.positionReferenceFromStarboard, f.beam),
      };
      break;
    case 129809: // class B static, part A
      enc = { aistype: 24, part: 0, mmsi: f.userId, shipname: f.name };
      break;
    case 129810: // class B static, part B
      enc = {
        aistype: 24,
        part: 1,
        mmsi: f.userId,
        cargo: f.typeOfShip !== undefined ? ShipTypeValues[f.typeOfShip as string] : undefined,
        callsign: f.callsign,
        ...dimensions(f.positionReferenceFromBow, f.length, f.positionReferenceFromStarboard, f.beam),
      };
      break;
    case 129041: // aid to navigation
      enc = {
        aistype: 21,
        mmsi: f.userId,
        aid_type: f.atonType !== undefined ? AtonTypeValues[f.atonType as string] : undefined,
        atonname: f.atonName,
        accuracy: f.positionAccuracy === PositionAccuracy.Low ? 0 : 1,
        lon: f.longitude,
        lat: f.latitude,
        off_position: f.offPositionIndicator === YesNo.Yes ? 1 : 0,
        raim: f.raim === RaimFlag.InUse ? 1 : 0,
        virtual_aid: f.virtualAtonFlag === YesNo.Yes ? 1 : 0,
        assigned: f.assignedModeFlag !== undefined ? AisAssignedModeValues[f.assignedModeFlag as string] : undefined,
        ...dimensions(f.positionReferenceFromTrueNorthFacingEdge, f.lengthDiameter, f.positionReferenceFromStarboardEdge, f.beamDiameter),
      };
      break;
  }
  if (!enc || typeof enc.mmsi !== "number" || enc.mmsi === 0) return null;
  const out = new AisEncode({ repeat: 0, own, ...enc });
  return out.valid ? out.nmea : null;
}

function dimensions(fromBow: unknown, length: unknown, fromStarboard: unknown, beam: unknown): Record<string, number> {
  const d: Record<string, number> = {};
  if (typeof fromBow === "number") d.dimA = fromBow;
  if (typeof fromBow === "number" && typeof length === "number") d.dimB = length - fromBow;
  if (typeof beam === "number" && typeof fromStarboard === "number") d.dimC = beam - fromStarboard;
  if (typeof fromStarboard === "number") d.dimD = fromStarboard;
  return d;
}

function radToDeg(rad: number | undefined): number | undefined {
  return rad === undefined ? undefined : Math.round((rad * 180) / Math.PI);
}

function mpsToKn(mps: number | undefined): number | undefined {
  return mps === undefined ? undefined : mps * 1.9438444924574;
}

// AIS rate of turn: 4.733 * sqrt(deg/min), sign preserved; 0 stays "not turning".
function rotToAis(radPerSec: number | undefined): number | undefined {
  if (radPerSec === undefined || radPerSec === 0) return radPerSec;
  const degPerMin = Math.abs(radPerSec) * 3437.74677078493;
  return Math.sign(radPerSec) * 4.733 * Math.sqrt(degPerMin);
}
