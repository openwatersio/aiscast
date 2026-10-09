import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Link } from "../src/link.js";
import { startFakeServer, type FakeServer } from "./fake-server.js";

// Date is faked here, so wait by counting real timeouts rather than by the clock.
async function until(cond: () => boolean, tries = 250): Promise<void> {
  for (let i = 0; !cond(); i++) {
    if (i >= tries) throw new Error("condition not met");
    await new Promise((r) => setTimeout(r, 20));
  }
}

let server: FakeServer;
let link: Link;
let closes: number[];

beforeEach(async () => {
  server = await startFakeServer();
  // The ping interval and the clock it reads. Socket I/O and the reconnect backoff keep real timers.
  vi.useFakeTimers({ toFake: ["setInterval", "Date"] });
  link = new Link(() => `${server.url.replace(/^http/, "ws")}/v1/stream`);
  closes = [];
  link.on("close", (code) => closes.push(code));
  link.start();
  await until(() => link.open && server.clients.length === 1);
});

afterEach(async () => {
  link.stop();
  vi.useRealTimers();
  await server.close();
});

describe("silence watchdog", () => {
  it("keeps a socket that answers its pings", async () => {
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(30_000);
      await new Promise((r) => setTimeout(r, 50)); // the pong
    }
    expect(link.open).toBe(true);
    expect(closes).toEqual([]);
  });

  it("drops a socket that has heard nothing for a minute, whatever TCP says", async () => {
    vi.advanceTimersByTime(90_000); // no pong gets a turn to arrive
    await until(() => closes.length > 0);
    expect(link.open).toBe(false);
    expect(closes).toEqual([1006]);
  });
});
