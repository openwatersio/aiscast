import { isValidImo } from "./ais";
import { fileTitle, type Photo, type VesselMedia } from "./media";

const COMMONS = "https://commons.wikimedia.org/w/api.php";
const WIKIDATA = "https://www.wikidata.org/w/api.php";

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
 * Each file's thumbnail and credit, from Commons' imageinfo, for up to 50 titles. Only images
 * are kept. `normalized` maps a title as asked to Commons' own form of it.
 */
async function imageInfo(titles: string[]): Promise<{ found: Array<Photo & { taken: number; title: string }>; normalized: Map<string, string> }> {
  const body = await wikimedia(COMMONS, {
    action: "query",
    prop: "imageinfo",
    iiprop: "url|size|mime|extmetadata",
    iiurlwidth: String(THUMB_WIDTH),
    iiextmetadatafilter: "Artist|LicenseShortName|LicenseUrl|ImageDescription|DateTimeOriginal|DateTime",
    titles: titles.join("|"),
  });

  // Commons answers under its own form of each title, which may differ from Wikidata's.
  const normalized = new Map<string, string>((body.query?.normalized ?? []).map((n: { from: string; to: string }) => [n.from, n.to]));

  const found: Array<Photo & { taken: number; title: string }> = [];
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
      title: page.title,
    });
  }
  return { found, normalized };
}

/**
 * The photo Wikidata gives the ship with this IMO (P458): its P18, which Wikidata serves as the
 * item's page image. An editor chose it for this hull, where a Commons category is filed by
 * hand and can hold another ship of the same name. An IMO on more than one item takes the lowest
 * QID, as the API's particulars do (server/wikidata.go), so the photo is that item's or none.
 */
async function wikidataPhoto(imo: number): Promise<string | undefined> {
  const body = await wikimedia(WIKIDATA, {
    action: "query",
    generator: "search",
    gsrsearch: `haswbstatement:P458=${imo}`,
    gsrlimit: "50",
    prop: "pageprops",
    ppprop: "page_image_free",
  });
  const pages: Array<{ title: string; pageprops?: { page_image_free?: string } }> = body.query?.pages ?? [];
  const qid = (p: { title: string }) => Number(p.title.slice(1));
  const item = pages.filter((p) => /^Q\d+$/.test(p.title)).sort((a, b) => qid(a) - qid(b))[0];
  const file = item?.pageprops?.page_image_free;
  return file ? `File:${file.replace(/_/g, " ")}` : undefined;
}

/**
 * The newest photos in a Commons category and its first ship-identity subcategories, led by the
 * photo Wikidata gives the ship when `imo` is known. Throws UpstreamError when Commons fails, so
 * the caller can cache that briefly. Wikidata failing costs only the lead, so the photos are
 * still answered, marked incomplete.
 */
async function lookupPhotos(category: string, imo?: number): Promise<{ media: VesselMedia; complete: boolean }> {
  const [top, lead] = await Promise.all([
    members(category, "file|subcat"),
    // Null when Wikidata failed, undefined when it has no photo.
    imo ? wikidataPhoto(imo).catch((e) => (isUpstream(e) ? null : Promise.reject(e))) : undefined,
  ]);
  const complete = lead !== null;
  const files = top.filter((m) => m.ns === 6).map((m) => m.title);
  const subcats = top.filter((m) => m.ns === 14).slice(0, MAX_SUBCATEGORIES);
  for (const sub of subcats) files.push(...(await members(sub.title, "file")).map((m) => m.title));

  // The lead comes first so the cap never drops it, and is asked for even when the category lacks it.
  const titles = [...new Set(lead ? [lead, ...files] : files)].slice(0, MAX_CANDIDATES);
  if (!titles.length) return { media: { photos: [], links: {} }, complete };

  const { found, normalized } = await imageInfo(titles);
  const leadTitle = lead && (normalized.get(lead) ?? lead);

  // Wikidata's photo, then recent livery.
  found.sort((a, b) => b.taken - a.taken);
  const at = found.findIndex((p) => p.title === leadTitle);
  if (at > 0) found.unshift(...found.splice(at, 1));
  const photos = found.slice(0, MAX_PHOTOS).map(({ taken: _, title: __, ...photo }) => photo);
  return {
    media: {
      photos,
      links:
        photos.length && files.length
          ? { commonsCategory: `https://commons.wikimedia.org/wiki/${encodeURI(category.replace(/ /g, "_"))}` }
          : {},
    },
    complete,
  };
}

/**
 * The media route's answer for a key: photographs from Wikimedia Commons. A seven-digit key is
 * an IMO, looked up in Commons' per-hull "IMO <n>" category, led by the photo Wikidata gives
 * that IMO; a nine-digit key is an MMSI, looked up in its "MMSI <n>" category, for vessels
 * without a usable IMO. Answers are cached at the edge, so Wikimedia sees a few requests per
 * vessel per week. The vessel's particulars come from the API, which syncs them from Wikidata
 * and the Coast Guard.
 */
export async function mediaResponse(key: string, requestUrl: string): Promise<Response> {
  let category: string;
  let imo: number | undefined;
  if (/^\d{7}$/.test(key)) {
    // A mistyped IMO would find another ship's photos or none; refuse it before asking.
    if (!isValidImo(Number(key))) return Response.json({ error: "invalid IMO" }, { status: 400 });
    category = `Category:IMO ${key}`;
    imo = Number(key);
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
    const answer = await lookupPhotos(category, imo);
    media = answer.media;
    // Photos without their lead are served, but asked for again soon.
    cacheControl = !answer.complete ? FAILED : media.photos.length ? FOUND : EMPTY;
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

/**
 * Photos of named Commons files, such as those a fleet lists, keyed by fileTitle(name), in
 * Commons' 50-title batches. Kept at the edge under the set of names, so a list edited in a
 * deploy is asked for afresh. Never rejects: Wikimedia failing answers with what it found, or
 * nothing, cached briefly.
 */
export async function namedPhotos(names: string[], requestUrl: string): Promise<Record<string, Photo>> {
  const titles = [...new Set(names.map(fileTitle))].sort();
  if (!titles.length) return {};
  const digest = await crypto.subtle.digest("SHA-1", new TextEncoder().encode(titles.join("|")));
  const id = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
  const cache = (caches as unknown as { default: Cache }).default;
  const cacheKey = new Request(new URL(`/ais/fleets/photos/${id}`, requestUrl));
  const hit = await cache.match(cacheKey).catch(() => undefined);
  if (hit) return hit.json();

  const photos: Record<string, Photo> = {};
  let complete = true;
  for (let i = 0; i < titles.length; i += MAX_CANDIDATES) {
    try {
      const { found, normalized } = await imageInfo(titles.slice(i, i + MAX_CANDIDATES));
      const asked = new Map([...normalized].map(([from, to]) => [to, from]));
      for (const { taken: _, title, ...photo } of found) photos[asked.get(title) ?? title] = photo;
    } catch {
      // Wikimedia failing, or answering with something other than its JSON: what was found is
      // the answer, and it is asked again soon.
      complete = false;
    }
  }
  const response = Response.json(photos, { headers: { "cache-control": complete ? FOUND : FAILED } });
  await cache.put(cacheKey, response).catch(() => undefined);
  return photos;
}
