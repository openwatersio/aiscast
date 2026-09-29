import { publicApiBase, storedToken, type VesselProps } from "./api";

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
// Enough to bridge the gap since the track was fetched, not to be a track in its own right.
const MAX_TRACK = 120;

type Listener = () => void;

export class Stream {
  readonly vessels = new Map<number, Vessel>();
  readonly credits = new Map<string, string>();
  /** `refused`: every stream this address may hold is open elsewhere, often another tab. */
  state: "connecting" | "live" | "reconnecting" | "capped" | "refused" = "connecting";
  eventsPerSec = 0;
  /** Bumped on every change, so React can tell a new frame from the same one. */
  version = 0;
  limits: Record<string, number | boolean> | undefined;

  #ws: WebSocket | undefined;
  #backoff = 1000;
  #bbox: BBox[] = [];
  #mmsi = new Set<number>();
  #count = 0;
  #trackFor: number | undefined;
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
    this.version++;
    for (const fn of this.#listeners) fn();
  }

  #connect() {
    // The token goes on the URL only because WebSocket has no request headers. Anonymous is
    // 20 messages/s and 100 square degrees; a personal token is 50/s and 400.
    const token = storedToken();
    const url =
      publicApiBase().replace(/^http/, "ws") +
      "/v1/stream" +
      (token ? `?key=${encodeURIComponent(token)}` : "");
    this.#ws = new WebSocket(url);
    this.#ws.onopen = () => this.#send();
    this.#ws.onclose = () => {
      if (this.state !== "refused") this.state = "reconnecting";
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
      // Not on open: the server accepts the socket before it checks the per-address stream
      // limit, then refuses and closes it. Resetting there retried every second forever.
      this.#backoff = 1000;
      this.state = "live";
      this.limits = ev.limits;
      this.#emit();
      return;
    }
    if (ev.type === "error") {
      // The viewport exceeds the area cap. Not a failure; the map switches to coverage.
      if (/bbox|area/i.test(ev.error ?? "")) {
        this.state = "capped";
        this.#emit();
      } else if (/concurrent/i.test(ev.error ?? "")) {
        this.state = "refused";
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
