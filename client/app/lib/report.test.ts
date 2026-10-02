import { afterEach, describe as group, expect, it, vi } from "vitest";
import { createBudget, describe, MAX_MESSAGE, MAX_STACK, scrub, scrubPath, toReport } from "./report";
import { createRateLimit, handleReport } from "./report.server";

const TOKEN = "ak1.eyJzdWIiOiJ1c2VyIiwicm9sZSI6InBlcnNvbmFsIn0.c2lnbmF0dXJlLWJ5dGVz";

group("scrub", () => {
  it("drops the query and fragment from a URL", () => {
    expect(scrub(`WebSocket connection to 'wss://ais.openwaters.io/v1/stream?key=${TOKEN}' failed`)).toBe(
      "WebSocket connection to 'wss://ais.openwaters.io/v1/stream' failed",
    );
    expect(scrub("https://openwaters.io/ais/vessels#map=9/47.6/-122.3 broke")).toBe(
      "https://openwaters.io/ais/vessels broke",
    );
    expect(scrub("GET https://ais.openwaters.io/v1/vessels?around=47.61,-122.33&q=x 500")).toBe(
      "GET https://ais.openwaters.io/v1/vessels 500",
    );
  });

  it("removes tokens wherever they appear", () => {
    expect(scrub(`bad token ${TOKEN}`)).toBe("bad token [token]");
    expect(scrub("Authorization: Bearer abc.def-123")).toBe("Authorization: Bearer [token]");
    expect(scrub("eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig")).toBe("[token]");
    expect(scrub("api_key=secret token%3Dak1.a.b x_ak1.c.d")).toBe("api_key=[token] token%3D[token] x_[token]");
  });

  it("drops queries and fragments from addresses without a scheme", () => {
    expect(scrub("/v1/vessels?around=60.1,24.9&key=secret: 503")).toBe("/v1/vessels 503");
    expect(scrub("/v1/vessels?foo&around=60.1,24.9 503")).toBe("/v1/vessels 503");
    expect(scrub("openwaters.io/ais/vessels#map=12/60.17/24.94 failed")).toBe("openwaters.io/ais/vessels failed");
    expect(scrub("https://x.test/a(b?around=60.1,24.9)")).toBe("https://x.test/a(b");
  });

  it("stays linear on hostile input", () => {
    // 32 KB each: tens of milliseconds when linear, seconds for any quadratic rule.
    const n = 16384;
    const start = performance.now();
    for (const text of ["a.".repeat(n), "ak1." + "a.".repeat(n), "a@" + "b.".repeat(n) + "!", "a://".repeat(n / 2), "key".repeat(n), "token".repeat(n)]) {
      scrub(text);
    }
    expect(performance.now() - start).toBeLessThan(1500);
  });

  it("removes email addresses", () => {
    expect(scrub("contact sailor@example.org now")).toBe("contact [email] now");
  });

  it("removes positions", () => {
    expect(scrub("tile https://tiles.example/v1/vessels/tiles/9/81/178.pbf failed")).toBe(
      "tile https://tiles.example/v1/vessels/tiles/{z}/{x}/{y}.pbf failed",
    );
    expect(scrub("tiles/12/2345/1234@2x.png")).toBe("tiles/{z}/{x}/{y}@2x.png");
    expect(scrub("Invalid LngLat (-122.3321, 47.6062)")).toBe("Invalid LngLat ([coord], [coord])");
  });

  it("keeps stack frames readable", () => {
    const chrome = "TypeError: x is undefined\n    at render (https://openwaters.io/ais/assets/root-Ab12.js:3:1450)";
    const safari = "render@https://openwaters.io/ais/assets/root-Ab12.js:3:1450\n@https://openwaters.io/ais/assets/entry.js:1:20";
    expect(scrub(chrome)).toBe(chrome);
    expect(scrub(safari)).toBe(safari);
  });
});

group("scrubPath", () => {
  it("keeps the section and drops the rest", () => {
    expect(scrubPath("/ais/vessels/368168720-cerulean")).toBe("/ais/vessels/*");
    expect(scrubPath("/ais/stations/kystverket/2573010")).toBe("/ais/stations/*");
    expect(scrubPath("/ais/vessels")).toBe("/ais/vessels");
    expect(scrubPath("/ais/<script>")).toBe("/ais/?");
    expect(scrubPath("")).toBe("/");
    expect(scrubPath("/ais/vessels.data")).toBe("/ais/vessels");
  });
});

group("describe", () => {
  it("reads errors, strings and odd values", () => {
    const e = new TypeError(`fetch failed for https://x.test/a?key=${TOKEN}`);
    expect(describe(e)).toMatchObject({ name: "TypeError", message: "fetch failed for https://x.test/a" });
    expect(describe("plain")).toEqual({ message: "plain" });
    expect(describe({ message: "shaped like an error" }).message).toBe("shaped like an error");
    expect(describe(Object.create(null)).message).toBe("unprintable value");
    const hostile = Object.defineProperty({}, "message", {
      get() {
        throw new Error("no");
      },
    });
    expect(describe(hostile).message).toBe("unreadable error");
    expect(describe({ name: `E ${TOKEN}`, message: "x" }).name).toBe("E [token]");
  });

  it("caps the message and stack", () => {
    const e = new Error("m".repeat(MAX_MESSAGE * 2));
    e.stack = "s".repeat(MAX_STACK * 2);
    const d = describe(e);
    expect(d.message).toHaveLength(MAX_MESSAGE);
    expect(d.stack).toHaveLength(MAX_STACK);
  });
});

group("toReport", () => {
  it("builds a report with the section, not the address", () => {
    expect(toReport("map", new Error("style failed"), "/ais/vessels/1-x")).toMatchObject({
      kind: "map",
      name: "Error",
      message: "style failed",
      path: "/ais/vessels/*",
    });
  });

  it("skips what there is nothing to learn from", () => {
    expect(toReport("error", "Script error.", "/ais/vessels")).toBeUndefined();
    expect(toReport("error", "ResizeObserver loop completed with undelivered notifications.", "/")).toBeUndefined();
    expect(toReport("rejection", new DOMException("aborted", "AbortError"), "/ais/vessels")).toBeUndefined();
    const ext = new Error("boom");
    ext.stack = "Error: boom\n    at chrome-extension://abcdef/content.js:1:1";
    expect(toReport("error", ext, "/ais/vessels")).toBeUndefined();
  });
});

group("createBudget", () => {
  it("sends each distinct report once, up to the cap", () => {
    const take = createBudget(2, true);
    expect(take("a")).toBe(true);
    expect(take("a")).toBe(false);
    expect(take("b")).toBe(true);
    expect(take("c")).toBe(false);
  });

  it("sends nothing from a page outside the sample", () => {
    expect(createBudget(5, false)("a")).toBe(false);
  });
});

group("createRateLimit", () => {
  it("allows the limit per window, then resets", () => {
    let t = 1_000_000;
    const allow = createRateLimit(2, 60_000, () => t);
    expect([allow(), allow(), allow()]).toEqual([true, true, false]);
    t += 59_999;
    expect(allow()).toBe(false);
    t += 1;
    expect(allow()).toBe(true);
  });
});

group("handleReport", () => {
  const always = () => true;
  const post = (body: string, headers: Record<string, string> = {}) =>
    new Request("https://openwaters.io/ais/errors", { method: "POST", body, headers });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("logs a scrubbed record with a stable shape", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const res = await handleReport(
      post(
        JSON.stringify({
          kind: "stream",
          name: "Error",
          message: `closed wss://ais.openwaters.io/v1/stream?key=${TOKEN}`,
          stack: `at x (https://openwaters.io/ais/assets/a.js?key=${TOKEN}:1:2)`,
          path: "/ais/vessels/368168720-cerulean?around=47.6,-122.3",
          extra: "ignored",
        }),
        { "user-agent": "Mozilla/5.0 Test", "cf-connecting-ip": "203.0.113.9" },
      ),
      always,
    );
    expect(res.status).toBe(204);
    expect(log).toHaveBeenCalledOnce();
    const record = log.mock.calls.at(0)?.[0];
    expect(record).toEqual({
      event: "app_error",
      source: "browser",
      kind: "stream",
      name: "Error",
      message: "closed wss://ais.openwaters.io/v1/stream",
      stack: "at x (https://openwaters.io/ais/assets/a.js)",
      path: "/ais/vessels/*",
      ua: "Mozilla/5.0 Test",
    });
    expect(JSON.stringify(record)).not.toContain("203.0.113.9");
  });

  it("refuses what is not a report", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect((await handleReport(new Request("https://openwaters.io/ais/errors"), always)).status).toBe(405);
    expect((await handleReport(post("not json"), always)).status).toBe(400);
    expect((await handleReport(post("null"), always)).status).toBe(400);
    expect((await handleReport(post(JSON.stringify({ kind: "nope", message: "x" })), always)).status).toBe(400);
    expect((await handleReport(post(JSON.stringify({ kind: "error" })), always)).status).toBe(400);
    expect((await handleReport(post(JSON.stringify({ kind: "error", message: "?a=1" })), always)).status).toBe(400);
    expect((await handleReport(post("x".repeat(20_000)), always)).status).toBe(413);
    expect((await handleReport(post("{}", { "sec-fetch-site": "cross-site" }), always)).status).toBe(403);
    expect(console.error).not.toHaveBeenCalled();
  });

  it("stops reading a body without a length once it is too large", async () => {
    let pulled = 0;
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        pulled++;
        controller.enqueue(new Uint8Array(1024));
      },
    });
    const req = new Request("https://openwaters.io/ais/errors", { method: "POST", body, duplex: "half" } as RequestInit);
    expect((await handleReport(req, always)).status).toBe(413);
    expect(pulled).toBeLessThan(40);
  });

  it("spends the rate limit only on reports", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const limit = vi.fn(() => true);
    await handleReport(post("junk"), limit);
    expect(limit).not.toHaveBeenCalled();
  });

  it("drops reports past the rate limit", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const limit = createRateLimit(1, 60_000, () => 0);
    const body = JSON.stringify({ kind: "error", message: "x", path: "/ais/vessels" });
    expect((await handleReport(post(body), limit)).status).toBe(204);
    expect((await handleReport(post(body), limit)).status).toBe(429);
    expect(log).toHaveBeenCalledOnce();
  });
});
