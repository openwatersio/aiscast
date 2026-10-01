import { mediaResponse } from "../lib/media.server";
import type { Route } from "./+types/vessel-media";

/** GET /ais/vessels/media/:key: a vessel's photos from Wikimedia Commons, as JSON. */
export function loader({ params, request }: Route.LoaderArgs) {
  return mediaResponse(params.key, request.url);
}
