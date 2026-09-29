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
| `WS /v1/stream` | Live events for the viewport and followed MMSIs |
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

The panel is a navigation stack, and every route is an entry on it. Opening a vessel from search, a station, or the map pushes it; the back arrow pops to the previous entry, or to the route's parent after a direct visit; a vessel's close button returns to the map. Search keeps its query and results in the shell, so popping back to it finds them as they were, and each entry keeps its scroll position.

The vessel URL carries the name as a slug: `/ais/vessels/368168720-cerulean`. The slug matches what people search for, which is a boat's name far more often than its MMSI. Names are neither unique nor permanent, so the MMSI stays canonical: any slug resolves, a wrong or missing one redirects once to the current form, and `<link rel="canonical">` names that form. A vessel with no known name is `/ais/vessels/368168720`. `?station=<id>` on `/ais/map` redirects to the station page, for links from the first viewer.

**Tiles underneath, the stream on top.** The vector tiles draw every vessel's last known position at every zoom, including moored boats for a week. Within the stream's area cap (100 square degrees anonymous, 400 with a token, read from the welcome frame) the stream draws the vessels it hears over them, live. Tiles cannot be edited, so a vessel the stream is drawing keeps its tile copy but hidden, through feature state set by MMSI, which survives a tile reload. When the stream forgets a vessel its tile copy shows again. Past the cap the stream follows only the open vessel.

Within the cap the stream speaks for every vessel in view, so the tiles reload only where it cannot: when a pan or zoom starts, since MapLibre brings back cached tiles as old as when they left the view; when the stream lets go of a vessel that is still in view, since its tile copy is only as current as the tile; and every five minutes, to clear moving vessels that went silent before the stream heard them. Past the cap there is no stream, and the tiles reload every 15 seconds. Every reload waits at least 10 seconds after the last, the server's rebuild period, and none happens while the tab is hidden.

The subscription asks for no snapshot. The tiles already paint where every vessel was, and the open vessel's pane fetches its record from `/v1/vessels/{mmsi}`, so the stream only has to say what changes. A position report carries no name or type, so a vessel the stream takes over borrows them from its tile until its own static report arrives, and keeps its label and colour. Vessels that leave the viewport are forgotten, so a stale stream position never hides a current tile. Both sources share one style, and the stream layer is sent to the map as a diff of the vessels that changed each second.

**One stream.** Anonymous clients get two concurrent streams per network address, which a household or a marina shares. The app holds one connection and rebuilds a single `subscribe` frame from the viewport plus the open vessel, letting go of the last one, since followed MMSIs are capped at 10. The server accepts a socket before it checks the stream limit, so the client resets its backoff only on `welcome`, and says so in the footer when another tab holds the stream. Everything else goes over the HTTP budget.

**Attribution is per source.** The app credits each source from the `attribution` field of its events and shows the open vessel's own credit and licence. The tiles carry one credit that links to the per-source list.

## Interface

The app should feel like Apple Maps or Google Maps to someone who uses either: the map is the page, one panel holds everything else, and the panel behaves the way theirs does.

**Phone: a bottom sheet.** One sheet, never dismissed, over a map that stays interactive, with three heights. It is the page's main content region, not a dialog, and it is ours rather than a library's: the server renders it with its content and its height, which a portal-based drawer cannot, and nothing else would give the map a resting position to aim the camera at while the sheet is still moving. Peek shows the grabber and the search field. Half is where a vessel opens. Full is for long lists. Content scrolls only at full height, and dragging down with the content at its top moves the sheet instead. Panning the map lowers the sheet to peek, and focusing search raises it to full. The sheet's position feeds the camera padding as it moves, so the open vessel stays centred in the map above it.

**Desktop: a floating panel** down the left edge, as in Google Maps, holding the same stack.

**Transitions.** A push slides the new entry in from the right while the old one slides partway left and dims; a pop reverses it. The direction comes from the history index, so the browser's back button animates as a pop. React's `<ViewTransition>` runs them, since React Router wraps navigations in transitions. Only the panel is captured: the page root opts out, so the map keeps rendering live underneath. Reduced motion turns them off.

**Themes.** Dark is the default, and Light and System are the other choices. The choice is a cookie scoped to `/ais`, so the Worker renders the right theme in the first response and nothing flashes. Colour tokens are written once with `light-dark()`, and each choice sets only `color-scheme`, with System setting `light dark`. The basemap is OpenFreeMap Fiord in dark and Positron in light, and the vessel, track, and label colours follow it.

**Design system.** Semantic tokens are Tailwind utilities (`bg-surface`, `text-fg-muted`, `border-line`), with the same names and values as openwaters.io, so no component references a CSS variable by hand. A type scale follows iOS: large title, title, headline, body, footnote, caption, in the system font with tabular figures for data. A small set of components lives in `app/components/ui/`, owned in this repo: `PanelHeader`, `List` and `ListRow` with `ClassDot` and `IconBadge`, `Section` and `Tile` in the inset-grouped style, `StatGrid`, `Facts`, `SearchField`, `IconButton` and `IconLink`, and `Menu`. Class names go through `cn()`, which knows the type scale. Base UI supplies the behaviour of menus and popovers, and `lucide-react` the icons. CI fails on a CSS variable in a `className` or `style`. Once openwaters.io uses the same tokens, they move to a package both import.

On the map: a status chip saying Live or Overview in place of the footer, the controls at the bottom right above the sheet on touch devices without zoom buttons, touch targets of at least 44 px, and an action row under a vessel's name for Follow, Share, and Track.

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
- [x] Tiles at every zoom with the stream drawn over them within the area cap, no snapshot, and one vessel never drawn twice.
- [x] Stream backoff reset on `welcome`, and a footer that names a refused stream.
- [x] CI job: typecheck, unit tests, build.
- [ ] Deploy. Create the `aiscast-web` Worker, connect Workers Builds as the website does (build `npm run build -w viewer`, deploy from `viewer/`), and confirm the routes answer in front of the website's Custom Domain.
- [ ] Mint the renderer's token and set it with `wrangler secret put AIS_TOKEN`. It needs a limit sized for crawlers rather than the personal tier.
- [ ] Cache vessel and station documents at the edge for a minute, so a crawler's second visit costs nothing.
- [ ] Website: link `/ais/` to `/ais/map`, allow the app's paths in `robots.txt`, and point the status monitor at the app.
- [ ] Retire `viewer/index.html`, `viewer/token.html`, and the Pages workflow. The Pages copy becomes a page that forwards to `openwaters.io/ais/map`, keeping `?station=`.
- [ ] Share one stream across tabs with a `SharedWorker`, so a second tab does not spend the address's second stream.
- [x] Theme: Dark, Light, and System, the cookie, `light-dark()` tokens, and the basemap and vessel colours following it.
- [x] The phone sheet: three heights set by the server, drag and flick with rubber-banding, native scrolling at full height, and a drag down from the top of the content moving the sheet.
- [ ] Try the sheet on real phones, iOS Safari especially, and tune the flick projection and the peek height.
- [x] Tokens as Tailwind utilities, the `ui/` components, and `lucide-react`; every view migrated, with the CI check that fails on a CSS variable in a component.
- [x] One navigation stack in place of the second pane, with push and pop transitions that follow the history index.
- [ ] Scroll position restored per stack entry, and a pending state for slow entries such as a large station's vessel list.
- [ ] A panel header per entry that shows the title at peek height, in place of the bare back arrow.
- [ ] Map chrome: status chip, controls, action row, touch targets; browser tests for the sheet heights and the back stack.
- [x] Station list detail: source kind, messages today, vessels heard.
- [x] Network page: vessel counts by kind.
- [ ] Network page: `/health`.
- [ ] Live charts from the stream. Every event carries `station` and `source`, so events per minute needs no server change.
- [ ] Browser tests for the flows above, run in CI against a local server, including that no MMSI is drawn by both the tiles and the stream. Dev builds put the map on `window.aiscastMap` for this.

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
