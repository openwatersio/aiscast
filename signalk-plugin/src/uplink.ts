import { mkdir, readdir, readFile, stat, unlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Frame, Link } from "./link.js";
import { payloadKey, tagged } from "./nmea.js";

const ACK_TIMEOUT = 30_000;
const FLUSH_EVERY = 15_000; // in-memory backlog to disk; a crash loses at most this much
const FILE_MAX = 500; // sentences per queue file = per replayed frame
const QUEUE_MAX_BYTES = 100 * 1024 * 1024;
const SEEN_TTL = 5 * 60_000; // payloads received from aiscast are never published back within this window
const SEEN_MAX = 20_000;

interface InFlight {
  sentences: string[];
  at: number;
  fromQueue: boolean; // still on disk: never copied back into the backlog
  settle?: (acked: boolean) => void;
}

export interface UplinkStats {
  sent: number; // sentences acked
  queued: number; // sentences on disk + in memory waiting
  dropped: number; // sentences discarded by the queue cap
  inFlight: number;
}

// Sends received sentences to aiscast as they arrive; anything not acked, or heard while offline, waits on disk
// and is replayed oldest-first on the next connection before live sending resumes.
export class Uplink {
  enabled = true; // false when no token: hearing then discards instead of queueing forever
  private backlog: string[] = [];
  private inflight: InFlight[] = [];
  private pendingLive: string[] = [];
  private liveScheduled = false;
  private draining = false;
  private drainAgain = false;
  private flushTimer: NodeJS.Timeout | null = null;
  private ackTimer: NodeJS.Timeout | null = null;
  private seen = new Map<string, number>();
  private readonly dir: string;
  private readonly onOpen = () => this.drain();
  private readonly onClose = () => this.requeueInFlight("socket closed");
  private readonly onFrame = (f: Frame) => this.ack(f);
  stats: UplinkStats = { sent: 0, queued: 0, dropped: 0, inFlight: 0 };

  constructor(
    private readonly link: Link,
    dataDir: string,
    private readonly log: (msg: string) => void = () => {},
  ) {
    this.dir = join(dataDir, "queue");
  }

  async start(): Promise<void> {
    await mkdir(this.dir, { recursive: true });
    this.stats.queued = await this.countQueued();
    this.link.on("open", this.onOpen);
    this.link.on("close", this.onClose);
    this.link.on("frame", this.onFrame);
    this.flushTimer = setInterval(() => this.flush(), FLUSH_EVERY);
    this.ackTimer = setInterval(() => this.checkAcks(), 5_000);
  }

  async stop(): Promise<void> {
    if (this.flushTimer) clearInterval(this.flushTimer);
    if (this.ackTimer) clearInterval(this.ackTimer);
    this.flushTimer = this.ackTimer = null;
    this.link.off("open", this.onOpen);
    this.link.off("close", this.onClose);
    this.link.off("frame", this.onFrame);
    this.requeueInFlight("stopping");
    this.backlog.unshift(...this.pendingLive);
    this.stats.queued += this.pendingLive.length;
    this.pendingLive = [];
    await this.flush();
  }

  // Remember a payload that came from aiscast so a local re-broadcast of it is not published back.
  noteReceived(sentence: string, now = Date.now()): void {
    if (this.seen.size >= SEEN_MAX) this.pruneSeen(now);
    this.seen.set(payloadKey(sentence), now + SEEN_TTL);
  }

  // A sentence the boat's receiver heard, already filtered to AIS by the caller.
  hear(sentence: string, now = Date.now(), source?: string): void {
    if (!this.enabled) return;
    const exp = this.seen.get(payloadKey(sentence));
    if (exp !== undefined) {
      if (exp > now) return;
      this.seen.delete(payloadKey(sentence));
    }
    const line = tagged(sentence, now, source);
    if (this.link.open && !this.draining && this.backlog.length === 0) {
      this.pendingLive.push(line);
      if (!this.liveScheduled) {
        this.liveScheduled = true;
        setImmediate(() => this.sendLive());
      }
    } else {
      this.backlog.push(line);
      this.stats.queued++;
      if (this.backlog.length >= FILE_MAX) this.flush();
    }
  }

  private sendLive(): void {
    this.liveScheduled = false;
    const sentences = this.pendingLive;
    this.pendingLive = [];
    if (sentences.length === 0) return;
    if (!this.sendFrame(sentences, false)) {
      this.backlog.push(...sentences);
      this.stats.queued += sentences.length;
    }
  }

  private sendFrame(sentences: string[], fromQueue: boolean, settle?: (acked: boolean) => void): boolean {
    const frame: Frame = { type: "publish", nmea: sentences };
    if (fromQueue) frame.replay = true; // aiscast archives stale replayed sentences without emitting them live
    if (!this.link.send(frame)) return false;
    this.inflight.push({ sentences, at: Date.now(), fromQueue, settle });
    this.stats.inFlight = this.inflight.length;
    return true;
  }

  private ack(f: Frame): void {
    if (f.type !== "ack") return;
    const head = this.inflight.shift();
    this.stats.inFlight = this.inflight.length;
    if (!head) return;
    this.stats.sent += head.sentences.length;
    head.settle?.(true);
  }

  private checkAcks(): void {
    const head = this.inflight[0];
    if (head && Date.now() - head.at > ACK_TIMEOUT) {
      this.log(`no ack for ${Math.round((Date.now() - head.at) / 1000)} s; reconnecting`);
      this.requeueInFlight("ack timeout");
      this.link.reconnect();
    }
  }

  private requeueInFlight(why: string): void {
    if (this.inflight.length === 0) return;
    const live: string[] = [];
    for (const f of this.inflight) {
      if (f.fromQueue) f.settle?.(false);
      else live.push(...f.sentences);
    }
    this.inflight = [];
    this.stats.inFlight = 0;
    if (live.length > 0) {
      this.log(`${why}: ${live.length} unacked sentences back to the queue`);
      this.backlog.unshift(...live);
      this.stats.queued += live.length;
    }
  }

  // Backlog → one file. Oldest files go when the cap is exceeded. Never throws: a full or read-only disk
  // must not take the Signal K process down, so the backlog is kept in memory and retried next time.
  private async flush(): Promise<void> {
    if (this.backlog.length === 0) return;
    const sentences = this.backlog;
    this.backlog = [];
    try {
      await writeFile(join(this.dir, `${Date.now()}.json`), JSON.stringify(sentences));
    } catch (err) {
      this.log(`queue write failed: ${(err as Error).message}`);
      this.backlog.unshift(...sentences);
      return;
    }
    await this.enforceCap().catch((err) => this.log(`queue cap: ${(err as Error).message}`));
  }

  private async files(): Promise<string[]> {
    return (await readdir(this.dir)).filter((f) => f.endsWith(".json")).sort();
  }

  private async readQueueFile(name: string): Promise<string[] | null> {
    try {
      const parsed = JSON.parse(await readFile(join(this.dir, name), "utf8")) as unknown;
      return Array.isArray(parsed) ? (parsed as string[]) : null;
    } catch {
      return null;
    }
  }

  private async countQueued(): Promise<number> {
    let n = 0;
    for (const f of await this.files()) {
      const sentences = await this.readQueueFile(f);
      if (sentences) n += sentences.length;
      else await unlink(join(this.dir, f)).catch(() => {});
    }
    return n;
  }

  private async enforceCap(): Promise<void> {
    const files = await this.files();
    const sizes = await Promise.all(files.map((f) => stat(join(this.dir, f)).then((s) => s.size, () => 0)));
    let total = sizes.reduce((a, b) => a + b, 0);
    for (let i = 0; i < files.length && total > QUEUE_MAX_BYTES; i++) {
      const n = (await this.readQueueFile(files[i]))?.length ?? 0;
      await unlink(join(this.dir, files[i])).catch(() => {});
      total -= sizes[i];
      this.stats.dropped += n;
      this.stats.queued -= n;
    }
  }

  // Replay queued files oldest-first, one frame in flight, then let live sending resume. A drain that is cut
  // short (socket closed, ack timeout) leaves the unacked remainder on disk for the next open.
  private drain(): void {
    if (this.draining) {
      this.drainAgain = true;
      return;
    }
    this.draining = true;
    this.drainAgain = false;
    this.drainLoop()
      .catch((err) => this.log(`drain failed: ${(err as Error).message}`))
      .finally(() => {
        this.draining = false;
        if (this.drainAgain && this.link.open) this.drain();
      });
  }

  private async drainLoop(): Promise<void> {
    for (;;) {
      await this.flush();
      const [next] = await this.files();
      if (!next) return;
      const path = join(this.dir, next);
      const sentences = await this.readQueueFile(next);
      if (!sentences) {
        await unlink(path).catch(() => {});
        continue;
      }
      for (let i = 0; i < sentences.length; i += FILE_MAX) {
        const chunk = sentences.slice(i, i + FILE_MAX);
        const acked = await new Promise<boolean>((resolve) => {
          if (!this.sendFrame(chunk, true, resolve)) resolve(false);
        });
        if (!acked) {
          if (i > 0) await writeFile(path, JSON.stringify(sentences.slice(i))).catch(() => {});
          return;
        }
        this.stats.queued -= chunk.length;
      }
      await unlink(path);
    }
  }

  private pruneSeen(now: number): void {
    for (const [k, exp] of this.seen) if (exp <= now) this.seen.delete(k);
    // Still full of live entries: drop the oldest (Map keeps insertion order) rather than scan on every event.
    let excess = this.seen.size - SEEN_MAX / 2;
    for (const k of this.seen.keys()) {
      if (excess-- <= 0) break;
      this.seen.delete(k);
    }
  }
}
