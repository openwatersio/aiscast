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

// A frame's worth of queued sentences and the files they came from, in order, with how many each holds.
interface Batch {
  sentences: string[];
  files: { name: string; count: number }[];
}

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
  private sizes = new Map<string, number>(); // bytes per queue file, kept up to date as files come and go
  private bytes = 0;
  private lastName = 0;
  private limits = { ...DEFAULT_LIMITS };
  private paceUntil = 0;
  private paceWake: (() => void) | null = null;
  private queueOp: Promise<unknown> = Promise.resolve();
  private drainDone: Promise<void> | null = null;
  private readonly dir: string;
  private readonly onOpen = () => this.drain();
  private readonly onClose = () => {
    this.requeueInFlight("socket closed");
    this.paceWake?.(); // the deadline stands; only the sleep ends, so the reconnect drain starts on time
  };
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
    await this.measure();
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
    // Split at the frame limit: over it the server silently keeps the first `frame` sentences and acks
    // short, which would read here as the per-minute limit and stall live sending for a minute.
    for (let i = 0; i < sentences.length; i += this.limits.frame) {
      if (!this.sendFrame(sentences.slice(i, i + this.limits.frame), false)) {
        this.backlog.push(...sentences.slice(i));
        this.stats.queued += sentences.length - i;
        return;
      }
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

  // Queue-file work runs one operation at a time, in order. The flush timer, the drain and the cap check all
  // mutate the same directory and the same listing, and every one of them awaits partway through: without
  // this, a flush could top up a file the drain had already read and was about to delete.
  private exclusive<T>(work: () => Promise<T>): Promise<T> {
    const run = this.queueOp.then(work);
    this.queueOp = run.then(
      () => {},
      () => {},
    );
    return run;
  }

  private flush(): Promise<void> {
    return this.exclusive(() => this.flushOnce()).catch(() => {});
  }

  // Backlog → the newest queue file while it has room, then files of FILE_MAX. Topping up is what keeps a
  // long offline stretch from leaving a file behind every fifteen seconds: the count follows the sentences
  // owed, not the time spent offline. Never throws: a full or read-only disk must not take the Signal K
  // process down, so whatever is not on disk yet stays in the backlog and is retried next time.
  private async flushOnce(): Promise<void> {
    if (this.backlog.length === 0) return;
    const sentences = this.backlog;
    this.backlog = [];
    const tail = this.listing.at(-1);
    const room = tail ? await this.readQueueFile(tail) : null;
    const topUp = room !== null && room.length < FILE_MAX;
    const pending = topUp ? room.concat(sentences) : sentences;
    const onDisk = topUp ? room.length : 0; // the tail's own sentences, already safe, at the front of pending
    for (let i = 0; i < pending.length; i += FILE_MAX) {
      const name = i === 0 && topUp ? tail! : this.newName();
      if (!(await this.write(name, pending.slice(i, i + FILE_MAX)))) {
        this.backlog.unshift(...pending.slice(Math.max(i, onDisk))); // keep what did not land, oldest first
        return;
      }
      if (name !== tail) this.listing.push(name); // newest name sorts last, so appending keeps the order
    }
  }

  // One queue file, written whole. The temp file keeps a half-written array from ever being picked up: the
  // listing only takes .json, and rename is atomic.
  private async write(name: string, sentences: string[]): Promise<boolean> {
    const path = join(this.dir, name);
    const tmp = `${path}.tmp`;
    const data = JSON.stringify(sentences);
    try {
      await writeFile(tmp, data);
      await rename(tmp, path);
    } catch (err) {
      this.log(`queue write failed: ${(err as Error).message}`);
      await unlink(tmp).catch(() => {});
      return false;
    }
    const size = Buffer.byteLength(data);
    this.bytes += size - (this.sizes.get(name) ?? 0);
    this.sizes.set(name, size);
    return true;
  }

  // Deletes a queue file. False when it would not go: the caller leaves it on the listing rather than
  // stranding a file on disk that nothing will look at again until the next start.
  private async remove(name: string): Promise<boolean> {
    try {
      await unlink(join(this.dir, name));
    } catch (err) {
      this.log(`queue delete failed: ${(err as Error).message}`);
      return false;
    }
    this.bytes -= this.sizes.get(name) ?? 0;
    this.sizes.delete(name);
    return true;
  }

  // Queue size on disk, measured once at start. Batched: a backlog of many thousands of files must not put
  // that many requests in flight at once.
  private async measure(): Promise<void> {
    this.sizes.clear();
    this.bytes = 0;
    for (let i = 0; i < this.listing.length; i += READ_BATCH) {
      const batch = this.listing.slice(i, i + READ_BATCH);
      const sizes = await Promise.all(batch.map((f) => stat(join(this.dir, f)).then((x) => x.size, () => 0)));
      sizes.forEach((size, j) => {
        this.sizes.set(batch[j], size);
        this.bytes += size;
      });
    }
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
    for (const f of bad) {
      if (!(await this.remove(f))) bad.delete(f); // still on disk: keep it listed rather than lose track of it
    }
    if (bad.size > 0) this.listing = this.listing.filter((f) => !bad.has(f));
    return n;
  }

  // Oldest files go when the queue outgrows its cap. This runs during a drain as well: a replay is paced,
  // so a receiver busy enough to outrun it would otherwise grow the queue past the cap unchecked.
  private async enforceCap(): Promise<void> {
    if (this.bytes <= QUEUE_MAX_BYTES) return; // the running total, so an idle tick touches no files at all
    await this.exclusive(() => this.trim());
  }

  private async trim(): Promise<void> {
    let i = 0;
    for (; i < this.listing.length && this.bytes > QUEUE_MAX_BYTES; i++) {
      const name = this.listing[i];
      const n = (await this.readQueueFile(name))?.length ?? 0;
      if (!(await this.remove(name))) break; // leave it listed: a file nothing can see is a file nothing frees
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
      const batch = await this.exclusive(() => this.nextBatch());
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
      // Nothing acked, or the remainder would not write: the files are as they were on disk, so hand them
      // straight back to the next drain.
      const kept = accepted !== null && (await this.exclusive(() => this.keep(batch, accepted)));
      if (accepted === null || !kept) {
        this.listing.unshift(...batch.files.map((f) => f.name));
        return;
      }
      this.stats.queued -= accepted;
    }
  }

  // The oldest queued sentences, up to one frame's worth, and the files they came from. Spanning several
  // files per frame is what lets a backlog of thousands of small files drain in batches rather than one
  // round trip each.
  private async nextBatch(): Promise<Batch | null> {
    const sentences: string[] = [];
    const files: Batch["files"] = [];
    while (this.listing.length > 0 && sentences.length < this.limits.frame) {
      const names = this.listing.slice(0, READ_BATCH); // read ahead: one open() at a time crawls on an SD card
      const read = await Promise.all(names.map((f) => this.readQueueFile(f)));
      let taken = 0;
      for (const queued of read) {
        if (!queued) {
          const name = names[taken];
          if (!(await this.remove(name))) return files.length > 0 ? { sentences, files } : null;
          taken++;
          continue;
        }
        // One oversized file still goes on its own; `keep` puts back whatever the frame did not cover.
        if (files.length > 0 && sentences.length + queued.length > this.limits.frame) break;
        sentences.push(...queued);
        files.push({ name: names[taken++], count: queued.length });
        if (sentences.length >= this.limits.frame) break;
      }
      this.listing.splice(0, taken);
      if (taken < names.length) break; // stopped short: the frame is full
    }
    return files.length > 0 ? { sentences, files } : null;
  }

  // Settles the files a frame came from against what the server actually took. Files it took whole are
  // deleted; the one the ack stopped inside is rewritten with its own remaining tail, and the files after it
  // are already right on disk and only go back on the listing. So only ever one file is rewritten, and it
  // can only shrink: a rate-limited replay cannot pile a whole frame into a single file. False when that
  // rewrite failed, in which case nothing was deleted and the caller puts every file back.
  private async keep(batch: Batch, accepted: number): Promise<boolean> {
    let start = 0; // where the file at `i` begins within batch.sentences
    let i = 0;
    while (i < batch.files.length && start + batch.files[i].count <= accepted) {
      start += batch.files[i].count;
      i++;
    }
    if (i < batch.files.length && accepted > start) {
      const tail = batch.sentences.slice(accepted, start + batch.files[i].count);
      if (!(await this.write(batch.files[i].name, tail))) return false;
    }
    this.listing.unshift(...batch.files.slice(i).map((f) => f.name));
    for (const f of batch.files.slice(0, i)) {
      // A file the server has taken but that will not delete is left where it is rather than put back on the
      // listing: re-sending it forever is worse than the duplicate the next start makes, which aiscast drops.
      await this.remove(f.name);
    }
    return true;
  }

  // One publish frame, paced to stay under the server's per-minute limit: sentences past it are dropped
  // and acked all the same, so an unpaced replay loses most of the backlog it is trying to deliver.
  // Resolves with the number of sentences the server took, or null if it never acked.
  private async publish(chunk: string[]): Promise<number | null> {
    if (this.stopped || !this.link.open) return null; // before the wait: a sleep stop() cannot see holds it up
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
