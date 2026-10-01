# client

The web client for aiscast at `openwaters.io/ais/`: a live map with vessel and station pages, search, and track playback. React Router in framework mode on a Cloudflare Worker, so `/ais/vessels/:mmsi` and `/ais/stations/:id` are server-rendered and can be shared and indexed. It is a client of the aiscast API like any other.

```sh
npm install                      # from the repo root; client/ is a workspace
npm run dev -w client            # http://localhost:5173/ais/vessels, rendering in workerd
npm test -w client               # unit tests
npm run typecheck -w client      # generates the Worker and route types, then tsc
npm run build -w client
npm run preview -w client        # the built Worker, locally
```

`AIS_API` in [wrangler.jsonc](wrangler.jsonc) is the API the server renders against, and the root loader hands it to the browser, so pointing the app at a local server is one setting. Put `AIS_API=http://localhost:8080` in `client/.dev.vars` to override it locally. `AIS_TOKEN`, a secret set with `wrangler secret put AIS_TOKEN`, is a partner token that gives server renders its limits, such as the longer track. Minted with an `rpm` claim (`aiscast-key new … -rpm <n>`), it also gives them their own HTTP request budget; without one, renders share the per-address limit of the Worker's egress addresses.

## Routes

`/ais/vessels` is the map and search, `/ais/vessels/:mmsi-:name` a vessel, `/ais/stations` the station list, `/ais/stations/:id` a station, and `/ais/network` what the network is receiving. The MMSI is canonical in a vessel URL and the name slug is cosmetic, so a wrong or missing slug redirects to the canonical form. `?station=<id>` on `/ais/vessels` redirects to the station page, and `/ais/map`, where the map was first published, redirects to `/ais/vessels`. The token page is the website's, at `/ais/token`, and the app reads the token it stores.

[wrangler.jsonc](wrangler.jsonc) lists the same top-level paths as the Worker's routes on `openwaters.io`. The website Worker answers everything else under `/ais/`. A new top-level route needs a line in both [app/routes.ts](app/routes.ts) and `wrangler.jsonc`. Built assets are written to `ais/assets/` so the assets binding serves them at the path they are linked from. The whole route manifest ships with the first page, because lazy route discovery would ask `/ais/__manifest`, a path the website owns.

## How it works

Each route has a server `loader` for a document request and a `clientLoader` for navigation inside the app. The client loader calls the API from the browser with the visitor's token, so after the first page the Worker is not involved. A vessel the stream has already heard opens from the stream's copy while the record loads.

The map, the stream, and the vessel cache are built once in [components/Shell.tsx](app/components/Shell.tsx) and outlive every navigation, because the stream is capped at two connections per network address and remounting would spend that budget. One panel floats over the map, a bottom sheet on a phone, and camera padding is measured from it, so a selected vessel centres in the part of the map you can see. The panel is one navigation stack. Back from vessels opened one after another on the map returns to the page they were opened over, while the browser's own back steps through each.

[lib/map.client.ts](app/lib/map.client.ts) is browser-only, which keeps MapLibre out of the Worker. The vector tiles from `/v1/vessels/tiles` draw every vessel's last known position at every zoom. They reload when a pan starts, when the stream lets go of a vessel still in view, and every five minutes, or every 15 seconds past the stream's area cap, where nothing else updates them. Within the stream's area cap, `/v1/stream` draws the vessels it hears on top, and each one's tile copy is hidden with feature state so no vessel is drawn twice. The subscription asks for no snapshot: the tiles paint the starting picture and the stream says what changes. Triangles point along heading, falling back to course; colour is ship-type class; opacity fades with age, the same for both sources. The viewer extrapolates nothing: it draws a vessel where that vessel last reported. In dev builds the map is on `window.aiscastMap` for browser tests.

The design system is Tailwind utilities over the tokens in [app.css](app/app.css), and the shared components in [components/ui/](app/components/ui/). A component never names a CSS variable, and `npm run check:styles` fails CI if one does.

## Vessel pages

`/ais/vessels/media/:key` answers a vessel's photos and particulars as JSON, from [lib/media.server.ts](app/lib/media.server.ts). The key is a 7-digit IMO, checked against its check digit, or for a vessel without one a ship's 9-digit MMSI. Photos are the newest eight in Wikimedia Commons' `Category:IMO <n>` and its first ship-name subcategories, or `Category:MMSI <n>`. Particulars come from the Wikidata item with that IMO (P458). The Worker asks Wikimedia with a User-Agent naming a contact, as its API policy requires, and caches the answer at the edge for a week, a day when there is nothing, and 15 minutes after an upstream failure. The browser hotlinks the 960px thumbnail the API returns, or the 120px one in search results; both are standard Wikimedia widths, which render freely. Every photo shows its credit, `© artist · licence`, linking to the file page and the licence. A document render uses the first photo as `og:image` when it comes within a second.

The drawing in [components/ui/ShipDiagram.tsx](app/components/ui/ShipDiagram.tsx) is a silhouette for the vessel's ITU type, stretched to its length and beam with the drawn ratio kept between 2:1 and 6:1. The antenna's offsets are read by ITU-R M.1371's rules in `vesselDimensions()` in [lib/ais.ts](app/lib/ais.ts): 511 and 63 mean "that many metres or more", so the size is unknown and nothing is drawn; a zero offset means the reference point is not available, so the hull is drawn without the antenna. The offsets come from the API's `to_bow`, `to_stern`, `to_port` and `to_starboard`, or from the stream's static data.

## Tracks

A vessel's Track section shows the last 6 hours to 12 months, or pages back through its history one period at a time. Past pages end on UTC boundaries, so a page is a quarter, half, or whole day, whole days for the 48-hour and 7-day ranges, and a calendar month or year for the longest two. Past 48 hours the server answers at one position a minute at most, and a year's chart is labelled by month. The server spreads the token's position limit over the range. Anonymous and personal tokens reach the last 48 hours; feeder and commercial tokens reach further. Feeder status is earned by feeding, so the client cannot know a token's reach in advance and reads it from the answer. When the server cuts a range short, or answers 403 for one wholly out of reach, the section says older positions need a feeder or commercial token, links to connecting a receiver, and stops paging back. A past range stands alone on the map, without the stream's live positions joined to it, and the map frames it when it loads. The GPX download covers the range on screen and is fetched with the visitor's token when there is one.

## Search

Search asks `/v1/vessels?q=` and shows what the stream holds until the server answers. The Type and Heard chips add `type=` and `max_age=` from [lib/searchFilters.ts](app/lib/searchFilters.ts): Heard's windows start at local midnight, the start of the week in the reader's locale, the month, or the year, sent as seconds to now, rounded to the minute. The same predicates filter the stream's results, so the list does not change shape when the answer arrives. Result rows ask for photos only once they have been on screen.

## Deploy

Cloudflare Workers Builds deploys the `aiscast-web` Worker from `main` and uploads a preview version for every other branch, with a preview URL on the pull request. It builds from the repo root, where the workspace's lockfile is, with `npm run build -w client`, then runs `npx wrangler deploy --cwd client`, or `npx wrangler versions upload --cwd client` for a branch. The build writes the Worker's generated config, which wrangler finds from `client/`. Only changes under `client/` trigger a build. `npm run deploy` in `client/` does the same by hand.
