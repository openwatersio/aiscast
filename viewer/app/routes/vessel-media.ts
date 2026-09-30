import { isValidImo } from "../lib/ais";
import { NO_MEDIA, type VesselMedia } from "../lib/media";
import { lookupPhotos, UpstreamError } from "../lib/media.server";
import type { Route } from "./+types/vessel-media";

// How long answers keep. Photos change rarely; a vessel with none is the common case and is
// asked again daily; a failure is not an answer, so it is retried soon.
const FOUND = "public, max-age=604800, stale-while-revalidate=86400";
const EMPTY = "public, max-age=86400";
const FAILED = "public, max-age=900";

/**
 * GET /ais/vessels/media/:key: photographs of a vessel from Wikimedia Commons, as JSON.
 * A seven-digit key is an IMO, looked up in Commons' per-hull "IMO <n>" category; a nine-digit
 * key is an MMSI, looked up in its "MMSI <n>" category, for vessels without a usable IMO. The
 * two lengths never overlap. Answers are cached at the edge, so Commons sees a few requests
 * per vessel per week.
 */
export async function loader({ params, request }: Route.LoaderArgs) {
  const key = params.key;
  let category: string;
  if (/^\d{7}$/.test(key)) {
    // A mistyped IMO would find another ship's photos or none; refuse it before asking.
    if (!isValidImo(Number(key))) return Response.json({ error: "invalid IMO" }, { status: 400 });
    category = `Category:IMO ${key}`;
  } else if (/^\d{9}$/.test(key)) {
    category = `Category:MMSI ${key}`;
  } else {
    return Response.json({ error: "expected a 7-digit IMO or a 9-digit MMSI" }, { status: 400 });
  }

  const cache = (caches as unknown as { default: Cache }).default;
  const cacheKey = new Request(new URL(new URL(request.url).pathname, request.url));
  const hit = await cache.match(cacheKey);
  if (hit) return hit;

  let media: VesselMedia;
  let cacheControl: string;
  try {
    media = await lookupPhotos(category);
    cacheControl = media.photos.length ? FOUND : EMPTY;
  } catch (e) {
    // The vessel page carries on without photos. Anything else is a bug and surfaces.
    if (!(e instanceof UpstreamError) && !(e instanceof DOMException)) throw e;
    media = NO_MEDIA;
    cacheControl = FAILED;
  }
  const response = Response.json(media, { headers: { "cache-control": cacheControl } });
  await cache.put(cacheKey, response.clone());
  return response;
}
