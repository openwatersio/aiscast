import { afterEach, describe, expect, it, vi } from "vitest";
import { vesselPath } from "./ais";
import { isSitemap, sitemap } from "./sitemap.server";

const auth = { api: "https://api.test" };

/** Answers the API paths given, and 404s the rest. */
function api(answers: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const path = url.slice(auth.api.length);
      if (!(path in answers)) return new Response("{}", { status: 404 });
      const body = answers[path];
      return body instanceof Response ? body : Response.json(body);
    }),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("sitemap", () => {
  it("matches only its own paths", () => {
    expect(isSitemap("/ais/sitemap.xml")).toBe(true);
    expect(isSitemap("/ais/sitemap-pages.xml")).toBe(true);
    expect(isSitemap("/ais/sitemap-vessels-2.xml")).toBe(true);
    expect(isSitemap("/ais/sitemap-vessels-x.xml")).toBe(false);
    // One address per page, so each is cached once.
    expect(isSitemap("/ais/sitemap-vessels-01.xml")).toBe(false);
    expect(isSitemap("/ais/sitemap-vessels-0.xml")).toBe(false);
    expect(isSitemap("/ais/vessels")).toBe(false);
  });

  it("indexes the pages file and one file per page of vessels", async () => {
    api({
      "/sitemap/vessels": {
        page_size: 50000,
        max_age_s: 2592000,
        pages: [
          { vessels: 50000, lastmod: "2026-10-02T10:00:00Z" },
          { vessels: 12, lastmod: "2026-10-02T11:00:00Z" },
        ],
      },
    });
    const res = await sitemap("/ais/sitemap.xml", auth);
    const body = await res.text();
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toMatch(/^application\/xml/);
    expect(body).toContain("<sitemapindex");
    expect(body).toContain("<sitemap><loc>https://openwaters.io/ais/sitemap-pages.xml</loc></sitemap>");
    expect(body).toContain(
      "<sitemap><loc>https://openwaters.io/ais/sitemap-vessels-2.xml</loc><lastmod>2026-10-02T11:00:00Z</lastmod></sitemap>",
    );
    expect(body).not.toContain("sitemap-vessels-3.xml");
  });

  it("lists the app's pages and the stations heard lately, but no one's own receiver", async () => {
    const station = (id: string, source: string, ageS: number) => ({ station: id, source, last_seen: "2026-10-02T10:00:00Z", last_age_s: ageS });
    api({
      "/v1/stations": [
        station("kystverket/2573010", "kystverket", 60),
        station("aishub", "aishub", 90 * 86400),
        station("udp:24dfc99708ff", "udp:24dfc99708ff", 60),
      ],
    });
    const body = await (await sitemap("/ais/sitemap-pages.xml", auth)).text();
    expect(body).toContain("<url><loc>https://openwaters.io/ais/vessels</loc></url>");
    expect(body).toContain("<url><loc>https://openwaters.io/ais/network</loc></url>");
    expect(body).toContain("<loc>https://openwaters.io/ais/stations/kystverket/2573010</loc><lastmod>2026-10-02T10:00:00Z</lastmod>");
    expect(body).not.toContain("stations/aishub");
    expect(body).not.toContain("udp:");
  });

  it("lists the app's pages before any station is heard, and is a 503 when the API is down", async () => {
    api({ "/v1/stations": [] });
    const empty = await sitemap("/ais/sitemap-pages.xml", auth);
    expect(empty.status).toBe(200);
    expect(await empty.text()).toContain("<url><loc>https://openwaters.io/ais/vessels</loc></url>");
    api({ "/v1/stations": new Response("down", { status: 502 }) });
    expect((await sitemap("/ais/sitemap-pages.xml", auth)).status).toBe(503);
  });

  it("lists each vessel at its canonical address, escaped", async () => {
    api({
      "/sitemap/vessels?page=1": {
        vessels: [
          { mmsi: 257000001, name: "FIRST", seen: "2026-10-02T10:00:00Z" },
          { mmsi: 257000002, name: "A&B", seen: "2026-10-02T11:00:00Z" },
        ],
      },
    });
    const body = await (await sitemap("/ais/sitemap-vessels-1.xml", auth)).text();
    expect(body).toContain(`<url><loc>https://openwaters.io/ais${vesselPath(257000001, "FIRST")}</loc><lastmod>2026-10-02T10:00:00Z</lastmod></url>`);
    expect(body).toContain(`https://openwaters.io/ais${vesselPath(257000002, "A&B").replace(/&/g, "&amp;")}</loc>`);
    expect(body).not.toMatch(/&(?!amp;|lt;|gt;|quot;|apos;)/);
  });

  it("is a 404 past the last page and a 503 when the API is down", async () => {
    api({ "/sitemap/vessels": new Response("down", { status: 502 }) });
    expect((await sitemap("/ais/sitemap-vessels-9.xml", auth)).status).toBe(404);
    expect((await sitemap("/ais/sitemap-vessels-5000.xml", auth)).status).toBe(404);
    const down = await sitemap("/ais/sitemap.xml", auth);
    expect(down.status).toBe(503);
    expect(down.headers.get("retry-after")).toBe("300");
    // A server without the endpoint is not one with no vessels.
    api({});
    expect((await sitemap("/ais/sitemap.xml", auth)).status).toBe(503);
  });
});
