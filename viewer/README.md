# viewer

The web client for aiscast at `openwaters.io/ais/`: a live map with vessel and station pages, search, and track playback. React Router in framework mode on a Cloudflare Worker, so `/ais/vessels/:mmsi` and `/ais/stations/:id` are server-rendered and can be shared and indexed. It is a client of the aiscast API like any other.

```sh
npm install                      # from the repo root; viewer/ is a workspace
npm run dev -w viewer            # http://localhost:5173/ais/map, rendering in workerd
npm test -w viewer               # unit tests
npm run typecheck -w viewer      # generates the Worker and route types, then tsc
npm run build -w viewer
npm run preview -w viewer        # the built Worker, locally
```

`AIS_API` in [wrangler.jsonc](wrangler.jsonc) is the API the server renders against, and the root loader hands it to the browser, so pointing the app at a local server is one setting. Put `AIS_API=http://localhost:8080` in `viewer/.dev.vars` to override it locally. `AIS_TOKEN`, a secret set with `wrangler secret put AIS_TOKEN`, gives server renders their own rate limit; without it they share the anonymous one.

## Routes

`/ais/map` is the map and search, `/ais/vessels/:mmsi-:name` a vessel, `/ais/stations` the station list, `/ais/stations/:id` a station, and `/ais/network` what the network is receiving. The MMSI is canonical in a vessel URL and the name slug is cosmetic, so a wrong or missing slug redirects to the canonical form. `?station=<id>` on `/ais/map` redirects to the station page. The token page is the website's, at `/ais/token`, and the app reads the token it stores.

[wrangler.jsonc](wrangler.jsonc) lists the same top-level paths as the Worker's routes on `openwaters.io`. The website Worker answers everything else under `/ais/`. A new top-level route needs a line in both [app/routes.ts](app/routes.ts) and `wrangler.jsonc`. Built assets are written to `ais/assets/` so the assets binding serves them at the path they are linked from.

## How it works

Each route has a server `loader` for a document request and a `clientLoader` for navigation inside the app. The client loader calls the API from the browser with the visitor's token, so after the first page the Worker is not involved. A vessel the stream has already heard opens from the stream's copy while the record loads.

The map, the stream, and the vessel cache are built once in [components/Shell.tsx](app/components/Shell.tsx) and outlive every navigation, because the stream is capped at two connections per network address and remounting would spend that budget. The panels float over the map, and camera padding is measured from them, so a selected vessel centres in the part of the map you can see. A vessel opened from a list takes a second pane and leaves the list in place.

[lib/map.client.ts](app/lib/map.client.ts) is browser-only, which keeps MapLibre out of the Worker. Within the stream's area cap it draws vessels from `/v1/stream`: triangles point along heading, falling back to course; colour is ship-type class; opacity fades with age. Above the cap it draws the vector tiles from `/v1/vessels/tiles` and reloads them every 15 seconds. The viewer extrapolates nothing: it draws a vessel where that vessel last reported.

[index.html](index.html) and [token.html](token.html) are the single-page viewer that GitHub Pages serves at [openwatersio.github.io/aiscast](https://openwatersio.github.io/aiscast/) until the app is deployed.
