// Pages outside this app that it points readers to. Absolute, so a dev server or a preview
// deploy, which serve only this app, still reach them.

const WEBSITE = "https://openwaters.io";

/** The landing page, which explains the network to someone who arrived on a vessel. */
export const ABOUT = `${WEBSITE}/ais/`;
/** Where a builder starts: streaming and snapshot examples, and the API reference. */
export const DEVELOPERS = `${WEBSITE}/api/ais/`;
/** How to connect a receiver. A section of the landing page until the website gives it a page. */
export const CONTRIBUTE = `${WEBSITE}/ais/#contribute`;
/** What the map and the API collect about visitors, stations, and vessels. */
export const PRIVACY = `${WEBSITE}/ais/privacy/`;
export const SIGNALK_PLUGIN = "https://github.com/openwatersio/aiscast/tree/main/signalk-plugin#readme";
export const GITHUB = "https://github.com/openwatersio/aiscast";

/** Shown wherever a page has no picture of its own, so a shared link still unfurls as ours. */
export const DEFAULT_SHARE_IMAGE = `${WEBSITE}/og/ais.png`;

/**
 * The contributor ask, one line wherever it appears, so it reads as one invitation rather
 * than a dozen variations.
 */
export const CONTRIBUTE_PROMPT = "Put your receiver on the map";
