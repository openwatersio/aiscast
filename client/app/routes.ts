import { index, route, type RouteConfig } from "@react-router/dev/routes";

// Paths are under the /ais/ basename. The Worker's routes in wrangler.jsonc list the same
// prefixes, so adding a top-level path here means adding it there too.
export default [
  index("routes/index.tsx"),
  route("vessels", "routes/home.tsx"),
  // Where the map was first published, kept as a permanent redirect for links to it.
  route("map", "routes/map.ts"),
  route("vessels/:param", "routes/vessel.tsx"),
  // JSON for the vessel page's photos, served by the Worker (lib/media.server.ts).
  route("vessels/media/:key", "routes/vessel-media.ts"),
  // JSON for the fleets a vessel is in, for the foot of its page (lib/fleets.server.ts).
  route("vessels/fleets/:mmsi", "routes/vessel-fleets.ts"),
  route("stations", "routes/stations.tsx"),
  // Station ids contain slashes (`kystverket/2573010`).
  route("stations/*", "routes/station.tsx"),
  route("network", "routes/network.tsx"),
  // A fleet or a group of fleets, by its path under app/fleets/; `/fleets` itself is the top group.
  route("fleets/*", "routes/fleet.tsx"),
  // Where fleets were first published.
  route("explore/*", "routes/explore.ts"),
  route("*", "routes/not-found.tsx"),
] satisfies RouteConfig;
