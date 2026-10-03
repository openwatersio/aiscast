import { describe, MAX_MESSAGE, MAX_NAME, MAX_STACK, REPORT_KINDS, scrubbed, scrubPath, type ReportKind } from "./report";

/**
 * One line in Workers Logs per error, browser or server, with the same fields, so a query on
 * `event = "app_error"` finds both. Nothing about the reader is kept beyond the user agent,
 * which is what tells one browser's fault from another's.
 */
export interface ErrorRecord {
  event: "app_error";
  source: "browser" | "server";
  /** A ReportKind from the browser; `request` or `render` from the server. */
  kind: ReportKind | "request" | "render";
  name?: string;
  message: string;
  stack?: string;
  path: string;
  ua?: string;
}

const MAX_UA = 300;
// A report is a few kilobytes at most: the message and stack are capped before sending.
const MAX_BODY = 16 * 1024;

export function logError(record: Omit<ErrorRecord, "event">) {
  console.error({ event: "app_error", ...record } satisfies ErrorRecord);
}

/** Logs an error from the server render or a loader. */
export function logServerError(kind: "request" | "render", error: unknown, request: Request) {
  logError({ source: "server", kind, ...describe(error), path: scrubPath(new URL(request.url).pathname) });
}

/**
 * Allows `max` calls per window. Held per isolate: a limit keyed on the sender would need
 * its address, which is what this is built not to keep. It stops one runaway page from
 * flooding the logs, and Cloudflare's many isolates still let a real outage through.
 */
export function createRateLimit(max: number, windowMs: number, now = Date.now): () => boolean {
  let start = 0;
  let count = 0;
  return () => {
    const t = now();
    if (t - start >= windowMs) {
      start = t;
      count = 0;
    }
    return ++count <= max;
  };
}

const allow = createRateLimit(60, 60_000);

const str = (v: unknown, max: number) => (typeof v === "string" && v ? scrubbed(v, max) : undefined);

/** The body as text, or undefined past `max` bytes, read no further than that. */
async function readCapped(request: Request, max: number): Promise<string | undefined> {
  if (!request.body) return "";
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}

/**
 * Answers the browser's beacon at /ais/errors. The browser scrubs the report, and it is
 * scrubbed again here, because anything can post to this address.
 */
export async function handleReport(request: Request, limit = allow): Promise<Response> {
  if (request.method !== "POST") return new Response(null, { status: 405, headers: { allow: "POST" } });
  // A browser names the page a POST comes from, and the app's beacon is same-origin, so a
  // request with no Origin or another one is not the app's. One that forges it is held to the
  // rate limit and scrubbed like any other.
  if (request.headers.get("origin") !== new URL(request.url).origin) return new Response(null, { status: 403 });
  const text = await readCapped(request, MAX_BODY);
  if (text === undefined) return new Response(null, { status: 413 });

  let body: Record<string, unknown>;
  try {
    body = JSON.parse(text);
  } catch {
    return new Response(null, { status: 400 });
  }
  const kind = body?.kind as ReportKind;
  if (!REPORT_KINDS.includes(kind) || typeof body.message !== "string" || !body.message) {
    return new Response(null, { status: 400 });
  }
  // Ahead of scrubbing, which is the costly part.
  if (!limit()) return new Response(null, { status: 429 });
  const message = scrubbed(body.message, MAX_MESSAGE);
  if (!message) return new Response(null, { status: 400 });

  logError({
    source: "browser",
    kind,
    name: str(body.name, MAX_NAME),
    message,
    stack: str(body.stack, MAX_STACK),
    path: scrubPath(typeof body.path === "string" ? body.path : ""),
    ua: request.headers.get("user-agent")?.slice(0, MAX_UA),
  });
  return new Response(null, { status: 204 });
}
