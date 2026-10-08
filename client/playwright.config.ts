import { defineConfig, devices } from "@playwright/test";
import { e2eAuth } from "./e2e/auth";
import { e2ePorts, pickE2ePorts } from "./e2e/ports";

const CI = Boolean(process.env.CI);
await pickE2ePorts();
const ports = e2ePorts();
const APP = `http://127.0.0.1:${ports.app}`;
const API = `http://127.0.0.1:${ports.api}`;
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
      env: { ISSUER_PUBKEYS: issuer, ADDR: `127.0.0.1:${ports.api}`, UDP_ADDR: `127.0.0.1:${ports.udp}` },
      timeout: 180_000,
      stdout: "ignore",
    },
    {
      // Built for wrangler.jsonc's e2e environment, with the map on window.aiscastMap. Renders
      // get this run's server and token from .dev.vars.e2e, which wrangler reads ahead of a
      // developer's own .dev.vars and copies into the build. It ignores the process environment
      // whenever either file exists. The file goes once the build has its copy, so an older
      // commit's e2e build never picks up a dead port.
      command: `trap 'rm -f .dev.vars.e2e' INT TERM; printf 'AIS_API=%s\\nAIS_TOKEN=%s\\n' "$AIS_API" "$AIS_TOKEN" > .dev.vars.e2e && CLOUDFLARE_ENV=e2e VITE_E2E=1 npx react-router build; s=$?; rm -f .dev.vars.e2e; [ $s = 0 ] && npx vite preview --host 127.0.0.1 --port ${ports.app} --strictPort`,
      url: `${APP}/ais/vessels`,
      env: { AIS_API: API, AIS_TOKEN: token },
      timeout: 120_000,
    },
  ],
});
