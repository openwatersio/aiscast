import type { VesselFeature } from "./api";
import type { BoatVersion } from "./explore";

export function previewPosition(
  boat: BoatVersion,
  feature?: VesselFeature,
): [number, number] | undefined {
  if (!boat.mmsi || feature?.properties.mmsi !== boat.mmsi || !feature.geometry)
    return undefined;
  const [lon, lat] = feature.geometry.coordinates;
  if (
    !Number.isFinite(lon) ||
    !Number.isFinite(lat) ||
    Math.abs(lon) > 180 ||
    Math.abs(lat) > 90
  )
    return undefined;
  return [lon, lat];
}
