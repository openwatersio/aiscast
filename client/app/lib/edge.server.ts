import { themeFromCookie } from "./theme";

/**
 * A vessel or station page, the pages a crawler walks the sitemap through. Their HTML is the
 * same for every visitor with the same theme, so the edge keeps each for a minute and a second
 * visit costs the API nothing. The photos' JSON under /vessels/media/ caches itself.
 */
const SHARED_PAGE = /^\/ais\/(?:vessels\/(?!media\/)[^/]+|stations\/.+)$/;

/** Whether a request is for a page the edge keeps a shared copy of. */
export function isSharedPage(request: Request, url: URL) {
  return request.method === "GET" && SHARED_PAGE.test(url.pathname) && !url.pathname.endsWith(".data");
}

/**
 * Where a shared page's copy is kept. The theme is rendered into the page, so each choice is
 * its own copy. The query stays in the key: these pages render nothing from it today, and a
 * page that comes to would otherwise be served a copy made for another query.
 */
export function pageCacheKey(url: URL, cookie: string | null) {
  return `${url.origin}/ais/__edge/${themeFromCookie(cookie)}${url.pathname.slice(4)}${url.search}`;
}

/**
 * The response for `key` from this data centre's cache, or `make`'s, stored for as many
 * seconds as `ttls` gives its status, and not stored for a status it leaves out. The Cache API
 * is a no-op on workers.dev, so previews always render.
 */
export async function edgeCached(
  cache: Cache,
  key: string,
  ttls: Partial<Record<number, number>>,
  waitUntil: (p: Promise<unknown>) => void,
  make: () => Promise<Response>,
) {
  const hit = await cache.match(key);
  if (hit) return hit;
  const made = await make();
  const ttl = ttls[made.status];
  if (!ttl) return made;
  const res = new Response(made.body, made);
  res.headers.set("Cache-Control", `public, max-age=${ttl}`);
  waitUntil(cache.put(key, res.clone()));
  return res;
}

/** A 405 for anything but GET or HEAD, for the paths the Worker answers itself; otherwise undefined. */
export function notGetOrHead(request: Request): Response | undefined {
  if (request.method === "GET" || request.method === "HEAD") return undefined;
  return new Response("Method not allowed", { status: 405, headers: { Allow: "GET, HEAD" } });
}
