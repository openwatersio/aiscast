import type { BBox } from "./stream";

// One stream for every tab in this browser. Anonymous clients get two concurrent streams per
// network address, which a household or a marina shares, so a stream per tab spent that
// budget on one person's third tab. The hub holds the socket, subscribes to the union of what
// its tabs are looking at, and passes each tab the events in its own view. It runs in a
// SharedWorker, or in the tab itself where there is none.

/** What one tab wants from the stream. */
export interface TabView {
  bbox: BBox[];
  mmsi: number[];
  /** A hidden tab's map draws nothing, so it yields first when the views do not all fit. */
  visible: boolean;
  /** When the tab was last shown or focused, in ms. Of two visible windows, the focused one wins. */
  active: number;
}

export type HubState = "connecting" | "live" | "reconnecting" | "capped" | "refused";
export type Limits = Record<string, unknown>;

export type ToHub = ({ type: "view" } & TabView) | { type: "close" };
export type ToTab =
  /**
   * `served` is false when this tab's boxes or follows did not fit in the union. `following`
   * says whether its follows did, which they can while its boxes do not.
   */
  | { type: "status"; state: HubState; served: boolean; following: boolean; limits?: Limits }
  /** The server's frame as it arrived, for the tab to parse. */
  | { type: "event"; data: string };

export interface Subscription {
  bbox: BBox[];
  mmsi: number[];
}

function bboxArea(b: BBox): number {
  return Math.abs(b[2] - b[0]) * Math.abs(b[3] - b[1]);
}

function covers(o: BBox, b: BBox): boolean {
  return o[0] <= b[0] && o[1] <= b[1] && o[2] >= b[2] && o[3] >= b[3];
}

/**
 * The one subscription that serves these tabs, within the key's limits. The server sums the
 * boxes' areas against the cap, so a box another tab already covers is left out rather than
 * counted twice. Tabs are taken visible first, then most recently active, and a tab whose
 * boxes or follows do not fit in what remains gets none of them: half a view would look live
 * and miss vessels. A tab is served when all it asked for is in; one whose follows fit but
 * whose boxes do not still follows, so the vessel it has open stays live.
 */
export function plan(
  tabs: readonly TabView[],
  limits?: Limits,
): Subscription & { served: boolean[]; following: boolean[] } {
  // 0 is unlimited and below 0 is MMSI-only, as in the welcome's limits.
  const area = typeof limits?.area === "number" ? limits.area : 0;
  const maxMmsi = typeof limits?.mmsis === "number" && limits.mmsis > 0 ? limits.mmsis : Infinity;
  const order = tabs
    .map((_, i) => i)
    .sort((a, b) => Number(tabs[b]!.visible) - Number(tabs[a]!.visible) || tabs[b]!.active - tabs[a]!.active);

  const bbox: BBox[] = [];
  const mmsi = new Set<number>();
  const served = tabs.map(() => false);
  const following = tabs.map(() => false);
  let used = 0;
  for (const i of order) {
    const tab = tabs[i]!;
    const newMmsi = new Set(tab.mmsi.filter((m) => !mmsi.has(m)));
    if (mmsi.size + newMmsi.size > maxMmsi) continue;
    newMmsi.forEach((m) => mmsi.add(m));
    following[i] = true;
    const extra = tab.bbox.filter((b) => !bbox.some((o) => covers(o, b)));
    const need = extra.reduce((sum, b) => sum + bboxArea(b), 0);
    if (!tab.bbox.length || area === 0 || (area > 0 && used + need <= area)) {
      bbox.push(...extra);
      used += need;
      served[i] = true;
    }
  }
  return { bbox, mmsi: [...mmsi], served, following };
}

interface EventFrame {
  mmsi: number;
  lat?: number;
  lon?: number;
}

/**
 * Whether a tab asked for this event. The server fills a vessel's last known position into
 * every event it sends, static data included, so an event without one matched by MMSI alone.
 */
export function wants(tab: TabView & { served: boolean; following: boolean }, ev: EventFrame): boolean {
  if (tab.following && tab.mmsi.includes(ev.mmsi)) return true;
  const { lat, lon } = ev;
  if (!tab.served || lat == null || lon == null) return false;
  return tab.bbox.some(([s, w, n, e]) => lat >= s && lat <= n && lon >= w && lon <= e);
}

/** A SharedWorker's port, or one end of a MessageChannel, typed for one side. */
export interface Port<Out, In> {
  postMessage(msg: Out): void;
  onmessage: ((e: MessageEvent<In>) => void) | null;
  close?(): void;
}

type TabPort = Port<ToTab, ToHub>;

interface Tab extends TabView {
  served: boolean;
  following: boolean;
  heard: number;
}

/** How often the hub tells its tabs it is alive, which is how a tab notices it has gone. */
export const HUB_HEARTBEAT = 5e3;
// A tab hidden for five minutes has its timers held to one a minute, and a frozen one has
// none, so this is generous. A frozen tab's view leaving the union is what should happen.
const TAB_SILENCE = 3 * 60e3;

export class Hub {
  state: HubState = "connecting";
  #url: string;
  #socket: (url: string) => WebSocket;
  #ws: WebSocket | undefined;
  #retry: ReturnType<typeof setTimeout> | undefined;
  #backoff = 1000;
  #welcomed = false;
  #limits: Limits | undefined;
  #tabs = new Map<TabPort, Tab>();
  /** The subscription the server holds, as last sent. */
  #sent = "";

  constructor(url: string, socket = (u: string) => new WebSocket(u)) {
    this.#url = url;
    this.#socket = socket;
    setInterval(() => this.#tick(), HUB_HEARTBEAT);
  }

  /** A tab counts from its first view, so a port that never says what it wants plans nothing. */
  add(port: TabPort) {
    port.onmessage = (e) => this.#onTab(port, e.data);
    port.postMessage(this.#status());
  }

  #onTab(port: TabPort, msg: ToHub) {
    if (msg.type === "close") {
      if (this.#tabs.delete(port)) this.#replan();
      return;
    }
    const { bbox, mmsi, visible, active } = msg;
    const prev = this.#tabs.get(port);
    this.#tabs.set(port, {
      bbox,
      mmsi,
      visible,
      active,
      served: prev?.served ?? true,
      following: prev?.following ?? true,
      heard: Date.now(),
    });
    // A reaped tab that wakes up is a new one, and needs the state it missed.
    if (!prev) port.postMessage(this.#status());
    if (!this.#ws && !this.#retry) this.#connect();
    this.#replan();
  }

  #status(tab?: Tab): ToTab {
    const { served = true, following = true } = tab ?? {};
    return { type: "status", state: this.state, served, following, limits: this.#limits };
  }

  #broadcast() {
    for (const [port, tab] of this.#tabs) port.postMessage(this.#status(tab));
  }

  #replan() {
    if (!this.#tabs.size) return this.#idle();
    const entries = [...this.#tabs];
    const next = plan(
      entries.map(([, t]) => t),
      this.#limits,
    );
    entries.forEach(([port, tab], i) => {
      if (tab.served === next.served[i] && tab.following === next.following[i]) return;
      tab.served = next.served[i]!;
      tab.following = next.following[i]!;
      port.postMessage(this.#status(tab));
    });
    this.#send({ bbox: next.bbox, mmsi: next.mmsi });
  }

  #send(sub: Subscription) {
    // The server checks a subscription against the key's limits, which arrive in the welcome.
    if (!this.#welcomed || this.#ws?.readyState !== WebSocket.OPEN) return;
    const key = JSON.stringify(sub);
    if (key === this.#sent) return;
    const empty = !sub.bbox.length && !sub.mmsi.length;
    // An empty subscribe asks for everything; a key with an area cap is refused it.
    if (empty && !this.#sent) return;
    this.#ws.send(JSON.stringify(empty ? { type: "unsubscribe" } : { type: "subscribe", ...sub }));
    this.#sent = key;
  }

  /** No tab is left, so the stream goes back to the address's budget. */
  #idle() {
    clearTimeout(this.#retry);
    this.#retry = undefined;
    const ws = this.#ws;
    this.#ws = undefined;
    this.#welcomed = false;
    this.#sent = "";
    this.state = "connecting";
    ws?.close();
  }

  #connect() {
    this.#retry = undefined;
    const ws = this.#socket(this.#url);
    this.#ws = ws;
    ws.onclose = () => {
      if (this.#ws !== ws) return;
      this.#ws = undefined;
      this.#welcomed = false;
      this.#sent = "";
      if (this.state !== "refused") this.state = "reconnecting";
      this.#broadcast();
      if (!this.#tabs.size) return;
      this.#retry = setTimeout(() => this.#connect(), this.#backoff);
      this.#backoff = Math.min(this.#backoff * 2, 30e3);
    };
    ws.onerror = () => ws.close();
    ws.onmessage = (e) => this.#ws === ws && this.#onFrame(e.data);
  }

  #onFrame(raw: string) {
    let ev: EventFrame & { type: string; error?: string; limits?: Limits };
    try {
      ev = JSON.parse(raw);
    } catch {
      return;
    }
    if (ev.type === "welcome") {
      // Not on open: the server accepts the socket before it checks the per-address stream
      // limit, then refuses and closes it. Resetting there retried every second forever.
      this.#backoff = 1000;
      this.#welcomed = true;
      this.#limits = ev.limits;
      this.state = "live";
      this.#replan();
      this.#broadcast();
      return;
    }
    if (ev.type === "error") {
      // A box outside what the key may subscribe to. Not a failure; the map shows the tiles.
      if (/bbox|area/i.test(ev.error ?? "")) {
        this.state = "capped";
        this.#broadcast();
      } else if (/concurrent/i.test(ev.error ?? "")) {
        this.state = "refused";
        this.#broadcast();
      }
      return;
    }
    if (ev.type !== "event") return;
    if (this.state !== "live") {
      this.state = "live";
      this.#broadcast();
    }
    for (const [port, tab] of this.#tabs) {
      if (wants(tab, ev)) port.postMessage({ type: "event", data: raw });
    }
  }

  #tick() {
    const cutoff = Date.now() - TAB_SILENCE;
    let reaped = false;
    for (const [port, tab] of this.#tabs) {
      if (tab.heard < cutoff) reaped = this.#tabs.delete(port);
    }
    if (reaped) this.#replan();
    this.#broadcast();
  }
}
