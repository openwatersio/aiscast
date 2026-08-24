// Own-ship reports built from Signal K when an AIS transponder is not available, tagged s:self so aiscast marks them self-reported.
// Adapted from @signalk/aisreporter (Apache-2.0): https://github.com/SignalK/aisreporter
import type { ServerAPI } from "@signalk/server-api";
import ggencoder from "ggencoder";

const { AisEncode } = ggencoder; // CommonJS without static named exports

export const SOURCE_TAG = "self";
const REPORT_EVERY = 60_000;
const STATIC_EVERY = 6 * 60_000;
const TRANSPONDER_QUIET = 5 * 60_000; // a class B transponder sends at least every 3 min; longer silence means none
// AIS "not available" sentinels; ggencoder would otherwise encode 0 (stopped, heading north).
const SOG_NA_KN = 102.3;
const COG_NA_DEG = 360;
const HDG_NA = 511;

export interface Position {
  latitude: number;
  longitude: number;
}

export function ownPosition(app: ServerAPI): Position | null {
  const p = app.getSelfPath("navigation.position") as { value?: Position } | Position | undefined;
  const v = p && "value" in p ? p.value : (p as Position | undefined);
  if (!v || typeof v.latitude !== "number" || typeof v.longitude !== "number") return null;
  if (Math.abs(v.latitude) < 1e-6 && Math.abs(v.longitude) < 1e-6) return null; // Null Island
  return v;
}

export class OwnShip {
  private lastHeard = 0;
  private lastStatic = 0;
  private lastLat?: number;
  private lastLon?: number;
  private timer: NodeJS.Timeout | null = null;

  constructor(
    private readonly app: ServerAPI,
    private readonly emit: (sentence: string, now: number) => void,
  ) {}

  start(): void {
    this.timer = setInterval(() => this.tick(), REPORT_EVERY);
    this.tick();
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  // A real !AIVDO came through: the boat has a transponder, so nothing is synthesized for a while.
  heard(now = Date.now()): void {
    this.lastHeard = now;
  }

  tick(now = Date.now()): void {
    if (now - this.lastHeard < TRANSPONDER_QUIET) return;
    const mmsi = this.app.getSelfPath("mmsi") as string | number | undefined;
    if (!mmsi) return;
    const pos = ownPosition(this.app);
    if (!pos) return;
    // Positions are skipped while unchanged (stuck GPS, tree re-emitting the same value); static is due regardless.
    if (pos.latitude !== this.lastLat || pos.longitude !== this.lastLon) {
      const sentence = this.position(mmsi, pos);
      if (sentence) this.emit(sentence, now);
      this.lastLat = pos.latitude;
      this.lastLon = pos.longitude;
    }
    if (now - this.lastStatic >= STATIC_EVERY) {
      this.lastStatic = now;
      for (const s of this.static(mmsi)) this.emit(s, now);
    }
  }

  private position(mmsi: string | number, pos: Position): string | null {
    const sog = this.num("navigation.speedOverGround.value");
    const cog = this.num("navigation.courseOverGroundTrue.value");
    let hdg = this.num("navigation.headingTrue.value");
    if (hdg === undefined) {
      const mag = this.num("navigation.headingMagnetic.value");
      const variation = this.num("navigation.magneticVariation.value"); // east-positive, so true = magnetic + variation
      if (mag !== undefined && variation !== undefined) hdg = mag + variation;
    }
    return encode({
      aistype: 18,
      repeat: 0,
      own: true,
      mmsi,
      lat: pos.latitude,
      lon: pos.longitude,
      accuracy: 0,
      // ggencoder truncates to the field resolution (0.1 kn, 0.1°, 1°), so round first; a course near due
      // north must wrap to 0, not round up to the 360 "not available" sentinel
      sog: sog === undefined ? SOG_NA_KN : Math.round(sog * 19.438444924574) / 10,
      cog: cog === undefined ? COG_NA_DEG : (Math.round(deg(cog) * 10) % 3600) / 10,
      hdg: hdg === undefined ? HDG_NA : Math.round(deg(hdg)) % 360,
    });
  }

  private static(mmsi: string | number): string[] {
    const out: string[] = [];
    const name = this.app.getSelfPath("name");
    if (typeof name === "string" && name) {
      const partA = encode({ aistype: 24, repeat: 0, own: true, part: 0, mmsi, shipname: name });
      if (partA) out.push(partA);
    }
    const cargo = this.num("design.aisShipType.value.id");
    const callsign = this.app.getSelfPath("communication.callsignVhf");
    const length = this.num("design.length.value.overall");
    const beam = this.num("design.beam.value");
    const partB: Record<string, unknown> = {};
    if (cargo !== undefined) partB.cargo = cargo;
    if (typeof callsign === "string" && callsign) partB.callsign = callsign;
    if (length !== undefined && beam !== undefined) {
      const fromBow = this.num("sensors.gps.fromBow.value") ?? length / 2;
      const fromCenter = this.num("sensors.gps.fromCenter.value") ?? 0; // Signal K: positive to port
      partB.dimA = Math.round(fromBow);
      partB.dimB = Math.round(length - fromBow);
      partB.dimC = Math.round(beam / 2 - fromCenter); // dimC is to port, dimD to starboard
      partB.dimD = Math.round(beam / 2 + fromCenter);
    }
    if (Object.keys(partB).length > 0) {
      const sentence = encode({ aistype: 24, repeat: 0, own: true, part: 1, mmsi, ...partB });
      if (sentence) out.push(sentence);
    }
    return out;
  }

  private num(path: string): number | undefined {
    const v = this.app.getSelfPath(path);
    return typeof v === "number" && Number.isFinite(v) ? v : undefined;
  }
}

// ggencoder leaves nmea as [] and sets valid=false for a message shape it does not implement.
function encode(msg: Record<string, unknown>): string | null {
  const enc = new AisEncode(msg);
  return enc.valid ? enc.nmea : null;
}

// Radians → degrees in [0, 360); a magnetic + variation sum can fall outside one turn.
function deg(rad: number): number {
  return ((((rad * 180) / Math.PI) % 360) + 360) % 360;
}
