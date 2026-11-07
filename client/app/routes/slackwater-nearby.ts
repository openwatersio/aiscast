import { serverWaitUntil } from "../lib/context";
import { nearbyResponse } from "../lib/slackwater.server";
import type { Route } from "./+types/slackwater-nearby";

/** GET /ais/vessels/slackwater/:at: the Slackwater stations near `lat,lon`, as JSON. */
export function loader({ params, context }: Route.LoaderArgs) {
  return nearbyResponse(params.at, context.get(serverWaitUntil));
}
