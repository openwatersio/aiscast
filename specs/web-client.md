# Web client

The viewer becomes an application at `openwaters.io/ais/`: a map that fills the page, panels over it, real routes, vessel and station pages, and search. It is a client of the public API like any other, so every capability it gains is one any other client gains.

The server work it depends on is planned in [specs/history-api.md](history-api.md). This spec covers the client: where it runs, how it renders, and what is left to ship.

## What the API gives the app

| Endpoint | Gives the app |
| --- | --- |
| `GET /v1/vessels/{mmsi}` | One vessel from the durable record, however long ago it was heard: position, particulars, flag, destination, ETA |
| `GET /v1/vessels?q=` | Name or MMSI search over every vessel the record holds |
| `GET /v1/vessels/{mmsi}/track` | The last 48 hours of positions, with GPX |
| `GET /v1/vessels/tiles/{z}/{x}/{y}` and `tiles.json` | Vector tiles of last known positions at any zoom, rebuilt every 10 s, with no area cap |
| `WS /v1/stream` | Live events for the viewport and followed MMSIs, with `snapshot: true` for each vessel's last position and static report |
| `GET /v1/stations`, `GET /v1/stations/{id}` | Station counters and the vessels each station was latest to hear |
| `GET /v1/stats` | Network totals, per-source rates, delay percentiles |

Every JSON endpoint answers a CORS preflight that allows `Authorization`, so the browser sends its token as a header.

Not there yet: history past 48 hours, time series a chart can read, station names, a vessel opt-out list, a sitemap feed, and any notion of a user. [specs/history-api.md](history-api.md) orders that work.

## Where it runs

The app is a Cloudflare Worker, `aiscast-web`, built from [viewer/](../viewer/). Its routes on the `openwaters.io` zone are `/ais/map*`, `/ais/vessels/*`, `/ais/stations*`, `/ais/network*`, and `/ais/assets/*`. Route Workers run in front of a Custom Domain, so those paths reach this Worker and everything else stays with the website Worker, which owns `openwaters.io`. The website keeps `/ais/` (the landing page), `/ais/token`, and the comparison pages.

The app and the API are on different origins. The API stays at `ais.openwaters.io`, bearer-only with open CORS. The app shares the website's origin, which has three consequences:

- The token the website's `/ais/token` page mints is in the app's own `localStorage`, so the app uses it with no hand-off.
- Accounts can use cookies on the app's origin. The app's Worker holds the session and calls the API with a token, and the API never sees an ambient credential.
- Vessel pages build search ranking for `openwaters.io`.

Rendering runs at the edge, not on the ingest box. A crawler walking every vessel page spends Worker time, not the CPU that ingest and fan-out share. Static assets come from the nearest Cloudflare location. A change to the app deploys without touching the box.

## How it renders

React Router 8 in framework mode, React 19, Tailwind, MapLibre.

Every route has two loaders. The server `loader` answers a direct visit, a crawler, or an unfurl bot. It awaits the data, so the title, description, canonical URL, Open Graph tags, JSON-LD, and the facts are all in the first response. The `clientLoader` answers navigation inside the app by calling the API from the browser, so after the first page nothing goes through the Worker. For a vessel the stream has already heard, the pane opens at once from the stream's copy and the record fills in when it arrives.

The server `loader` fetches with `AIS_TOKEN`, a Worker secret. Without it every render shares one anonymous rate limit, which a crawler exhausts in a minute.

The map, the WebSocket, and the vessel cache live in the root layout and are built once. A route swaps the panels and asks the map for a camera move, a focus, or a fit. Nothing remounts the map.

## Shape of the app

```
/ais/map                    the map, with search and the destinations
/ais/vessels/:mmsi-:slug    a vessel; the map follows and highlights it
/ais/stations               the station list
/ais/stations/:id           a station; the map fits its current traffic
/ais/network                sources, rates, delay
```

A vessel opened from a list takes a second pane beside it, and the list stays where it was. A vessel opened directly, from a shared link or a crawler, fills the sidebar. Where a vessel was opened from travels in history state, not in its URL, so a copied link is always the canonical one.

The vessel URL carries the name as a slug: `/ais/vessels/368168720-cerulean`. The slug matches what people search for, which is a boat's name far more often than its MMSI. Names are neither unique nor permanent, so the MMSI stays canonical: any slug resolves, a wrong or missing one redirects once to the current form, and `<link rel="canonical">` names that form. A vessel with no known name is `/ais/vessels/368168720`. `?station=<id>` on `/ais/map` redirects to the station page, for links from the first viewer.

**One stream.** Anonymous clients get two concurrent streams per network address, which a household or a marina shares. The app holds one connection and rebuilds a single `subscribe` frame from the viewport plus the followed vessel. The server accepts a socket before it checks that limit, so the client resets its backoff only on `welcome`, and says so in the footer when another tab holds the stream. Everything else goes over the HTTP budget.

**Above the area cap, tiles.** The stream caps at 100 square degrees anonymous and 400 with a token, taken from the welcome frame. Past that the map draws the vector tiles and reloads them every 15 seconds. The stream then follows only the open vessel, which draws over its tile.

**Attribution is per source.** The app credits each source from the `attribution` field of its events and shows the open vessel's own credit and licence. The tiles carry one credit that links to the per-source list.

## Indexing

Vessel pages are indexed by default, and the vessel opt-out is the one control. This is the project's existing policy applied to a page: publish all, honor opt-outs. `/v1/vessels/{mmsi}` already serves a named vessel's position to anyone, under licences that invite redistribution, so withholding the same facts from a crawler would protect nobody. Every other AIS site publishes small craft by default and honors removal requests. Tracks follow the same rule.

The opt-out sets `noindex` and withdraws the page, alongside the suppression at fan-out and in history. History is metered by tier because the query path costs money, not because of what it reveals, and the two stay separate in code and in the words the user sees.

The sitemap is about page quality, not privacy. A vessel with a name, a type, and recent activity is worth offering a crawler. A bare MMSI heard once is thin content that spends crawl budget. The sitemap gates on that and on the opt-out list; an unlisted page is still indexable if a crawler reaches it by link.

An unknown MMSI answers 404 with `noindex`. A vessel the record holds always renders, saying when it was last heard.

## Stage 1: ship the app

- [x] Persistent map across navigation: one canvas, one WebSocket, and one document load through search, stations, a station, a vessel, and back.
- [x] Server-rendered vessel, station, stations, and network pages with the full head, 301 slug redirects, and 404 with `noindex` for an unknown vessel or station.
- [x] Client loaders that call the API from the browser, with the vessel pane opening from the stream's copy.
- [x] List and detail panes, search over the record, the track bar with playback and GPX, and live values over the record in the vessel pane.
- [x] Tiles above the area cap, with the cap read from the welcome frame.
- [x] Stream backoff reset on `welcome`, and a footer that names a refused stream.
- [x] CI job: typecheck, unit tests, build.
- [ ] Deploy. Create the `aiscast-web` Worker, connect Workers Builds as the website does (build `npm run build -w viewer`, deploy from `viewer/`), and confirm the routes answer in front of the website's Custom Domain.
- [ ] Mint the renderer's token and set it with `wrangler secret put AIS_TOKEN`. It needs a limit sized for crawlers rather than the personal tier.
- [ ] Cache vessel and station documents at the edge for a minute, so a crawler's second visit costs nothing.
- [ ] Website: link `/ais/` to `/ais/map`, allow the app's paths in `robots.txt`, and point the status monitor at the app.
- [ ] Retire `viewer/index.html`, `viewer/token.html`, and the Pages workflow. The Pages copy becomes a page that forwards to `openwaters.io/ais/map`, keeping `?station=`.
- [ ] Show stationary vessels at close zoom. The tiles keep a moored boat for a week and the stream only what it hears live, so a berth empties as you zoom in. Either draw the tiles under the stream at every zoom, filtered to vessels the stream is not drawing, or seed the viewport from `/v1/vessels?bbox=`.
- [ ] Share one stream across tabs with a `SharedWorker`, so a second tab does not spend the address's second stream.
- [ ] Station list detail: source kind, vessels heard, duplicates. All are in `/v1/stations`.
- [ ] Network page: vessel counts by kind and `/health`.
- [ ] Live charts from the stream. Every event carries `station` and `source`, so events per minute needs no server change.
- [ ] Browser tests for the flows above, run in CI against a local server.

## Later stages

Each follows a server step in [specs/history-api.md](history-api.md).

- **Seven-day charts** on station and network pages, once `?series=hourly` exists.
- **Station names and heard-first counts** on station pages, and the OG image they make possible.
- **Sitemap** at `/ais/sitemap.xml`, served by the app from `GET /v1/vessels?updated_since=`, and the opt-out list wired to `noindex`.
- **History:** a time range on the vessel page past 48 hours, GeoJSON export, and playback over a bbox, once the archive stage lands. The app treats "you need a token for this" as a normal state, not an error.
- **Accounts:** sign in with Google or GitHub on the app's Worker, a session cookie on `openwaters.io`, and OAuth as a way to mint tokens rather than a second credential. The API keeps bearer tokens as its only credential. Then claiming and naming stations, managing keys, and seeing usage and tier.

## Constraints

- Anonymous: 2 concurrent streams per address, 100 square degrees, 20 messages per second, 10 followed MMSIs, 120 requests per minute, 600 tile requests per minute. A personal token raises the stream to 400 square degrees and 50 messages per second. The app is built for the anonymous numbers and treats a token as an upgrade.
- `/v0` is frozen. Everything new is additive under `/v1`.
- The app draws a vessel where it last reported and extrapolates nothing.
- Every source carries its own licence and credit line, and the app shows the attribution that came with the data.
- Station locations are never asked for and derived locations are shown only coarse. A coverage footprint is a bbox, never a pin.
- URLs are the expensive part to change. The paths above are settled before anything is submitted to a search engine and keep working afterwards.
- UDP feeders send to `udp.ais.openwaters.io`, so the API name can go behind Cloudflare without touching them. The app does not depend on that either way.
