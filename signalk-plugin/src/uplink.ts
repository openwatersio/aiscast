import type { FileHandle } from "node:fs/promises";
import { appendFile, mkdir, open, readdir, readFile, truncate, unlink } from "node:fs/promises";
import { join } from "node:path";
import type { Frame, Link } from "./link.js";
import { payloadKey, tagged } from "./nmea.js";

const ACK_TIMEOUT = 30_000;
const FLUSH_EVERY = 15_000; // in-memory backlog to disk; a crash loses at most this much
const FLUSH_AT = 500; // backlog sentences that bring a flush forward
export const SEGMENT_MAX = 1024 * 1024; // bytes per segment file before the next append rolls over
const READ_BYTES = 256 * 1024; // window one frame is read through; comfortably more than a full frame holds
const QUEUE_MAX_BYTES = 100 * 1024 * 1024;
const CAP_EVERY = 60_000;
const SEEN_TTL = 5 * 60_000; // payloads received from aiscast are never published back within this window
const SEEN_MAX = 20_000;
const SCAN_BATCH = 8; // segments read at once at start; the disk is usually an SD card
const NEWLINE = 0x0a;
const SUFFIX = ".log";
// Errors that say a queue entry will never be readable, whatever the disk does next. Anything else (an
// exhausted file table, a bad sector, a busy device) may clear, and a segment must not be discarded over one
// of those while the sentences in it are still there to send.
const UNREADABLE = new Set(["EISDIR", "ENOTDIR", "EACCES", "EPERM", "ELOOP", "ENAMETOOLONG"]);
// The server's publish limits, until a welcome frame gives this station's own. A frame is truncated to
// `frame`, and anything past `perMin` is dropped and still acked, so both have to be respected here.
const DEFAULT_LIMITS = { frame: 1000, perMin: 6000 };
const PACE_MARGIN = 0.9; // of the per-minute limit: clock skew and jitter must not push a frame over it
const LIMIT_WINDOW = 60_000; // the server's publish limit resets on a window this long

// Why a segment yielded no sentences. "spent" holds nothing after the head, "damaged" holds nothing a frame
// could ever carry, and "failed" could not be read this time but may read fine later.
type Miss = "spent" | "damaged" | "failed";

// Complete sentences read out of one segment, and where they sit in it.
interface Read {
  lines: string[];
  widths: number[]; // bytes each line occupies, including its newline
  to: number; // byte offset just past the last complete line read
  eof: boolean; // nothing follows `to` in the segment
}

// The part of one segment a frame drew from. Chunks run oldest-first, and the first starts at `head`.
interface Chunk {
  name: string;
  from: number;
  to: number;
  count: number;
  eof: boolean; // `to` is the end of the segment, so acking the chunk whole spends it
}

// A frame's worth of queued sentences and the segment ranges they came from, in order.
interface Batch {
  sentences: string[];
  widths: number[];
  chunks: Chunk[];
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
//
// The queue is a directory of append-only segments, each one newline-delimited sentences, rolled over at
// SEGMENT_MAX. Sentences are only ever appended, and a frame the server takes only advances a byte offset
// into the oldest segment, so nothing on disk is rewritten between the append that puts a sentence there and
// the unlink that drops the whole segment. That offset is in memory, so a restart replays the oldest segment
// from its start; aiscast drops the duplicates, and the replay is bounded by one segment.
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
  private listing: string[] = []; // segment names, oldest first; the drain works from this, not readdir
  private sizes = new Map<string, number>(); // bytes per segment, kept up to date as they grow and go
  private stale = new Set<string>(); // segments done with that would not delete; retried by `sweep`
  private bytes = 0;
  private head = 0; // bytes of listing[0] the server has already taken
  private sending: string | null = null; // newest segment a frame in flight was read from
  private unwritable: string | null = null; // segment an append failed on; the next one starts elsewhere
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
    this.head = 0;
    await mkdir(this.dir, { recursive: true });
    this.listing = (await readdir(this.dir)).filter((f) => f.endsWith(SUFFIX)).sort();
    this.lastName = Number(this.listing.at(-1)?.slice(0, -SUFFIX.length)) || 0;
    const tail = this.listing.at(-1);
    if (tail !== undefined) await this.repair(tail);
    this.stats.queued = await this.scan();
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
      if (this.backlog.length >= FLUSH_AT) this.flush();
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

  // Queue work runs one operation at a time, in order. The flush timer, the drain and the cap check all
  // mutate the same directory and the same listing, and every one of them awaits partway through: without
  // this, a flush could roll a segment the drain had already read and was about to unlink.
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

  // Backlog → the end of the newest segment. Appending is what keeps a long offline stretch cheap: the cost
  // of a flush follows the sentences it carries, not the size of the segment they land in. Never throws: a
  // full or read-only disk must not take the Signal K process down, so an append that fails leaves its
  // sentences in the backlog to be retried next time.
  private async flushOnce(): Promise<void> {
    // In batches, because the backlog is not bounded by FLUSH_AT. That only brings a flush forward, and a
    // burst can pile up far past it before the first one gets its turn at the lock. One append has to stay
    // small next to a segment for the rollover in `append` to bound anything.
    for (let failures = 0; this.backlog.length > 0; ) {
      if (await this.append(this.backlog.splice(0, FLUSH_AT))) {
        failures = 0;
        continue;
      }
      // One retry, which `append` starts on a new segment: the first failure may have been the entry at the
      // tail rather than the disk, and that is worth finding out now rather than a flush interval from now.
      if (++failures > 1) return;
    }
  }

  // One batch onto the newest segment, or onto a new one. False when it did not land, in which case the
  // sentences are back at the front of the backlog.
  private async append(sentences: string[]): Promise<boolean> {
    const data = Buffer.from(sentences.map((s) => `${s}\n`).join(""));
    // Three reasons to start a segment rather than append to the newest one. A full one rolls over instead of
    // splitting the append, since a batch is small next to a segment and letting one overshoot costs a little
    // size and saves writing any part of it twice. One a frame in flight was read from rolls over however
    // much room it has left, since that frame was read against the size it had then and appending would put
    // sentences behind an end the drain has already passed. And one an append has already failed on rolls
    // over whatever is wrong with it: an entry that is not a writable file would otherwise keep every later
    // sentence out of the queue for as long as it sat at the tail.
    const tail = this.listing.at(-1);
    const fresh =
      tail === undefined ||
      tail === this.sending ||
      tail === this.unwritable ||
      (this.sizes.get(tail) ?? 0) >= SEGMENT_MAX;
    const name = fresh ? this.newName() : tail;
    if (fresh) {
      // Listed before the append, not after: a write that fails partway still leaves bytes on disk, and a
      // segment nothing tracks is one the cap cannot reclaim and the drain will never look at.
      this.listing.push(name); // the newest name sorts last, so appending keeps the order
      this.sizes.set(name, 0);
    }
    try {
      await appendFile(join(this.dir, name), data);
    } catch (err) {
      this.log(`queue write failed: ${(err as Error).message}`);
      // A disk that filled up can take part of an append. Cutting back to the last whole sentence and
      // re-measuring leaves a clean end for the retry, which re-appends every sentence from the backlog.
      await this.repair(name);
      this.unwritable = name;
      this.backlog.unshift(...sentences);
      return false;
    }
    this.sizes.set(name, (this.sizes.get(name) ?? 0) + data.length);
    this.bytes += data.length;
    return true;
  }

  // Deletes a segment whose sentences are done with, delivered or dropped. One that will not go is set aside
  // for `sweep` and stops being accounted for either way: it is off the listing, so counting bytes nothing
  // can reclaim would only drive the cap to discard queued sentences that are still deliverable.
  private async remove(name: string): Promise<void> {
    this.bytes -= this.sizes.get(name) ?? 0;
    this.sizes.delete(name);
    try {
      await unlink(join(this.dir, name));
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === "ENOENT") return; // already gone is the outcome wanted
      this.log(`queue delete failed: ${(err as Error).message}`);
      this.stale.add(name);
    }
  }

  // Retries the segments that would not delete. For the life of the process they are never read or replayed,
  // only deleted: the server already has their sentences, or they hold none worth sending. The set is not
  // kept on disk, so a restart reads them back as ordinary segments and sends them once more; that costs a
  // duplicate aiscast already drops, which is cheaper than a second durable file to keep correct.
  private async sweep(): Promise<void> {
    for (const name of [...this.stale]) {
      try {
        await unlink(join(this.dir, name));
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code !== "ENOENT") return; // still failing; again next tick
      }
      this.stale.delete(name);
    }
  }

  // Cuts a segment back to its last complete sentence and brings its measured size in line with the disk. A
  // crash, or an append that failed partway, can leave one ending mid-sentence, and appending to that would
  // splice the next sentence onto the broken one.
  private async repair(name: string): Promise<void> {
    const path = join(this.dir, name);
    try {
      const buf = await readFile(path);
      const cut = buf.length > 0 && buf[buf.length - 1] !== NEWLINE ? buf.lastIndexOf(NEWLINE) + 1 : buf.length;
      if (cut !== buf.length) {
        await truncate(path, cut);
        this.log(`queue: trimmed a half-written sentence from ${name}`);
      }
      this.bytes += cut - (this.sizes.get(name) ?? 0);
      this.sizes.set(name, cut);
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === "ENOENT") return; // the append never got as far as creating it
      this.log(`queue repair failed: ${(err as Error).message}`);
    }
  }

  // Size and sentence count of every segment, measured once at start. One pass: counting needs the contents
  // anyway, and the size comes off the same buffer. Batched, so a long backlog does not put every segment in
  // memory at once.
  private async scan(): Promise<number> {
    this.sizes.clear();
    this.stale.clear();
    this.bytes = 0;
    let queued = 0;
    for (let i = 0; i < this.listing.length; i += SCAN_BATCH) {
      const names = this.listing.slice(i, i + SCAN_BATCH);
      const read = await Promise.all(names.map((f) => readFile(join(this.dir, f)).catch(() => null)));
      read.forEach((buf, j) => {
        this.sizes.set(names[j], buf?.length ?? 0);
        this.bytes += buf?.length ?? 0;
        if (buf) for (let k = 0; k < buf.length; k++) if (buf[k] === NEWLINE) queued++;
      });
    }
    return queued;
  }

  // Monotonic, so a segment rolled in the same millisecond as the last one cannot take its name.
  private newName(): string {
    const now = Date.now();
    this.lastName = now > this.lastName ? now : this.lastName + 1;
    return `${this.lastName}${SUFFIX}`;
  }

  // Complete sentences from one segment, starting at `from`, or why there were none.
  private async readFrom(name: string, from: number): Promise<Read | Miss> {
    let fh: FileHandle | undefined;
    try {
      fh = await open(join(this.dir, name), "r");
      const size = (await fh.stat()).size;
      const len = Math.min(READ_BYTES, size - from);
      if (len <= 0) return "spent";
      const buf = Buffer.allocUnsafe(len);
      const { bytesRead } = await fh.read(buf, 0, len, from);
      // Everything up to the last newline is whole. No newline at all means nothing in the window is a
      // sentence a frame could carry, since no real one runs to READ_BYTES, so what is there is damage.
      const cut = buf.subarray(0, bytesRead).lastIndexOf(NEWLINE);
      if (cut < 0) return "damaged";
      const lines = buf.subarray(0, cut).toString("utf8").split("\n");
      const to = from + cut + 1;
      return { lines, widths: lines.map((l) => Buffer.byteLength(l) + 1), to, eof: to >= size };
    } catch (err) {
      const code = (err as NodeJS.ErrnoException).code ?? "";
      if (code === "ENOENT") return "spent";
      this.log(`queue read failed: ${(err as Error).message}`);
      return UNREADABLE.has(code) ? "damaged" : "failed";
    } finally {
      await fh?.close().catch(() => {});
    }
  }

  // Oldest segments go when the queue outgrows its cap. This runs during a drain as well: a replay is paced,
  // so a receiver busy enough to outrun it would otherwise grow the queue past the cap unchecked.
  private async enforceCap(): Promise<void> {
    if (this.stale.size > 0) await this.exclusive(() => this.sweep());
    if (this.bytes <= QUEUE_MAX_BYTES) return; // the running total, so an idle tick touches no files at all
    await this.exclusive(() => this.trim());
  }

  private async trim(): Promise<void> {
    while (this.listing.length > 0 && this.bytes > QUEUE_MAX_BYTES) {
      const name = this.listing[0];
      const owed = await this.countFrom(name, this.head); // what goes is what was never sent
      // Could not be read this time. The cap waits a tick rather than discard a segment whose sentences may
      // still be sitting there, the same call the drain makes on a read that only failed.
      if (owed === null) break;
      await this.remove(name);
      this.listing.shift();
      this.head = 0;
      this.stats.dropped += owed;
      this.stats.queued -= owed;
    }
  }

  // Sentences a segment still owes from `from` on. Null when it could not be read this time and may read
  // fine later, which the caller must not mistake for a segment that owes nothing.
  private async countFrom(name: string, from: number): Promise<number | null> {
    try {
      const buf = await readFile(join(this.dir, name));
      let n = 0;
      for (let i = from; i < buf.length; i++) if (buf[i] === NEWLINE) n++;
      return n;
    } catch (err) {
      const code = (err as NodeJS.ErrnoException).code ?? "";
      if (code === "ENOENT") return 0; // already gone, so nothing is owed
      if (UNREADABLE.has(code)) return 0; // damaged: its sentences went with its contents
      this.log(`queue count failed: ${(err as Error).message}`);
      return null;
    }
  }

  // Replay the queue oldest-first, one frame in flight, then let live sending resume. A drain that is cut
  // short (socket closed, ack timeout) leaves the head where it was, so the next one resumes there.
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
        if (this.backlog.length === 0) {
          // Disk and memory both empty: live sending resumes, and nothing is owed whatever the running count
          // says. Reconciling here is what keeps the status line honest after a segment counted at start
          // turns out to be damaged, since its sentences are gone and their number went with them. Only when
          // the listing is clear: a segment left alone after a failed read still holds sentences to send.
          if (this.listing.length === 0) this.stats.queued = 0;
          return;
        }
        // Sentences heard during the drain go out straight from memory. Writing them to a segment only to
        // read it back would hand the drain fresh work on every pass, and it would never reach the end.
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
      const accepted = await this.publish(batch.sentences);
      // Settled either way. Never acked means nothing on disk was touched and the head never moved, so the
      // next drain starts on the same sentence this one was sending; the segments still have to come back
      // into play for appending. Settling reports what it actually accounted for, which is short of the ack
      // when the cap took a segment out from under the frame and counted those sentences itself.
      this.stats.queued -= await this.exclusive(() => this.settle(batch, accepted ?? 0));
      if (accepted === null) return;
    }
  }

  // The oldest queued sentences, up to one frame's worth, and the segment ranges they came from. A frame is
  // filled from as many segments as it takes, so segments at the head that rolled over small do not each
  // cost their own round trip.
  private async nextBatch(): Promise<Batch | null> {
    const sentences: string[] = [];
    const widths: number[] = [];
    const chunks: Chunk[] = [];
    while (sentences.length < this.limits.frame && chunks.length < this.listing.length) {
      const name = this.listing[chunks.length];
      const from = chunks.length === 0 ? this.head : 0;
      const got = await this.readFrom(name, from);
      if (typeof got === "string") {
        // A read that only failed leaves the segment alone: the disk may well come back, and discarding one
        // over a full file table or a bad sector would throw away sentences still waiting to be sent. Spent
        // and damaged segments go, and only from the head, since the offset belongs to the head alone.
        if (got === "failed" || chunks.length > 0) break;
        await this.remove(name);
        this.listing.shift();
        this.head = 0;
        continue;
      }
      const take = Math.min(this.limits.frame - sentences.length, got.lines.length);
      let to = from;
      for (let j = 0; j < take; j++) to += got.widths[j];
      sentences.push(...got.lines.slice(0, take));
      widths.push(...got.widths.slice(0, take));
      const whole = take === got.lines.length && got.eof;
      chunks.push({ name, from, to, count: take, eof: whole });
      if (!whole) break; // more of this segment is waiting: the next frame starts where this one stopped
    }
    if (chunks.length === 0) return null;
    // Marked inside the lock, so no flush can land on the segment between reading it and holding it.
    this.sending = chunks[chunks.length - 1].name;
    return { sentences, widths, chunks };
  }

  // Moves the head past the sentences the server actually took. Segments taken whole are unlinked, and the
  // one the ack stopped inside keeps its place with the offset moved on. Nothing is rewritten, so a frame
  // the server only partly took costs no writes at all. Returns how many sentences it accounted for, which
  // is all the server took unless the cap reached a segment first and counted that one itself.
  private async settle(batch: Batch, accepted: number): Promise<number> {
    this.sending = null;
    let i = 0;
    for (const chunk of batch.chunks) {
      // Trimmed by the cap while the frame was in flight, along with everything the frame drew after it.
      if (this.listing[0] !== chunk.name) return i;
      if (accepted - i < chunk.count) {
        let to = chunk.from;
        for (let j = i; j < accepted; j++) to += batch.widths[j];
        this.head = to;
        return accepted;
      }
      i += chunk.count;
      if (!chunk.eof) {
        this.head = chunk.to; // the frame filled up inside this segment; the rest of it waits
        return i;
      }
      await this.remove(chunk.name);
      this.listing.shift();
      this.head = 0;
    }
    return i;
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
