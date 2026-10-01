import { createPropertyExpression, latest } from "@maplibre/maplibre-gl-style-spec";
import { expect, openMap, test } from "./fixtures";

test("no vessel is drawn by both the tiles and the stream", async ({ page }) => {
  await openMap(page);

  // A tile's copy of a vessel the stream draws is marked with the `live` feature state...
  const drawn = () =>
    page.evaluate(() => {
      const map = window.aiscastMap!;
      const mmsi = (f: { properties: Record<string, unknown> }) => Number(f.properties.mmsi);
      const live = new Set(map.queryRenderedFeatures({ layers: ["vessel-still", "vessel-moving"] }).map(mmsi));
      const inBoth = map.queryRenderedFeatures({ layers: ["tile-still", "tile-moving"] }).filter((f) => live.has(mmsi(f)));
      return { inBoth: inBoth.length, unmarked: [...new Set(inBoth.filter((f) => !f.state.live).map(mmsi))] };
    });
  // Only a vessel both sources hold can be drawn twice, so wait for some.
  await expect.poll(async () => (await drawn()).inBoth, { timeout: 30_000 }).toBeGreaterThan(0);
  await expect.poll(async () => (await drawn()).unmarked).toEqual([]);

  // ...and every layer drawing the tiles turns that state into nothing on screen.
  const layers = await page.evaluate(() =>
    window
      .aiscastMap!.getStyle()
      .layers.flatMap((l) => ("source" in l && l.source === "tiles" ? [{ id: l.id, type: l.type, paint: l.paint ?? {} }] : [])),
  );
  expect(layers.map((l) => l.id)).toEqual(expect.arrayContaining(["tile-still", "tile-moving", "tile-label"]));
  const vessel = { type: 1 as const, id: 1, properties: { mmsi: 1, name: "TEST", age_s: 0, hdg: 90 } };
  for (const layer of layers) {
    const opacities = Object.entries(layer.paint).filter(([property]) => property.endsWith("-opacity"));
    expect(opacities.length, `${layer.id} has an opacity`).toBeGreaterThan(0);
    for (const [property, value] of opacities) {
      const spec = (latest as unknown as Record<string, Record<string, never>>)[`paint_${layer.type}`]![property]!;
      const parsed = createPropertyExpression(value, property, spec);
      if (parsed.result !== "success") throw new Error(`${layer.id} ${property}: ${JSON.stringify(parsed.value)}`);
      const at = (state: Record<string, boolean>) => parsed.value.evaluate({ zoom: 10 }, vessel, state);
      expect(at({ live: true }), `${layer.id} ${property} for a live vessel`).toBe(0);
      expect(at({}), `${layer.id} ${property} for a vessel only the tiles draw`).toBeGreaterThan(0);
    }
  }
});
