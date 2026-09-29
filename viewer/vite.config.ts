import { cloudflare } from "@cloudflare/vite-plugin";
import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  // Written under ais/ as well as requested from there. The assets binding serves a file at
  // its path in the build, so `base: "/ais/"` alone would link /ais/assets/ and write /assets/.
  build: { assetsDir: "ais/assets" },
  plugins: [cloudflare({ viteEnvironment: { name: "ssr" } }), tailwindcss(), reactRouter()],
  // MapLibre's worker is an ES module and imports a shared chunk.
  worker: { format: "es" },
});
