import { index, route, type RouteConfig } from "@react-router/dev/routes";

// Paths are under the /ais/ basename. The Worker's routes in wrangler.jsonc list the same
// prefixes, so adding a top-level path here means adding it there too.
export default [
  index("routes/index.tsx"),
  route("map", "routes/home.tsx"),
  route("vessels/:param", "routes/vessel.tsx"),
  route("stations", "routes/stations.tsx"),
  // Station ids contain slashes (`kystverket/2573010`).
  route("stations/*", "routes/station.tsx"),
  route("network", "routes/network.tsx"),
  route("*", "routes/not-found.tsx"),
] satisfies RouteConfig;
