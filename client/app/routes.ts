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
  route("stations", "routes/stations.tsx"),
  // Station ids contain slashes (`kystverket/2573010`).
  route("stations/*", "routes/station.tsx"),
  route("network", "routes/network.tsx"),
  route("explore", "routes/explore.tsx"),
  route("explore/youtube", "routes/explore-youtube.tsx"),
  route("explore/youtube/sailors", "routes/youtube-sailors.tsx"),
  route("explore/youtube/cruisers", "routes/youtube-cruisers.tsx"),
  route("*", "routes/not-found.tsx"),
] satisfies RouteConfig;
