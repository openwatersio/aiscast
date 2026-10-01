import { expect, openMap, test } from "./fixtures";

test("no vessel is drawn by both the tiles and the stream", async ({ page }) => {
  await openMap(page);
  const drawn = () =>
    page.evaluate(() => {
      const map = window.aiscastMap!;
      const mmsi = (f: { properties: Record<string, unknown> }) => Number(f.properties.mmsi);
      const live = new Set(map.queryRenderedFeatures({ layers: ["vessel-still", "vessel-moving"] }).map(mmsi));
      const inBoth = map.queryRenderedFeatures({ layers: ["tile-still", "tile-moving"] }).filter((f) => live.has(mmsi(f)));
      // The tile layers hide a feature whose `live` state is set.
      return { inBoth: inBoth.length, twice: [...new Set(inBoth.filter((f) => !f.state.live).map(mmsi))] };
    });

  // Only a vessel both sources hold can be drawn twice, so wait for some.
  await expect.poll(async () => (await drawn()).inBoth, { timeout: 30_000 }).toBeGreaterThan(0);
  await expect.poll(async () => (await drawn()).twice).toEqual([]);
});
