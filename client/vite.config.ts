import { cloudflare } from "@cloudflare/vite-plugin";
import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

// Builds and the dev server share Vite's cache directory by default, so a build run while the
// dev server is up re-bundles its dependencies under new hashes, and the open page's requests
// for the old ones fail with 504 "Outdated Optimize Dep". The dev server keeps its own.
const cacheDir = process.argv.includes("dev") ? "node_modules/.vite-dev" : undefined;

export default defineConfig({
  cacheDir,
  // Written under ais/ as well as requested from there. The assets binding serves a file at
  // its path in the build, so `base: "/ais/"` alone would link /ais/assets/ and write /assets/.
  build: { assetsDir: "ais/assets" },
  plugins: [cloudflare({ viteEnvironment: { name: "ssr" } }), tailwindcss(), reactRouter()],
  // MapLibre's worker is an ES module and imports a shared chunk.
  worker: { format: "es" },
  // Bundled when the dev server starts. Vite's scan misses these, because the app's client
  // entry is generated, so it found them on the first page load, re-bundled, and invalidated
  // that page's copies, which failed with 504 "Outdated Optimize Dep".
  optimizeDeps: {
    include: [
      "react",
      "react-dom",
      "react-dom/client",
      "react-router",
      "react-router/dom",
      "maplibre-gl",
      "@base-ui/react/menu",
      "lucide-react",
      "clsx",
      "tailwind-merge",
    ],
  },
});
