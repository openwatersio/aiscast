import type { Page } from "@playwright/test";
import { vesselPath } from "../app/lib/ais";
import { namedVessel } from "./data";
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
const STATION = /\/ais\/stations\/digitraffic(#.*)?$/;
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
  await page.getByRole("navigation", { name: "Browse" }).getByRole("link", { name: /^Stations/ }).click();
  await expect(page).toHaveURL(STATIONS);
  await page.getByRole("link", { name: /^digitraffic/ }).click();
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
