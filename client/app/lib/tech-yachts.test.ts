import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createMemoryRouter, RouterProvider } from "react-router";
import { expect, it } from "vitest";
import Explore from "../routes/explore";

it("offers tech yachts alongside YouTube without a YouTube header shortcut", () => {
  const router = createMemoryRouter([
    { id: "root", path: "/", Component: Explore },
  ]);
  const html = renderToStaticMarkup(createElement(RouterProvider, { router }));
  expect(html).toContain('href="/ais/explore/tech-yachts"');
  expect(html).toContain("Tech billionaires &amp; their yachts");
  expect(html).toContain("YouTube channels");
  expect(html).not.toContain("YouTube directory");
});
