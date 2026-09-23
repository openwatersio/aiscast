import { API_PUBLIC, type VesselProps } from "./api";

// One connection for the whole app. Anonymous clients get two concurrent streams per
// network address, which a household or a marina shares, so a second connection here would
// spend somebody else's budget. bbox and mmsi filters are ORed in a single subscribe frame,
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
  /** Positions collected while this page has been open. Not history; see the spec. */
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
const MAX_TRACK = 500;

type Listener = () => void;

export class Stream {
  readonly vessels = new Map<number, Vessel>();
  readonly credits = new Map<string, string>();
  state: "connecting" | "live" | "reconnecting" | "capped" = "connecting";
  eventsPerSec = 0;
  limits: Record<string, number | boolean> | undefined;

  #ws: WebSocket | undefined;
  #backoff = 1000;
  #bbox: BBox[] = [];
  #mmsi = new Set<number>();
  #count = 0;
  #listeners = new Set<Listener>();
  #dirty = false;

  constructor() {
    this.#connect();
    setInterval(() => {
      this.eventsPerSec = this.#count;
      this.#count = 0;
      this.#sweep();
      if (this.#dirty) {
        this.#dirty = false;
        this.#emit();
      }
    }, 1000);
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

  /** Replaces the whole subscription; the server treats a resubscribe as wholesale. */
  setView(bbox: BBox[], mmsi: Iterable<number> = this.#mmsi) {
    this.#bbox = bbox;
    this.#mmsi = new Set(mmsi);
    this.#send();
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
    for (const fn of this.#listeners) fn();
  }

  /**
   * The token this browser minted on the token page, if it has one. Anonymous is 2 streams,
   * 20 messages/s and 100 square degrees; personal is 50/s and 400. The token is a bearer
   * credential, so it goes on the URL only because WebSocket has no request headers.
   */
  static storedToken(): string | undefined {
    try {
      const raw = localStorage.getItem("aiscast.token");
      if (!raw) return undefined;
      const { token, claims } = JSON.parse(raw);
      if (claims?.exp && claims.exp * 1000 <= Date.now()) return undefined;
      return typeof token === "string" ? token : undefined;
    } catch {
      return undefined;
    }
  }

  #connect() {
    const token = Stream.storedToken();
    const url =
      API_PUBLIC.replace(/^http/, "ws") +
      "/v1/stream" +
      (token ? `?key=${encodeURIComponent(token)}` : "");
    this.#ws = new WebSocket(url);
    this.#ws.onopen = () => {
      this.state = "live";
      this.#backoff = 1000;
      this.#send();
    };
    this.#ws.onclose = () => {
      this.state = "reconnecting";
      this.#emit();
      setTimeout(() => this.#connect(), this.#backoff);
      this.#backoff = Math.min(this.#backoff * 2, 30e3);
    };
    this.#ws.onerror = () => this.#ws?.close();
    this.#ws.onmessage = (e) => this.#onMessage(JSON.parse(e.data));
  }

  #send() {
    if (this.#ws?.readyState !== WebSocket.OPEN) return;
    if (!this.#bbox.length && !this.#mmsi.size) return;
    this.#ws.send(
      JSON.stringify({
        type: "subscribe",
        bbox: this.#bbox,
        mmsi: [...this.#mmsi],
        snapshot: true,
      }),
    );
  }

  #onMessage(ev: StreamEvent & { limits?: Record<string, number | boolean> }) {
    if (ev.type === "welcome") {
      this.limits = ev.limits;
      this.#emit();
      return;
    }
    if (ev.type === "error") {
      // The viewport exceeds the area cap. Not a failure; the map switches to coverage.
      if (/bbox|area/i.test(ev.error ?? "")) {
        this.state = "capped";
        this.#emit();
      }
      return;
    }
    if (ev.type !== "event") return;
    this.state = "live";
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
        v.track.push([ev.lon, ev.lat, t]);
        if (v.track.length > MAX_TRACK) v.track.splice(0, v.track.length - MAX_TRACK);
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

  #sweep() {
    const cutoff = Date.now() - TTL;
    for (const [mmsi, v] of this.vessels) {
      if (v.seen < cutoff && !this.#mmsi.has(mmsi)) {
        this.vessels.delete(mmsi);
        this.#dirty = true;
      }
    }
  }
}
