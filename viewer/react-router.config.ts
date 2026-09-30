import type { Config } from "@react-router/dev/config";

export default {
  ssr: true,
  // The app shares openwaters.io with the website, which owns everything else under /ais/.
  basename: "/ais/",
  // The whole route manifest ships with the first page. Discovering routes lazily would ask
  // /ais/__manifest, a path the Worker does not claim, and the website would answer 404.
  routeDiscovery: { mode: "initial" },
} satisfies Config;
