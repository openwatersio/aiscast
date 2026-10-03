// Error reports from the browser, sent to the Worker at /ais/errors and logged there with
// report.server.ts. A report carries what went wrong and where in the app, and nothing that
// identifies the reader: no query strings or fragments, no tokens, no map position.

export const REPORT_PATH = "/ais/errors";

export const REPORT_KINDS = ["error", "rejection", "boundary", "map", "webgl", "stream"] as const;
export type ReportKind = (typeof REPORT_KINDS)[number];

/** What the browser sends. */
export interface ClientReport {
  kind: ReportKind;
  name?: string;
  message: string;
  stack?: string;
  /** The app section, such as `/ais/vessels/*`, never the address itself. */
  path: string;
}

export const MAX_NAME = 100;
export const MAX_MESSAGE = 500;
export const MAX_STACK = 4000;

// The share of page loads that report at all. A fault in one browser shows on every visit
// from it, so a sample still finds it, and a sudden spike costs half the log volume.
const SAMPLE_RATE = 0.5;
// Per page load. A loop that throws on every frame says the same thing after the first.
const MAX_PER_PAGE = 5;

/**
 * Strips what could identify a reader from free text: query strings and fragments, which
 * carry `?key=` tokens, `around=` positions and `#map=` views; bare tokens; email addresses;
 * tile coordinates; and coordinates with three or more decimals. Every quantifier that can
 * restart at each character is bounded, because the Worker runs this on whatever is posted.
 */
export function scrub(text: string): string {
  const out = text
    // The whole URL, then the query cut from it: a pattern that has to find `?` after the
    // scheme backtracks through every later `://` when there is none.
    .replace(/\b[a-z][a-z0-9+.-]{0,15}:\/\/[^\s'"()<>]*/gi, (url) => url.replace(/[?#].*/, ""))
    // The same for an address with no scheme, such as `/v1/vessels?around=…` or `#map=…`. The
    // name cannot hold `?` or `#`, so each scan ends where the next could start: linear.
    .replace(/[?#][^\s'"<>=&?#]+[=&][^\s'"<>]*/g, "")
    // An aiscast token is `ak1.<claims>.<signature>`; a JWT starts with `eyJ`, base64 for `{"`.
    // No word boundary before either, so one after `%3D` or `_` is caught too.
    .replace(/ak1\.[\w-]+(?:\.[\w-]+)?/g, "[token]")
    .replace(/eyJ[\w-]+\.[\w-]+(?:\.[\w-]+)?/g, "[token]")
    .replace(/\b(bearer|basic)\s+[\w.~+/=-]+/gi, "$1 [token]")
    // Anchored on the `=`, with the name looked for behind it, so most positions cost nothing.
    .replace(/(=|%3D)(?<=(?:key|token)[\w-]{0,32}(?:=|%3D))[^\s&'"]+/gi, "$1[token]")
    // z/x/y in a tile URL places the map, which opens where the reader is. Before the email
    // rule, which would take `1234@2x.png` and leave z and x.
    .replace(/\/\d+\/\d+\/\d+(?=[./@]|$)/g, "/{z}/{x}/{y}")
    .replace(/(?<![\w.])-?\d{1,3}\.\d{3,}(?![\w.])/g, "[coord]");
  // Tried at every position, the email pattern is the costliest, and most text has no `@`.
  return out.includes("@")
    ? out.replace(/[\w.+-]{1,64}@[\w-]{1,63}(?:\.[\w-]{1,63}){0,8}\.[a-z]{2,24}\b/gi, "[email]")
    : out;
}

/**
 * Scrubbed and cut to `max`. Cut first, with room to spare, so scrubbing never sees more
 * than a few kilobytes; a cut can split a token or a URL, but what is left still matches.
 */
export function scrubbed(text: string, max: number): string {
  return scrub(text.slice(0, max * 2)).slice(0, max);
}

/** The first two segments of a path, `/ais/vessels/123-name` as `/ais/vessels/*`. */
export function scrubPath(pathname: string): string {
  // `.data` is React Router's suffix for a navigation's loader request.
  const parts = pathname.replace(/\.data$/, "").split("/").filter(Boolean);
  const head = parts.slice(0, 2).map((p) => (/^[\w-]{1,32}$/.test(p) ? p : "?"));
  return "/" + head.join("/") + (parts.length > 2 ? "/*" : "");
}

/** Name, message and stack from anything thrown, scrubbed and cut to length. */
export function describe(error: unknown): { name?: string; message: string; stack?: string } {
  let name: unknown;
  let message: unknown = error;
  let stack: unknown;
  try {
    if (typeof error === "object" && error !== null && "message" in error) {
      ({ name, message, stack } = error as Partial<Error>);
    }
  } catch {
    message = "unreadable error";
  }
  return {
    name: typeof name === "string" && name ? scrubbed(name, MAX_NAME) : undefined,
    message: scrubbed(printable(message), MAX_MESSAGE),
    stack: typeof stack === "string" && stack ? scrubbed(stack, MAX_STACK) : undefined,
  };
}

function printable(value: unknown): string {
  try {
    return String(value);
  } catch {
    return "unprintable value";
  }
}

/**
 * The report for an error, or undefined for one there is nothing to learn from: a
 * cross-origin script's "Script error.", a browser extension's own failure, an aborted
 * request, or the ResizeObserver warning browsers raise as an error and nothing can act on.
 */
export function toReport(kind: ReportKind, error: unknown, pathname: string): ClientReport | undefined {
  const { name, message, stack } = describe(error);
  if (name === "AbortError") return undefined;
  if (/^Script error\.?$/.test(message) && !stack) return undefined;
  if (message.startsWith("ResizeObserver loop")) return undefined;
  if (stack && /\b(?:chrome|moz|safari(?:-web)?)-extension:\/\//.test(stack)) return undefined;
  return { kind, name, message, stack, path: scrubPath(pathname) };
}

/**
 * Says whether a report may be sent: none at all from a page outside the sample, then each
 * distinct one once, up to `max`.
 */
export function createBudget(max: number, sampled: boolean): (key: string) => boolean {
  const seen = new Set<string>();
  return (key) => {
    if (!sampled || seen.size >= max || seen.has(key)) return false;
    seen.add(key);
    return true;
  };
}

let budget: ((key: string) => boolean) | undefined;

/** Sends a report to the Worker, if this page is sampled and has budget left. */
export function reportError(kind: ReportKind, error: unknown) {
  if (typeof navigator === "undefined" || !navigator.sendBeacon) return;
  // Reporting must never be the thing that breaks the page, whatever was thrown.
  try {
    budget ??= createBudget(MAX_PER_PAGE, Math.random() < SAMPLE_RATE);
    const report = toReport(kind, error, location.pathname);
    if (!report || !budget(`${report.kind}:${report.name}:${report.message}`)) return;
    navigator.sendBeacon(REPORT_PATH, JSON.stringify(report));
  } catch {}
}

let installed = false;

/** Reports errors nothing else caught. React's error boundaries report their own. */
export function installErrorReporting() {
  if (installed || typeof window === "undefined") return;
  installed = true;
  // Without an `error`, a cross-origin script's failure, the message is all there is.
  window.addEventListener("error", (e) => reportError("error", e.error ?? e.message));
  window.addEventListener("unhandledrejection", (e) => reportError("rejection", e.reason));
}
