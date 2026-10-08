import { fleetCard, fleetsWith } from "../lib/fleets.server";
import { namedPhotos } from "../lib/media.server";
import type { Route } from "./+types/vessel-fleets";

/**
 * GET /ais/vessels/fleets/:mmsi: the cards of the fleets a vessel is in, with their named cover
 * photos, as JSON, for the foot of its page. The page asks the browser, since navigation in the
 * app loads a vessel there without the server.
 */
export async function loader({ params, request }: Route.LoaderArgs) {
  const mmsi = Number(params.mmsi);
  const cards = Number.isInteger(mmsi) ? fleetsWith(mmsi).map(fleetCard) : [];
  const photos = await namedPhotos(cards.flatMap((c) => c.photo ?? []), request.url);
  return Response.json({ cards, photos }, { headers: { "cache-control": "public, max-age=3600" } });
}
