import { createPropertyExpression, latest } from "@maplibre/maplibre-gl-style-spec";
import { vesselPath } from "../app/lib/ais";
import { api, heardGear, namedVessel } from "./data";
import { expect, openMap, test, waitForFlight } from "./fixtures";

test("no vessel is drawn by both the tiles and the stream", async ({ page }) => {
  await openMap(page);

  // A tile's copy of a vessel the stream draws is marked with the `live` feature state...
  const drawn = () =>
    page.evaluate(() => {
      const map = window.aiscastMap!;
      const mmsi = (f: { properties: Record<string, unknown> }) => Number(f.properties.mmsi);
      const live = new Set(map.queryRenderedFeatures({ layers: ["vessel-still", "vessel-moving", "vessel-gear"] }).map(mmsi));
      const inBoth = map.queryRenderedFeatures({ layers: ["tile-still", "tile-moving", "tile-gear"] }).filter((f) => live.has(mmsi(f)));
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
  expect(layers.map((l) => l.id)).toEqual(expect.arrayContaining(["tile-still", "tile-moving", "tile-gear", "tile-label"]));
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

test("fishing gear is drawn as its own square, not a vessel's arrow or dot", async ({ page }) => {
  // The buoy reports the course of its drift, which would turn a vessel's arrow.
  const { mmsi } = await heardGear();
  await openMap(page);
  const layersOf = () =>
    page.evaluate((m) => {
      const ids = new Set<string>();
      for (const f of window.aiscastMap!.queryRenderedFeatures()) if (Number(f.properties?.mmsi) === m) ids.add(f.layer.id);
      return [...ids];
    }, mmsi);
  await expect.poll(async () => (await layersOf()).some((id) => id.endsWith("-gear")), { timeout: 30_000 }).toBe(true);
  expect((await layersOf()).filter((id) => /-(still|moving)$/.test(id))).toEqual([]);
});

test("the map opens where the visitor is, unless the link has a #map= hash", async ({ page }) => {
  // Where the Worker places a visitor depends on the address the test runs from, so the
  // document's location is swapped for a known one: Oslo.
  await page.route("**/ais/vessels", async (route) => {
    const res = await route.fetch();
    const html = (await res.text()).replace(/<meta name="aiscast-visitor"[^>]*>/, "");
    await route.fulfill({ response: res, body: html.replace("</head>", '<meta name="aiscast-visitor" content="10.75,59.91"></head>') });
  });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && /hydrat/i.test(m.text()) && errors.push(m.text()));
  const firstView = () =>
    page.waitForFunction(() => {
      const map = window.aiscastMap;
      if (!map) return undefined;
      const c = map.getCenter();
      return { lon: c.lng, lat: c.lat, zoom: map.getZoom() };
    });

  await page.goto("/ais/vessels");
  const atVisitor = await (await firstView()).jsonValue();
  expect(atVisitor!.lon).toBeCloseTo(10.75, 1);
  expect(atVisitor!.lat).toBeCloseTo(59.91, 1);
  expect(errors).toEqual([]);

  await page.goto("about:blank");
  await page.goto("/ais/vessels#map=8/59.85/24.9");
  const atHash = await (await firstView()).jsonValue();
  expect(atHash).toMatchObject({ zoom: 8 });
  expect(atHash!.lon).toBeCloseTo(24.9, 1);
  expect(atHash!.lat).toBeCloseTo(59.85, 1);
});

test("a vessel's page opens on the vessel, without a flight to it", async ({ page }) => {
  const { mmsi, name } = await namedVessel();
  const { geometry } = await api<{ geometry: { coordinates: [number, number] } | null }>(`/v1/vessels/${mmsi}`);
  expect(geometry).not.toBeNull();
  // Every frame from the start, whether the camera was animating.
  await page.addInitScript(() => {
    const w = window as unknown as { aiscastMap?: { isMoving(): boolean }; animated?: boolean };
    const watch = () => {
      if (w.aiscastMap?.isMoving()) w.animated = true;
      requestAnimationFrame(watch);
    };
    requestAnimationFrame(watch);
  });

  await page.goto(`/ais${vesselPath(mmsi, name)}`);
  await waitForFlight(page);
  const at = await page.evaluate(() => window.aiscastMap!.getCenter().toArray());
  expect(at[0]).toBeCloseTo(geometry!.coordinates[0], 1);
  expect(at[1]).toBeCloseTo(geometry!.coordinates[1], 1);
  expect(await page.evaluate(() => (window as unknown as { animated?: boolean }).animated ?? false)).toBe(false);
});
