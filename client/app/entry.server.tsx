import { isbot } from "isbot";
import { renderToReadableStream } from "react-dom/server";
import type { EntryContext } from "react-router";
import { ServerRouter } from "react-router";

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
      // Errors before the shell renders reject and are logged by React Router.
      if (shellRendered) console.error(error);
    },
  });
  shellRendered = true;

  // A crawler or an unfurl bot reads the document once, so it gets the whole thing.
  const ua = request.headers.get("user-agent");
  if ((ua && isbot(ua)) || routerContext.isSpaMode) await body.allReady;

  headers.set("Content-Type", "text/html; charset=utf-8");
  // Every page carries the visitor's location from the root loader, so no shared cache may keep one.
  headers.set("Cache-Control", "private");
  return new Response(body, { headers, status });
}
