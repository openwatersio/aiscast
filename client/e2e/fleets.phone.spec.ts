import { API } from "./data";
import { expect, test } from "./fixtures";

// Two of the fleet's yachts a few miles apart off Monaco; the rest the network has never heard.
const HEARD: Record<number, [number, number]> = { 319225400: [7.42, 43.73], 319012000: [7.27, 43.69] };

test("a fleet on a phone is framed close in, above the sheet", async ({ page }) => {
  await page.route(`${API}/v1/vessels/*`, (route) => {
    const mmsi = Number(new URL(route.request().url()).pathname.split("/").pop());
    const at = HEARD[mmsi];
    return at
      ? route.fulfill({
          headers: { "access-control-allow-origin": "*" },
          json: { type: "Feature", id: mmsi, geometry: { type: "Point", coordinates: at }, properties: { mmsi, seen: new Date().toISOString(), source: "aishub" } },
        })
      : route.fulfill({ status: 404, json: {}, headers: { "access-control-allow-origin": "*" } });
  });
  await page.route("**/ais/vessels/media/*", (route) => route.fulfill({ json: { photos: [], links: {} } }));
  await page.goto("/ais/fleets/tech-billionaires/sailing");
  const sheetTop = (await page.getByRole("region", { name: "Panel" }).boundingBox())!.y;
  // Close enough to tell the two apart, which the whole world is not, and both in the map's
  // uncovered part.
  await expect.poll(() => page.evaluate(() => (window.aiscastMap!.isMoving() ? 0 : window.aiscastMap!.getZoom()))).toBeGreaterThan(8);
  const { points } = (await page.evaluate(() => {
    const map = window.aiscastMap!;
    return { points: [map.project([7.42, 43.73]), map.project([7.27, 43.69])].map((p) => ({ x: p.x, y: p.y })) };
  }))!;
  const width = page.viewportSize()!.width;
  for (const p of points) {
    expect(p.x).toBeGreaterThan(0);
    expect(p.x).toBeLessThan(width);
    expect(p.y).toBeGreaterThan(0);
    expect(p.y).toBeLessThan(sheetTop);
  }
});
