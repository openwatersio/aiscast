import { execFileSync } from "node:child_process";

// Binds two TCP ports and a UDP port on 127.0.0.1, lets the system pick each, and prints them.
const FREE_PORTS = `
const net = require("node:net"), dgram = require("node:dgram");
const tcp = () => new Promise((ok) => { const s = net.createServer().listen(0, "127.0.0.1", () => ok(s)); });
const udp = () => new Promise((ok) => { const s = dgram.createSocket("udp4"); s.bind(0, "127.0.0.1", () => ok(s)); });
Promise.all([tcp(), udp(), tcp()]).then((socks) => {
  console.log(socks.map((s) => s.address().port).join(" "));
  socks.forEach((s) => s.close());
});`;

/**
 * The ports this run's server, volunteer receiver, and app listen on. Free ones are picked once
 * in the runner, so worktrees running the tests side by side never share a server; the config
 * runs again in each worker, and the workers inherit the runner's environment. Setting
 * AISCAST_E2E_API_PORT, AISCAST_E2E_UDP_PORT, and AISCAST_E2E_APP_PORT pins them.
 */
export function e2ePorts(): { api: number; udp: number; app: number } {
  if (!process.env.AISCAST_E2E_API_PORT || !process.env.AISCAST_E2E_UDP_PORT || !process.env.AISCAST_E2E_APP_PORT) {
    // The config is synchronous, so the lookup runs in a child process. A port can be taken
    // before the server or app binds it, after its build. A taken TCP port fails the run at
    // startup; a taken UDP port leaves the server up, and only the volunteer tests fail.
    const [api, udp, app] = execFileSync(process.execPath, ["-e", FREE_PORTS], { encoding: "utf8" }).trim().split(" ");
    process.env.AISCAST_E2E_API_PORT ||= api;
    process.env.AISCAST_E2E_UDP_PORT ||= udp;
    process.env.AISCAST_E2E_APP_PORT ||= app;
  }
  return {
    api: Number(process.env.AISCAST_E2E_API_PORT),
    udp: Number(process.env.AISCAST_E2E_UDP_PORT),
    app: Number(process.env.AISCAST_E2E_APP_PORT),
  };
}

