import { describe, expect, it } from "vitest";
import { edgeCached, isSharedPage, pageCacheKey } from "./edge.server";

/** The Cache API, as far as edgeCached uses it. */
function fakeCache() {
  const store = new Map<string, Response>();
  const cache = {
    match: async (key: string) => store.get(key)?.clone(),
    put: async (key: string, res: Response) => void store.set(key, res),
  } as unknown as Cache;
  return { cache, store };
}

const page = (body: string, status = 200) => async () =>
  new Response(body, { status, headers: { "content-type": "text/html; charset=utf-8", "cache-control": "private" } });

describe("edge cache", () => {
  it("keeps a page's 200 and answers the next request from it", async () => {
    const { cache, store } = fakeCache();
    const pending: Promise<unknown>[] = [];
    const first = await edgeCached(cache, "k", { 200: 60 }, (p) => pending.push(p), page("one"));
    expect(await first.text()).toBe("one");
    await Promise.all(pending);
    expect(store.get("k")?.headers.get("cache-control")).toBe("public, max-age=60");
    const second = await edgeCached(cache, "k", { 200: 60 }, (p) => pending.push(p), page("two"));
    expect(await second.text()).toBe("one");
  });

  it("keeps each status for its own time, and nothing for the rest", async () => {
    const { cache, store } = fakeCache();
    const pending: Promise<unknown>[] = [];
    const ttls = { 200: 3600, 404: 300 };
    expect((await edgeCached(cache, "a", { 200: 60 }, (p) => pending.push(p), page("gone", 404))).status).toBe(404);
    expect((await edgeCached(cache, "b", ttls, (p) => pending.push(p), page("down", 503))).status).toBe(503);
    await edgeCached(cache, "c", ttls, (p) => pending.push(p), page("gone", 404));
    await Promise.all(pending);
    expect([...store.keys()]).toEqual(["c"]);
    expect(store.get("c")?.headers.get("cache-control")).toBe("public, max-age=300");
  });

  it("keys a page by theme and query, and nothing else from the request", () => {
    const url = new URL("https://openwaters.io/ais/vessels/257000001-first");
    expect(pageCacheKey(url, null)).toBe("https://openwaters.io/ais/__edge/system/vessels/257000001-first");
    expect(pageCacheKey(url, "aiscast-theme=dark; other=1")).toBe("https://openwaters.io/ais/__edge/dark/vessels/257000001-first");
    expect(pageCacheKey(url, "aiscast-theme=<script>")).toBe(pageCacheKey(url, null));
    expect(pageCacheKey(new URL(`${url}?x=1`), null)).toMatch(/\?x=1$/);
  });

  it("shares vessel and station pages only", () => {
    const shared = (path: string, method = "GET") => {
      const url = new URL(`https://openwaters.io${path}`);
      return isSharedPage(new Request(url, { method }), url);
    };
    expect(shared("/ais/vessels/257000001-first")).toBe(true);
    expect(shared("/ais/stations/kystverket/2573010")).toBe(true);
    expect(shared("/ais/vessels")).toBe(false);
    expect(shared("/ais/vessels/media/9128403")).toBe(false);
    expect(shared("/ais/vessels/257000001-first.data")).toBe(false);
    expect(shared("/ais/vessels/257000001-first", "POST")).toBe(false);
  });
});
