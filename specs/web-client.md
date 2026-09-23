# Web client

The viewer becomes an application: a persistent sidebar beside the map, real routes, vessel and station pages, search, and eventually an account. It stays what it is today, a client of the public API like any other, so every capability it gains is a capability any other client gains.

The staging below is driven by one constraint: most of what the app needs does not exist as an API yet. Stage 1 ships the whole shell on the API as it stands. Each later stage adds the smallest server change that unlocks the next group of features, cheapest first.

## What exists today

`viewer/index.html` is a single static page: a MapLibre map, a `/v1/stream` WebSocket subscribed to the viewport, a click popup, and a `?station=` mode that fades everyone else's vessels and prints one station's counters. `viewer/token.html` mints a personal token in the browser. GitHub Pages serves both from `viewer/` with no build step, at `openwatersio.github.io/aiscast/`.

The API the app can use today:

| Endpoint | Gives the app |
| --- | --- |
| `GET /v1/vessels?bbox=&mmsi=` | GeoJSON of current positions, for the initial paint and for MMSI lookup |
| `WS/SSE /v1/stream` | Live events with the full decoded AIS message, plus `snapshot: true` to replay each tracked vessel's last position and last static report |
| `GET /v1/stations` | All 27 stations with 24 h and 7 d event counts, vessels heard, duplicates, coverage bbox, last seen |
| `GET /v1/stations/{id}` | One station's counters plus the vessels it was latest to hear |
| `GET /v1/stats` | Network totals, per-source event counts, vessel counts by kind, delay percentiles, client counts |
| `POST /v1/keys` | A personal token for a browser-generated Ed25519 key |
| `GET /health`, `GET /openapi.json` | Status and the machine-readable spec |

Two things about `/v1/stream` matter more than they look. Every event carries the complete go-ais struct, so a `ShipStaticData` frame delivers call sign, IMO, dimensions, draught, ETA, and destination even though `/v1/vessels` drops all of them. And a subscription's `bbox` and `mmsi` filters are ORed in one frame, so a single connection can serve the viewport and a set of followed vessels at once. Together those two facts are why Stage 1 can build a genuinely detailed vessel page without touching the server.

What does not exist: search by name, a per-vessel endpoint, any position history at all, per-station or network time series in a shape a chart can read, station names, and any notion of a user.

## Shape of the app

One page, one WebSocket, a sidebar that owns the routes, and a map that persists across all of them.

```
/                     map, with the vessel list for the current viewport
/vessels              search results and the list for the current viewport
/vessels/:mmsi        vessel detail; map follows and highlights
/stations             station list, coverage rectangles drawn on the map
/stations/:id         station detail; map fits its coverage
/network              network status: sources, rates, delays, health
/token                get a token, see what it allows, name your station
/account              (stage 5)
```

The map is never torn down. Routing swaps the sidebar panel and asks the map for a camera change and a highlight, nothing more.

`/vessels/368168720` is the URL a skipper sends their family and the URL a search engine indexes. It pairs with `/stations/:id`, it gives `/vessels` a real index page to be, and it matches what the API already calls this collection. Vessel and station URLs are the product's public surface, so they are real paths that a server answers with real HTML, never a fragment resolved after a bundle loads.

The vessel's name joins it as a slug, so the canonical form is `/vessels/368168720-cerulean`. The slug makes the URL match the query someone actually types, which is a boat's name far more often than its MMSI, and it is visible text in the result Google shows. Names are neither unique nor permanent, so the MMSI stays canonical and the slug stays cosmetic: any slug resolves, a wrong or missing one redirects once to the current canonical form, and `<link rel="canonical">` always names that form. A vessel with no known name is just `/vessels/368168720`.

**One stream, always.** Anonymous clients get two concurrent streams per network address, which one household or one marina wifi shares. The app therefore holds exactly one connection and rebuilds a single `subscribe` frame from the current viewport plus the set of followed MMSIs. A second browser tab is a second stream, which is the budget spent. Everything else, meaning stations, stats, and MMSI lookups, goes over the 120 requests per minute HTTP budget.

**The zoomed-out view is a coverage view.** Anonymous subscriptions cap at 100 square degrees and a personal token only reaches 400, so there is no tier at which the app can stream the world. Rather than show an empty ocean, the app switches above the cap to the coverage layer: station footprints, per-source counts, and where the network hears traffic. That is the honest picture at that zoom, and it is also the "what is on the feed" page the project wants (#30). Vessels reappear when the viewport fits the cap again.

**Attribution is per source, not a footer.** Licensing is per source and never merged, so the app keeps crediting from each event's `attribution` field the way the current viewer does, and carries it into the vessel and station pages too.

## Decisions to make before Stage 1

**Rendering is the decision, and it settles hosting and the framework with it.** A vessel page has to work three ways: a person opens it, a chat client unfurls it, and a crawler indexes it. Only the first works in a client-rendered app. An unfurl bot never runs JavaScript, so a shared link to a boat shows the hostname and nothing else. A crawler does run JavaScript, but it queues rendering separately from crawling and spends that budget grudgingly on pages that arrive empty. Fifty thousand near-identical empty shells is the worst thing to put in front of one.

So `/vessels/:mmsi` and `/stations/:id` are answered with real HTML: a title naming the vessel, a meta description, Open Graph and Twitter tags, a canonical URL, JSON-LD, and the current facts in the markup. The app takes over for the live parts. What renders that HTML is the open question, and one constraint rules out the most obvious answer before the options start.

**`ais.openwaters.io` cannot go behind Cloudflare.** It accepts UDP NMEA on port 10110, which is how six volunteer stations feed today, and Cloudflare does not proxy UDP. Proxying the hostname repoints its A record at Cloudflare's anycast addresses, and every UDP feeder starts sending into a black hole. UDP is fire and forget, so nothing reports an error: the station simply stops appearing, and the operator has no signal that anything broke.

There is also no way to warn them. Station identity is a keyed hash of the sender's address and never the address itself, which is a deliberate privacy property, and its consequence is that the project cannot contact its own UDP feeders. A migration that requires them to act cannot be announced. The hostname is meanwhile baked into third-party configuration the project does not control, including AIS-catcher's `-u ais.openwaters.io 10110` and docker-shipfeeder's `UDP_FEEDS`, with outreach in flight to spread it further.

Worth fixing in the repo regardless of this plan: `server/deploy/README.md` presents Cloudflare proxying as a switch to flip for DDoS cover, paired with `TRUST_CF_HEADERS=1`, and says nothing about UDP. As written it invites someone to silently disconnect every volunteer station during an incident, which is the worst possible moment.

That leaves three options.

**Option A, the Go server renders.** It already holds the vessel cache in memory, so an `html/template` shell costs one map lookup. No new service, no second hop, no API call to itself, and no dependency on an endpoint that does not exist yet. Assets ship in an `embed.FS`, same origin as the API, which removes CORS rather than parameterizing it. The costs: the Go binary grows a template layer and an asset bundle, a viewer deploy becomes a server deploy, and the client router and sidebar are hand-rolled.

**Option B, Astro renders on the box.** The shape, concretely:

```
ais.openwaters.io (DNS-only A record, straight to the box)
  :443  Caddy
          /v0/*, /v1/*, /health, /metrics  ->  127.0.0.1:8080   aiscast (Go)
          everything else                  ->  127.0.0.1:4321   Astro (@astrojs/node)
                                                   |
                                                   `-- renders /vessels/:mmsi and /stations/:id
                                                       by calling 127.0.0.1:8080 over loopback
  :10110/udp                               ->  aiscast, untouched
```

The hostname keeps resolving to the box, so UDP ingest never enters the picture. The API call the vessel page makes is loopback, so the second hop that ruled against edge rendering costs nothing worth measuring, and the Go server stays a pure data server with no view layer.

What it buys is what Astro is good at and what this app is shaped like: mostly server-rendered HTML with one interactive island, file-based routing, per-route `<head>`, `@astrojs/sitemap`, and the same stack the website already runs with React islands, Tailwind, `maplibre-gl`, and `react-map-gl`. The website is `output: "static"` with per-route opt-in SSR, which is exactly the mode this app wants. Astro is Vite underneath, so the seascape precedent is not contradicted. Only the adapter differs from the website, `@astrojs/node` in place of `@astrojs/cloudflare`, so the code moves between them later if the hosting question is ever reopened.

The cost is a Node runtime on a box that runs one Go binary today, plus a systemd unit and a second artifact in the deploy. `server/deploy/rootfs/` is built for precisely that addition. Two operational notes: Caddy's `grace_period` is 10 s so that reloads do not wait on open SSE connections, and the same reasoning applies to restarting Astro; and a crawler hitting a cold or failed Node process must not take the API down with it, so the Caddy route needs to fail independently of the `/v1` routes.

**Option C, Astro on Cloudflare at a different URL.** If the URL shape is negotiable, this is the cheapest of the three. The app builds in the website repo or in this one, deploys to Cloudflare, and `ais.openwaters.io` is never touched, so UDP is not even a consideration. It gives up `ais.openwaters.io/vessels/368168720` for something under `openwaters.io/ais/` or a new proxied subdomain, and if it lives in the website repo it also moves the app away from the API it consumes and onto the marketing site's release cadence.

Option B is the choice. It keeps the URL the project wants, keeps the volunteers feeding, keeps the Go server a data server, and answers the framework question by adopting the one the org already runs. Option A is the fallback if a Node runtime on the box turns out to be unwelcome; Option C is the fallback if the URL matters less than avoiding a second process. `ci.yml` gains a Node job under all three, because the artifact is now something CI produces.

If `ais.openwaters.io` behind Cloudflare ever becomes worth having, the migration is possible but deliberate: give the box a second address, point a new `feed.openwaters.io` at it, document that as the UDP target, bind UDP on both, and watch the old address go quiet. That measurement is the only way to know when flipping is safe, precisely because the feeders cannot be asked. It is a lot of machinery for a URL, and nothing in this plan needs it.

**Whatever renders, the map survives navigation.** This is the one requirement that constrains the framework choice, and it is easy to lose in Astro's multi-page model. Tearing down the document on every navigation tears down MapLibre, the WebSocket, and the client's warm vessel cache, and the stream budget is two connections per network address. So the arrangement is: the server renders the entry document for a direct hit or a crawl, and after hydration a client island owns the map, the stream, and in-app routing. Clicking from a vessel to a station never remounts the map. Astro supports this through a persisted island, and it must be proven in the first week rather than assumed, because the whole app design rests on it.

**GitHub Pages is not where this lands.** A URL is an asset that accrues value and costs something to move, so the app should be born at the address it keeps. Pages' only deep-link mechanism is a `404.html` fallback, which answers every vessel URL with an HTTP 404 status and then rewrites the page in JavaScript. Crawlers treat that as the soft 404 it is, and no amount of content fixes a 404 status line. Pages also cannot render per-vessel HTML at all. It stays as long as it takes to stand the app up, and no longer.

Three things hold from the first commit whichever option wins: paths are real paths and never hash fragments, the API base stays configurable so `?server=` keeps working against a local server, and nothing is submitted to a search engine until the URL is final.

**Vessel pages are indexed by default, and the opt-out is the control.** This is the project's existing policy applied to a page rather than a new question: publish all, honor opt-outs. `/v1/vessels?mmsi=` already serves a named vessel's current position to anyone, with no token and open CORS, under licenses that invite redistribution. Withholding the same facts from a crawler while the API hands them to any script would protect nobody and cost the project the traffic. Every other AIS site publishes small craft by default and honors removal requests, which is also what the policy here already commits to, so the reasonable expectation of a boater transmitting AIS is that their vessel is visible.

This covers tracks too, not only current position. Every AIS site publishes vessel history, so a track is no more novel than a position, and the same reasoning applies: the data is broadcast in the clear and already served by this API under licenses that invite redistribution.

So: index by default, publish by default, no special-casing by vessel class, and the vessel opt-out is the single control. It sets `noindex`, withdraws the page, and applies alongside the suppression already specified at fan-out and in history. History is still metered by tier, but that is a funding decision about what the query infrastructure costs to run, not a privacy gate, and the two should not be confused in the UI.

One filter remains and it is about page quality rather than privacy, in Stage 2 with the sitemap: a vessel with a name, a type, and recent activity is worth offering a crawler, while a bare MMSI heard once is thin content that spends crawl budget and earns nothing.

Note for the archive work: `specs/archive.md` on the `lake` branch says first-party products ship aggregates only, "not tracks of named pleasure craft." That is the opposite of what this says. One of the two should change, and since this one reflects the decision, that one needs updating so the next reader is not handed two answers.

**Who owns the token flow.** `openwaters.io/ais/token` on the marketing site is the canonical, linked-everywhere URL. The app needs its own token view regardless, because a token holder has to manage a station somewhere. Stage 1 ports the flow into the app and leaves `viewer/token.html` in place as a redirect. Whether the marketing page then becomes an explainer that links into the app is a call to make with the website repo, not silently.

## Stage 1: the app, and the pages worth linking to

Almost every feature here reads an API that exists today. Rendering and robots are the exception, and they are in Stage 1 because a shareable, indexable vessel URL is a stated goal and retrofitting it later means changing URLs people have already sent each other.

- [x] Prove the persistent map first. A server-rendered route, a hydrating island that owns MapLibre and the WebSocket, and a navigation between two routes that does not remount either. Everything below assumes this works, so it is the first thing built, not the last.
- [x] The build: TypeScript, `viewer/` as a workspace, MapLibre as a dependency instead of a CDN script. Astro with `@astrojs/node` under Option B, or Vite alone under Option A.
- [ ] Under Option B, the box grows a second service: a systemd unit beside `aiscast.service`, a Caddy route sending `/v0`, `/v1`, `/health`, and `/metrics` to Go and everything else to Astro, and a deploy that ships both artifacts. `ais.openwaters.io` stays DNS-only and UDP ingest is not touched.
- [x] App shell. The map is the page, full bleed, with floating panels over it: a sidebar holding search and the destinations, and a second panel that appears only when a route has content. Map state stays in the URL hash. Camera padding is measured from the panels so a selected vessel centres in the visible map rather than behind them.
- [x] Server-rendered `/vessels/:mmsi` and `/stations/:id`. Title, meta description, Open Graph and Twitter tags, canonical URL, JSON-LD, and the current facts in the markup, with the app hydrating over it. The vessel route reads `GET /v1/vessels?mmsi=`, which already exists and is anonymous-safe, so the per-vessel endpoint stays in Stage 2 after all.
- [x] A vessel's country from its MMSI MID prefix. A pure lookup table, no data cost, and it turns a bare number into something a page can be titled with and a person can recognize.
- [x] `robots.txt` stops being `Disallow: /`. It currently blocks the whole host on purpose, because crawlers were finding `/v1/vessels` and `/v1/receive` in the code samples on `openwaters.io/ais/` and reporting the 4xx answers as errors. The replacement keeps `/v0` and `/v1` disallowed and allows the app's pages, which preserves the original intent. It only takes effect once Caddy routes `/robots.txt` to the app.
- [ ] Vessel pages are indexable by default. The `noindex` control exists and is used for pages with nothing to show, but there is no opt-out list to wire it to yet. That list is the Stage 2 piece; this stays open until it has a source.
- [x] One stream manager: single connection, reconnect with backoff, subscription rebuilt from viewport plus followed MMSIs, welcome frame parsed so the app knows its own limits. It uses the browser's stored token when there is one, which is what lifts the view from 100 square degrees to 400.
- [x] Search. MMSI goes to `/v1/vessels?mmsi=`, which works anonymously and resolves a vessel anywhere in the world. Name search runs over the vessels the client has tracked this session, labelled as exactly that. Global name search is Stage 2 and the placeholder text should not promise it before then.
- [x] Vessel page. Subscribing by MMSI with `snapshot: true` returns the last position and the last static report, so the page shows call sign, IMO, dimensions, draught, ETA, and destination alongside position, course, speed, and navigational status. It also shows source, station, license, attribution, and the raw NMEA. Following works worldwide because MMSI subscriptions ignore the area cap. The server-rendered position seeds the client cache, so the vessel draws even when the stream is refused.
- [x] Session track. The client keeps the positions it receives while open and draws them as a line. It is not history and the page says so, but it is the right renderer for Stage 3 and it makes a followed vessel legible immediately.
- [ ] Station list from `/v1/stations`, sorted by 24 h events, showing source kind, vessels heard, duplicates, and last seen. It currently shows the id, 24 h events, and age; the rest is missing.
- [x] Station page from `/v1/stations/{id}`: counters, coverage rectangle on the map, and the vessels it was latest to hear. Replaces `?station=`, which redirects. Station ids are stable across deploys, because `STATION_SALT` keys the UDP hashes on the box, so these are permalinks a volunteer can share.
- [ ] Coverage view: every station's bbox drawn together, the aggregate the project has not shown yet. The rectangles draw on the station and network routes; what is missing is the map falling back to them above the area cap instead of showing empty water.
- [ ] Network page from `/v1/stats`: per-source event rates, delay percentiles, exclusive vessels per source, and station counts are in. Vessel counts by kind and `/health` are not.
- [ ] Live charts built from the stream itself. Every event carries `station` and `source`, so the app can plot events per minute per source and per station for as long as it stays open, with no server support. Historical charts wait for Stage 2.
- [x] Token view: the current flow, plus what the token allows. The token this browser holds is used for its own stream automatically, which is the 400 square degrees and 50 messages per second. Minting stays an explicit action behind the contributor agreement, never automatic.
- [ ] Retire `viewer/index.html` and `viewer/token.html`. They are still what GitHub Pages serves, and the README, the status monitor, and the marketing site all point at it, so they stay until the box serves the app.

## Stage 2: the cheap server additions

Most of these are small and need no new storage. The first one is the exception, and it is here because both SEO and the vessel page depend on it.

- [ ] **A durable vessel record.** The cache drops a vessel 30 minutes after its last report, so `/vessels/:mmsi` for a boat sitting at a dock has nothing to render. A page that answers 404 for most of its life cannot be indexed, and a link a skipper sent their family stops working the moment the boat stops moving. That makes indexing depend on keeping a small per-MMSI row that outlives the cache: name, call sign, IMO, type, dimensions, country, first seen, last seen, and the last known position. Roughly 52,000 rows today and a few hundred thousand within a year, which is a SQLite file. It is also the same SQLite that Stage 5 needs for accounts, so introducing it here means introducing it once. A vessel page then always renders, saying plainly when it was last heard.
- [ ] **Global vessel search.** `GET /v1/vessels?q=` matching name prefix and MMSI. Over the live cache that is a scan of roughly 52,000 entries, trivial in Go; over the durable record above it also finds vessels not currently being heard, which is what a person searching a boat name expects. Needs a result cap and its own rate limit.
- [ ] **`GET /v1/vessels/{mmsi}`** (#32, first item). Last known state including the static fields the cache currently drops. The cleanest version widens the `vessel` struct to keep call sign, IMO, dimensions, draught, ETA, and destination, which also makes them survive a restart, rather than reaching only clients who happen to catch a type 5. This and the durable record are the same work approached from two directions and should be done together. Under Options B and C the endpoint itself moves to Stage 1, because the vessel page is built on it; what stays here is backing it with the durable record instead of the 30-minute cache.
- [ ] **Sitemap.** A sitemap index over the vessels worth indexing, generated from the durable record, plus stations and the static pages. Gate entries on having enough substance to be a real page, meaning a name and a recent enough last-seen, and on the opt-out list. Fifty thousand thin pages spend crawl budget and earn nothing; a few thousand good ones earn more. Note this gates the sitemap, not the pages: an unlisted vessel page is still indexable if a crawler reaches it by link.
- [ ] **Hourly series.** Every station already carries a 168-bucket hourly ring, and so do network events, duplicates, streams, requests, and each source. All of it is persisted across restarts. Exposing the arrays is a serialization change of roughly twenty lines and it turns every chart in the app from "since you opened this tab" into seven real days.
- [ ] **Station names** (#51) and **heard-first counts** (#53). A token holder sets a display name by signing with the key they already have. Heard-first is the number that says what a station adds, where `duplicates` says the opposite. Together they make the station page something an operator wants to share, and they are the precondition for the OG image (#52) and for any leaderboard.
- [ ] **A clustered or tiled overview.** The 100-square-degree cap means no tier can stream a world view. A pre-aggregated count per cell, served as tiles with a short cache lifetime, gives the app a real zoomed-out map and cuts fan-out cost at the same time. This is the draft on cached tiles, and it is the difference between a coverage map and a world map.
- [ ] `GET /v1/stations/{id}` currently scans the full vessel map and rebuilds and sorts the whole station list twice per request. Fix it before station pages get traffic.

## Stage 3: recent tracks without the archive

A per-MMSI ring buffer in memory, a few hundred positions per vessel, decimated on write. It costs bounded memory, needs no new infrastructure, and serves `GET /v1/vessels/{mmsi}/track` for the recent window, which is the window a chart client actually wants. The Stage 1 session-track renderer draws it unchanged.

This is deliberately placed before the archive work. It delivers most of the perceived value of tracks at a fraction of the cost, and it lets the API shape settle before anything expensive is built behind it.

Both the depth of the window and who may ask for it follow the policy: history is a metered tier, and the opt-out suppression list applies to tracks the moment tracks exist.

## Stage 4: history from the archive

This is #31 and #32, and it is the first stage that costs real money to run.

- [ ] Normalized events written to Parquet in R2, with both query paths verified first (#31). Today's archive is source-native, keyed by source and hour, and write-only: the S3 client can PUT and HEAD and nothing else. Answering "where was this vessel yesterday" against it means fetching and decoding every source's hour files, so a normalized, MMSI-partitioned store is not an optimization, it is the enabling work.
- [ ] `GET /v1/vessels/{mmsi}/track?from&to` over the archive, stitched onto the Stage 3 hot window.
- [ ] `GET /v1/history?bbox&from&to`.
- [ ] Coverage heatmap tiles, the H3 version the research already designs, replacing Stage 1's bounding rectangles (#30).
- [ ] App: a time range on the vessel page, GPX and GeoJSON export, playback over a bbox, and real coverage cells.

One constraint binds this stage and should shape the UI rather than being bolted onto it. History is a feeder and commercial capability, because the query infrastructure costs money to run, so the app has to handle "you need a token for this" as a normal state rather than an error. That is a metering decision, not a privacy one; the opt-out list is what handles privacy, here as everywhere else.

## Stage 5: accounts

Worth being clear about the size of this. The server has no user, no session, and no cookie. Its entire persistence is two JSON files rewritten whole every ten seconds, which is fine for counters and unusable for account data. Identity today is a device key: a personal token's `sub` is literally the Ed25519 public key that asked for it.

The datastore is the one piece Stage 2 already brought in, for the durable vessel record. Accounts, station names, and revocation are more tables in the same SQLite file rather than new infrastructure, which is most of the reason to introduce it early rather than here.

The graft that does not fight the design is to keep tokens as the only wire credential and make OAuth a minting interface. A user signs in with Google or GitHub, the account is linked to one or more device keys, and the app still talks to the API with a token. `signToken` and `verify` stay untouched.

- [ ] Account, session, and revocation tables in the SQLite introduced at Stage 2.
- [ ] OAuth against Google and GitHub, sessions, CSRF. Note that every handler currently sends `Access-Control-Allow-Origin: *`, which browsers reject alongside credentialed requests, and that the WebSocket accepts any origin. Both are safe with bearer tokens and become a problem the moment ambient cookie auth exists. The session endpoints need their own origin policy, and this is the stage that forces the hosting decision above.
- [ ] Runtime revocation. `REVOKED_SUBS` is an environment variable read once at boot, so deleting an account cannot currently take effect without a restart.
- [ ] App: sign in, claim and name your stations, manage keys, see your usage and tier, and a history entitlement that follows the account rather than the key.

Proof of possession is a known gap, flagged in `auth.go`: a personal token is a bearer token and anyone holding it is that device. `bind_ip` is the only mitigation today. Accounts are the natural moment to fix it, not a reason to delay them.

## Constraints that run through every stage

- Anonymous gets 2 concurrent streams per address, 100 square degrees, 20 messages per second, 10 followed MMSIs, and 120 requests per minute. Personal raises those to 2, 400, 50, and 50. The app is built for the anonymous numbers and treats a token as an upgrade.
- `/v0` is frozen. Everything new is additive under `/v1`.
- The viewer draws a vessel where that vessel last reported and extrapolates nothing. That stays true however app-like the client becomes.
- Every source carries its own license and its own credit line, and the consumer's obligation is to display the attribution that came with the event.
- Station locations are never asked for and derived locations are shown only coarse. A coverage footprint is a bbox, never a pin.
- Vessel opt-out applies at fan-out and in history queries, and extends to pages: an opt-out sets `noindex` and withdraws the page. It is the single control, and everything else is published.
- History is metered by tier because the query path costs money, not because of what it reveals. Keep that distinct from the opt-out in both the code and the wording the user sees.
- URLs are the part of this that is expensive to change. `/vessels/:mmsi` and `/stations/:id` are settled before anything is submitted to a search engine, and they keep working afterwards. The MMSI is canonical; a name slug, if adopted, is cosmetic and redirects to it.
- `ais.openwaters.io` resolves straight to the box and stays that way. UDP ingest on 10110 depends on it, Cloudflare cannot proxy UDP, and the feeders it would disconnect cannot be identified or warned. Any proposal that puts a proxy in front of that hostname has to solve UDP first.
