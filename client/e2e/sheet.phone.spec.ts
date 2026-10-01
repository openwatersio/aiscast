import type { Page } from "@playwright/test";
import { MAP_HASH } from "./data";
import { expect, test } from "./fixtures";

type Detent = "peek" | "half" | "full";

const panel = (page: Page) => page.getByRole("region", { name: "Panel" });
const handle = (page: Page) => page.getByRole("button", { name: /^Panel height:/ });

/** Where the sheet's top rests, once its transition has finished. */
async function restingTop(page: Page): Promise<number> {
  let last = NaN;
  let top = NaN;
  await expect
    .poll(async () => {
      last = top;
      top = (await panel(page).boundingBox())!.y;
      return Math.abs(top - last) < 0.5;
    })
    .toBe(true);
  return top;
}

/** The sheet is at `detent`: its handle says so, and its top is where that height puts it. */
async function expectDetent(page: Page, detent: Detent) {
  await expect(handle(page)).toHaveAccessibleName(new RegExp(`^Panel height: ${detent}\\.`));
  const height = page.viewportSize()!.height;
  const top = await restingTop(page);
  if (detent === "peek") expect(top).toBeGreaterThan(height - 100);
  if (detent === "half") expect(Math.abs(top - height / 2)).toBeLessThan(10);
  if (detent === "full") expect(top).toBeLessThan(100);
}

/**
 * A one-finger drag, through the browser's own touch input. Each move is stamped `stepMs`
 * after the last, which is what the sheet reads a drag's speed from, so a drag is as fast or
 * slow as asked however busy the machine running it is.
 */
async function drag(page: Page, from: { x: number; y: number }, toY: number, { steps = 20, stepMs = 30 } = {}) {
  const cdp = await page.context().newCDPSession(page);
  const start = Date.now();
  const touch = (type: "touchStart" | "touchMove" | "touchEnd", step: number, y?: number) =>
    cdp.send("Input.dispatchTouchEvent", {
      type,
      touchPoints: y == null ? [] : [{ x: from.x, y }],
      timestamp: (start + step * stepMs) / 1000,
    });
  await touch("touchStart", 0, from.y);
  for (let i = 1; i <= steps; i++) await touch("touchMove", i, from.y + ((toY - from.y) * i) / steps);
  await touch("touchEnd", steps);
  await cdp.detach();
  // So the next gesture's stamps come after this one's.
  await page.waitForTimeout(Math.max(0, start + steps * stepMs - Date.now()));
}

/** Where to put a finger on the sheet: its handle. */
async function grip(page: Page) {
  const box = (await handle(page).boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

test.beforeEach(async ({ page }) => {
  await page.goto(`/ais/vessels${MAP_HASH}`);
  await expectDetent(page, "peek");
});

test("tapping the handle steps up through the heights and wraps to the lowest", async ({ page }) => {
  for (const next of ["half", "full", "peek"] as const) {
    await handle(page).tap();
    await expectDetent(page, next);
  }
});

test("a drag leaves the sheet at the nearest height", async ({ page }) => {
  const height = page.viewportSize()!.height;
  await drag(page, await grip(page), height / 2 + 20);
  await expectDetent(page, "half");
  await drag(page, await grip(page), 40);
  await expectDetent(page, "full");
  await drag(page, await grip(page), height - 20);
  await expectDetent(page, "peek");
});

test("a flick raises the sheet where a slow drag as short would not", async ({ page }) => {
  const from = await grip(page);
  await drag(page, from, from.y - 80, { steps: 20, stepMs: 40 });
  await expectDetent(page, "peek");

  await drag(page, from, from.y - 80, { steps: 4, stepMs: 10 });
  await expect(handle(page)).not.toHaveAccessibleName(/^Panel height: peek\./);
});

test("a page opens the sheet halfway, and moving the map lowers it", async ({ page }) => {
  await handle(page).tap();
  await handle(page).tap();
  await expectDetent(page, "full");
  await page.getByRole("navigation", { name: "Browse" }).getByRole("link", { name: /^Stations/ }).tap();
  await expect(page).toHaveURL(/\/ais\/stations/);
  await expectDetent(page, "half");

  const height = page.viewportSize()!.height;
  await drag(page, { x: 200, y: height / 4 }, height / 4 + 80);
  await expectDetent(page, "peek");
});
