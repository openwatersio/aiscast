/**
 * Which marker the map draws a station with, as MapLibre filter expressions over a tile's or the
 * stream's feature properties. A vessel with a heading or course (`hdg`) is an arrow pointed along
 * it, and one without is a dot. Gear is its own square, upright whatever it reports: a net buoy is
 * not a vessel, and its course is the drift of the net it marks. The square borrows neither a
 * vessel's triangle nor an AIS aid's diamond, the shapes ECDIS gives those.
 */
const GEAR = ["==", ["get", "kind"], "gear"];
export const GEAR_FILTER = GEAR;
export const MOVING_FILTER = ["all", ["has", "hdg"], ["!", GEAR]];
export const STILL_FILTER = ["all", ["!", ["has", "hdg"]], ["!", GEAR]];
