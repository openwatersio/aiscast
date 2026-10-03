import type { Page, WebSocket } from "@playwright/test";
import { expect, mapStatus, openMap, test, useOneStreamToken } from "./fixtures";

/** Messages the stream passed this page in its last whole second. */
const perSecond = (page: Page) => page.evaluate(() => window.aiscastStream!.eventsPerSec);

// The socket is in a SharedWorker, out of Playwright's sight. With a token allowed one stream,
// a second socket would be refused, and its tab would say "Live in another tab".
test("tabs share one stream", async ({ context, page }) => {
  await useOneStreamToken(context);
  await openMap(page);
  await expect(mapStatus(page)).toHaveText("Live");

  const second = await context.newPage();
  await openMap(second);
  await expect(mapStatus(second)).toHaveText("Live");
  await expect(mapStatus(page)).toHaveText("Live");

  // Closing the first tab leaves the stream to the second, which keeps hearing vessels. The
  // count covers the second before it is read, so only a reading two seconds on is all after.
  await page.close();
  const closed = Date.now();
  await expect
    .poll(async () => Date.now() - closed > 2_000 && (await perSecond(second)) > 0, { timeout: 15_000 })
    .toBe(true);
  await expect(mapStatus(second)).toHaveText("Live");

  // A third tab joins it rather than opening another.
  const third = await context.newPage();
  await openMap(third);
  await expect(mapStatus(third)).toHaveText("Live");
});

test("a browser without SharedWorker streams from the tab", async ({ page }) => {
  await page.addInitScript(() => delete (window as { SharedWorker?: unknown }).SharedWorker);
  const sockets: WebSocket[] = [];
  page.on("websocket", (ws) => sockets.push(ws));
  await openMap(page);
  await expect(mapStatus(page)).toHaveText("Live");
  const streams = sockets.filter((ws) => new URL(ws.url()).pathname === "/v1/stream");
  expect(streams).toHaveLength(1);
  expect(streams[0]!.isClosed()).toBe(false);
});
