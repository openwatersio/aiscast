import { featureFilter } from "@maplibre/maplibre-gl-style-spec";
import { describe, expect, it } from "vitest";
import { GEAR_FILTER, MOVING_FILTER, STILL_FILTER } from "./vesselFilters";

const moving = featureFilter(MOVING_FILTER as never, "layers[0].filter");
const still = featureFilter(STILL_FILTER as never, "layers[1].filter");
const gear = featureFilter(GEAR_FILTER as never, "layers[2].filter");
const marker = (properties: Record<string, unknown>) => {
  const feature = { type: 1, properties } as never;
  const zoom = { zoom: 8 } as never;
  return [
    moving.filter(zoom, feature) ? "arrow" : undefined,
    still.filter(zoom, feature) ? "dot" : undefined,
    gear.filter(zoom, feature) ? "square" : undefined,
  ].filter(Boolean);
};

describe("vessel markers", () => {
  it("draw a net buoy as its own square, though it reports the course of its drift", () => {
    expect(marker({ mmsi: 233510227, kind: "gear", hdg: 245.3 })).toEqual(["square"]);
    expect(marker({ mmsi: 233510227, kind: "gear" })).toEqual(["square"]);
  });

  it("draw a vessel with a heading or course as an arrow, and one without as a dot", () => {
    expect(marker({ mmsi: 257000001, kind: "vessel", hdg: 90 })).toEqual(["arrow"]);
    expect(marker({ mmsi: 257000002, kind: "vessel" })).toEqual(["dot"]);
    expect(marker({ mmsi: 992576072, kind: "aton" })).toEqual(["dot"]);
  });
});
