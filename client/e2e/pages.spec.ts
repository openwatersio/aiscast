import { vesselPath } from "../app/lib/ais";
import { namedVessel } from "./data";
import { expect, test } from "./fixtures";

// With scripts off, whatever the page shows came from the Worker's render.
test.use({ javaScriptEnabled: false });

/** An MMSI in no country's range, so no vessel has it. */
const UNHEARD = 100000001;

test("a vessel page renders the vessel, with its name in the head", async ({ page }) => {
  const { mmsi, name } = await namedVessel();
  const path = `/ais${vesselPath(mmsi, name)}`;
  const title = `${name} (${mmsi}) live position | Open Waters AIS`;

  const res = await page.goto(path);
  expect(res?.status()).toBe(200);
  await expect(page).toHaveTitle(title);
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute("href", `https://openwaters.io${path}`);
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute("content", title);
  expect(await page.locator('meta[name="description"]').getAttribute("content")).toContain(`Live AIS position for ${name}`);
  await expect(page.locator('meta[name="robots"]')).toHaveCount(0);
  const ld = JSON.parse((await page.locator('script[type="application/ld+json"]').textContent()) ?? "");
  expect(ld).toMatchObject({
    "@type": "Vehicle",
    name,
    url: `https://openwaters.io${path}`,
    identifier: expect.arrayContaining([{ "@type": "PropertyValue", propertyID: "MMSI", value: String(mmsi) }]),
  });
  // One heading, the vessel's, in sight: not a fallback with the vessel hidden behind it.
  await expect(page.getByRole("heading", { level: 1, includeHidden: true })).toHaveText([name]);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
});

test("a vessel's address without its name, or with another, redirects to the canonical one", async ({ request }) => {
  const { mmsi, name } = await namedVessel();
  for (const path of [`/ais/vessels/${mmsi}`, `/ais/vessels/${mmsi}-not-its-name`]) {
    const res = await request.get(path, { maxRedirects: 0 });
    expect(res.status(), path).toBe(301);
    expect(new URL(res.headers().location!, res.url()).pathname, path).toBe(`/ais${vesselPath(mmsi, name)}`);
  }
});

test("a vessel the network has never heard is a 404 that asks not to be indexed", async ({ page }) => {
  const res = await page.goto(`/ais/vessels/${UNHEARD}`);
  expect(res?.status()).toBe(404);
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
  await expect(page.locator('link[rel="canonical"]')).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(`MMSI ${UNHEARD}`);
  await expect(page.getByText("The network has never heard this vessel.")).toBeVisible();
});

test("an address that is not a vessel is a 404", async ({ page }) => {
  const res = await page.goto("/ais/vessels/not-a-vessel");
  expect(res?.status()).toBe(404);
  await expect(page).toHaveTitle("Not found | Open Waters AIS");
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
});

test("a station page renders the station, with its id in the head", async ({ page }) => {
  const res = await page.goto("/ais/stations/digitraffic");
  expect(res?.status()).toBe(200);
  await expect(page).toHaveTitle("digitraffic receiving station | Open Waters AIS");
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute("href", "https://openwaters.io/ais/stations/digitraffic");
  await expect(page.locator('meta[name="description"]')).toHaveAttribute("content", /^AIS receiving station digitraffic: [\d,]+ messages in 24 hours/);
  await expect(page.locator('meta[name="robots"]')).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("digitraffic");
  await expect(page.getByRole("link", { name: /MMSI \d+/ }).first()).toBeVisible();
});

test("a station the network has not heard is a 404 that asks not to be indexed", async ({ page }) => {
  const res = await page.goto("/ais/stations/nowhere/0");
  expect(res?.status()).toBe(404);
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
  await expect(page.getByText("No station with this id has been heard since the server started.")).toBeVisible();
});

test("the sitemap lists the vessel pages at their canonical addresses", async ({ request }) => {
  const index = await request.get("/ais/sitemap.xml");
  expect(index.status()).toBe(200);
  expect(index.headers()["content-type"]).toMatch(/^application\/xml/);
  const sitemaps = [...(await index.text()).matchAll(/<loc>https:\/\/openwaters\.io(\/ais\/[^<]+)<\/loc>/g)].map((m) => m[1]);
  expect(sitemaps).toContain("/ais/sitemap-pages.xml");
  expect(sitemaps).toContain("/ais/sitemap-vessels-1.xml");

  const pages = await (await request.get("/ais/sitemap-pages.xml")).text();
  expect(pages).toContain("<loc>https://openwaters.io/ais/stations/digitraffic</loc>");

  const vessels = await (await request.get("/ais/sitemap-vessels-1.xml")).text();
  const first = /<loc>https:\/\/openwaters\.io(\/ais\/vessels\/[^<]+)<\/loc><lastmod>/.exec(vessels)?.[1];
  expect(first).toBeTruthy();
  // Listed at the address the page answers at, not one that redirects.
  expect((await request.get(first!, { maxRedirects: 0 })).status()).toBe(200);
  expect((await request.get("/ais/sitemap-vessels-999.xml")).status()).toBe(404);
});

test("a page kept at the edge carries no visitor's location but the current one's", async ({ request }) => {
  // The second request is answered from the copy the first left. Were the location kept in
  // it, the Worker would add the reader's beside it. The query is this run's own, so the first
  // is not answered from a copy an earlier run left.
  const path = `/ais/stations/digitraffic?run=${Date.now()}`;
  const tags = async () => ((await (await request.get(path)).text()).match(/name="aiscast-visitor"/g) ?? []).length;
  const first = await tags();
  expect(first).toBeLessThanOrEqual(1);
  expect(await tags()).toBe(first);
});

test("old map and station links redirect to where those pages are now", async ({ request }) => {
  const redirects = {
    "/ais/map": "/ais/vessels",
    "/ais/map?station=digitraffic": "/ais/vessels?station=digitraffic",
    "/ais/vessels?station=digitraffic": "/ais/stations/digitraffic",
  };
  for (const [from, to] of Object.entries(redirects)) {
    const res = await request.get(from, { maxRedirects: 0 });
    expect(res.status(), from).toBe(301);
    const location = new URL(res.headers().location!, res.url());
    expect(location.pathname + location.search, from).toBe(to);
  }
});
