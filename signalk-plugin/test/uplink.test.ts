import { EventEmitter } from "node:events";
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Frame, Link } from "../src/link.js";
import { SEGMENT_MAX, Uplink } from "../src/uplink.js";

const VDM = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23";
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
async function until(cond: () => boolean | Promise<boolean>, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await cond())) {
    if (Date.now() > end) throw new Error("condition not met");
    await sleep(20);
  }
}

// A socket that acks as fast as frames arrive. `accept` caps how many sentences of a frame it takes, the
// way the server's per-minute publish limit does.
class FakeLink extends EventEmitter {
  open = true;
  accept = Infinity;
  published: Frame[] = [];
  send(frame: Frame): boolean {
    if (!this.open) return false;
    this.published.push(frame);
    const n = Math.min((frame.nmea as string[]).length, this.accept);
    setImmediate(() => this.emit("frame", { type: "ack", n }));
    return true;
  }
  reconnect(): void {}
  // Real connects deliver a welcome first; a generous limit keeps the pacer out of the way of the tests.
  connect(limits: Record<string, number> = { publish_per_min: 6_000_000 }): void {
    this.open = true;
    this.emit("frame", { type: "welcome", limits });
    this.emit("open");
  }
  asLink(): Link {
    return this as unknown as Link;
  }
}

let dir: string;
let link: FakeLink;
let up: Uplink;

const queue = () => join(dir, "queue");
const files = async () => (await readdir(queue())).sort();
const sentences = (f: Frame) => f.nmea as string[];
const asLines = (ss: string[]) => ss.map((s) => `${s}\n`).join("");

async function seed(segments: string[][]): Promise<void> {
  await mkdir(queue(), { recursive: true });
  await Promise.all(
    segments.map((ss, i) => writeFile(join(queue(), `${1700000000000 + i}.log`), asLines(ss))),
  );
}

async function read(name: string): Promise<string[]> {
  const text = await readFile(join(queue(), name), "utf8");
  return text === "" ? [] : text.slice(0, -1).split("\n");
}

// Every sentence still on disk, oldest first, however many segments it takes.
async function onDisk(): Promise<string[]> {
  const out: string[] = [];
  for (const f of await files()) out.push(...(await read(f)));
  return out;
}

const bytes = async () => Promise.all((await files()).map((f) => readFile(join(queue(), f))));

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "aiscast-uplink-"));
  link = new FakeLink();
  up = new Uplink(link.asLink(), dir, () => {});
});

afterEach(async () => {
  await up.stop();
  await rm(dir, { recursive: true, force: true });
});

describe("draining the queue", () => {
  it("empties a large backlog while sentences keep arriving", async () => {
    await seed(Array.from({ length: 4 }, (_, s) => Array.from({ length: 500 }, (_, i) => `${VDM}#${s}-${i}`)));
    await up.start();
    // A receiver hearing traffic throughout: every sentence heard mid-drain used to become its own queue
    // file, so the drain traded one file for another and the directory never emptied.
    const heard = setInterval(() => up.hear(VDM), 5);
    link.connect();
    try {
      await until(async () => (await files()).length === 0, 20_000);
    } finally {
      clearInterval(heard);
    }
    await until(async () => (await files()).length === 0 && up.stats.queued === 0, 10_000);
    await sleep(50); // the drain's last pass finishes
    up.hear(VDM);
    await until(() => link.published.some((f) => !f.replay)); // live sending resumes once the queue is gone
  }, 30_000);

  it("carries several segments in one frame", async () => {
    await seed(Array.from({ length: 50 }, (_, i) => [`${VDM}#${i}`]));
    await up.start();
    link.connect();
    await until(async () => (await files()).length === 0);
    expect(link.published).toHaveLength(1);
    expect(sentences(link.published[0])).toHaveLength(50);
    expect(link.published[0].replay).toBe(true);
  });

  it("reads on from where the last frame stopped inside a segment", async () => {
    const all = Array.from({ length: 10 }, (_, i) => `${VDM}#${i}`);
    await seed([all]);
    await up.start();
    link.connect({ publish_frame: 4, publish_per_min: 6_000_000 });
    await until(async () => (await files()).length === 0);
    expect(link.published.map((f) => sentences(f).length)).toEqual([4, 4, 2]);
    expect(link.published.flatMap(sentences)).toEqual(all); // each sentence once, in order
  });

  it("never rewrites a segment when the server takes only part of a frame", async () => {
    await seed([Array.from({ length: 10 }, (_, i) => `${VDM}#${i}`)]);
    const before = await bytes();
    await up.start();
    link.accept = 4; // a short ack stops the drain partway through the segment
    link.connect();
    await until(() => link.published.length === 1);
    await sleep(100);
    expect(await bytes()).toEqual(before); // untouched on disk: only the head offset moved
    expect(up.stats.sent).toBe(4);
    expect(up.stats.queued).toBe(6); // the six the server dropped are still owed, not counted as sent
  });

  it("requeues the remainder of a live frame the server only partly took", async () => {
    await up.start();
    link.accept = 4;
    link.connect();
    await until(async () => (await files()).length === 0);
    for (let i = 0; i < 10; i++) up.hear(`${VDM}#${i}`);
    await until(() => link.published.some((f) => !f.replay));
    await sleep(100);
    expect(up.stats.sent).toBe(4);
    expect(up.stats.queued).toBe(6);
    await up.stop(); // stop flushes the backlog, so the remainder survives a restart
    expect(await onDisk()).toHaveLength(6);
  });

  it("leaves the queue untouched when the socket drops mid-drain", async () => {
    await seed([[`${VDM}#0`], [`${VDM}#1`]]);
    await up.start();
    link.open = false;
    link.emit("open"); // an open that is already gone by the time the drain sends
    await sleep(100);
    expect(await files()).toHaveLength(2);
    expect(up.stats.queued).toBe(2);
  });

  it("leaves every segment intact when a replay spanning several is cut short", async () => {
    const full = (tag: string) => Array.from({ length: 500 }, (_, i) => `${VDM}#${tag}${i}`);
    await seed([full("a"), full("b")]);
    const before = await bytes();
    await up.start();
    link.accept = 10; // a frame of 1000 from two segments, almost all of it refused
    link.connect();
    await until(() => link.published.length === 1);
    await sleep(200);
    expect(await bytes()).toEqual(before);
    expect(up.stats.queued).toBe(990);
  });

  it("appends to the newest segment instead of leaving one behind per flush", async () => {
    link.open = false;
    // A boat offline for weeks flushes every fifteen seconds; a file each time is what filled the directory.
    for (let round = 0; round < 3; round++) {
      await up.start();
      up.hear(VDM);
      up.hear(VDM);
      await up.stop();
    }
    expect(await files()).toHaveLength(1);
    expect(await onDisk()).toHaveLength(6);
  });

  it("rolls to a new segment once the newest one is full", async () => {
    link.open = false;
    await up.start();
    for (let i = 0; i < Math.ceil(SEGMENT_MAX / (VDM.length + 1)); i++) up.hear(VDM);
    await up.stop(); // one flush, one segment: an append is never split, so it overshoots instead
    expect(await files()).toHaveLength(1);
    expect((await bytes())[0].length).toBeGreaterThanOrEqual(SEGMENT_MAX);

    await up.start();
    up.hear(VDM);
    await up.stop();
    expect(await files()).toHaveLength(2);
  }, 20_000);

  it("loses nothing when a flush lands while the drain is reading the same segment", async () => {
    await seed([[`${VDM}#queued`]]);
    await up.start();
    link.connect(); // the drain starts reading the head segment
    for (let i = 0; i < 500; i++) up.hear(`${VDM}#${i}`); // FLUSH_AT: a flush fires mid-read, on that segment
    await until(async () => (await files()).length === 0, 10_000);
    await up.stop();

    const delivered = [...link.published.flatMap(sentences), ...(await onDisk())];
    const heard = new Set(delivered.map((s) => s.replace(/^\\[^\\]*\\/, "")));
    expect(heard.has(`${VDM}#queued`)).toBe(true);
    for (let i = 0; i < 500; i++) expect(heard.has(`${VDM}#${i}`)).toBe(true);
  }, 20_000);

  it("splits a live burst at the server's frame limit", async () => {
    await up.start();
    link.connect({ publish_frame: 3, publish_per_min: 6_000_000 });
    up.hear(VDM);
    await until(() => link.published.length === 1);
    await sleep(50); // the drain over an empty queue finishes and live sending takes over
    link.published.length = 0;
    for (let i = 0; i < 10; i++) up.hear(`${VDM}#${i}`);
    await until(() => link.published.flatMap(sentences).length === 10);
    // Over the frame limit the server keeps the first `frame` and acks short, which reads here as the
    // per-minute limit and would park live sending for a whole minute.
    expect(link.published.every((f) => !f.replay)).toBe(true);
    expect(Math.max(...link.published.map((f) => sentences(f).length))).toBeLessThanOrEqual(3);
    expect(up.stats.queued).toBe(0);
  });

  it("stops without waiting out the publish limit", async () => {
    await seed([[`${VDM}#0`], [`${VDM}#1`]]);
    await up.start();
    link.accept = 1; // a short ack parks the drain until the limit window is over
    link.connect();
    await until(() => link.published.length === 1);
    const began = Date.now();
    await up.stop();
    expect(Date.now() - began).toBeLessThan(2000);
    expect(up.stats.queued).toBe(1);
  });

  it("gets past a queue entry it can neither read nor delete", async () => {
    await mkdir(queue(), { recursive: true });
    await mkdir(join(queue(), "1600000000000.log")); // a directory: unreadable, and unlink refuses it
    await writeFile(join(queue(), "1700000000001.log"), `${VDM}#real\n`);
    await up.start();
    link.connect();
    await until(() => link.published.flatMap(sentences).some((s) => s.includes("#real")));
    await sleep(200);
    // The stuck entry is off the queue's books but still on disk, and the sentence behind it still went.
    expect(await files()).toEqual(["1600000000000.log"]);
    expect(link.published.flatMap(sentences).filter((s) => s.includes("#real"))).toHaveLength(1);
  });

  it("does not leave the queue count inflated when a counted segment turns unreadable", async () => {
    await seed([[`${VDM}#0`, `${VDM}#1`, `${VDM}#2`]]);
    await up.start();
    expect(up.stats.queued).toBe(3); // counted at start, so discarding it later has to be accounted for
    await writeFile(join(queue(), (await files())[0]), "no sentence here, and no newline to end one");
    link.connect();
    await until(() => up.stats.queued === 0);
    expect(await files()).toHaveLength(0);
  });

  it("paces a replay under the server's per-minute publish limit", async () => {
    await seed(Array.from({ length: 4 }, (_, i) => [`${VDM}#${i}`]));
    await up.start();
    link.connect({ publish_per_min: 60, publish_frame: 1 }); // 1/s, so four frames cannot go out at once
    await until(() => link.published.length === 1);
    await sleep(300);
    expect(link.published).toHaveLength(1);
    expect(await files()).toHaveLength(3);
  });
});

describe("reading a queue off disk", () => {
  it("drops a sentence left half-written by a crash", async () => {
    await mkdir(queue(), { recursive: true });
    await writeFile(join(queue(), "1700000000000.log"), `${VDM}#0\n${VDM}#1\n${VDM}#ha`);
    link.open = false;
    await up.start();
    expect(up.stats.queued).toBe(2);
    up.hear(`${VDM}#2`); // the append that follows must not splice onto the half-written sentence
    link.connect();
    await until(async () => (await files()).length === 0);
    const heard = link.published.flatMap(sentences).map((s) => s.replace(/^\\[^\\]*\\/, ""));
    expect(heard).toEqual([`${VDM}#0`, `${VDM}#1`, `${VDM}#2`]);
  });
});
