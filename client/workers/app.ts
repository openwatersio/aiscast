import { createRequestHandler, RouterContextProvider } from "react-router";
import { serverEnv } from "../app/lib/context";
import { edgeCached, isSharedPage, pageCacheKey } from "../app/lib/edge.server";
import { stationCard, stationCardId } from "../app/lib/shareCard.server";
import { isSitemap, sitemap } from "../app/lib/sitemap.server";
import { visitorMeta } from "../app/lib/visitor";
import { REPORT_PATH } from "../app/lib/report";
import { handleReport } from "../app/lib/report.server";

declare global {
  // A secret, set with `wrangler secret put AIS_TOKEN`, so the generated Env leaves it out.
  interface Env {
    AIS_TOKEN?: string;
  }
}

/** Cloudflare's guess at where the request came from, which it cannot always make. */
function locate(request: Request): [number, number] | undefined {
  const { latitude, longitude } = request.cf ?? {};
  if (!latitude || !longitude) return undefined;
  const lat = Number(latitude);
  const lon = Number(longitude);
  return Number.isFinite(lat) && Number.isFinite(lon) ? [lon, lat] : undefined;
}

const handler = createRequestHandler(
  () => import("virtual:react-router/server-build"),
  import.meta.env.MODE,
);

/**
 * The visitor's location, written into a page's head on its way out, so the map opens where
 * they are. After the edge cache, so the copy it keeps names no one's location.
 */
function withVisitor(res: Response, request: Request): Response {
  const at = locate(request);
  if (!at || !res.headers.get("content-type")?.startsWith("text/html")) return res;
  return new HTMLRewriter()
    .on("head", { element: (head) => void head.append(visitorMeta(at), { html: true }) })
    .transform(res);
}

const PAGE_TTLS = { 200: 60 };
/**
 * The sitemaps change by the hour at most, and each costs the API a read of the whole record.
 * A page past the last is kept briefly, as the next one may be listed soon.
 */
const SITEMAP_TTLS = { 200: 3600, 404: 300 };
/** A card's numbers cover 24 hours, so an hour old is fresh enough for a link preview. */
const CARD_TTLS = { 200: 3600, 301: 3600, 404: 300 };

const notGet = (request: Request) =>
  request.method !== "GET" && request.method !== "HEAD"
    ? new Response("Method not allowed", { status: 405, headers: { Allow: "GET, HEAD" } })
    : undefined;

export default {
  async fetch(request, env, ctx) {
    // On openwaters.io the Worker's routes send it only paths under /ais/. A workers.dev
    // address, such as a preview version's, sends it everything, and the app is at /ais/.
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/ais/")) return Response.redirect(new URL("/ais/vessels", url), 302);
    if (url.pathname === REPORT_PATH) return handleReport(request);
    const auth = { api: env.AIS_API, token: env.AIS_TOKEN || undefined };
    const cache = (caches as unknown as { default: Cache }).default;
    const waitUntil = (p: Promise<unknown>) => ctx.waitUntil(p);
    const render = () => {
      const context = new RouterContextProvider();
      context.set(serverEnv, auth);
      return handler(request, context);
    };

    // Sitemaps and cards are keyed without the query, which changes neither and would otherwise
    // let anyone make the Worker read the whole record, or draw a card, again.
    const bare = `${url.origin}${url.pathname}`;
    if (isSitemap(url.pathname)) {
      return notGet(request) ?? edgeCached(cache, bare, SITEMAP_TTLS, waitUntil, () => sitemap(url.pathname, auth));
    }
    const card = stationCardId(url.pathname);
    if (card != null) {
      return notGet(request) ?? edgeCached(cache, bare, CARD_TTLS, waitUntil, () => stationCard(auth, card));
    }
    if (!isSharedPage(request, url)) return withVisitor(await render(), request);

    const key = pageCacheKey(url, request.headers.get("cookie"));
    const res = await edgeCached(cache, key, PAGE_TTLS, waitUntil, render);
    // The browser keeps nothing: a theme switch writes the cookie, and the next load must show it.
    const out = new Response(res.body, res);
    out.headers.set("Cache-Control", "private");
    return withVisitor(out, request);
  },
} satisfies ExportedHandler<Env>;
