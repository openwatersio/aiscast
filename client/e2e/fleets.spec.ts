import { API } from "./data";
import { expect, test } from "./fixtures";

test("fleets open from the map's menu, list their vessels, and open one on the map", async ({ page }) => {
  // Koru answers; every other vessel is one the network has never heard.
  await page.route(`${API}/v1/vessels/*`, (route) => {
    const mmsi = Number(new URL(route.request().url()).pathname.split("/").pop());
    return mmsi === 319225400
      ? route.fulfill({
          headers: { "access-control-allow-origin": "*" },
          json: {
            type: "Feature",
            id: mmsi,
            geometry: { type: "Point", coordinates: [7.4, 43.7] },
            properties: { mmsi, name: "KORU", seen: new Date().toISOString(), source: "aishub" },
          },
        })
      : route.fulfill({ status: 404, json: {}, headers: { "access-control-allow-origin": "*" } });
  });
  await page.route("**/ais/vessels/media/*", (route) =>
    route.fulfill({
      json: {
        photos: route.request().url().endsWith("/9857298")
          ? [{ thumb: "https://images.example.test/koru.svg", width: 800, height: 450, page: "https://commons.wikimedia.org/wiki/File:Koru.jpg", artist: "Yacht photographer", license: "CC BY-SA 4.0" }]
          : [],
        links: {},
      },
    }),
  );
  await page.route("https://images.example.test/*", (route) =>
    route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" width="800" height="450"><rect width="800" height="450" fill="#c5dbe5"/></svg>' }),
  );

  await page.goto("/ais/vessels");
  await page.getByRole("navigation", { name: "Browse" }).getByRole("link", { name: /Fleets/ }).click();
  await expect(page).toHaveURL(/\/ais\/fleets$/);
  // Groups nest: Superyachts holds the tech billionaires' yachts.
  const superyachts = page.getByRole("link", { name: /^Superyachts/ });
  await expect(superyachts).toContainText("3 fleets");
  await superyachts.click();
  await expect(page).toHaveURL(/\/ais\/fleets\/superyachts$/);
  // A group's cover is its first fleet's, and carries the photo's credit.
  const group = page.getByRole("link", { name: /Tech billionaires/ });
  await expect(group).toContainText("2 fleets");
  await group.click();
  await expect(page).toHaveURL(/\/ais\/fleets\/superyachts\/tech-billionaires$/);
  await expect(page.getByRole("link", { name: /Sailing yachts/ })).toContainText("© Yacht photographer · CC BY-SA 4.0");
  // The credit shows as a © until it is pointed at, then opens to the whole of it.
  const credit = page.getByRole("link", { name: /Sailing yachts/ }).locator('[class*="group/credit"]');
  expect((await credit.boundingBox())!.width).toBeLessThan(40);
  await credit.hover();
  await expect.poll(async () => (await credit.boundingBox())!.width).toBeGreaterThan(120);
  await page.getByRole("link", { name: /Sailing yachts/ }).click();
  await expect(page).toHaveURL(/\/ais\/fleets\/superyachts\/tech-billionaires\/sailing$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Sailing yachts");
  // The one yacht heard is framed beside the panel, not under it.
  const panelRight = await page.getByRole("region", { name: "Panel" }).evaluate((el) => el.getBoundingClientRect().right);
  await expect
    .poll(() => page.evaluate(() => (window.aiscastMap!.isMoving() ? -1 : window.aiscastMap!.project([7.4, 43.7]).x)))
    .toBeGreaterThan(panelRight);
  await expect(page.getByRole("link", { name: "Ownership history" })).toHaveCount(4);
  // An unconfirmed identity is listed, but not as a link into the map.
  await expect(page.getByText("Rán VII")).toBeVisible();
  await expect(page.getByRole("link", { name: /Rán VII/ })).toHaveCount(0);
  // The most recently heard lead, so Koru, the only one heard, comes first.
  await expect(page.locator("[data-sheet-scroll] li h3").first()).toHaveText(/^Koru/);
  await page.getByRole("link", { name: /^Koru/ }).click();
  await expect(page).toHaveURL(/\/ais\/vessels\/319225400/);
  // Back from the vessel returns to the fleet, and back from the fleet to its group.
  await page.goBack();
  await page.getByRole("link", { name: "Back" }).click();
  await expect(page).toHaveURL(/\/ais\/fleets\/superyachts\/tech-billionaires(#|$)/);
});
