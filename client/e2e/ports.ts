import { createSocket } from "node:dgram";
import { createServer } from "node:net";

const VARS = ["AISCAST_E2E_API_PORT", "AISCAST_E2E_UDP_PORT", "AISCAST_E2E_APP_PORT"] as const;

/**
 * Picks the ports this run's server, volunteer receiver, and app listen on, once, in the runner,
 * so worktrees running the tests side by side never share a server. The workers inherit the
 * runner's environment and read them with e2ePorts. Setting all of AISCAST_E2E_API_PORT,
 * AISCAST_E2E_UDP_PORT, and AISCAST_E2E_APP_PORT pins them.
 */
export async function pickE2ePorts(): Promise<void> {
  const pinned = VARS.filter((v) => process.env[v]).length;
  // The lookup cannot avoid a pinned port it does not hold, so it could hand one out again.
  if (pinned > 0 && pinned < VARS.length) throw new Error(`set all of ${VARS.join(", ")} or none`);
  if (pinned) return;
  // All three are held at once, so they differ. A port can still be taken before the server or
  // app binds it, after its build. A taken TCP port fails the run at startup; a taken UDP port
  // leaves the server up, and only the volunteer tests fail.
  const tcp = () =>
    new Promise<{ port: number; close(): void }>((ok) => {
      const s = createServer().listen(0, "127.0.0.1", () => ok({ port: (s.address() as { port: number }).port, close: () => s.close() }));
    });
  const udp = () =>
    new Promise<{ port: number; close(): void }>((ok) => {
      const s = createSocket("udp4").bind(0, "127.0.0.1", () => ok({ port: s.address().port, close: () => s.close() }));
    });
  const socks = await Promise.all([tcp(), udp(), tcp()]);
  socks.forEach((s, i) => {
    process.env[VARS[i]!] = String(s.port);
    s.close();
  });
}

/** The ports pickE2ePorts set in the runner. */
export function e2ePorts(): { api: number; udp: number; app: number } {
  const [api, udp, app] = VARS.map((v) => Number(process.env[v]));
  if (!api || !udp || !app) throw new Error("e2e ports unset: playwright.config.ts picks them");
  return { api, udp, app };
}
