import { createRequestHandler, RouterContextProvider } from "react-router";
import { serverEnv, visitorLocation } from "../app/lib/context";

declare global {
  // A secret, set with `wrangler secret put AIS_TOKEN`, so the generated Env leaves it out.
  interface Env {
    AIS_TOKEN?: string;
  }
}

/** Cloudflare's guess at where the request came from, which it cannot always make. */
function locate(request: Request): [number, number] | undefined {
  const { latitude, longitude } = request.cf ?? {};
  if (!latitude || !longitude) return undefined;
  const lat = Number(latitude);
  const lon = Number(longitude);
  return Number.isFinite(lat) && Number.isFinite(lon) ? [lon, lat] : undefined;
}

const handler = createRequestHandler(
  () => import("virtual:react-router/server-build"),
  import.meta.env.MODE,
);

export default {
  fetch(request, env) {
    // On openwaters.io the Worker's routes send it only paths under /ais/. A workers.dev
    // address, such as a preview version's, sends it everything, and the app is at /ais/.
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/ais/")) return Response.redirect(new URL("/ais/vessels", url), 302);
    const context = new RouterContextProvider();
    context.set(serverEnv, { api: env.AIS_API, token: env.AIS_TOKEN || undefined });
    context.set(visitorLocation, locate(request));
    return handler(request, context);
  },
} satisfies ExportedHandler<Env>;
