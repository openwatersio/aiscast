import { defineConfig } from "vitest/config";

// The unit tests are pure functions. vite.config.ts would start the Cloudflare and React
// Router plugins, which run the app, not tests.
export default defineConfig({ test: { include: ["app/**/*.test.ts"] } });
