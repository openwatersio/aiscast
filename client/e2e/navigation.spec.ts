import type { Page } from "@playwright/test";
import { vesselPath } from "../app/lib/ais";
import { heardFromVolunteer, namedVessel } from "./data";
import {
  expect,
  mapStatus,
  openMap,
  openVesselOnMap,
  test,
  useOneStreamToken,
  waitForFlight,
  waitForVessels,
} from "./fixtures";

// The map writes its camera into the address's hash as it moves.
const HOME = /\/ais\/vessels(#.*)?$/;
const STATIONS = /\/ais\/stations(#.*)?$/;
const STATION = /\/ais\/stations\/udp:[0-9a-f]+(#.*)?$/;
const vesselURL = (mmsi: number) => new RegExp(`/ais/vessels/${mmsi}(-[^/#]*)?(#.*)?$`);

const back = (page: Page) => page.getByRole("link", { name: "Back", exact: true }).click();

/** Opens a second vessel on the map from a vessel's page, zoomed back out to find one. */
async function openAnotherVesselOnMap(page: Page, first: number) {
  await waitForFlight(page);
  await page.evaluate(() => window.aiscastMap!.jumpTo({ zoom: 8 }));
  await waitForVessels(page);
  return openVesselOnMap(page, [first]);
}

async function openStationFromHome(page: Page) {
  await heardFromVolunteer();
  await page.getByRole("navigation", { name: "Browse" }).getByRole("link", { name: /^Stations/ }).click();
  await expect(page).toHaveURL(STATIONS);
  await page.getByRole("link", { name: /^Anonymous/ }).click();
  await expect(page).toHaveURL(STATION);
}

test("Back from vessels opened one after another on the map returns to the page under them", async ({ page }) => {
  await openMap(page);
  const first = await openVesselOnMap(page);
  const second = await openAnotherVesselOnMap(page, first);

  // The browser's own back steps through each vessel.
  await page.goBack();
  await expect(page).toHaveURL(vesselURL(first));
  await page.goForward();
  await expect(page).toHaveURL(vesselURL(second));

  await back(page);
  await expect(page).toHaveURL(HOME);
  await expect(page.getByRole("searchbox")).toBeVisible();
});

test("Back from a station's vessel returns to the station, then the list, then the map", async ({ page }) => {
  await openMap(page);
  await openStationFromHome(page);
  await page.getByRole("link", { name: /MMSI \d+/ }).first().click();
  await expect(page).toHaveURL(/\/ais\/vessels\/\d+/);

  await back(page);
  await expect(page).toHaveURL(STATION);
  await back(page);
  await expect(page).toHaveURL(STATIONS);
  await back(page);
  await expect(page).toHaveURL(HOME);
});

test("Back after a direct visit goes to the page's parent", async ({ page }) => {
  const { mmsi, name } = await namedVessel();
  await page.goto(`/ais${vesselPath(mmsi, name)}`);
  await back(page);
  await expect(page).toHaveURL(HOME);

  await page.goto("/ais/stations/digitraffic");
  await back(page);
  await expect(page).toHaveURL(STATIONS);
});

test("Back after a reload still returns through the app's history", async ({ page }) => {
  await openMap(page);
  await openStationFromHome(page);
  await page.reload();
  await back(page);
  await expect(page).toHaveURL(STATIONS);

  // A direct visit, reloaded, still has nothing of the app's behind it.
  await page.goto("/ais/stations/digitraffic");
  await page.reload();
  await back(page);
  await expect(page).toHaveURL(STATIONS);
});

test("Back after a reload skips vessels opened one after another on the map", async ({ page }) => {
  await openMap(page);
  const first = await openVesselOnMap(page);
  await openAnotherVesselOnMap(page, first);
  await page.reload();
  await back(page);
  await expect(page).toHaveURL(HOME);
});

test("Back skips vessels opened one after another when one's address is corrected", async ({ page }) => {
  // Every record names its vessel differently from the map, which happens for real until the
  // stream hears a vessel's static data. A page opened by the map's name then redirects.
  await page.route(/\/v1\/vessels\/\d+$/, async (route) => {
    const response = await route.fetch();
    const feature = await response.json();
    await route.fulfill({ response, json: { ...feature, properties: { ...feature.properties, name: "E2E RENAMED" } } });
  });
  await openMap(page);
  const first = await openVesselOnMap(page);
  await waitForFlight(page);
  await page.evaluate(() => window.aiscastMap!.jumpTo({ zoom: 8 }));
  await waitForVessels(page);
  // With a name from the stream the page opens from that and never asks the record, so the
  // stream is made not to have heard one. Its reports and the tiles would give it one back.
  const second = await openVesselOnMap(page, [first], (mmsi) =>
    page.evaluate((mmsi) => {
      const heard = window.aiscastStream!.vessels.get(mmsi);
      if (heard) Object.defineProperty(heard, "name", { get: () => undefined, set: () => {}, configurable: true });
    }, mmsi),
  );
  await expect(page).toHaveURL(new RegExp(`/ais/vessels/${second}-e2e-renamed(#|$)`));
  // The state is put back just after the redirect lands.
  await expect
    .poll(() => page.evaluate(() => (history.state as { usr?: { backSteps?: number } } | null)?.usr?.backSteps))
    .toBe(2);

  await back(page);
  await expect(page).toHaveURL(HOME);
});

test("one map and one stream last through search, stations, a station, a vessel and back", async ({ page }) => {
  // The stream's socket is in a SharedWorker, out of Playwright's sight. With a token allowed
  // one stream, a second would be refused, and the map would say so.
  await useOneStreamToken(page.context());
  const { mmsi, name } = await namedVessel();

  await openMap(page);
  await page.evaluate(() => {
    const w = window as { firstMap?: unknown; firstStream?: unknown };
    w.firstMap = window.aiscastMap;
    w.firstStream = window.aiscastStream;
  });

  await openStationFromHome(page);
  await page.getByRole("link", { name: /MMSI \d+/ }).first().click();
  await expect(page).toHaveURL(/\/ais\/vessels\/\d+/);
  await back(page);
  await expect(page).toHaveURL(STATION);
  await back(page);
  await expect(page).toHaveURL(STATIONS);
  await back(page);
  await expect(page).toHaveURL(HOME);

  await page.getByRole("searchbox").fill(name);
  // By its address: another vessel's name can start with this one's.
  await page.locator(`a[href="/ais${vesselPath(mmsi, name)}"]`).click();
  await expect(page).toHaveURL(vesselURL(mmsi));
  await back(page);
  await expect(page).toHaveURL(HOME);

  expect(await page.evaluate(() => (window as { firstMap?: unknown }).firstMap === window.aiscastMap)).toBe(true);
  expect(await page.evaluate(() => (window as { firstStream?: unknown }).firstStream === window.aiscastStream)).toBe(true);
  await expect(page.getByRole("region", { name: "Map" })).toHaveCount(1);
  await expect(mapStatus(page)).toHaveText("Live");
});

test("the bar takes the page's title once the large title scrolls under its buttons", async ({ page }) => {
  const { mmsi, name } = await namedVessel();
  await page.goto(`/ais${vesselPath(mmsi, name)}`);
  // Hydrated, so the scroll below is not undone by the page restoring its own.
  await page.waitForFunction(() => window.aiscastMap);
  const barTitle = page.getByText(name, { exact: true }).and(page.locator("span"));
  await expect(barTitle).toHaveAttribute("aria-hidden", "true");

  // The large title's top 10px under the buttons, which end 44px down, and the rest in sight.
  await page.locator("[data-sheet-scroll]").evaluate((scroller) => {
    const title = scroller.querySelector("h1")!;
    scroller.scrollTop += title.getBoundingClientRect().top - scroller.getBoundingClientRect().top - 34;
  });
  await expect(page.getByRole("heading", { level: 1 })).toBeInViewport();
  await expect(barTitle).toHaveAttribute("aria-hidden", "false");
});

test("The map's fleets are asked for once, and returning without the Worker keeps the map", async ({ page }) => {
  let asked = 0;
  page.on("request", (r) => r.url().includes("/ais/vessels.data") && asked++);
  await openMap(page);
  await expect(page.getByRole("link", { name: "Browse all" })).toBeVisible();
  const toStations = async () => {
    await page.getByRole("navigation", { name: "Browse" }).getByRole("link", { name: /^Stations/ }).click();
    await expect(page).toHaveURL(STATIONS);
  };
  await toStations();
  await back(page);
  await expect(page.getByRole("link", { name: "Browse all" })).toBeVisible();
  await toStations();
  await back(page);
  await expect(page.getByRole("link", { name: "Browse all" })).toBeVisible();
  expect(asked).toBe(1);

  // In a fresh page, offline or a Worker that cannot answer: the fleets are the only thing it asks for.
  await page.reload();
  await waitForVessels(page);
  await toStations();
  await page.route("**/ais/vessels.data*", (route) => route.abort());
  await back(page);
  await expect(page).toHaveURL(HOME);
  await expect(page.getByRole("navigation", { name: "Browse" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Browse all" })).toHaveCount(0);
  await waitForVessels(page);
});
