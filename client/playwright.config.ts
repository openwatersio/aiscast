import { defineConfig, devices } from "@playwright/test";
import { e2eAuth } from "./e2e/auth";

const CI = Boolean(process.env.CI);
const APP = "http://127.0.0.1:4173";
// wrangler.jsonc's e2e environment renders against this.
const API = "http://127.0.0.1:8787";
const { token, issuer } = e2eAuth();

export default defineConfig({
  testDir: "e2e",
  // The map renders in software, which is slow on a CI runner.
  timeout: 60_000,
  expect: { timeout: 10_000 },
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  forbidOnly: CI,
  reporter: CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: APP,
    trace: "retain-on-failure",
    // The token the website's token page would have stored, which the app sends with every
    // request and puts on the stream's URL.
    storageState: {
      cookies: [],
      origins: [{ origin: APP, localStorage: [{ name: "aiscast.token", value: JSON.stringify({ token }) }] }],
    },
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] }, testIgnore: /phone/ },
    // Chromium rather than the device's WebKit: it takes touch input through CDP, which the
    // sheet's drag and flick tests need, and CI installs one browser.
    { name: "phone", use: { ...devices["iPhone 15"], defaultBrowserType: "chromium" }, testMatch: /phone/ },
  ],
  // Neither is reused: a running server would not know this run's issuer, nor a running app
  // its token.
  webServer: [
    {
      command: "sh e2e/server.sh",
      url: `${API}/health`,
      env: { ISSUER_PUBKEYS: issuer },
      timeout: 180_000,
      stdout: "ignore",
    },
    {
      // Built for wrangler.jsonc's e2e environment, with the map on window.aiscastMap. The
      // preview passes its own environment to the Worker, which is how renders get the token.
      command: `CLOUDFLARE_ENV=e2e VITE_E2E=1 npx react-router build && CLOUDFLARE_INCLUDE_PROCESS_ENV=true npx vite preview --host 127.0.0.1 --port 4173 --strictPort`,
      url: `${APP}/ais/vessels`,
      env: { AIS_TOKEN: token },
      timeout: 120_000,
    },
  ],
});
