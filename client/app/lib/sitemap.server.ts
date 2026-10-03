import { isVolunteer, vesselPath, volunteerReceiver } from "./ais";
import { ApiUnavailable, getSitemapPages, getSitemapVessels, getStations, type ApiAuth } from "./api";
import { SITE } from "./meta";

/**
 * The sitemaps live beside the pages they list, as the protocol asks: a sitemap may only list
 * URLs under its own directory. The index lists the app's pages and stations in one file and
 * the vessels in as many as the server's pages, up to 50,000 URLs each.
 */
export const SITEMAP_INDEX = "/ais/sitemap.xml";
const PAGES_SITEMAP = "/ais/sitemap-pages.xml";
// No leading zeros, so each page has one address and one cached copy.
const VESSELS_SITEMAP = /^\/ais\/sitemap-vessels-([1-9]\d{0,3})\.xml$/;

/** The app's own pages. They change with the network, not on a date worth stating. */
const APP_PAGES = ["/vessels", "/stations", "/network", "/explore", "/explore/youtube/sailors"];

/** A station unheard for this long is a page about a receiver that has gone quiet. */
const STATION_MAX_AGE_S = 30 * 24 * 3600;

const ENTITIES: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&apos;" };
const xml = (s: string) => s.replace(/[&<>"']/g, (c) => ENTITIES[c] ?? c);

function entry(tag: "url" | "sitemap", loc: string, lastmod?: string) {
  return `<${tag}><loc>${xml(loc)}</loc>${lastmod ? `<lastmod>${xml(lastmod)}</lastmod>` : ""}</${tag}>`;
}

function document(root: "urlset" | "sitemapindex", entries: string[]) {
  return new Response(
    `<?xml version="1.0" encoding="UTF-8"?>\n<${root} xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${entries.join("\n")}\n</${root}>\n`,
    { headers: { "Content-Type": "application/xml; charset=utf-8" } },
  );
}

export function isSitemap(pathname: string) {
  return pathname === SITEMAP_INDEX || pathname === PAGES_SITEMAP || VESSELS_SITEMAP.test(pathname);
}

/**
 * The sitemap at `pathname`, which isSitemap has matched. A 404 for a vessel page past the
 * last, and a 503 when the API does not answer, which a crawler retries.
 */
export async function sitemap(pathname: string, auth: ApiAuth): Promise<Response> {
  try {
    if (pathname === SITEMAP_INDEX) {
      const { pages } = await getSitemapPages(auth);
      return document("sitemapindex", [
        entry("sitemap", `${SITE}/sitemap-pages.xml`),
        ...pages.map((p, i) => entry("sitemap", `${SITE}/sitemap-vessels-${i + 1}.xml`, p.lastmod)),
      ]);
    }
    if (pathname === PAGES_SITEMAP) {
      // Undefined is an outage; an empty list is a server that has not heard a station yet.
      const stations = await getStations(auth);
      if (!stations) throw new ApiUnavailable("/v1/stations: no answer");
      return document("urlset", [
        ...APP_PAGES.map((path) => entry("url", `${SITE}${path}`)),
        // A volunteer's receiver is a person's own station, often on their boat or at home, so
        // the sitemap does not list it. Its page is as it was for anyone who follows a link.
        ...stations
          .filter((st) => st.last_age_s <= STATION_MAX_AGE_S && !isVolunteer(st.source) && !volunteerReceiver(st.station))
          .map((st) => entry("url", `${SITE}/stations/${st.station}`, st.last_seen)),
      ]);
    }
    const page = Number(VESSELS_SITEMAP.exec(pathname)?.[1]);
    // The server numbers no more than 1,000 pages.
    const vessels = page >= 1 && page <= 1000 ? await getSitemapVessels(auth, page) : undefined;
    if (!vessels) return new Response("Not found", { status: 404 });
    return document(
      "urlset",
      vessels.map((v) => entry("url", `${SITE}${vesselPath(v.mmsi, v.name)}`, v.seen)),
    );
  } catch (e) {
    if (!(e instanceof ApiUnavailable)) throw e;
    console.error(e);
    return new Response("The AIS API is unavailable", { status: 503, headers: { "Retry-After": "300" } });
  }
}
