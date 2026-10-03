import { publicApiBase, storedToken, type VesselProps } from "./api";
import { reportError } from "./report";
import { Hub, HUB_HEARTBEAT, type HubState, type Limits, type Port, type ToHub, type ToTab } from "./streamHub";

// One connection for the whole browser. Anonymous clients get two concurrent streams per
// network address, which a household or a marina shares, so a second connection here would
// spend somebody else's budget. Every tab talks to one hub, in a SharedWorker, which holds the
// socket (see streamHub.ts). bbox and mmsi filters are ORed in a single subscribe frame,
// which is what lets one socket serve the viewport and a followed vessel at the same time.

export interface Vessel {
  mmsi: number;
  name?: string;
  lat?: number;
  lon?: number;
  cog?: number;
  sog?: number;
  heading?: number;
  navStatus?: number;
  shipType?: number;
  kind: VesselProps["kind"];
  seen: number;
  source?: string;
  station?: string;
  license?: string;
  attribution?: string;
  callSign?: string;
  imo?: number;
  destination?: string;
  draught?: number;
  dimension?: { A: number; B: number; C: number; D: number };
  eta?: { Month: number; Day: number; Hour: number; Minute: number };
  /**
   * Positions collected since this vessel was opened, kept only for the one on screen. The
   * server serves the real track, and this exists to carry the drawn line from the end of
   * that fetch to the vessel's current mark.
   */
  track: Array<[number, number, number]>;
}

export type BBox = [number, number, number, number];

interface StreamEvent {
  type: string;
  error?: string;
  mmsi: number;
  time: string;
  source: string;
  station: string;
  attribution?: string;
  license?: string;
  msg_type: string;
  lat?: number;
  lon?: number;
  message?: Record<string, any>;
}

const POSITION_TYPES = new Set([
  "PositionReport",
  "StandardClassBPositionReport",
  "ExtendedClassBPositionReport",
  "LongRangeAisBroadcastMessage",
  "StandardSearchAndRescueAircraftReport",
  "BaseStationReport",
  "AidsToNavigationReport",
]);

const TTL = 30 * 60e3; // matches the server's vessel cache
// The hub forgets a tab it has not heard from in a while, so a quiet tab says it is still here.
const TAB_HEARTBEAT = 20e3;
// A hub that has missed this many heartbeats is gone: its worker crashed or was stopped.
const HUB_SILENCE = 6 * HUB_HEARTBEAT;
const TOKEN_KEY = "aiscast.token";
// Enough to bridge the gap since the track was fetched, not to be a track in its own right.
const MAX_TRACK = 120;

type Listener = () => void;

export class Stream {
  readonly vessels = new Map<number, Vessel>();
  readonly credits = new Map<string, string>();
  /**
   * `refused`: every stream this address may hold is open elsewhere, or this browser's stream
   * cannot fit this tab's view beside other tabs' under the key's area cap or limit on
   * followed vessels.
   */
  state: "connecting" | "live" | "reconnecting" | "capped" | "refused" = "connecting";
  eventsPerSec = 0;
  /** Bumped on every change, so React can tell a new frame from the same one. */
  version = 0;
  limits: Limits | undefined;

  #port: Port<ToHub, ToTab> | undefined;
  #worker: SharedWorker | undefined;
  #url = "";
  #local: { url: string; hub: Hub } | undefined;
  /** Whether the port is a SharedWorker's rather than a hub's in this tab. */
  #onWorker = false;
  /** Whether the hub has said anything on this port. */
  #spoke = false;
  #shared = typeof SharedWorker === "function";
  /** When the hub last said anything. */
  #heard = 0;
  /** When this tab last sent the hub its view. */
  #told = 0;
  #active = Date.now();
  /** Whether the hub's subscription includes this tab's boxes and follows. */
  #served = true;
  /** Whether it includes this tab's follows, which it can without the boxes. */
  #following = true;
  #bbox: BBox[] = [];
  #mmsi = new Set<number>();
  #count = 0;
  #trackFor: number | undefined;
  #listeners = new Set<Listener>();
  #dirty = false;

  constructor() {
    if (import.meta.env.DEV || import.meta.env.VITE_E2E) (window as { aiscastStream?: Stream }).aiscastStream = this;
    this.#connect();
    setInterval(() => {
      this.eventsPerSec = this.#count;
      this.#count = 0;
      this.#sweep();
      if (this.#dirty) {
        this.#dirty = false;
        this.#emit();
      }
      // Also how a tab that was frozen finds its way back, since its port may have been reaped.
      const now = Date.now();
      // A hub in this tab sleeps when the tab does, so its silence says nothing.
      if (this.#onWorker && now - this.#heard > HUB_SILENCE) this.#connect();
      // The hub's URL carries the token, which can expire or be replaced.
      else if (now - this.#told > TAB_HEARTBEAT) streamUrl() === this.#url ? this.#send() : this.#connect();
    }, 1000);
    const activate = () => {
      if (document.visibilityState === "visible") this.#active = Date.now();
      this.#send();
    };
    document.addEventListener("visibilitychange", activate);
    window.addEventListener("focus", activate);
    window.addEventListener("pagehide", () => this.#post({ type: "close" }));
    // Back from the back-forward cache, after pagehide let the hub go.
    window.addEventListener("pageshow", (e) => e.persisted && this.#connect());
    // A token saved in another tab raises the limits, and lives on the stream's URL.
    window.addEventListener("storage", (e) => e.key === TOKEN_KEY && this.#connect());
  }

  /**
   * Adopt a position the server already rendered. The stream is capped at two connections
   * per network address, which a marina or a household shares, so a page whose only source
   * of truth is the socket shows an empty sea exactly when someone most wants to see a boat.
   * Anything the socket later delivers is newer and wins.
   */
  seed(v: Partial<Vessel> & { mmsi: number; seen: number }) {
    const existing = this.vessels.get(v.mmsi);
    if (existing && existing.seen >= v.seen) return;
    this.vessels.set(v.mmsi, {
      kind: "vessel",
      track: existing?.track ?? [],
      ...existing,
      ...v,
    } as Vessel);
    this.#emit();
  }

  subscribe(fn: Listener): () => void {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  }

  /**
   * Collect positions for this vessel alone. Every vessel in view used to accumulate a
   * track, which for a busy viewport is hundreds of thousands of points that nothing draws.
   */
  trackOnly(mmsi: number | undefined) {
    if (this.#trackFor === mmsi) return;
    const previous = this.#trackFor === undefined ? undefined : this.vessels.get(this.#trackFor);
    if (previous) previous.track = [];
    this.#trackFor = mmsi;
  }

  /**
   * Replaces the whole subscription; the server treats a resubscribe as wholesale. There is
   * no snapshot: the tiles paint where every vessel was, and the stream says what changes.
   */
  setView(bbox: BBox[], mmsi: Iterable<number> = this.#mmsi) {
    this.#bbox = bbox;
    this.#mmsi = new Set(mmsi);
    this.#prune();
    this.#send();
  }

  /**
   * Particulars from a tile or the record, for a vessel the stream has heard only positions
   * from. A position report carries no name or type, and a static report comes every six
   * minutes, so without this a vessel loses its label and colour when the stream takes it
   * over. What the stream heard itself always wins.
   */
  adopt(mmsi: number, from: { name?: string; kind?: Vessel["kind"]; shipType?: number }) {
    const v = this.vessels.get(mmsi);
    if (!v) return;
    if (!v.name && from.name) v.name = from.name;
    if (!v.shipType && from.shipType) v.shipType = from.shipType;
    if (v.kind === "vessel" && from.kind) v.kind = from.kind;
    this.#dirty = true;
  }

  follow(mmsi: number) {
    if (this.#mmsi.has(mmsi)) return;
    this.#mmsi.add(mmsi);
    this.#send();
  }

  unfollow(mmsi: number) {
    if (this.#mmsi.delete(mmsi)) this.#send();
  }

  #emit() {
    this.version++;
    for (const fn of this.#listeners) fn();
  }

  #connect() {
    this.#post({ type: "close" });
    if (this.#port) this.#port.onmessage = null;
    this.#port?.close?.();
    if (this.#worker) this.#worker.onerror = null;
    const url = streamUrl();
    this.#url = url;
    const shared = this.#sharedPort(url);
    this.#onWorker = Boolean(shared);
    const port = shared ?? this.#localPort(url);
    port.onmessage = (e: MessageEvent<ToTab>) => this.#onHub(e.data);
    this.#port = port;
    this.#heard = Date.now();
    this.#spoke = false;
    this.#send();
  }

  #sharedPort(url: string): Port<ToHub, ToTab> | undefined {
    if (!this.#shared) return undefined;
    try {
      const worker = new SharedWorker(new URL("./stream.worker.ts", import.meta.url), { type: "module", name: url });
      // A browser with only classic shared workers fails to load this one, and gets a hub here.
      // Once the hub has spoken, an error is the watchdog's to handle.
      worker.onerror = () => {
        if (this.#spoke) return;
        this.#shared = false;
        this.#connect();
      };
      this.#worker = worker;
      return worker.port;
    } catch {
      this.#shared = false;
      return undefined;
    }
  }

  /** A hub of this tab's own, and so a stream of its own, where there is no SharedWorker. */
  #localPort(url: string): Port<ToHub, ToTab> {
    if (this.#local?.url !== url) this.#local = { url, hub: new Hub(url) };
    const { port1, port2 } = new MessageChannel();
    this.#local.hub.add(port1);
    return port2;
  }

  #post(msg: ToHub) {
    this.#port?.postMessage(msg);
  }

  #send() {
    this.#told = Date.now();
    this.#post({
      type: "view",
      bbox: this.#bbox,
      mmsi: [...this.#mmsi],
      visible: document.visibilityState === "visible",
      active: this.#active,
    });
  }

  #onHub(msg: ToTab) {
    this.#heard = Date.now();
    this.#spoke = true;
    if (msg.type === "event") return this.#onEvent(JSON.parse(msg.data));
    if (msg.fault) reportError("stream", new Error(msg.fault));
    // Another tab's view took the area this one needs, so the tiles carry it, as when refused.
    const state = msg.state === "live" && !msg.served ? "refused" : msg.state;
    const lost = (this.#served && !msg.served) || (this.#following && !(msg.following ?? msg.served));
    this.#served = msg.served;
    // A hub from an older build, kept alive across a deploy, sends no following.
    this.#following = msg.following ?? msg.served;
    if (lost) this.#prune();
    // Every heartbeat carries the status, and an unchanged one is not a new frame.
    if (state === this.state && JSON.stringify(msg.limits) === JSON.stringify(this.limits)) return;
    this.state = state;
    this.limits = msg.limits;
    this.#emit();
  }

  #onEvent(ev: StreamEvent) {
    if (ev.type !== "event") return;
    this.#count++;
    this.#fold(ev);
    this.#dirty = true;
  }

  #fold(ev: StreamEvent) {
    const m = ev.message ?? {};
    const v: Vessel = this.vessels.get(ev.mmsi) ?? {
      mmsi: ev.mmsi,
      kind: "vessel",
      seen: 0,
      track: [],
    };
    const t = Date.parse(ev.time);

    // AISHub lags minutes behind the live feeds, so a late arrival must not drag a marker
    // backwards. Static fields still fold in; only the position and its derivatives are held.
    const stale = v.lat != null && t < v.seen - 1000;

    if (ev.lat != null && ev.lon != null && !stale) {
      const moved = v.lat !== ev.lat || v.lon !== ev.lon;
      v.lat = ev.lat;
      v.lon = ev.lon;
      if (moved) {
        if (ev.mmsi === this.#trackFor) {
          v.track.push([ev.lon, ev.lat, t]);
          if (v.track.length > MAX_TRACK) v.track.splice(0, v.track.length - MAX_TRACK);
        }
      }
    }

    if (POSITION_TYPES.has(ev.msg_type) && !stale) {
      const lr = ev.msg_type === "LongRangeAisBroadcastMessage";
      v.cog = m.Cog != null && m.Cog < (lr ? 511 : 360) ? m.Cog : undefined;
      v.sog = m.Sog != null && m.Sog < (lr ? 63 : 102.3) ? m.Sog : undefined;
      v.heading = m.TrueHeading != null && m.TrueHeading < 511 ? m.TrueHeading : undefined;
    }
    if (m.NavigationalStatus != null && m.NavigationalStatus !== 15 && !stale) {
      v.navStatus = m.NavigationalStatus;
    }

    const name = m.Name ?? m.ReportA?.Name;
    if (name) v.name = String(name).trim();
    const type = m.Type ?? m.ReportB?.ShipType;
    if (type) v.shipType = type;
    if (m.CallSign) v.callSign = String(m.CallSign).trim();
    if (m.ImoNumber) v.imo = m.ImoNumber;
    if (m.Destination) v.destination = String(m.Destination).trim();
    if (m.MaximumStaticDraught) v.draught = m.MaximumStaticDraught;
    if (m.Dimension?.A || m.Dimension?.B) v.dimension = m.Dimension;
    if (m.Eta?.Month) v.eta = m.Eta;

    if (ev.msg_type === "AidsToNavigationReport") v.kind = "aton";
    else if (ev.msg_type === "BaseStationReport") v.kind = "base";
    else if (ev.msg_type === "StandardSearchAndRescueAircraftReport") v.kind = "sar";

    if (!stale) {
      v.seen = t;
      v.source = ev.source;
      v.station = ev.station;
      v.license = ev.license;
      v.attribution = ev.attribution;
    }
    this.vessels.set(ev.mmsi, v);

    // Licensing is per source, so only sources actually seen are credited. Keyed by source
    // kind, as /v1/vessels keys its attribution member.
    const kind = ev.source.split(":")[0];
    if (kind && !this.credits.has(kind)) {
      this.credits.set(kind, ev.attribution ?? `AIS: ${ev.source}`);
      this.#dirty = true;
    }
  }

  /** A vessel this tab follows and the hub still streams to it. */
  #followed(mmsi: number): boolean {
    return this.#following && this.#mmsi.has(mmsi);
  }

  /**
   * Forgets vessels outside the subscription. The stream stops reporting a vessel once it is
   * out of view, so its last position here goes stale while the tile under it stays current.
   */
  #prune() {
    const boxes = this.#served ? this.#bbox : [];
    for (const [mmsi, v] of this.vessels) {
      if (this.#followed(mmsi)) continue;
      const inView =
        v.lat != null &&
        v.lon != null &&
        boxes.some(([s, w, n, e]) => v.lat! >= s && v.lat! <= n && v.lon! >= w && v.lon! <= e);
      if (!inView) {
        this.vessels.delete(mmsi);
        this.#dirty = true;
      }
    }
  }

  #sweep() {
    const cutoff = Date.now() - TTL;
    for (const [mmsi, v] of this.vessels) {
      if (v.seen < cutoff && !this.#followed(mmsi)) {
        this.vessels.delete(mmsi);
        this.#dirty = true;
      }
    }
  }
}

function streamUrl(): string {
  // The token goes on the URL only because WebSocket has no request headers. Anonymous is
  // 20 messages/s and 100 square degrees; a personal token is 50/s and 400.
  const token = storedToken();
  return publicApiBase().replace(/^http/, "ws") + "/v1/stream" + (token ? `?key=${encodeURIComponent(token)}` : "");
}
