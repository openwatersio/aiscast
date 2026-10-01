import { namedVessels } from "./data";

/**
 * A server that has just started knows only the positions Digitraffic has sent it since, and
 * names arrive with static data, every few minutes per vessel. The tests need named vessels
 * to open, so they wait here until there are some.
 */
export default async function globalSetup() {
  const deadline = Date.now() + 180_000;
  let found = 0;
  let failure: unknown;
  while (Date.now() < deadline) {
    try {
      found = (await namedVessels()).length;
      failure = undefined;
      if (found >= 5) return;
    } catch (e) {
      // The server can still be starting; anything else is the cause if this times out.
      failure = e;
    }
    await new Promise((r) => setTimeout(r, 2_000));
  }
  throw new Error(`Digitraffic named ${found} vessels in the test area within three minutes`, { cause: failure });
}
