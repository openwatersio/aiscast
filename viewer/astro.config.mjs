import node from "@astrojs/node";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";

// Static by default, SSR opted into per route. Only the pages whose <head> has to name a
// specific vessel or station need a server render; the shell around them is the same file
// every time. `standalone` so the built server runs as its own systemd unit behind Caddy,
// which routes /v0, /v1, /health and /metrics to the Go server and everything else here.
export default defineConfig({
  site: process.env.SITE ?? "https://ais.openwaters.io",
  output: "static",
  // Astro's HTML compressor drops the whitespace between text and a following tag when a
  // newline separates them, so `station?\n<a>` renders as `station?hello@…`. HTML says that
  // newline collapses to a space; the compressor does not. Off, so prose reads as written
  // and nobody has to sprinkle `{" "}` through the templates. Caddy compresses anyway.
  compressHTML: false,
  adapter: node({ mode: "standalone" }),
  // Search lives in the sidebar, so there is no vessel index page to land on.
  redirects: { "/vessels": "/" },
  vite: {
    plugins: [tailwindcss()],
    // MapLibre's worker is an ES module and imports a shared chunk.
    worker: { format: "es" },
  },
});
