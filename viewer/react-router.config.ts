import type { Config } from "@react-router/dev/config";

export default {
  ssr: true,
  // The app shares openwaters.io with the website, which owns everything else under /ais/.
  basename: "/ais/",
} satisfies Config;
