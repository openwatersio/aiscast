import { namedVessels } from "./data";

/**
 * A server that has just started knows only the positions Digitraffic has sent it since, and
 * names arrive with static data, every few minutes per vessel. The tests need named vessels
 * to open, so they wait here until there are some.
 */
export default async function globalSetup() {
  const deadline = Date.now() + 180_000;
  let found = 0;
  while (Date.now() < deadline) {
    found = (await namedVessels().catch(() => [])).length;
    if (found >= 5) return;
    await new Promise((r) => setTimeout(r, 2_000));
  }
  throw new Error(`Digitraffic named ${found} vessels in the test area within three minutes`);
}
