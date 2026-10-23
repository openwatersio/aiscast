import { test as base, expect, type BrowserContext, type Page } from "@playwright/test";
import { e2eAuth, oneStreamToken } from "./auth";
import { API, MAP_HASH } from "./data";

// The basemap is OpenFreeMap's, and nothing these tests check depends on it. A blank style in
// its place keeps them off a third-party server and spares CI's software renderer its tiles.
// The glyph URL stays because the vessel labels are invalid without one; its requests get
// empty glyph ranges.
const BLANK_STYLE = {
  version: 8,
  glyphs: "https://tiles.openfreemap.org/fonts/{fontstack}/{range}.pbf",
  sources: {},
  layers: [{ id: "background", type: "background", paint: { "background-color": "#1b2838" } }],
};

export const test = base.extend({
  context: async ({ context }, use) => {
    await context.route("https://tiles.openfreemap.org/**", (route) =>
      new URL(route.request().url()).pathname.startsWith("/styles/")
        ? route.fulfill({ json: BLANK_STYLE })
        : route.fulfill({ body: "", contentType: "application/x-protobuf" }),
    );
    // The app asks for vessel tiles without the token, so they count against the address's
    // tile limit, and every test shares 127.0.0.1. With the token each test is its own visitor.
    await context.route(`${API}/v1/vessels/tiles/**`, (route) =>
      route.continue({ headers: { ...route.request().headers(), authorization: `Bearer ${e2eAuth().token}` } }),
    );
    await use(context);
  },
});

export { expect };

/** The map and the stream, as dev and e2e builds expose them. */
declare global {
  interface Window {
    aiscastMap?: import("maplibre-gl").Map;
    aiscastStream?: import("../app/lib/stream").Stream;
  }
}

/** Opens `path` with the map over the test area, once the stream and the tiles are drawing. */
export async function openMap(page: Page, path = "/ais/vessels") {
  await page.goto(path + MAP_HASH);
  await waitForVessels(page);
}

export async function waitForVessels(page: Page) {
  await page.waitForFunction(
    () => {
      const map = window.aiscastMap;
      return Boolean(map?.getSource("tiles") && map.querySourceFeatures("vessels").length > 0);
    },
    undefined,
    { timeout: 30_000 },
  );
}

/**
 * Waits for the camera to arrive at a vessel just opened. The flight can start a moment after
 * the page opens, and it ends at zoom 12 or closer.
 */
export async function waitForFlight(page: Page) {
  await expect
    // Straight after a page load the map may not be built yet.
    .poll(
      () =>
        page.evaluate(() => {
          const map = window.aiscastMap;
          return Boolean(map && !map.isMoving() && map.getZoom() >= 12);
        }),
      { timeout: 15_000 },
    )
    .toBe(true);
}

/**
 * A vessel drawn on the map that a click will land on: nothing over it but the map, and no
 * other vessel near enough to take the click instead. One that will stay where it is drawn
 * comes first.
 */
export async function vesselOnMap(page: Page, exclude: number[] = []) {
  const deadline = Date.now() + 15_000;
  let last: Target | undefined;
  for (;;) {
    // The same vessel in the same place twice running. After the camera moves, the tiles for
    // the new view load and the stream subscribes to it, and vessels they add can land next
    // to one that was clear a moment ago.
    const { target, seen } = await findVessel(page, exclude);
    if (target && last && target.mmsi === last.mmsi && Math.hypot(target.x - last.x, target.y - last.y) < 2) return target;
    if (Date.now() > deadline) throw new Error(`no vessel on the map clear enough to click: ${JSON.stringify(seen)}`);
    last = target;
    await page.waitForTimeout(500);
  }
}

interface Target {
  mmsi: number;
  x: number;
  y: number;
}

function findVessel(page: Page, exclude: number[]) {
  return page.evaluate((exclude) => {
    const map = window.aiscastMap!;
    const seen = { moving: map.isMoving(), zoom: map.getZoom(), drawn: 0, isolated: 0 };
    if (seen.moving) return { seen };
    const canvas = map.getCanvas();
    const box = canvas.getBoundingClientRect();
    const drawn = map
      .queryRenderedFeatures({ layers: ["vessel-still", "vessel-moving", "vessel-gear", "tile-still", "tile-moving", "tile-gear"] })
      // A tile's copy of a vessel the stream is drawing is there but hidden.
      .filter((f) => f.source === "vessels" || !f.state.live)
      .map((f) => ({
        mmsi: Number(f.properties.mmsi),
        at: map.project((f.geometry as GeoJSON.Point).coordinates as [number, number]),
        // The stream moves a vessel it draws a little with each report. A tile's copy is where
        // the vessel was when the tile was built, which for one under way can be far behind,
        // and once the stream hears the vessel the copy is hidden and the click finds nothing.
        // A stationary vessel's copy is where the stream will draw it.
        steady:
          f.source === "vessels" || Number(f.properties.sog ?? Infinity) < 0.5 || [1, 5].includes(Number(f.properties.nav_status)),
      }))
      // Steady ones first. The order features are drawn in changes as the stream redraws, and
      // samples are compared.
      .sort((a, b) => Number(b.steady) - Number(a.steady) || a.mmsi - b.mmsi);
    seen.drawn = drawn.length;
    const clear = (x: number, y: number) =>
      [[0, 0], [-12, 0], [12, 0], [0, -12], [0, 12]].every(([dx, dy]) => document.elementFromPoint(x + dx!, y + dy!) === canvas);
    for (const { mmsi, at } of drawn) {
      if (exclude.includes(mmsi)) continue;
      if (drawn.some((o) => o.mmsi !== mmsi && Math.hypot(o.at.x - at.x, o.at.y - at.y) < 30)) continue;
      seen.isolated++;
      const x = box.left + at.x;
      const y = box.top + at.y;
      if (clear(x, y)) return { target: { mmsi, x, y }, seen };
    }
    return { seen };
  }, exclude);
}

/**
 * Clicks a vessel on the map and waits for its page. A vessel can still leave the spot between
 * finding it and the click, which then opens nothing, so another is tried. `beforeClick` runs
 * just before each click, with the vessel about to be clicked.
 */
export async function openVesselOnMap(page: Page, exclude: number[] = [], beforeClick?: (mmsi: number) => Promise<unknown>) {
  const from = new URL(page.url()).pathname;
  const missed: number[] = [];
  for (let attempt = 1; ; attempt++) {
    const { mmsi, x, y } = await vesselOnMap(page, [...exclude, ...missed]);
    // A click taken for a miss can still open its vessel, only slowly.
    const now = new URL(page.url()).pathname;
    const late = now !== from && /\/ais\/vessels\/(\d+)/.exec(now);
    if (late) return Number(late[1]);
    await beforeClick?.(mmsi);
    await page.mouse.click(x, y);
    const left = await expect
      .poll(() => new URL(page.url()).pathname, { timeout: 5_000 })
      .not.toBe(from)
      .then(
        () => true,
        () => false,
      );
    if (left || attempt === 3) {
      await expect(page).toHaveURL(new RegExp(`/ais/vessels/${mmsi}(-|#|$)`));
      return mmsi;
    }
    missed.push(mmsi);
  }
}

/** The chip over the map that says whether it is live. Search has a status of its own. */
export function mapStatus(page: Page) {
  return page.locator(".status-dock");
}

/** Has every page in this context use a token of its own that may hold only one stream. */
export async function useOneStreamToken(context: BrowserContext) {
  await context.addInitScript((token) => {
    // about:blank has no storage of its own.
    try {
      localStorage.setItem("aiscast.token", JSON.stringify({ token }));
    } catch {}
  }, oneStreamToken());
}
