import { isValidImo } from "./ais";
import { type Particulars, type Photo, type VesselMedia } from "./media";

const COMMONS = "https://commons.wikimedia.org/w/api.php";
const WIKIDATA = "https://www.wikidata.org/w/api.php";

// Wikimedia asks every client to name itself and give a contact. A browser cannot set this
// header, which is one reason the lookup runs in the Worker.
const USER_AGENT = "aiscast-web/1.0 (https://openwaters.io/ais/; ais@openwaters.io)";

// One of Wikimedia's standard thumbnail widths. Since early 2026 other widths are throttled
// or refused when requested directly, so only the thumburl the API returns is ever linked.
const THUMB_WIDTH = 960;

const MAX_PHOTOS = 8;
// imageinfo takes up to 50 titles in one call. Asking for more than we show is what lets the
// newest eight be picked, since the category lists files by name.
const MAX_CANDIDATES = 50;
// Ship-identity subcategories under an IMO category: one per name the hull has carried.
const MAX_SUBCATEGORIES = 2;

// How long answers keep. Photos and particulars change rarely; a vessel with neither is the
// common case and is asked again daily; a failure is not an answer, so it is retried soon.
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
async function lookupPhotos(category: string): Promise<Pick<VesselMedia, "photos" | "links">> {
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

type Claim = { rank: string; mainsnak: { datavalue?: { value: any } }; qualifiers?: Record<string, unknown> };

/** A property's current value: the preferred claim, else one without an end time (P582). */
function best(claims: Claim[] | undefined): any {
  const usable = (claims ?? []).filter((c) => c.rank !== "deprecated" && c.mainsnak.datavalue);
  const pick = usable.find((c) => c.rank === "preferred") ?? usable.find((c) => !c.qualifiers?.P582) ?? usable[0];
  return pick?.mainsnak.datavalue!.value;
}

// Units a ship's dimensions are given in, by Wikidata item: metre and foot.
const TO_METERS: Record<string, number> = { Q11573: 1, Q3710: 0.3048 };

function meters(quantity: { amount?: string; unit?: string } | undefined): number | undefined {
  const factor = TO_METERS[String(quantity?.unit).split("/").pop()!];
  const n = Number(quantity?.amount);
  return factor && Number.isFinite(n) ? Math.round(n * factor * 100) / 100 : undefined;
}

/**
 * The vessel's Wikidata item, found by its IMO (P458), as particulars and links. A bot import
 * made items for most IMO-registered ships, so this answers for more of them than Commons.
 */
async function lookupParticulars(imo: string): Promise<Pick<VesselMedia, "particulars" | "links">> {
  const search = await wikimedia(WIKIDATA, { action: "query", list: "search", srsearch: `haswbstatement:P458=${imo}` });
  const qid: string | undefined = search.query?.search?.[0]?.title;
  if (!qid) return { particulars: null, links: {} };

  const body = await wikimedia(WIKIDATA, { action: "wbgetentities", ids: qid, props: "claims|sitelinks/urls", sitefilter: "enwiki" });
  const item = body.entities?.[qid];
  const value = (property: string) => best(item?.claims?.[property]);

  // Builder, registry, operator and owner are items themselves, named in one more call.
  const refs = { builder: value("P176")?.id, registry: value("P8047")?.id, operator: value("P137")?.id, owner: value("P127")?.id };
  const ids = [...new Set(Object.values(refs).filter(Boolean))];
  const labels = ids.length
    ? (await wikimedia(WIKIDATA, { action: "wbgetentities", ids: ids.join("|"), props: "labels", languages: "en", languagefallback: "1" })).entities
    : {};
  const label = (id: string | undefined): string | undefined => (id ? labels?.[id]?.labels?.en?.value : undefined);

  const tonnage = value("P1093");
  const particulars: Particulars = {
    entered: Number(/^\+(\d{4})/.exec(value("P729")?.time ?? "")?.[1]) || undefined,
    builder: label(refs.builder),
    yardNumber: value("P617"),
    length: meters(value("P2043")),
    beam: meters(value("P2261")),
    draught: meters(value("P2262")),
    grossTonnage: tonnage?.unit === "1" ? Number(tonnage.amount) : undefined,
    callsign: value("P2317"),
    registry: label(refs.registry),
    operator: label(refs.operator),
    owner: label(refs.owner),
  };
  return {
    particulars: Object.values(particulars).some((v) => v != null) ? particulars : null,
    links: { wikidata: `https://www.wikidata.org/wiki/${qid}`, wikipedia: item?.sitelinks?.enwiki?.url },
  };
}

/**
 * The media route's answer for a key: photographs from Wikimedia Commons and, for an IMO,
 * particulars from Wikidata. A seven-digit key is an IMO, looked up in Commons' per-hull
 * "IMO <n>" category; a nine-digit key is an MMSI, looked up in its "MMSI <n>" category, for
 * vessels without a usable IMO. Wikidata is asked by IMO only: its MMSI claims measured no
 * vessels that the IMO lookup misses. Answers are cached at the edge, so Wikimedia sees a few
 * requests per vessel per week.
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

  const [photos, about] = await Promise.allSettled([
    lookupPhotos(category),
    key.length === 7 ? lookupParticulars(key) : Promise.resolve({ particulars: null, links: {} }),
  ]);
  for (const result of [photos, about]) if (result.status === "rejected" && !isUpstream(result.reason)) throw result.reason;
  const media: VesselMedia = {
    photos: photos.status === "fulfilled" ? photos.value.photos : [],
    particulars: about.status === "fulfilled" ? about.value.particulars : null,
    links: {
      ...(photos.status === "fulfilled" ? photos.value.links : {}),
      ...(about.status === "fulfilled" ? about.value.links : {}),
    },
  };
  // Half an answer is served but kept only briefly, so the missing half is asked for again.
  const failed = photos.status === "rejected" || about.status === "rejected";
  const cacheControl = failed ? FAILED : media.photos.length || media.particulars ? FOUND : EMPTY;
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
