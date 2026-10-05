import { test, expect } from "./fixtures";
import { API } from "./data";

test("tech yachts separate sailing and motor boats and retain identity and ownership history on mobile", async ({
  page,
}, testInfo) => {
  await page.route(`${API}/v1/vessels/*`, (route) =>
    route.fulfill({
      status: 503,
      json: {},
      headers: { "access-control-allow-origin": "*" },
    }),
  );
  await page.goto("/ais/explore");
  await expect(
    page.getByRole("navigation", { name: "AIS", exact: true }),
  ).not.toContainText("YouTube");
  await page.getByRole("link", { name: /Tech billionaires/ }).click();
  await expect(page).toHaveURL(/\/ais\/explore\/tech-yachts$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Tech billionaires & their yachts",
  );
  await expect(page.locator("#sailing article")).toHaveCount(4);
  await expect(page.locator("#motor article")).toHaveCount(14);
  await expect(page.locator("#map, .sheet")).toHaveCount(0);
  await expect(page.locator("#koru")).toContainText("AIS reports unavailable");
  await expect(
    page
      .locator("#koru")
      .getByRole("link", { name: "Open Koru in the AIS viewer", exact: true }),
  ).toHaveAttribute("href", "/ais/vessels/319225400");
  await expect(
    page
      .locator("#dragonfly")
      .getByRole("link", {
        name: "Open Dragonfly in the AIS viewer",
        exact: true,
      }),
  ).toHaveAttribute("href", "/ais/vessels/319296900");
  await expect(
    page
      .locator("#whisper")
      .getByRole("link", {
        name: "Open Whisper in the AIS viewer",
        exact: true,
      }),
  ).toHaveAttribute("href", "/ais/vessels/538072792");
  for (const id of ["skat", "rising-sun", "senses", "octopus", "tatoosh"]) {
    await expect(page.locator(`#${id}`)).toContainText("Former owner");
  }
  await expect(page.locator("#venus")).toContainText("Commissioned by");
  await expect(page.locator("#ran-vii")).toContainText(
    "AIS identity not yet confirmed",
  );
  await expect(page.locator("#ran-vii")).toContainText(
    "electric auxiliary propulsion",
  );
  await expect(page.locator("#eos")).toContainText("93 m / 305 ft");
  await expect(
    page.getByRole("link", { name: "Ownership history", exact: true }),
  ).toHaveCount(18);
  await page.screenshot({
    path: testInfo.outputPath("tech-yachts-desktop.png"),
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("link", { name: "Motor · 14", exact: true }).click();
  await expect(
    page.getByRole("heading", { level: 2, name: "Motor", exact: true }),
  ).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath("tech-yachts-phone.png") });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});

test("the sailors directory has channel links and map previews without the live viewer shell", async ({
  page,
}) => {
  await page.route(`${API}/v1/vessels/*`, (route) =>
    route.fulfill({
      status: 404,
      json: {},
      headers: { "access-control-allow-origin": "*" },
    }),
  );
  await page.goto("/ais/explore/youtube/sailors");
  await expect(
    page.getByRole("heading", { level: 1, name: "YouTube sailors" }),
  ).toBeVisible();
  await expect(page.locator("article")).toHaveCount(14);
  await expect(
    page.getByRole("link", { name: "Explore", exact: true }),
  ).toHaveAttribute("href", "/ais/explore");
  await expect(page.getByRole("link", { name: / on YouTube$/ })).toHaveCount(
    14,
  );
  expect(
    await page
      .locator("article")
      .evaluateAll((cards) => cards.slice(0, 5).map((card) => card.id)),
  ).toEqual(["wynns", "tally-ho", "nbjs", "phoenix", "distant-shores"]);
  await expect(page.locator("#phoenix")).toContainText("Phoenix do Mar");
  await expect(page.locator("#alluring-arctic")).toContainText("Lumi");
  await expect(page.locator("#wind-hippie")).toContainText("Gecko");
  await expect(page.locator("#distant-shores")).toContainText(
    "Distant Shores IV",
  );
  await expect(page.locator("#map")).toHaveCount(0);
  await expect(page.locator(".sheet")).toHaveCount(0);
  await expect(page.locator("#wynns")).toContainText("Leopard 43");
  await expect(page.locator("#florence")).toContainText("Oyster Heritage 37");
  await expect(page.locator("#nbjs")).toContainText("Tessie");
  await expect(page.locator("#sam-holmes")).toContainText("Pickled Herring");
  await expect(page.locator("article").last()).toHaveAttribute(
    "id",
    "magic-carpet",
  );
  await expect(page.locator("#magic-carpet")).toContainText("Magic Carpet II");
  await expect(page.locator("#zatara, #doodles, #mj-sailing")).toHaveCount(0);
  await expect(page.locator("#delos")).toContainText(
    "AIS identity not yet confirmed",
  );
  await expect(
    page.locator("#la-vagabonde").getByRole("link", {
      name: "Open La Vagabonde III in the AIS viewer",
      exact: true,
    }),
  ).toHaveAttribute("href", "/ais/vessels/268233302");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator("#tally-ho").scrollIntoViewIfNeeded();
  await expect(
    page
      .locator("#tally-ho")
      .getByRole("heading", { name: "Sampson Boat Co." }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});

test("a preview reports its date and opens that boat, while an API failure leaves the directory usable", async ({
  page,
}) => {
  await page.route(`${API}/v1/vessels/*`, (route) => {
    const mmsi = Number(
      new URL(route.request().url()).pathname.split("/").pop(),
    );
    return mmsi === 268233302
      ? route.fulfill({
          headers: { "access-control-allow-origin": "*" },
          json: {
            type: "Feature",
            id: mmsi,
            geometry: { type: "Point", coordinates: [10, 59] },
            properties: {
              mmsi,
              name: "LA VAGABOND III",
              seen: "2026-10-01T12:00:00Z",
              source: "aishub",
            },
            attribution: { aishub: "Open Waters AIS · AISHub" },
          },
        })
      : route.fulfill({
          status: 503,
          json: {},
          headers: { "access-control-allow-origin": "*" },
        });
  });
  await page.goto("/ais/explore/youtube/sailors");
  const card = page.locator("#la-vagabonde");
  await card.scrollIntoViewIfNeeded();
  await expect(card.locator("time")).toHaveAttribute(
    "datetime",
    "2026-10-01T12:00:00Z",
  );
  await expect(card).toContainText("AISHub");
  await expect(card).toContainText("Last heard");
  await expect(
    card.getByRole("link", {
      name: "Open La Vagabonde III in the AIS viewer",
      exact: true,
    }),
  ).toHaveAttribute("href", "/ais/vessels/268233302-la-vagabond-iii");
  await page.locator("#uma").scrollIntoViewIfNeeded();
  await expect(page.locator("#uma")).toContainText("AIS reports unavailable");
  await card.scrollIntoViewIfNeeded();
  await card
    .getByRole("link", {
      name: "Open La Vagabonde III in the AIS viewer",
      exact: true,
    })
    .click();
  await expect(page).toHaveURL(/\/ais\/vessels\/268233302/);
  await expect(page.locator("#map")).toBeVisible();
});

test("the YouTube directory links sailors and cruisers, with usable cruiser cards on mobile", async ({
  page,
}) => {
  await page.route(`${API}/v1/vessels/*`, (route) =>
    route.fulfill({
      status: 404,
      json: {},
      headers: { "access-control-allow-origin": "*" },
    }),
  );
  await page.goto("/ais/explore");
  await page.getByRole("link", { name: /YouTube channels/ }).click();
  await expect(page).toHaveURL(/\/ais\/explore\/youtube$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "YouTube channels" }),
  ).toBeVisible();
  await expect(page.locator("article")).toHaveCount(2);
  expect(
    await page
      .locator("article")
      .evaluateAll((cards) => cards.map((card) => card.id)),
  ).toEqual(["wynns", "the-71-percent"]);
  await expect(
    page
      .locator("#wynns")
      .getByRole("link", { name: "Open Undra in the AIS viewer", exact: true }),
  ).toHaveAttribute("href", "/ais/vessels/368478440");
  await expect(
    page.locator("#the-71-percent").getByRole("link", {
      name: "Open Ruth Pearl II in the AIS viewer",
      exact: true,
    }),
  ).toHaveAttribute("href", "/ais/vessels/503190280");
  await expect(
    page.getByRole("link", { name: "Browse all sailors", exact: true }),
  ).toHaveAttribute("href", "/ais/explore/youtube/sailors");
  await page
    .getByRole("link", { name: "Browse all cruisers", exact: true })
    .click();
  await expect(page).toHaveURL(/\/ais\/explore\/youtube\/cruisers$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "YouTube cruisers" }),
  ).toBeVisible();
  await expect(page.locator("article")).toHaveCount(8);
  expect(
    await page
      .locator("article")
      .evaluateAll((cards) => cards.slice(0, 2).map((card) => card.id)),
  ).toEqual(["the-71-percent", "lady-liselot"]);
  await expect(page.getByRole("link", { name: / on YouTube$/ })).toHaveCount(8);
  for (const name of [
    "MV Freedom",
    "Argonaut II",
    "The 71 Percent",
    "Aboard Mermaid Monster",
    "Henk | Cruising MV Lady Liselot",
    "Tony Fleming",
    "Adventures of Motor Yacht OLOH",
    "Tula’s Endless Summer",
  ]) {
    await expect(
      page.getByRole("heading", { level: 2, name, exact: true }),
    ).toHaveCount(1);
  }
  await expect(page.locator("#tula")).toContainText("Sunset");
  await expect(page.locator("#tula")).toContainText("LaurieSue");
  await expect(page.locator("#the-71-percent")).toContainText("Selene 49");
  await expect(
    page.locator("#lady-liselot").getByRole("link", {
      name: "Open Lady Liselot in the AIS viewer",
      exact: true,
    }),
  ).toHaveAttribute("href", "/ais/vessels/244129609");
  await expect(page.locator("#map, .sheet")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator("#tula").scrollIntoViewIfNeeded();
  await expect(
    page.locator("#tula").getByRole("heading", { level: 2 }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.getByRole("link", { name: "Sailors", exact: true }).click();
  await expect(page).toHaveURL(/\/ais\/explore\/youtube\/sailors$/);
});
