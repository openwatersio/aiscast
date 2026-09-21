import type { Delta, ServerAPI } from "@signalk/server-api";
import { Parser } from "@signalk/nmea0183-signalk";
import type { Frame, Link } from "./link.js";
import { stripTag } from "./nmea.js";
import { ownPosition } from "./ownship.js";

export type ReceiveMode = "off" | "auto" | "always";

export interface DownlinkOptions {
  mode: ReceiveMode;
  radiusNm: number;
  source: string; // $source on injected deltas
  selfSource: string | null; // aiscast `source` of our own publishes, dropped on the way back
  onReceived?: (sentence: string) => void; // loop guard hook
  onInjected?: (sentence: string) => void; // relay to NMEA 0183 output, for chartplotters and tablets
}

export interface DownlinkStats {
  targets: number; // distinct contexts injected in the last 10 minutes
  events: number;
  subscribed: boolean;
}

interface SubscribeFrame {
  type: "subscribe";
  snapshot: true;
  bbox?: [number, number, number, number][];
  mmsi?: number[];
  [key: string]: unknown; // Frame compatibility
}

const LOCAL_QUIET = 90_000; // auto: no local AIS for this long → subscribe
const RESUBSCRIBE_FRACTION = 0.25; // re-send the box after moving this fraction of the radius
const VHF_WINS = 60_000; // always: leave a target alone when another source updated it this recently
const TARGET_TTL = 10 * 60_000;

interface Box {
  lat: number;
  lon: number;
  radiusNm: number;
}

// Subscribes to aiscast around the boat and injects the events as Signal K deltas through the server's own
// AIS parser, so injected targets are indistinguishable in shape from VHF-received ones except by $source.
export class Downlink {
  private parser = new Parser();
  private lastLocal = 0;
  private box: Box | null = null;
  private subscribed = false;
  private timer: NodeJS.Timeout | null = null;
  private targets = new Map<string, number>();
  private buddies: number[] = [];
  private sentMmsi = "";
  private mmsiCap = Infinity; // from the welcome frame's limits; the server refuses a too-long list as a whole frame
  private lastFrame = ""; // last subscribe frame sent, to avoid re-sending one the server refused
  private refusedFrame = "";
  private selfMmsi: string | null;
  stats: DownlinkStats = { targets: 0, events: 0, subscribed: false };

  constructor(
    private readonly app: ServerAPI,
    private readonly link: Link,
    private opts: DownlinkOptions,
    private readonly log: (msg: string) => void = () => {},
  ) {
    const mmsi = app.getSelfPath("mmsi");
    this.selfMmsi = mmsi == null ? null : String(mmsi);
  }

  start(): void {
    this.link.on("open", () => {
      this.subscribed = false;
      this.refusedFrame = "";
      this.tick();
    });
    this.link.on("close", () => {
      this.subscribed = false;
      this.stats.subscribed = false;
    });
    this.link.on("error", (message) => {
      // aiscast refuses a subscribe frame as a unit and keeps the old subscription (none). Remember the
      // refused frame so the 10 s tick does not resend it verbatim; any change to it is tried again.
      if (/bbox|mmsi/i.test(message)) {
        this.subscribed = this.stats.subscribed = false;
        this.refusedFrame = this.lastFrame;
      }
    });
    this.link.on("frame", (f) => this.onFrame(f));
    this.timer = setInterval(() => this.tick(), 10_000);
    this.tick();
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  // The boat's own receiver heard AIS; in `auto` mode that silences the network feed.
  localHeard(now = Date.now()): void {
    this.lastLocal = now;
    if (this.subscribed && this.opts.mode === "auto") this.tick(now);
  }

  // Buddy MMSIs to follow wherever they are, independent of the receive mode: the point is traffic
  // beyond local reception, so the list keeps the subscription alive even when the radius half is quiet.
  setBuddies(mmsis: number[]): void {
    this.buddies = mmsis;
    this.tick();
  }

  // How many buddies have been heard from recently.
  buddiesSeen(now = Date.now()): number {
    let n = 0;
    for (const m of this.buddies) {
      const at = this.targets.get(`vessels.urn:mrn:imo:mmsi:${m}`);
      if (at != null && now - at < TARGET_TTL) n++;
    }
    return n;
  }

  private wanted(now: number): boolean {
    switch (this.opts.mode) {
      case "off":
        return false;
      case "always":
        return true;
      case "auto":
        return now - this.lastLocal > LOCAL_QUIET;
    }
  }

  tick(now = Date.now()): void {
    if (!this.link.open) return;
    const wantBox = this.wanted(now);
    const pos = wantBox ? ownPosition(this.app) : null;
    // Never subscribe a box without a fix: an empty bbox is the whole world. While the fix is gone, the
    // last box stands in so a GPS dropout does not tear down the subscription.
    const box = pos ? { lat: pos.latitude, lon: pos.longitude, radiusNm: this.opts.radiusNm } : wantBox ? this.box : null;
    // Over the cap, the server would refuse the whole frame, bbox included: follow what fits instead.
    const mmsi = this.buddies.length > this.mmsiCap ? this.buddies.slice(0, this.mmsiCap) : this.buddies;
    if (!box && mmsi.length === 0) {
      if (this.subscribed && this.link.send({ type: "unsubscribe" })) {
        this.subscribed = false;
        this.stats.subscribed = false;
        this.box = null;
        this.sentMmsi = "";
        this.log("unsubscribed");
      }
      return;
    }
    const boxChanged =
      box !== this.box &&
      (!box ||
        !this.box ||
        distanceNm(box.lat, box.lon, this.box.lat, this.box.lon) > this.box.radiusNm * RESUBSCRIBE_FRACTION ||
        this.box.radiusNm !== box.radiusNm);
    if (this.subscribed && !boxChanged && mmsi.join() === this.sentMmsi) return;
    // snapshot: the server replays its cache for the new coverage, so targets appear at connect
    // instead of one by one as they next transmit (a moored class B is minutes between reports).
    const frame: SubscribeFrame = { type: "subscribe", snapshot: true };
    if (box) frame.bbox = [bbox(box)];
    if (mmsi.length > 0) frame.mmsi = mmsi;
    const encoded = JSON.stringify(frame);
    if (encoded === this.refusedFrame) return;
    this.lastFrame = encoded;
    if (this.link.send(frame)) {
      this.box = box;
      this.sentMmsi = mmsi.join();
      this.subscribed = true;
      this.stats.subscribed = true;
      const parts = [
        box && `${box.radiusNm} nm around ${box.lat.toFixed(3)},${box.lon.toFixed(3)}`,
        mmsi.length > 0 && `${mmsi.length} buddies`,
      ];
      this.log(`subscribed ${parts.filter(Boolean).join(" + ")}`);
    }
  }

  private onFrame(f: Frame): void {
    if (f.type === "welcome") {
      const cap = (f as { limits?: { mmsis?: number } }).limits?.mmsis;
      if (typeof cap === "number" && cap !== this.mmsiCap) {
        this.mmsiCap = cap;
        this.tick(); // a tighter cap trims the list; a looser one restores it
      }
      return;
    }
    if (f.type !== "event") return;
    const ev = f as unknown as AisEvent;
    if (!Array.isArray(ev.nmea) || ev.nmea.length === 0) return;
    // Own-vessel echoes (our publishes, or another station hearing our transmission) skip the loop guard:
    // marking them seen would swallow our own future uplink of an identical payload (re-synthesized s:self
    // position, type 24 rebroadcast unchanged every few minutes). Self is never injected, so there is no loop.
    if (this.opts.selfSource && ev.source === this.opts.selfSource) return;
    if (!ev.mmsi || String(ev.mmsi) === this.selfMmsi) return;
    for (const s of ev.nmea) this.opts.onReceived?.(s);
    if (POSITION_TYPES.has(ev.msg_type ?? "") && (ev.lat == null || ev.lon == null)) return; // aiscast rejected the position

    let delta: Delta | null = null;
    for (const s of ev.nmea) {
      try {
        delta = (this.parser.parse(stripTag(s)) as unknown as Delta | null) ?? delta;
      } catch (err) {
        this.log(`parse failed for ${s}: ${(err as Error).message}`);
        return;
      }
    }
    if (!delta?.context || !delta.updates?.length) return;
    if (delta.context === this.app.selfContext) return;
    if (this.opts.mode === "always" && this.vhfIsFresh(delta.context, Date.now())) return;

    for (const u of delta.updates) {
      delete (u as { source?: unknown }).source;
      u.$source = this.opts.source as Delta["updates"][number]["$source"];
      if (ev.time) u.timestamp = ev.time as Delta["updates"][number]["timestamp"];
    }
    this.app.handleMessage(this.opts.source, delta);
    // Same commit point as the delta, so the "local reception wins" check above suppresses the sentence too
    // and a target heard both ways goes out once. The TAG block is aiscast's receive time, which no
    // chartplotter reads and some choke on. ponytail: fragments relay verbatim; the plotter reassembles.
    if (this.opts.onInjected && isLive(ev.time)) for (const s of ev.nmea) this.opts.onInjected(stripTag(s));
    this.stats.events++;
    this.targets.set(delta.context, Date.now());
    if (this.stats.events % 100 === 0) this.pruneTargets();
    this.stats.targets = this.targets.size;
  }

  // Another source (the boat's receiver) updated this target recently: do not overwrite it.
  private vhfIsFresh(context: string, now: number): boolean {
    const pos = this.app.getPath(`${context}.navigation.position`) as { $source?: string; timestamp?: string } | undefined;
    if (!pos?.$source || pos.$source === this.opts.source || !pos.timestamp) return false;
    return now - Date.parse(pos.timestamp) < VHF_WINS;
  }

  private pruneTargets(): void {
    const cutoff = Date.now() - TARGET_TTL;
    for (const [k, t] of this.targets) if (t < cutoff) this.targets.delete(k);
  }
}

interface AisEvent {
  type: "event";
  time?: string;
  source?: string;
  nmea?: string[];
  mmsi?: number;
  msg_type?: string;
  lat?: number;
  lon?: number;
}

// Live enough to put on the 0183 wire. A Signal K app reads the delta's timestamp and `$source` and can
// tell a replayed position from a VHF one; a chartplotter gets a bare !AIVDM and cannot. Everything the
// snapshot replays on subscribe carries its original receive time, as do AISHub's ~5-minute aggregate and
// late satellite passes, so one age gate covers all three. It also keeps the replay burst off the wire:
// a few hundred sentences at once is minutes of backlog on a 4800-baud serial line. Static messages are
// gated the same way rather than passed through, and reach the plotter on the target's next
// retransmission, within about six minutes. An unparseable time counts as stale: for a wire a helmsman
// steers by, silence beats a confident wrong position.
// ponytail: one fixed window for every message type, no per-type budget.
const LIVE_FOR_0183 = 120_000;

function isLive(time: string | undefined, now = Date.now()): boolean {
  const t = time ? Date.parse(time) : NaN;
  return now - t < LIVE_FOR_0183; // NaN comparisons are false, so an unreadable time is not live
}

const POSITION_TYPES = new Set([
  "PositionReport",
  "StandardClassBPositionReport",
  "ExtendedClassBPositionReport",
  "LongRangeAisBroadcastMessage",
  "StandardSearchAndRescueAircraftReport",
]);

// [minLat, minLon, maxLat, maxLon] around a centre. ponytail: clamped at the poles and antimeridian rather than split.
export function bbox(b: Box): [number, number, number, number] {
  const dLat = b.radiusNm / 60;
  const dLon = b.radiusNm / (60 * Math.max(0.05, Math.cos((b.lat * Math.PI) / 180)));
  return [
    Math.max(-90, b.lat - dLat),
    Math.max(-180, b.lon - dLon),
    Math.min(90, b.lat + dLat),
    Math.min(180, b.lon + dLon),
  ];
}

export function distanceNm(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const toRad = Math.PI / 180;
  const dLat = (lat2 - lat1) * toRad;
  const dLon = (lon2 - lon1) * toRad;
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(lat1 * toRad) * Math.cos(lat2 * toRad) * Math.sin(dLon / 2) ** 2;
  return 2 * 3440.065 * Math.asin(Math.sqrt(a));
}
