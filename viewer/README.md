# viewer

The web client for aiscast: a live map with vessel and station pages, search, and the
self-serve token flow. Astro with the Node adapter, so `/vessels/:mmsi` and `/stations/:id`
are server-rendered and can be shared and indexed. It is a client of the aiscast API like
any other.

```sh
npm install                 # from the repo root; viewer/ is a workspace
npm run dev -w @openwaters/aiscast-viewer
npm test -w @openwaters/aiscast-viewer
```

`PUBLIC_AIS_API` points the browser at a server (default `https://ais.openwaters.io`).
`API_SERVER` points server-side rendering at one, which in production is loopback to the Go
server on the same box. The map position lives in the URL hash.

Routes: `/` is the map and search, `/vessels/:mmsi-:name` a vessel, `/stations/:id` a
station, `/network` what the network is receiving, `/token` a personal token. The MMSI is
canonical in a vessel URL and the name slug is cosmetic, so a wrong or missing slug
redirects to the canonical form. `?station=<id>` redirects to the station page.

The map is the page and the panels float over it. It is never unmounted across navigation,
because the stream is capped at two connections per network address and remounting would
spend that budget. Camera padding is measured from the panels, so a selected vessel centres
in the map you can see rather than behind one.

Vessels fill from `GET /v1/vessels` and update over `/v1/stream`. Triangles point along
heading (COG fallback), colour is ship-type class, and opacity fades with age. A vessel
disappears 30 minutes after its last report. The viewer extrapolates nothing: it draws a
vessel where that vessel last reported.

[index.html](index.html) and [token.html](token.html) are the previous single-page viewer,
still what GitHub Pages serves at
[openwatersio.github.io/aiscast](https://openwatersio.github.io/aiscast/) until the box
serves this app.
