import { EventEmitter } from "node:events";
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Frame, Link } from "../src/link.js";
import { Uplink } from "../src/uplink.js";

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
const files = () => readdir(queue());
const sentences = (f: Frame) => f.nmea as string[];

async function seed(names: string[][]): Promise<void> {
  await mkdir(queue(), { recursive: true });
  await Promise.all(
    names.map((lines, i) => writeFile(join(queue(), `${1700000000000 + i}.json`), JSON.stringify(lines))),
  );
}

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
    await seed(Array.from({ length: 2000 }, () => [VDM]));
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
    up.hear(VDM);
    await until(() => link.published.some((f) => !f.replay)); // live sending resumes once the queue is gone
  }, 30_000);

  it("carries several queue files in one frame", async () => {
    await seed(Array.from({ length: 50 }, () => [VDM]));
    await up.start();
    link.connect();
    await until(async () => (await files()).length === 0);
    expect(link.published).toHaveLength(1);
    expect(sentences(link.published[0])).toHaveLength(50);
    expect(link.published[0].replay).toBe(true);
  });

  it("keeps a frame's remainder on disk when the server takes only part of it", async () => {
    await seed([Array.from({ length: 10 }, (_, i) => `${VDM}#${i}`)]);
    await up.start();
    link.accept = 4;
    link.connect();
    await until(() => link.published.length === 1);
    await sleep(100);
    const left = await files();
    expect(left).toHaveLength(1);
    expect(JSON.parse(await readFile(join(queue(), left[0]), "utf8"))).toEqual([
      `${VDM}#4`, `${VDM}#5`, `${VDM}#6`, `${VDM}#7`, `${VDM}#8`, `${VDM}#9`,
    ]);
    expect(up.stats.sent).toBe(4);
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
    expect(up.stats.queued).toBe(6); // the six the server dropped are still owed, not counted as sent
    await up.stop(); // stop flushes the backlog, so the remainder survives a restart
    const left = await files();
    expect(left).toHaveLength(1);
    expect(JSON.parse(await readFile(join(queue(), left[0]), "utf8"))).toHaveLength(6);
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

  it("tops up the newest file instead of leaving one behind per flush", async () => {
    link.open = false;
    // A boat offline for weeks flushes every fifteen seconds; a file each time is what filled the directory.
    for (let round = 0; round < 3; round++) {
      await up.start();
      up.hear(VDM);
      up.hear(VDM);
      await up.stop();
    }
    const left = await files();
    expect(left).toHaveLength(1);
    expect(JSON.parse(await readFile(join(queue(), left[0]), "utf8"))).toHaveLength(6);
  });

  it("starts a new file once the newest one is full", async () => {
    link.open = false;
    await up.start();
    for (let i = 0; i < 500; i++) up.hear(VDM); // FILE_MAX: the backlog goes to disk on its own
    await up.stop();
    await up.start();
    up.hear(VDM);
    await up.stop();
    expect(await files()).toHaveLength(2);
  });

  it("loses nothing when a flush lands while the drain is reading the same file", async () => {
    await seed([[`${VDM}#queued`]]);
    await up.start();
    link.connect(); // the drain starts reading the tail file
    for (let i = 0; i < 500; i++) up.hear(`${VDM}#${i}`); // FILE_MAX: a flush fires mid-read, on that file
    await until(async () => (await files()).length === 0, 10_000);
    await up.stop();

    const delivered = link.published.flatMap(sentences);
    for (const name of await files()) {
      delivered.push(...(JSON.parse(await readFile(join(queue(), name), "utf8")) as string[]));
    }
    const heard = new Set(delivered.map((s) => s.replace(/^\\[^\\]*\\/, "")));
    expect(heard.has(`${VDM}#queued`)).toBe(true);
    for (let i = 0; i < 500; i++) expect(heard.has(`${VDM}#${i}`)).toBe(true);
  }, 20_000);

  it("splits a flush that overfills the newest file", async () => {
    link.open = false;
    await up.start();
    up.hear(VDM);
    up.hear(VDM);
    up.hear(VDM);
    await up.stop(); // a file of 3, with room to spare

    await up.start();
    for (let i = 0; i < 600; i++) up.hear(VDM); // tops the file up to 500 rather than making one of 503
    await up.stop();
    const sizes = await Promise.all(
      (await files()).map(async (f) => (JSON.parse(await readFile(join(queue(), f), "utf8")) as string[]).length),
    );
    expect(Math.max(...sizes)).toBeLessThanOrEqual(500);
    expect(sizes.reduce((a, b) => a + b, 0)).toBe(603);
  });

  it("rewrites only the file a short ack stopped inside", async () => {
    await seed([
      [`${VDM}#0`, `${VDM}#1`, `${VDM}#2`],
      [`${VDM}#3`, `${VDM}#4`, `${VDM}#5`],
    ]);
    await up.start();
    link.accept = 4; // stops inside the second file, which keeps its own tail rather than the whole remainder
    link.connect();
    await until(() => link.published.length === 1);
    await sleep(100);
    const left = await files();
    expect(left).toHaveLength(1);
    expect(JSON.parse(await readFile(join(queue(), left[0]), "utf8"))).toEqual([`${VDM}#4`, `${VDM}#5`]);
  });

  it("keeps files within FILE_MAX when a replay spanning several is cut short", async () => {
    const full = (tag: string) => Array.from({ length: 500 }, (_, i) => `${VDM}#${tag}${i}`);
    await seed([full("a"), full("b")]);
    await up.start();
    link.accept = 10; // a frame of 1000 from two files, almost all of it refused
    link.connect();
    await until(() => link.published.length === 1);
    await sleep(200);
    const sizes = await Promise.all(
      (await files()).map(async (f) => (JSON.parse(await readFile(join(queue(), f), "utf8")) as string[]).length),
    );
    expect(Math.max(...sizes)).toBeLessThanOrEqual(500); // not one file holding the whole refused remainder
    expect(sizes.reduce((a, b) => a + b, 0)).toBe(990);
  });

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
