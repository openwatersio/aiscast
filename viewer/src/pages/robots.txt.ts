import type { APIRoute } from "astro";

// The Go server answers `Disallow: /` for the whole host, because crawlers were finding
// /v1/vessels and /v1/receive in the code samples on openwaters.io/ais and reporting the
// 4xx answers as site errors. That intent is kept here: the API stays disallowed and only
// the app's pages are offered. Caddy routes /robots.txt to this app, not to the Go server.
const body = `User-agent: *
Allow: /
Disallow: /v0/
Disallow: /v1/
Disallow: /metrics
Disallow: /health
Disallow: /openapi.json

Sitemap: https://ais.openwaters.io/sitemap.xml
`;

export const GET: APIRoute = () =>
  new Response(body, { headers: { "content-type": "text/plain; charset=utf-8" } });
