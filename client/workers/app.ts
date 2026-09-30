import { createRequestHandler, RouterContextProvider } from "react-router";
import { serverEnv } from "../app/lib/context";

declare global {
  // A secret, set with `wrangler secret put AIS_TOKEN`, so the generated Env leaves it out.
  interface Env {
    AIS_TOKEN?: string;
  }
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
    return handler(request, context);
  },
} satisfies ExportedHandler<Env>;
