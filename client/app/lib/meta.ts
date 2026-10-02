import type { MetaDescriptor } from "react-router";
import { DEFAULT_SHARE_IMAGE } from "./links";

/** Where the app is served. Canonical URLs are absolute, so they name it. */
export const SITE = "https://openwaters.io/ais";

/**
 * Everything a page's head says about it. A route's meta replaces its parent's rather than
 * adding to it, so every route builds the whole set here.
 */
export function pageMeta({
  title,
  description,
  path,
  noindex = false,
  image = DEFAULT_SHARE_IMAGE,
  jsonLd,
}: {
  title: string;
  description: string;
  /** Path within the app, which is also the canonical URL's. */
  path: string;
  noindex?: boolean;
  /** An absolute URL, for link previews. Pages without one share the network's card. */
  image?: string;
  jsonLd?: Record<string, unknown>;
}): MetaDescriptor[] {
  const url = `${SITE}${path}`;
  return [
    { title },
    { name: "description", content: description },
    { tagName: "link", rel: "canonical", href: url },
    ...(noindex ? [{ name: "robots", content: "noindex, follow" }] : []),
    { property: "og:type", content: "website" },
    { property: "og:title", content: title },
    { property: "og:description", content: description },
    { property: "og:url", content: url },
    { property: "og:site_name", content: "Open Waters AIS" },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:title", content: title },
    { name: "twitter:description", content: description },
    { property: "og:image", content: image },
    { name: "twitter:image", content: image },
    ...(jsonLd ? [{ "script:ld+json": jsonLd }] : []),
  ];
}
