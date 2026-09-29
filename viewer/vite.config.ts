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
});
