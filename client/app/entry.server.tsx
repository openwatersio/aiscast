import { isbot } from "isbot";
import { renderToReadableStream } from "react-dom/server";
import type { EntryContext, HandleErrorFunction } from "react-router";
import { isRouteErrorResponse, ServerRouter } from "react-router";
import { logServerError } from "./lib/report.server";

/** Loader, action and render errors, in place of React Router's own console.error. */
export const handleError: HandleErrorFunction = (error, { request }) => {
  if (request.signal.aborted) return;
  if (isRouteErrorResponse(error)) {
    // A response React Router made from a thrown error keeps it, untyped, as `error`. One
    // without is a response a route threw on purpose, such as the 503 for an API outage,
    // which the API's own monitoring covers. A 4xx, such as a POST to a page, is the request's.
    const cause = (error as { error?: unknown }).error;
    if (cause === undefined || error.status < 500) return;
    return logServerError("request", cause, request);
  }
  logServerError("request", error, request);
};

export default async function handleRequest(
  request: Request,
  status: number,
  headers: Headers,
  routerContext: EntryContext,
) {
  let shellRendered = false;
  const body = await renderToReadableStream(<ServerRouter context={routerContext} url={request.url} />, {
    // React writes a finished Suspense boundary larger than this as its fallback, with the
    // content in a hidden element that a script swaps in. A vessel page's facts are larger
    // than the 12.8 kB default, and a reader or crawler without scripts would see "Loading…"
    // under "MMSI <n>". Content that is ready is written in place; pending content streams.
    progressiveChunkSize: Number.POSITIVE_INFINITY,
    onError(error: unknown) {
      status = 500;
      // Errors before the shell renders reject, and React Router passes them to handleError.
      // A reader who leaves mid-stream aborts every pending boundary, which is not a fault.
      if (shellRendered && !request.signal.aborted) logServerError("render", error, request);
    },
  });
  shellRendered = true;

  // A crawler or an unfurl bot reads the document once, so it gets the whole thing.
  const ua = request.headers.get("user-agent");
  if ((ua && isbot(ua)) || routerContext.isSpaMode) await body.allReady;

  headers.set("Content-Type", "text/html; charset=utf-8");
  // The theme cookie is rendered into the page, and a browser that kept it would show the old
  // theme after a switch. The edge keeps one copy per theme (workers/app.ts).
  headers.set("Cache-Control", "private");
  return new Response(body, { headers, status });
}
