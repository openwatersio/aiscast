import { readFileSync } from "node:fs";
import type { APIRequestContext } from "@playwright/test";
import { vesselPath, vesselSlug } from "../app/lib/ais";
import { heardFerry, heardGear, namedVessel } from "./data";
import { expect, test } from "./fixtures";

// With scripts off, whatever the page shows came from the Worker's render.
test.use({ javaScriptEnabled: false });

/** An MMSI in no country's range, so no vessel has it. */
const UNHEARD = 100000001;

for (const [label, findVessel] of [["a vessel", namedVessel], ["a ferry", heardFerry]] as const) {
  test(`${label} page renders the vessel, with its name in the head`, async ({ page, request }) => {
    const { mmsi, name } = await findVessel();
    const path = `/ais${vesselPath(mmsi, name)}`;
    // The vessel may have an explicit ferry identity in its synced particulars.
    const title = new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?: ferry tracker|, [a-z ]+: live AIS position) \\| Open Waters AIS$`);

    const res = await page.goto(path);
    expect(res?.status()).toBe(200);
    await expect(page).toHaveTitle(title);
    await expect(page.locator('link[rel="canonical"]')).toHaveAttribute("href", `https://openwaters.io${path}`);
    await expect(page.locator('meta[property="og:title"]')).toHaveAttribute("content", title);
    const description = await page.locator('meta[name="description"]').getAttribute("content");
    expect(description).toContain(`${name} is a`);
    expect(description).toContain(`MMSI ${mmsi}`);
    expect(description).not.toMatch(/Slackwater|slackwater/);
    await expect(page.locator('meta[name="robots"]')).toHaveCount(0);
    const ld = JSON.parse((await page.locator('script[type="application/ld+json"]').textContent()) ?? "");
    expect(ld).toMatchObject({
      "@type": "Vehicle",
      name,
      url: `https://openwaters.io${path}`,
      identifier: expect.arrayContaining([{ "@type": "PropertyValue", propertyID: "MMSI", value: String(mmsi) }]),
    });
    // Its photo when Wikimedia has one, else its card.
    const image = await page.locator('meta[property="og:image"]').getAttribute("content");
    expect(image === `https://openwaters.io/ais/vessels/${mmsi}.png` || image?.startsWith("https://thumb.wikimedia.org/") || image?.startsWith("https://upload.wikimedia.org/")).toBe(true);
    await expectCard(request, `/ais/vessels/${mmsi}.png`);
    // One heading, the vessel's, in sight: not a fallback with the vessel hidden behind it.
    await expect(page.getByRole("heading", { level: 1, includeHidden: true })).toHaveText([name]);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    if (label === "a ferry") {
      await page.setViewportSize({ width: 390, height: 844 });
      await expect(page.getByRole("heading", { name: "Riding this ferry?" })).toBeVisible();
      const link = page.getByRole("link", { name: "Get Slackwater", exact: true });
      await expect(link).toHaveCount(1);
      await expect(link).toHaveAttribute("href", "https://slackwater.xyz/?ref=ais-ferry");
      await expect(link).toHaveAttribute("target", "_blank");
      await expect(link).toHaveAttribute("rel", "noopener noreferrer");
      const card = await page.getByRole("complementary", { name: "Slackwater" }).boundingBox();
      expect(card!.x).toBeGreaterThanOrEqual(0);
      expect(card!.x + card!.width).toBeLessThanOrEqual(390);
      expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    }
  });
}

test.describe("Slackwater handoff", () => {
  test.use({ javaScriptEnabled: true });

  test("click, keyboard and middle-click each count once and preserve the ferry page", async ({ page, context }) => {
    const events: unknown[][] = [];
    await page.exposeFunction("recordHandoff", (...args: unknown[]) => events.push(args));
    await context.route("https://plausible.io/js/script.tagged-events.js", (route) => route.fulfill({
      contentType: "application/javascript",
      body: "window.startPlausible=()=>{const q=window.plausible?.q||[];window.plausible=(...args)=>window.recordHandoff(...args);q.forEach(args=>window.plausible(...args))};",
    }));
    await context.route("https://slackwater.xyz/**", (route) => route.fulfill({ contentType: "text/html", body: "<title>Slackwater test destination</title>" }));
    const { mmsi, name } = await heardFerry();
    const path = `/ais${vesselPath(mmsi, name)}#map=12/48.12/-123.43`;
    await page.goto(path);
    await page.waitForFunction(() => Boolean(window.aiscastMap));
    const link = page.getByRole("link", { name: "Get Slackwater", exact: true });
    const activations = [() => link.click(), () => link.press("Enter"), () => link.click({ button: "middle" })];
    for (const [index, activate] of activations.entries()) {
      const destination = context.waitForEvent("request", { predicate: (request) => request.url() === "https://slackwater.xyz/?ref=ais-ferry" });
      await activate();
      expect((await destination).headers().referer).toBeUndefined();
      expect(new URL(page.url()).pathname + new URL(page.url()).hash).toBe(path);
      // The first click must survive in the inline queue before the analytics script initializes.
      if (index === 0) await page.evaluate("window.startPlausible()");
    }
    await expect.poll(() => events).toEqual(Array.from({ length: 3 }, () => ["slackwater_handoff", { props: { context: "ferry" } }]));
  });
});

test("a vessel's address without its name, or with another, redirects to the canonical one", async ({ request }) => {
  const { mmsi, name } = await namedVessel();
  for (const path of [`/ais/vessels/${mmsi}`, `/ais/vessels/${mmsi}-not-its-name`]) {
    const res = await request.get(path, { maxRedirects: 0 });
    expect(res.status(), path).toBe(301);
    expect(new URL(res.headers().location!, res.url()).pathname, path).toBe(`/ais${vesselPath(mmsi, name)}`);
  }
});

test("gear's address carries no name, and its page asks not to be indexed", async ({ page, request }) => {
  const { mmsi, name } = await heardGear();
  // A net buoy's name is its serial and battery level, so the address leaves it out.
  const named = `/ais/vessels/${mmsi}-${vesselSlug(name)}`;
  const res = await request.get(named, { maxRedirects: 0 });
  expect(res.status(), named).toBe(301);
  expect(new URL(res.headers().location!, res.url()).pathname).toBe(`/ais/vessels/${mmsi}`);

  const doc = await page.goto(`/ais/vessels/${mmsi}`);
  expect(doc?.status()).toBe(200);
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute("href", `https://openwaters.io/ais/vessels/${mmsi}`);
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
  // The 10 m square it reports is a placeholder, so the page draws no hull for it.
  await expect(page.getByText("Fishing gear").first()).toBeVisible();
  await expect(page.getByText("AIS antenna position")).toHaveCount(0);
  await expect(page.getByText("Dimensions", { exact: true })).toHaveCount(0);
  // Nothing a vessel's page offers that gear has not got: photos, a heading, a status, a boat to own.
  await expect(page.getByText("Share your photo of this vessel")).toHaveCount(0);
  await expect(page.getByText("Add photo")).toHaveCount(0);
  await expect(page.getByText("Drift", { exact: true })).toBeVisible();
  await expect(page.getByText("Heading", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Is this your boat?")).toHaveCount(0);
  await expect(page.locator('script[type="application/ld+json"]')).toHaveCount(0);
  // With no photo to float over, the bar stands on the page and the title starts below it.
  await expect(page.locator("[data-over-photo]")).toHaveCount(0);
  const close = await page.getByRole("link", { name: "Close" }).boundingBox();
  const title = await page.getByRole("heading", { level: 1 }).boundingBox();
  expect(title!.y, "the title starts below the bar's buttons").toBeGreaterThanOrEqual(close!.y + close!.height);
});

test("a vessel the network has never heard is a 404 that asks not to be indexed", async ({ page }) => {
  const res = await page.goto(`/ais/vessels/${UNHEARD}`);
  expect(res?.status()).toBe(404);
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
  await expect(page.locator('link[rel="canonical"]')).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(`MMSI ${UNHEARD}`);
  await expect(page.getByText("The network has never heard this vessel.")).toBeVisible();
});

test("a fleet links its vessels by their canonical address before any report is in", async ({ page }) => {
  await page.goto("/ais/fleets/cruise-ships/carnival");
  await expect(page.getByRole("link", { name: "Carnival Adventure" })).toHaveAttribute("href", "/ais/vessels/311001595-carnival-adventure");
});

test("a fleet that does not exist is a 404 in the app, asking not to be indexed", async ({ page }) => {
  const res = await page.goto("/ais/fleets/nope");
  expect(res?.status()).toBe(404);
  await expect(page.getByText("There is nothing at this address.")).toBeVisible();
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", /noindex/);
});

test("the IndexNow key is served where the server's submissions say it is", async ({ request }) => {
  const key = /indexNowKey\s*=\s*"([0-9a-f]{32})"/.exec(readFileSync(new URL("../../server/indexnow.go", import.meta.url), "utf8"))?.[1];
  expect(key).toBeDefined();
  const res = await request.get(`/ais/vessels/${key}.txt`);
  expect(res.status()).toBe(200);
  expect((await res.text()).trim()).toBe(key);
});

test("an address that is not a vessel is a 404", async ({ page }) => {
  const res = await page.goto("/ais/vessels/not-a-vessel");
  expect(res?.status()).toBe(404);
  await expect(page).toHaveTitle("Not found | Open Waters AIS");
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
});

test("the network and station list pages share their own cards", async ({ page, request }) => {
  for (const name of ["network", "stations"]) {
    await page.goto(`/ais/${name}`);
    await expect(page.locator('meta[property="og:image"]')).toHaveAttribute("content", `https://openwaters.io/ais/${name}.png`);
    await expectCard(request, `/ais/${name}.png`);
  }
});

test("a station page renders the station, with its id in the head", async ({ page, request }) => {
  const res = await page.goto("/ais/stations/digitraffic");
  expect(res?.status()).toBe(200);
  await expect(page).toHaveTitle("Digitraffic (Finland) receiving station | Open Waters AIS");
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute("href", "https://openwaters.io/ais/stations/digitraffic");
  // Without ClickHouse, as this server runs, the station has no vessel counts, so its messages alone.
  await expect(page.locator('meta[name="description"]')).toHaveAttribute("content", /^AIS receiving station Digitraffic \(Finland\): ([\d,]+ vessels? and )?[\d,]+ messages? in 24 hours/);
  await expect(page.locator('meta[name="robots"]')).toHaveCount(0);
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute("content", "https://openwaters.io/ais/stations/digitraffic.png");
  const card = await request.get("/ais/stations/digitraffic.png");
  expect(card.status()).toBe(200);
  expect(card.headers()["content-type"]).toBe("image/png");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Digitraffic (Finland)");
  await expect(page.getByRole("link", { name: /MMSI \d+/ }).first()).toBeVisible();
});

test("station cards asked for at once each render whole", async ({ request }) => {
  // Spellings of one station miss the edge cache together, so each is a render of its own.
  const cards = await Promise.all(["digitraffic", "%64igitraffic", "digi%74raffic"].map((id) => request.get(`/ais/stations/${id}.png`)));
  for (const card of cards) {
    expect(card.status()).toBe(200);
    const png = await card.body();
    expect(png.subarray(1, 4).toString("ascii")).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([1200, 630]);
  }
});

test("a station the network has not heard is a 404 that asks not to be indexed", async ({ page }) => {
  const res = await page.goto("/ais/stations/nowhere/0");
  expect(res?.status()).toBe(404);
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex, follow");
  await expect(page.getByText("No station with this id has been heard recently.")).toBeVisible();
});

test("the aisstream.io station links its comparison and status pages, even while it is not heard", async ({ page }) => {
  // The e2e server has no aisstream subscription, so this is the station as it is after 30 days unheard.
  await page.goto("/ais/stations/aisstream");
  await expect(page.getByRole("link", { name: "Open Waters AIS token" })).toHaveAttribute("href", "https://openwaters.io/ais/token/");
  await expect(page.getByRole("link", { name: "Compare the APIs →" })).toHaveAttribute("href", "https://openwaters.io/ais/vs/aisstream/");
  await expect(page.getByRole("link", { name: "status page" })).toHaveAttribute("href", "https://openwaters.io/ais/alternatives/aisstream/");
  // A feed the e2e server does hear: its page renders whole, without the prompt.
  await page.goto("/ais/stations/digitraffic");
  await expect(page.getByText("Data feed")).toBeVisible();
  await expect(page.getByRole("link", { name: "Compare the APIs →" })).toHaveCount(0);
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

test("old map, station and explore links redirect to where those pages are now", async ({ request }) => {
  const redirects = {
    "/ais/map": "/ais/vessels",
    "/ais/map?station=digitraffic": "/ais/vessels?station=digitraffic",
    "/ais/vessels?station=digitraffic": "/ais/stations/digitraffic",
    "/ais/explore": "/ais/fleets",
    "/ais/explore/youtube": "/ais/fleets/youtube",
    "/ais/explore/youtube/sailors": "/ais/fleets/youtube/sailors",
    "/ais/explore/youtube/cruisers": "/ais/fleets/youtube/cruisers",
    "/ais/explore/tech-yachts": "/ais/fleets/superyachts/tech-billionaires",
    "/ais/explore/youtube/sailors/": "/ais/fleets/youtube/sailors",
    // A name every object has is not a fleet.
    "/ais/explore/constructor": "/ais/fleets",
  };
  for (const [from, to] of Object.entries(redirects)) {
    const res = await request.get(from, { maxRedirects: 0 });
    expect(res.status(), from).toBe(301);
    const location = new URL(res.headers().location!, res.url());
    expect(location.pathname + location.search, from).toBe(to);
  }
});

/** A card at `path`: a whole 1200×630 PNG. */
async function expectCard(request: APIRequestContext, path: string) {
  const card = await request.get(path);
  expect(card.status(), path).toBe(200);
  const png = await card.body();
  expect(png.subarray(1, 4).toString("ascii")).toBe("PNG");
  expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([1200, 630]);
}
