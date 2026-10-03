import { describe, expect, it } from "vitest";
import type { VesselFeature } from "./api";
import { previewPosition } from "./tracking-preview";

const boat = {
  name: "Example",
  model: "Cutter",
  chapter: "First boat",
  mmsi: 235093681,
};
const feature = (
  mmsi: number,
  coordinates: [number, number] | null,
): VesselFeature => ({
  type: "Feature",
  id: mmsi,
  geometry: coordinates ? { type: "Point", coordinates } : null,
  properties: {
    mmsi,
    kind: "vessel",
    seen: "2026-10-03T10:00:00Z",
    source: "aishub",
    station: "aishub",
    msg_type: "PositionReport",
  },
});

describe("directory map positions", () => {
  it("uses the selected boat's report, including coordinates on the equator", () => {
    expect(previewPosition(boat, feature(boat.mmsi, [0, 0]))).toEqual([0, 0]);
  });
  it("does not put another vessel or a previous boat's position on the preview", () => {
    expect(previewPosition(boat, feature(368478440, [5, 50]))).toBeUndefined();
    expect(
      previewPosition(
        { ...boat, mmsi: undefined },
        feature(boat.mmsi, [5, 50]),
      ),
    ).toBeUndefined();
  });
  it("leaves the map unpinned for unknown, positionless, or invalid reports", () => {
    expect(previewPosition(boat, undefined)).toBeUndefined();
    for (const coords of [null, [181, 50], [5, 91], [NaN, 50]] as Array<
      [number, number] | null
    >) {
      expect(previewPosition(boat, feature(boat.mmsi, coords))).toBeUndefined();
    }
  });
});
