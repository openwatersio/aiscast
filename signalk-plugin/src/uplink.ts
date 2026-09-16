import { mkdir, readdir, readFile, rename, stat, unlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Frame, Link } from "./link.js";
import { payloadKey, tagged } from "./nmea.js";

const ACK_TIMEOUT = 30_000;
const FLUSH_EVERY = 15_000; // in-memory backlog to disk; a crash loses at most this much
const FILE_MAX = 500; // sentences per queue file
const QUEUE_MAX_BYTES = 100 * 1024 * 1024;
const CAP_EVERY = 60_000;
const SEEN_TTL = 5 * 60_000; // payloads received from aiscast are never published back within this window
const SEEN_MAX = 20_000;
const READ_BATCH = 32; // queue files read at once at start; the disk is usually an SD card
// The server's publish limits, until a welcome frame gives this station's own. A frame is truncated to
// `frame`, and anything past `perMin` is dropped and still acked, so both have to be respected here.
const DEFAULT_LIMITS = { frame: 1000, perMin: 6000 };
const PACE_MARGIN = 0.9; // of the per-minute limit: clock skew and jitter must not push a frame over it
const LIMIT_WINDOW = 60_000; // the server's publish limit resets on a window this long

interface InFlight {
  sentences: string[];
  at: number;
  replay: boolean; // sent by the drain, which owns requeueing it; a closed socket must not copy it back
  settle?: (accepted: number | null) => void;
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
  private stopped = false;
  private flushTimer: NodeJS.Timeout | null = null;
  private ackTimer: NodeJS.Timeout | null = null;
  private capTimer: NodeJS.Timeout | null = null;
  private seen = new Map<string, number>();
  private listing: string[] = []; // queue file names, oldest first; the drain works from this, not readdir
  private lastName = 0;
  private limits = { ...DEFAULT_LIMITS };
  private paceUntil = 0;
  private paceWake: (() => void) | null = null;
  private capping = false;
  private flushing: Promise<void> = Promise.resolve();
  private drainDone: Promise<void> | null = null;
  private readonly dir: string;
  private readonly onOpen = () => this.drain();
  private readonly onClose = () => this.requeueInFlight("socket closed");
  private readonly onFrame = (f: Frame) => this.frame(f);
  stats: UplinkStats = { sent: 0, queued: 0, dropped: 0, inFlight: 0 };

  constructor(
    private readonly link: Link,
    dataDir: string,
    private readonly log: (msg: string) => void = () => {},
  ) {
    this.dir = join(dataDir, "queue");
  }

  async start(): Promise<void> {
    this.stopped = false;
    await mkdir(this.dir, { recursive: true });
    this.listing = (await readdir(this.dir)).filter((f) => f.endsWith(".json")).sort();
    this.lastName = Number(this.listing.at(-1)?.slice(0, -".json".length)) || 0;
    this.stats.queued = await this.countQueued();
    this.link.on("open", this.onOpen);
    this.link.on("close", this.onClose);
    this.link.on("frame", this.onFrame);
    this.flushTimer = setInterval(() => this.flush(), FLUSH_EVERY);
    this.ackTimer = setInterval(() => this.checkAcks(), 5_000);
    this.capTimer = setInterval(() => {
      this.enforceCap().catch((err) => this.log(`queue cap: ${(err as Error).message}`));
    }, CAP_EVERY);
  }

  async stop(): Promise<void> {
    this.stopped = true;
    for (const t of [this.flushTimer, this.ackTimer, this.capTimer]) if (t) clearInterval(t);
    this.flushTimer = this.ackTimer = this.capTimer = null;
    this.link.off("open", this.onOpen);
    this.link.off("close", this.onClose);
    this.link.off("frame", this.onFrame);
    // Settle the drain before flushing: it hands back the frame it was sending, and a drain sitting out the
    // publish limit must not hold up plugin stop for the rest of that minute.
    this.requeueInFlight("stopping");
    this.paceWake?.();
    await this.drainDone;
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

  private sendFrame(sentences: string[], replay: boolean, settle?: (accepted: number | null) => void): boolean {
    const frame: Frame = { type: "publish", nmea: sentences };
    if (replay) frame.replay = true; // aiscast archives stale replayed sentences without emitting them live
    if (!this.link.send(frame)) return false;
    this.inflight.push({ sentences, at: Date.now(), replay, settle });
    this.stats.inFlight = this.inflight.length;
    return true;
  }

  private frame(f: Frame): void {
    if (f.type === "welcome") {
      const lim = (f as { limits?: { publish_frame?: number; publish_per_min?: number } }).limits;
      if (typeof lim?.publish_frame === "number" && lim.publish_frame > 0) this.limits.frame = lim.publish_frame;
      if (typeof lim?.publish_per_min === "number" && lim.publish_per_min > 0) this.limits.perMin = lim.publish_per_min;
      return;
    }
    if (f.type === "ack") this.ack(f);
  }

  // The ack carries the number of sentences the server took. A short count means the rest ran into the
  // per-minute publish limit and were dropped, so they go back on the queue instead of counting as sent.
  private ack(f: Frame): void {
    const head = this.inflight.shift();
    this.stats.inFlight = this.inflight.length;
    if (!head) return;
    const n = typeof f.n === "number" ? Math.max(0, Math.min(f.n, head.sentences.length)) : head.sentences.length;
    this.stats.sent += n;
    if (head.settle) {
      head.settle(n);
      return;
    }
    const rest = head.sentences.slice(n);
    if (rest.length === 0) return;
    this.log(`publish limit: ${rest.length} sentences back to the queue`);
    this.backlog.unshift(...rest);
    this.stats.queued += rest.length;
    this.paceUntil = Date.now() + LIMIT_WINDOW;
    this.drain(); // live sending is blocked until the backlog is empty again; only a drain empties it
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
      if (f.replay) f.settle?.(null);
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

  // Flushes run one at a time and in order, so two of them cannot top up the same file and lose one's work.
  private flush(): Promise<void> {
    this.flushing = this.flushing.then(() => this.flushOnce());
    return this.flushing;
  }

  // Backlog → the newest queue file while it has room, a new one otherwise. Topping up is what keeps a long
  // offline stretch from leaving a file behind every fifteen seconds: the count follows the sentences owed,
  // not the time spent offline. Never throws: a full or read-only disk must not take the Signal K process
  // down, so the backlog is kept in memory and retried next time.
  private async flushOnce(): Promise<void> {
    if (this.backlog.length === 0) return;
    const sentences = this.backlog;
    this.backlog = [];
    // The drain takes files off the front of the listing, so the last one is never a file it is sending.
    const tail = this.listing.at(-1);
    const room = tail ? await this.readQueueFile(tail) : null;
    const name = room && room.length < FILE_MAX ? tail! : this.newName();
    const path = join(this.dir, name);
    const tmp = `${path}.tmp`; // the listing only takes .json, so a torn write is never picked up
    try {
      await writeFile(tmp, JSON.stringify(room && room.length < FILE_MAX ? room.concat(sentences) : sentences));
      await rename(tmp, path);
    } catch (err) {
      this.log(`queue write failed: ${(err as Error).message}`);
      await unlink(tmp).catch(() => {});
      this.backlog.unshift(...sentences);
      return;
    }
    if (name !== tail) this.listing.push(name); // newest name sorts last, so appending keeps the order
  }

  // Monotonic, so a file written in the same millisecond as the last one cannot take its name.
  private newName(): string {
    const now = Date.now();
    this.lastName = now > this.lastName ? now : this.lastName + 1;
    return `${this.lastName}.json`;
  }

  private async readQueueFile(name: string): Promise<string[] | null> {
    try {
      const parsed = JSON.parse(await readFile(join(this.dir, name), "utf8")) as unknown;
      return Array.isArray(parsed) ? (parsed as string[]) : null;
    } catch {
      return null;
    }
  }

  // Sentences already on disk, for the status line. Read in batches: a backlog of many thousands of files
  // would otherwise hold up plugin start one open() at a time.
  private async countQueued(): Promise<number> {
    let n = 0;
    const bad = new Set<string>();
    for (let i = 0; i < this.listing.length; i += READ_BATCH) {
      const batch = this.listing.slice(i, i + READ_BATCH);
      const read = await Promise.all(batch.map((f) => this.readQueueFile(f)));
      read.forEach((sentences, j) => {
        if (sentences) n += sentences.length;
        else bad.add(batch[j]);
      });
    }
    if (bad.size > 0) {
      await Promise.all([...bad].map((f) => unlink(join(this.dir, f)).catch(() => {})));
      this.listing = this.listing.filter((f) => !bad.has(f));
    }
    return n;
  }

  // Oldest files go when the queue outgrows its cap. Skipped while a drain is running: the drain is already
  // shrinking the queue, and both work from the same listing.
  private async enforceCap(): Promise<void> {
    if (this.draining || this.capping || this.listing.length === 0) return;
    this.capping = true;
    try {
      await this.trim();
    } finally {
      this.capping = false;
    }
  }

  private async trim(): Promise<void> {
    const sizes = await Promise.all(
      this.listing.map((f) => stat(join(this.dir, f)).then((s) => s.size, () => 0)),
    );
    let total = sizes.reduce((a, b) => a + b, 0);
    let i = 0;
    for (; i < this.listing.length && total > QUEUE_MAX_BYTES; i++) {
      const n = (await this.readQueueFile(this.listing[i]))?.length ?? 0;
      await unlink(join(this.dir, this.listing[i])).catch(() => {});
      total -= sizes[i];
      this.stats.dropped += n;
      this.stats.queued -= n;
    }
    this.listing.splice(0, i);
  }

  // Replay the queue oldest-first, one frame in flight, then let live sending resume. A drain that is cut
  // short (socket closed, ack timeout) leaves the unacked remainder on disk for the next open.
  private drain(): void {
    if (this.draining) {
      this.drainAgain = true;
      return;
    }
    this.draining = true;
    this.drainAgain = false;
    this.drainDone = this.drainLoop()
      .catch((err) => this.log(`drain failed: ${(err as Error).message}`))
      .finally(() => {
        this.draining = false;
        this.drainDone = null;
        if (this.drainAgain && this.link.open) this.drain();
      });
  }

  private async drainLoop(): Promise<void> {
    for (;;) {
      if (this.stopped) return;
      const batch = await this.nextBatch();
      if (!batch) {
        if (this.backlog.length === 0) return; // disk and memory both empty: live sending resumes
        // Sentences heard during the drain go out straight from memory. Writing them to a file only to read
        // it back would hand the drain a fresh file on every pass, and it would never reach the end.
        const chunk = this.backlog.splice(0, this.limits.frame);
        const accepted = await this.publish(chunk);
        if (accepted === null) {
          this.backlog.unshift(...chunk);
          return;
        }
        this.stats.queued -= accepted;
        this.backlog.unshift(...chunk.slice(accepted));
        continue;
      }
      const chunk = batch.sentences.slice(0, this.limits.frame);
      const accepted = await this.publish(chunk);
      // Nothing acked: the files are untouched on disk, so hand them straight back to the next drain.
      if (accepted === null || !(await this.keep(batch.files, batch.sentences.slice(accepted)))) {
        this.listing.unshift(...batch.files);
        return;
      }
      this.stats.queued -= accepted;
    }
  }

  // The oldest queued sentences, up to one frame's worth, and the files they came from. Spanning several
  // files per frame is what lets a backlog of thousands of small files drain in batches rather than one
  // round trip each.
  private async nextBatch(): Promise<{ sentences: string[]; files: string[] } | null> {
    const sentences: string[] = [];
    const files: string[] = [];
    while (this.listing.length > 0 && sentences.length < this.limits.frame) {
      const names = this.listing.slice(0, READ_BATCH); // read ahead: one open() at a time crawls on an SD card
      const read = await Promise.all(names.map((f) => this.readQueueFile(f)));
      let taken = 0;
      for (const queued of read) {
        if (!queued) {
          await unlink(join(this.dir, names[taken++])).catch(() => {});
          continue;
        }
        // One oversized file still goes on its own; `keep` writes back whatever the frame did not cover.
        if (files.length > 0 && sentences.length + queued.length > this.limits.frame) break;
        sentences.push(...queued);
        files.push(names[taken++]);
        if (sentences.length >= this.limits.frame) break;
      }
      this.listing.splice(0, taken);
      if (taken < names.length) break; // stopped short: the frame is full
    }
    return files.length > 0 ? { sentences, files } : null;
  }

  // Replace the files a frame came from with whatever the server did not take, under the oldest of their
  // names so the queue keeps its order. False when the remainder could not be written: nothing is deleted,
  // and the caller puts the files back.
  private async keep(files: string[], rest: string[]): Promise<boolean> {
    if (rest.length > 0) {
      const path = join(this.dir, files[0]);
      const tmp = `${path}.tmp`; // the listing only takes .json, so a torn write is never picked up
      try {
        await writeFile(tmp, JSON.stringify(rest));
        await rename(tmp, path); // atomic: the file is either the whole frame or the remainder, never half
      } catch (err) {
        this.log(`queue rewrite failed: ${(err as Error).message}`);
        await unlink(tmp).catch(() => {});
        return false;
      }
      this.listing.unshift(files[0]);
    }
    for (const name of rest.length > 0 ? files.slice(1) : files) {
      await unlink(join(this.dir, name)).catch(() => {});
    }
    return true;
  }

  // One publish frame, paced to stay under the server's per-minute limit: sentences past it are dropped
  // and acked all the same, so an unpaced replay loses most of the backlog it is trying to deliver.
  // Resolves with the number of sentences the server took, or null if it never acked.
  private async publish(chunk: string[]): Promise<number | null> {
    const wait = this.paceUntil - Date.now();
    if (wait > 0) {
      await new Promise<void>((resolve) => {
        this.paceWake = resolve;
        setTimeout(resolve, wait).unref();
      });
      this.paceWake = null;
    }
    if (this.stopped || !this.link.open) return null;
    this.paceUntil = Date.now() + Math.ceil((chunk.length * 60_000) / (this.limits.perMin * PACE_MARGIN));
    const accepted = await new Promise<number | null>((resolve) => {
      if (!this.sendFrame(chunk, true, resolve)) resolve(null);
    });
    // Short ack: this minute's allowance is spent, so wait out the window rather than burn the rest there.
    if (accepted !== null && accepted < chunk.length) this.paceUntil = Date.now() + LIMIT_WINDOW;
    return accepted;
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
