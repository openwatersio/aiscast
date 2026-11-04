import { heardFromVolunteer } from "./data";
import { expect, test } from "./fixtures";

test("a station page hydrates cleanly though its vessels' ages move on between server and browser", async ({ page }) => {
  const id = await heardFromVolunteer();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && /hydrat/i.test(m.text()) && errors.push(m.text()));
  // The browser's clock ten seconds on from the server's, as a slow load would leave it.
  await page.clock.setFixedTime(Date.now() + 10_000);
  await page.goto(`/ais/stations/${id}`);
  // Hydrated once React has claimed the server's heading.
  await page.waitForFunction(() => Object.keys(document.querySelector("h1") ?? {}).some((k) => k.startsWith("__reactFiber")));
  await expect(page.getByRole("link", { name: /MMSI \d+/ }).first()).toBeVisible();
  expect(errors).toEqual([]);
});

test("a station page without the series' figures leaves out its chart and footprint", async ({ page }) => {
  // The e2e server runs without ClickHouse, so it serves no hours, types, or coverage cells.
  const id = await heardFromVolunteer();
  const coverage = page.waitForResponse((r) => r.url().includes("/v1/coverage/tiles.json?station="));
  await page.goto(`/ais/stations/${id}`);
  await expect(page.getByText(/^Last heard /)).toBeVisible();
  await expect(page.getByRole("img", { name: /^Messages per hour/ })).toHaveCount(0);
  // Once the coverage request has had its answer, which here is none, and the map has its style.
  expect((await coverage).ok()).toBe(false);
  await expect.poll(() => page.evaluate(() => window.aiscastMap?.isStyleLoaded() ?? false)).toBe(true);
  expect(await page.evaluate(() => Boolean(window.aiscastMap?.getSource("footprint")))).toBe(false);
});
