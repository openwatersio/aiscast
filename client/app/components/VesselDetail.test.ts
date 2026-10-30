import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { VesselFeature } from "../lib/api";
import { meta } from "../routes/vessel";
import { VesselDetail } from "./VesselDetail";

const ferry: VesselFeature = {
  type: "Feature",
  id: 367720690,
  geometry: null,
  properties: {
    mmsi: 367720690,
    name: "GREY LADY IV",
    kind: "vessel",
    type: 40,
    seen: "2026-10-29T12:00:00Z",
    source: "aishub",
    station: "aishub",
    msg_type: "ShipStaticData",
  },
};

describe("ferry vessel pages", () => {
  it("renders the ferry identity and official service link in the initial HTML", () => {
    const html = renderToStaticMarkup(createElement(MemoryRouter, null,
      createElement(VesselDetail, { mmsi: ferry.properties.mmsi, feature: ferry }),
    ));
    expect(html).toContain("Ferry · type 40");
    expect(html).toContain('href="https://www.hylinecruises.com/nantucket-ferry/"');
    expect(html.replace(/<[^>]*>/g, "")).toContain("Hy-Line Cruises ferry serving Hyannis–Nantucket.");
  });

  it("passes enrichment through to search and sharing metadata", () => {
    const feature: VesselFeature = {
      ...ferry,
      id: 230000001,
      properties: { ...ferry.properties, mmsi: 230000001, name: "TEST SHIP", particulars: { ship_type: "car ferry", operator: "Example Ferries" } },
    };
    const descriptors = meta({ loaderData: { mmsi: 230000001, name: "TEST SHIP", feature } } as Parameters<typeof meta>[0]);
    expect(descriptors).toContainEqual({ title: "TEST SHIP ferry tracker | Open Waters AIS" });
    expect(descriptors).toContainEqual({ property: "og:title", content: "TEST SHIP ferry tracker | Open Waters AIS" });
    expect(descriptors).toContainEqual({ name: "description", content: expect.stringContaining("Example Ferries ferry") });
    expect(descriptors).toContainEqual({ tagName: "link", rel: "canonical", href: "https://openwaters.io/ais/vessels/230000001-test-ship" });
  });

  it("keeps the generic title for a passenger ship without a ferry identity", () => {
    const feature: VesselFeature = {
      ...ferry,
      id: 230000001,
      properties: { ...ferry.properties, mmsi: 230000001, name: "TEST SHIP", type: 60 },
    };
    const descriptors = meta({ loaderData: { mmsi: 230000001, name: "TEST SHIP", feature } } as Parameters<typeof meta>[0]);
    expect(descriptors).toContainEqual({ title: "TEST SHIP, passenger ship: live AIS position | Open Waters AIS" });
    expect(descriptors).toContainEqual({ property: "og:title", content: "TEST SHIP, passenger ship: live AIS position | Open Waters AIS" });
  });
});
