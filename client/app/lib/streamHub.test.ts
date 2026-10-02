import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BBox } from "./stream";
import { Hub, plan, wants, type TabView, type ToHub, type ToTab } from "./streamHub";

const OSLO: BBox = [59, 10, 60, 11];
const HELSINKI: BBox = [60, 24, 61, 25];
const INSIDE_OSLO: BBox = [59.2, 10.2, 59.8, 10.8];

function view(over: Partial<TabView> = {}): TabView {
  return { bbox: [], mmsi: [], visible: true, active: 0, ...over };
}

describe("plan", () => {
  it("subscribes to every tab's boxes and follows", () => {
    const p = plan([view({ bbox: [OSLO], mmsi: [1] }), view({ bbox: [HELSINKI], mmsi: [2] })]);
    expect(p.bbox).toEqual([OSLO, HELSINKI]);
    expect(p.mmsi).toEqual([1, 2]);
    expect(p.served).toEqual([true, true]);
  });

  it("counts a box another tab covers once", () => {
    const p = plan([view({ bbox: [OSLO], active: 2 }), view({ bbox: [INSIDE_OSLO], active: 1 })], { area: 1 });
    expect(p.bbox).toEqual([OSLO]);
    expect(p.served).toEqual([true, true]);
  });

  it("follows a vessel two tabs follow once", () => {
    expect(plan([view({ mmsi: [1] }), view({ mmsi: [1] })]).mmsi).toEqual([1]);
  });

  it("gives the area cap to the visible tab before a hidden one", () => {
    const hidden = view({ bbox: [OSLO], visible: false, active: 9 });
    const shown = view({ bbox: [HELSINKI], visible: true, active: 1 });
    const p = plan([hidden, shown], { area: 1 });
    expect(p.bbox).toEqual([HELSINKI]);
    expect(p.served).toEqual([false, true]);
  });

  it("gives the area cap to the most recently active of two visible tabs", () => {
    const p = plan([view({ bbox: [OSLO], active: 1 }), view({ bbox: [HELSINKI], active: 2 })], { area: 1.5 });
    expect(p.bbox).toEqual([HELSINKI]);
    expect(p.served).toEqual([false, true]);
  });

  it("keeps a tab's follows when its boxes do not fit", () => {
    const p = plan([view({ bbox: [OSLO], active: 2 }), view({ bbox: [HELSINKI], mmsi: [7], active: 1 })], { area: 1 });
    expect(p.mmsi).toEqual([7]);
    expect(p.served).toEqual([true, false]);
  });

  it("serves a tab with no boxes", () => {
    expect(plan([view({ mmsi: [1] })], { area: -1 }).served).toEqual([true]);
  });

  it("stops following at the key's limit, keeping the active tab's", () => {
    const p = plan([view({ mmsi: [1, 2], active: 1 }), view({ mmsi: [3], active: 2 })], { mmsis: 2 });
    expect(p.mmsi).toEqual([3, 1]);
  });

  it("has no cap without limits or with an area of 0", () => {
    const tabs = [view({ bbox: [[-90, -180, 90, 180]] }), view({ bbox: [OSLO] })];
    expect(plan(tabs).served).toEqual([true, true]);
    expect(plan(tabs, { area: 0 }).served).toEqual([true, true]);
  });
});

describe("wants", () => {
  const tab = { ...view({ bbox: [OSLO], mmsi: [9] }), served: true };

  it("takes positions inside its boxes, edges included", () => {
    expect(wants(tab, { mmsi: 1, lat: 59.5, lon: 10.5 })).toBe(true);
    expect(wants(tab, { mmsi: 1, lat: 60, lon: 11 })).toBe(true);
    expect(wants(tab, { mmsi: 1, lat: 60.5, lon: 24.5 })).toBe(false);
  });

  it("takes a followed vessel anywhere, and one with no position", () => {
    expect(wants(tab, { mmsi: 9, lat: 0, lon: 0 })).toBe(true);
    expect(wants(tab, { mmsi: 9 })).toBe(true);
    expect(wants(tab, { mmsi: 1 })).toBe(false);
  });

  it("takes only its follows when its boxes are not served", () => {
    const unserved = { ...tab, served: false };
    expect(wants(unserved, { mmsi: 1, lat: 59.5, lon: 10.5 })).toBe(false);
    expect(wants(unserved, { mmsi: 9, lat: 59.5, lon: 10.5 })).toBe(true);
  });
});

class FakeSocket {
  static all: FakeSocket[] = [];
  readyState = 0;
  sent: unknown[] = [];
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  constructor(readonly url: string) {
    FakeSocket.all.push(this);
  }
  send(data: string) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.();
  }
  open(limits: Record<string, unknown> = {}) {
    this.readyState = 1;
    this.frame({ type: "welcome", limits });
  }
  frame(f: unknown) {
    this.onmessage?.({ data: JSON.stringify(f) });
  }
}

class FakePort {
  got: ToTab[] = [];
  onmessage: ((e: MessageEvent<ToHub>) => void) | null = null;
  postMessage(msg: ToTab) {
    this.got.push(msg);
  }
  say(msg: ToHub) {
    this.onmessage?.({ data: msg } as MessageEvent<ToHub>);
  }
  events() {
    return this.got.flatMap((m) => (m.type === "event" ? [JSON.parse(m.data).mmsi] : []));
  }
  last() {
    return this.got.filter((m) => m.type === "status").at(-1);
  }
}

describe("Hub", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeSocket.all = [];
  });
  afterEach(() => vi.useRealTimers());

  function setup() {
    const hub = new Hub("ws://x/v1/stream", (u) => new FakeSocket(u) as unknown as WebSocket);
    const a = new FakePort();
    const b = new FakePort();
    hub.add(a);
    hub.add(b);
    return { hub, a, b };
  }

  it("opens one socket for two tabs and subscribes to their union once welcomed", () => {
    const { a, b } = setup();
    expect(FakeSocket.all).toHaveLength(0);
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    b.say({ type: "view", ...view({ bbox: [HELSINKI], mmsi: [5] }) });
    expect(FakeSocket.all).toHaveLength(1);
    const ws = FakeSocket.all[0]!;
    expect(ws.sent).toEqual([]);
    ws.open();
    expect(ws.sent).toEqual([{ type: "subscribe", bbox: [OSLO, HELSINKI], mmsi: [5] }]);
    expect(a.last()).toMatchObject({ state: "live", served: true });
  });

  it("passes each tab only the events in its view", () => {
    const { a, b } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    b.say({ type: "view", ...view({ bbox: [HELSINKI], mmsi: [5] }) });
    const ws = FakeSocket.all[0]!;
    ws.open();
    ws.frame({ type: "event", mmsi: 1, lat: 59.5, lon: 10.5 });
    ws.frame({ type: "event", mmsi: 2, lat: 60.5, lon: 24.5 });
    ws.frame({ type: "event", mmsi: 5 });
    expect(a.events()).toEqual([1]);
    expect(b.events()).toEqual([2, 5]);
  });

  it("resubscribes only when the union changes", () => {
    const { a, b } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    const ws = FakeSocket.all[0]!;
    ws.open();
    b.say({ type: "view", ...view({ bbox: [INSIDE_OSLO] }) });
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    expect(ws.sent).toHaveLength(1);
    a.say({ type: "close" });
    expect(ws.sent.at(-1)).toEqual({ type: "subscribe", bbox: [INSIDE_OSLO], mmsi: [] });
  });

  it("tells a tab when another takes the area it needs", () => {
    const { a, b } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO], active: 1 }) });
    const ws = FakeSocket.all[0]!;
    ws.open({ area: 1.5 });
    b.say({ type: "view", ...view({ bbox: [HELSINKI], active: 2 }) });
    expect(a.last()).toMatchObject({ served: false });
    expect(ws.sent.at(-1)).toEqual({ type: "subscribe", bbox: [HELSINKI], mmsi: [] });
    ws.frame({ type: "event", mmsi: 1, lat: 59.5, lon: 10.5 });
    expect(a.events()).toEqual([]);
  });

  it("unsubscribes when no tab wants anything, and closes the socket when the last tab goes", () => {
    const { a, b } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    b.say({ type: "view", ...view() });
    const ws = FakeSocket.all[0]!;
    ws.open();
    a.say({ type: "view", ...view() });
    expect(ws.sent.at(-1)).toEqual({ type: "unsubscribe" });
    a.say({ type: "close" });
    expect(ws.readyState).toBe(1);
    b.say({ type: "close" });
    expect(ws.readyState).toBe(3);
    vi.advanceTimersByTime(60e3);
    expect(FakeSocket.all).toHaveLength(1);
  });

  it("forgets a tab it has not heard from", () => {
    const { a, b } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    b.say({ type: "view", ...view({ bbox: [HELSINKI] }) });
    const ws = FakeSocket.all[0]!;
    ws.open();
    for (let i = 0; i < 20; i++) {
      vi.advanceTimersByTime(20e3);
      b.say({ type: "view", ...view({ bbox: [HELSINKI] }) });
    }
    expect(ws.sent.at(-1)).toEqual({ type: "subscribe", bbox: [HELSINKI], mmsi: [] });
    // Back from being frozen, it is a tab again.
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    expect(ws.sent.at(-1)).toEqual({ type: "subscribe", bbox: [HELSINKI, OSLO], mmsi: [] });
  });

  it("backs off while refused and resubscribes after it reconnects", () => {
    const { a } = setup();
    a.say({ type: "view", ...view({ bbox: [OSLO] }) });
    const first = FakeSocket.all[0]!;
    first.frame({ type: "error", error: "concurrent connections per user exceeded" });
    first.close();
    expect(a.last()).toMatchObject({ state: "refused" });
    vi.advanceTimersByTime(999);
    expect(FakeSocket.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSocket.all).toHaveLength(2);
    FakeSocket.all[1]!.close();
    vi.advanceTimersByTime(1999);
    expect(FakeSocket.all).toHaveLength(2);
    vi.advanceTimersByTime(1);
    const third = FakeSocket.all[2]!;
    third.open();
    expect(a.last()).toMatchObject({ state: "live" });
    expect(third.sent).toEqual([{ type: "subscribe", bbox: [OSLO], mmsi: [] }]);
  });

  it("tells its tabs it is alive", () => {
    const { a } = setup();
    a.say({ type: "view", ...view() });
    const before = a.got.length;
    vi.advanceTimersByTime(5e3);
    expect(a.got.length).toBe(before + 1);
  });
});
