import { EventEmitter } from "node:events";
import WebSocket from "ws";

export interface Frame {
  type: string;
  [k: string]: unknown;
}

export interface LinkEvents {
  open: [];
  close: [code: number, reason: string];
  frame: [frame: Frame];
  error: [message: string];
}

const MIN_BACKOFF = 5_000;
const MAX_BACKOFF = 5 * 60_000;
const REFUSED_BACKOFF = 30 * 60_000; // after the server refused us (1008): don't hammer it
const PING_EVERY = 30_000;
const SILENCE = 60_000; // no frame or pong for this long → the socket is dead whatever TCP thinks

// One WebSocket to aiscast /v1/stream with reconnect, jittered backoff, and a silence watchdog.
// `url()` and `headers()` are re-evaluated on every connect so a refreshed token is picked up without a restart.
export class Link extends EventEmitter<LinkEvents> {
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: NodeJS.Timeout | null = null;
  private pinger: NodeJS.Timeout | null = null;
  private lastActivity = 0;
  private stopped = true;
  lastRefusal: string | null = null;

  constructor(
    private readonly url: () => string,
    private readonly headers: () => Record<string, string> = () => ({}),
    private readonly log: (msg: string) => void = () => {},
  ) {
    super();
  }

  get open(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  start(): void {
    this.stopped = false;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    this.stopPing();
    this.ws?.close(1000, "plugin stopped");
    this.ws = null;
  }

  // Reconnect now: drop the current socket, or cut a pending backoff short. Used after a token refresh.
  reconnect(): void {
    if (this.ws) {
      this.ws.terminate();
      return;
    }
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
      this.connect();
    }
  }

  send(frame: Frame): boolean {
    if (!this.open) return false;
    this.ws!.send(JSON.stringify(frame));
    return true;
  }

  private connect(): void {
    if (this.stopped) return;
    const url = this.url();
    this.log(`connecting to ${url}`);
    const ws = new WebSocket(url, { headers: this.headers(), perMessageDeflate: true, handshakeTimeout: 30_000 });
    this.ws = ws;
    this.lastActivity = Date.now();
    ws.on("open", () => {
      this.attempt = 0;
      this.lastRefusal = null;
      this.startPing(ws);
      this.emit("open");
    });
    ws.on("message", (data) => {
      this.lastActivity = Date.now();
      let frame: Frame;
      try {
        frame = JSON.parse(data.toString()) as Frame;
      } catch {
        return;
      }
      if (frame.type === "error") {
        const message = String(frame.error ?? "");
        this.emit("error", message);
      }
      this.emit("frame", frame);
    });
    ws.on("pong", () => {
      this.lastActivity = Date.now();
    });
    ws.on("error", (err) => this.log(`socket error: ${err.message}`));
    ws.on("close", (code, reason) => {
      this.stopPing();
      if (this.ws === ws) this.ws = null;
      const why = reason.toString();
      this.emit("close", code, why);
      if (code === 1008) this.lastRefusal = why;
      this.scheduleReconnect(code === 1008);
    });
  }

  private scheduleReconnect(refused: boolean): void {
    if (this.stopped || this.timer) return;
    const base = refused ? REFUSED_BACKOFF : Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** this.attempt++);
    const delay = base * (0.8 + Math.random() * 0.4);
    this.log(`reconnecting in ${Math.round(delay / 1000)} s`);
    this.timer = setTimeout(() => {
      this.timer = null;
      this.connect();
    }, delay);
  }

  private startPing(ws: WebSocket): void {
    this.stopPing();
    this.pinger = setInterval(() => {
      if (Date.now() - this.lastActivity > SILENCE) {
        this.log("no frames or pongs for 60 s; reconnecting");
        ws.terminate();
        return;
      }
      if (ws.readyState === WebSocket.OPEN) ws.ping();
    }, PING_EVERY);
  }

  private stopPing(): void {
    if (this.pinger) clearInterval(this.pinger);
    this.pinger = null;
  }
}
