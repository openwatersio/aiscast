import { isValidImo } from "./ais";
import { type Photo, type VesselMedia } from "./media";

const COMMONS = "https://commons.wikimedia.org/w/api.php";

// Wikimedia asks every client to name itself and give a contact. A browser cannot set this
// header, which is one reason the lookup runs in the Worker.
const USER_AGENT = "aiscast-web/1.0 (https://openwaters.io/ais/; hello@openwaters.io)";

// One of Wikimedia's standard thumbnail widths. Since early 2026 other widths are throttled
// or refused when requested directly, so only the thumburl the API returns is ever linked.
const THUMB_WIDTH = 960;

const MAX_PHOTOS = 8;
// imageinfo takes up to 50 titles in one call. Asking for more than we show is what lets the
// newest eight be picked, since the category lists files by name.
const MAX_CANDIDATES = 50;
// Ship-identity subcategories under an IMO category: one per name the hull has carried.
const MAX_SUBCATEGORIES = 2;

// How long answers keep. Photos change rarely; a vessel without any is the common case and is
// asked again daily; a failure is not an answer, so it is retried soon.
const FOUND = "public, max-age=604800, stale-while-revalidate=86400";
const EMPTY = "public, max-age=86400";
const FAILED = "public, max-age=900";

class UpstreamError extends Error {}

/** Wikimedia failing or too slow, which the vessel page carries on without. Anything else is a bug. */
const isUpstream = (e: unknown) => e instanceof UpstreamError || e instanceof DOMException;

async function wikimedia(api: string, params: Record<string, string>): Promise<any> {
  const url = new URL(api);
  for (const [k, v] of Object.entries({ ...params, format: "json", formatversion: "2" })) url.searchParams.set(k, v);
  const res = await fetch(url, { headers: { "user-agent": USER_AGENT }, signal: AbortSignal.timeout(5000) });
  if (!res.ok) throw new UpstreamError(`${url.host} ${res.status}`);
  const body = (await res.json()) as { error?: { code: string } };
  if (body.error) throw new UpstreamError(`${url.host} ${body.error.code}`);
  return body;
}

async function members(category: string, type: string): Promise<Array<{ ns: number; title: string }>> {
  const body = await wikimedia(COMMONS, { action: "query", list: "categorymembers", cmtitle: category, cmtype: type, cmlimit: "100" });
  return body.query?.categorymembers ?? [];
}

/** Commons metadata values are HTML: an Artist is often a link to a user page. */
function text(html: string | undefined): string | undefined {
  if (!html) return undefined;
  const plain = html
    .replace(/<[^>]*>/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&quot;/g, '"')
    .replace(/&#0?39;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&nbsp;/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  return plain || undefined;
}

/** When the photo was taken, for ordering: capture date, else upload date. Free text at worst. */
function taken(meta: Record<string, { value?: string }>): number {
  for (const key of ["DateTimeOriginal", "DateTime"]) {
    const t = Date.parse(text(meta[key]?.value) ?? "");
    if (Number.isFinite(t)) return t;
  }
  return 0;
}

/**
 * The newest photos in a Commons category and its first ship-identity subcategories.
 * Throws UpstreamError when Wikimedia fails, so the caller can cache that briefly.
 */
async function lookupPhotos(category: string): Promise<VesselMedia> {
  const top = await members(category, "file|subcat");
  const files = top.filter((m) => m.ns === 6).map((m) => m.title);
  const subcats = top.filter((m) => m.ns === 14).slice(0, MAX_SUBCATEGORIES);
  for (const sub of subcats) files.push(...(await members(sub.title, "file")).map((m) => m.title));

  const titles = [...new Set(files)].slice(0, MAX_CANDIDATES);
  if (!titles.length) return { photos: [], links: {} };

  const body = await wikimedia(COMMONS, {
    action: "query",
    prop: "imageinfo",
    iiprop: "url|size|mime|extmetadata",
    iiurlwidth: String(THUMB_WIDTH),
    iiextmetadatafilter: "Artist|LicenseShortName|LicenseUrl|ImageDescription|DateTimeOriginal|DateTime",
    titles: titles.join("|"),
  });

  const found: Array<Photo & { taken: number }> = [];
  for (const page of body.query?.pages ?? []) {
    const info = page.imageinfo?.[0];
    // Categories also hold PDFs, videos and plans; only photographs and drawings render here.
    if (!info?.thumburl || !String(info.mime ?? "").startsWith("image/")) continue;
    const meta = info.extmetadata ?? {};
    found.push({
      thumb: info.thumburl,
      width: info.thumbwidth,
      height: info.thumbheight,
      page: info.descriptionurl,
      // Every file in the census had an Artist; the file name stands in if one does not.
      artist: text(meta.Artist?.value) ?? String(page.title).replace(/^File:/, ""),
      license: text(meta.LicenseShortName?.value) ?? "See file page",
      // Metadata comes partly from the file page's wikitext; only a web link becomes an href.
      licenseUrl: /^https?:\/\//i.test(text(meta.LicenseUrl?.value) ?? "") ? text(meta.LicenseUrl?.value) : undefined,
      description: text(meta.ImageDescription?.value),
      taken: taken(meta),
    });
  }

  // Recent livery first.
  found.sort((a, b) => b.taken - a.taken);
  const photos = found.slice(0, MAX_PHOTOS).map(({ taken: _, ...photo }) => photo);
  return {
    photos,
    links: photos.length ? { commonsCategory: `https://commons.wikimedia.org/wiki/${encodeURI(category.replace(/ /g, "_"))}` } : {},
  };
}

/**
 * The media route's answer for a key: photographs from Wikimedia Commons. A seven-digit key is
 * an IMO, looked up in Commons' per-hull "IMO <n>" category; a nine-digit key is an MMSI, looked
 * up in its "MMSI <n>" category, for vessels without a usable IMO. Answers are cached at the edge,
 * so Wikimedia sees a few requests per vessel per week. The vessel's particulars come from the
 * API, which syncs them from Wikidata and the Coast Guard.
 */
export async function mediaResponse(key: string, requestUrl: string): Promise<Response> {
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
  const cacheKey = new Request(new URL(`/ais/vessels/media/${key}`, requestUrl));
  const hit = await cache.match(cacheKey);
  if (hit) return hit;

  let media: VesselMedia;
  let cacheControl: string;
  try {
    media = await lookupPhotos(category);
    cacheControl = media.photos.length ? FOUND : EMPTY;
  } catch (e) {
    if (!isUpstream(e)) throw e;
    // Served, but kept only briefly, so the photos are asked for again.
    media = { photos: [], links: {} };
    cacheControl = FAILED;
  }
  const response = Response.json(media, { headers: { "cache-control": cacheControl } });
  await cache.put(cacheKey, response.clone());
  return response;
}

/**
 * The first photo, for a page's og:image, when the answer comes within `ms`: at once from the
 * cache, which the page's own request for its photos fills, and otherwise not at all rather
 * than holding up the page. A lookup that loses the race may be cut off with the response.
 */
export async function firstPhoto(key: string, requestUrl: string, ms = 1000): Promise<string | undefined> {
  const res = await Promise.race([
    mediaResponse(key, requestUrl).catch(() => undefined),
    new Promise<undefined>((resolve) => setTimeout(resolve, ms)),
  ]);
  if (!res?.ok) return undefined;
  return ((await res.json()) as VesselMedia).photos[0]?.thumb;
}
