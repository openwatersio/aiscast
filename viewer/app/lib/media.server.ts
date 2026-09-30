import type { Photo, VesselMedia } from "./media";

const COMMONS = "https://commons.wikimedia.org/w/api.php";

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

class UpstreamError extends Error {}

async function commons(params: Record<string, string>): Promise<any> {
  const url = new URL(COMMONS);
  for (const [k, v] of Object.entries({ ...params, format: "json", formatversion: "2" })) url.searchParams.set(k, v);
  const res = await fetch(url, { headers: { "user-agent": USER_AGENT }, signal: AbortSignal.timeout(5000) });
  if (!res.ok) throw new UpstreamError(`Commons ${res.status}`);
  const body = (await res.json()) as { error?: { code: string } };
  if (body.error) throw new UpstreamError(`Commons ${body.error.code}`);
  return body;
}

async function members(category: string, type: string): Promise<Array<{ ns: number; title: string }>> {
  const body = await commons({ action: "query", list: "categorymembers", cmtitle: category, cmtype: type, cmlimit: "100" });
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
export async function lookupPhotos(category: string): Promise<VesselMedia> {
  const top = await members(category, "file|subcat");
  const files = top.filter((m) => m.ns === 6).map((m) => m.title);
  const subcats = top.filter((m) => m.ns === 14).slice(0, MAX_SUBCATEGORIES);
  for (const sub of subcats) files.push(...(await members(sub.title, "file")).map((m) => m.title));

  const titles = [...new Set(files)].slice(0, MAX_CANDIDATES);
  if (!titles.length) return { photos: [], particulars: null, links: {} };

  const body = await commons({
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
      licenseUrl: text(meta.LicenseUrl?.value),
      description: text(meta.ImageDescription?.value),
      taken: taken(meta),
    });
  }

  // Recent livery first.
  found.sort((a, b) => b.taken - a.taken);
  const photos = found.slice(0, MAX_PHOTOS).map(({ taken: _, ...photo }) => photo);
  return {
    photos,
    particulars: null,
    links: photos.length ? { commonsCategory: `https://commons.wikimedia.org/wiki/${encodeURI(category.replace(/ /g, "_"))}` } : {},
  };
}

export { UpstreamError };
