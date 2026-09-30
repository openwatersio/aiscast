# History and track APIs

Plan for [#32](https://github.com/openwatersio/aiscast/issues/32), and the server work the web client in [#88](https://github.com/openwatersio/aiscast/pull/88) still needs beyond it. It builds on [#63](https://github.com/openwatersio/aiscast/pull/63): the server writes the normalized stream, and the nightly packager turns closed days into `ais.positions`, `ais.receptions`, `ais.vessels`, and `ais.weather` in R2 Data Catalog.

## What the numbers say

Live volumes on 2026-09-26 from `/v1/stats`:

| Quantity | Value |
| --- | --- |
| Accepted events per day | 41.7 M |
| Events per second | 380 |
| Vessels in the cache | 54,700 |
| Stations | 37 |

Position reports are about 85 percent of events, so `ais.positions` gains roughly 35 M rows a day. The event id is 16 random bytes and does not compress, so it is still the largest column. Expect 1 to 1.5 GB a day for positions and a similar amount for receptions, about 1 TB a year, which is about $15 a month in R2 storage. Storage is not the cost that matters. Scanning is: R2 SQL bills $2.50 per TB scanned, so the layout of the tables decides what a track query costs.

The raw archive starts on 2026-08-20. The normalized writer stays off in production until the rollout in [#106](https://github.com/openwatersio/aiscast/pull/106) turns it on. Replaying raw from 2026-08-20 through it and packaging the result is the backfill this plan depends on.

## Three layers, one endpoint

A track request spans three places, and the handler stitches them:

1. **Hot**: the last 48 hours, on the box, in SQLite. The pipeline appends every accepted position report. It survives deploys, and a merge to `main` deploys, so an in-memory ring buffer would empty several times a week.
2. **Closed days**: `ais.positions` in R2 Data Catalog, queried from the Go server over the R2 SQL HTTP API. Closed days never change once the fingerprint settles, so results cache on disk indefinitely.
3. **Today before the hot window**: nothing, and that is fine once the hot window is 48 hours, because the packager closes yesterday at 01:30 UTC and the hot window reaches back past midnight of the day before.

The stitch rule is simple. Take archive rows for whole days up to yesterday, take hot rows from 00:00 today onward, and prefer hot rows where both exist.

### No Worker

#32 as written puts a Worker in front of R2 SQL. That no longer fits. `ais.openwaters.io` resolves straight to the box because UDP ingest needs it, so a Worker would live on a second hostname with a second codebase and a second deploy, and the web client and the MCP server would call across origins for the one endpoint that needs the token. The Go server can POST to `https://api.sql.cloudflarestorage.com/api/v1/accounts/{account}/r2-sql/query/{bucket}` with a bearer token exactly as a Worker would. The query backend sits behind a small interface with one method, so a DuckDB process or a Worker can replace it if R2 SQL latency or price turn out wrong.

### The lake layout

`ais.positions` and `ais.receptions` are partitioned on `day` and `bucket(mmsi, 32)`, so a per-vessel query reads one file in 32 per day, about 40 MB. `ais.positions` carries `cell`, the one-degree cell of the position as an integer, null without a position, and its files are sorted by `cell, mmsi, ts` with a row-group limit of 32,768 rows, so a bbox query prunes to about one row group per bucket file: a one-cell box reads 34 of 325 row groups in a measured six-hour sample. The id is 16 raw bytes. `ais.receptions` carries `mmsi` so per-station vessel counts are a one-table query. The packager refuses a table with a different layout rather than writing into it.

R2 SQL prunes on both. Measured on six real hours, 10 million positions, by the bytes R2 SQL reports reading for one aggregate:

| Filter | Bytes read |
| --- | --- |
| None, the whole six hours | 112 MB |
| One MMSI | 1.07 MB |
| One cell | 12.3 MB |
| One id alone | 176 MB |
| One id and its MMSI | 1.65 MB |

So the bucket prunes a vessel to about 1 percent, and the cell sort prunes a one-cell box to about 11 percent. An id alone scans the whole id column, because a random hash has no useful statistics. Every query the server sends carries `mmsi` whenever it names an id, and `day` always. R2 SQL returns the 16-byte id as base64, so the client decodes it before comparing with the hex id on the wire.

At a full day, a week's track for one vessel reads about 30 MB, and a one-cell box for one day reads about 50 MB. At $2.50 per TB scanned those are fractions of a cent, and latency, not price, is what the archive-stage spike measures next.

## The endpoints

All are additive under `/v1`, open CORS, in `openapi.json`, and counted against the 120 requests per minute budget.

### `GET /v1/vessels/{mmsi}`

The last known state of one vessel, whether or not it is in the 30-minute cache. The same GeoJSON Feature `/v1/vessels` returns, plus `first_seen` and an `attribution` member. `seen` is the last time it was heard, and `geometry` is null for a vessel whose position was never heard. A vessel never heard is a 404. An opted-out vessel is a 404 too.

Backed by a durable vessel record in SQLite: one row per MMSI with name, call sign, IMO, kind, class, ship type, flag, dimensions, draught, destination, ETA, first and last seen, and the last position with course, speed, heading, status, source, and station. About 55,000 rows today and a few hundred thousand within a year, under 100 MB with indexes. The record fits the cache as it is built:

- **The fold marks, a writer writes.** The fold already holds the cache lock, so it adds the MMSI to a dirty set and nothing more. A goroutine drains the set once a second in one transaction. The fold is the hot path, and the vessel benchmarks that run on every pull request guard it; step 1 adds one for the fold itself.
- **Replay never writes it.** The store attaches in `main` the way the normalized writer does, so the pipeline `aiscast replay` builds has none, and a replayed day cannot overwrite a live last position. An upsert also keeps the newer `seen`, so a late write cannot move a vessel backward.
- **One cell definition everywhere.** The row carries `cell`, computed by the same function the in-memory spatial index uses, which is also the lake's definition. It is indexed, so a bbox query over the record visits cells the same way the cache does.
- **One Feature encoder.** A row rebuilds a `vessel` and goes through the same encoder as the cache, so a Feature from the record and a Feature from the cache have the same shape.
- **Seeded from the snapshot.** On first boot the store takes every vessel in the cache snapshot. `ais.vessels` in the lake holds no position, IMO, dimensions, or destination, so it adds too little to be worth a seed path.

This is the first item of #32 and the item the vessel page in #88 is built on. Without it a vessel page answers 404 for most of a boat's life.

### Backdating the record from history

Only the live fold writes the record, so `first_seen` is the first time the server heard a vessel after the record existed, and a vessel last heard before that has no row at all. History has to reach the record, and it has to keep reaching it every time a historical source is ingested, not once:

- The upsert takes `first_seen` as the earlier of the stored and the incoming value, so older evidence moves it back and newer evidence never moves it forward. Position and `seen` keep their newer-wins rules, so an import of old data cannot overwrite a live position.
- The packager adds `first_ts` and the last known position (`last_ts`, `last_lat6`, `last_lon6`) to `ais.vessels`: the minimum over every day packaged for `first_ts`, latest-wins for the position, as its other fields already merge. These are additive columns.
- The server owns the merge rule, so the packager never writes SQLite. After each packaging run the server reads `ais.vessels` through the R2 SQL client and merges every row into the record. A vessel with no row gets one, with its last position from history. The live upsert cannot do this merge as it stands: it lets any non-blank incoming name or particular replace the stored one, which is right for live data and wrong for history, where the incoming value is usually older. The import fills blank fields only, takes the earlier `first_seen`, and takes a position only when it is newer.
- Every historical source lands in `ais.vessels`: the replayed normalized archive, and any archive from another provider added later. Each one backdates `first_seen` as it is packaged, with nothing source-specific in the server.

`first_seen` then means the earliest report any archive holds for the vessel, and the OpenAPI document says so from the step that lands the import.

### `GET /v1/vessels` past the 30-minute window

The cache keeps a vessel for 30 minutes after its last report, and that stays the meaning of "now" for the live map, `/v1/stats`, and the station counts. The record answers two questions the cache cannot:

- **`mmsi=` returns the last known position by default.** A caller who names a vessel wants to know where it was last heard, however long ago. An MMSI not in the cache is looked up in the record and returned as a normal Feature with its real `seen`. The MCP `get_vessels` tool follows the same rule, and its instructions stop saying a vessel unheard for 30 minutes is dropped.
- **`bbox=` takes an opt-in `max_age`.** The default stays 30 minutes, because the all-time answer for a harbor fills with vessels that left long ago and the coverage fringe fills with vessels that sailed out of range. A larger `max_age`, up to unlimited, reads the record by cell. The area cap no longer bounds the response once age is unbounded, so those answers cap at 500 rows and say `truncated`.

### `GET /v1/vessels?q=`

Name prefix or MMSI prefix over the durable record, case-insensitive, capped at 50 rows, sorted by last seen. It needs an index on the name and its own share of the rate budget. This is the global search the web client's sidebar leaves as a placeholder. The MCP `search_vessels_by_name` tool moves to the same query, so it also finds vessels not heard in the last 30 minutes.

`q` is the first of several filters on the same collection, and the design leaves room for the rest. Kind, class, ship type, and flag are typed columns in the vessel record rather than fields inside a JSON blob, so a filter on any of them is a `WHERE` clause. Filters are query parameters that combine with AND, with `q`, with `bbox`, and with `mmsi`, so `q` stops being required once another filter is present. A result set of a few hundred thousand rows scans in tens of milliseconds, so no filter needs its own index until measurements say otherwise. A search refuses parameters it does not know, so a later filter never silently changes what an older client received. The rest of `/v1/vessels` keeps ignoring unknown parameters, because existing clients may already send some.

### `GET /v1/vessels/{mmsi}/track?from&to&interval&format`

Positions for one vessel between two times, as a GeoJSON Feature: a `LineString` in `coordinates`, and in `properties` the MMSI, the resolved `from` and `to`, and arrays aligned with the coordinates for `times`, `sog`, `cog`, `heading`, and `nav_status`. `points` says how many, `truncated` says whether the cap cut the list, and `attribution` carries the credit lines of the sources that contributed. `interval=60s` thins to one point per interval. `format=gpx` returns a GPX 1.1 track, which is the export the vessel page wants. The map in #88 draws a `[lon, lat][]` today, so the GeoJSON form drops in.

Limits per request: seven days of range and 5,000 points, with a default of 1,000. Longer histories page by moving `to`. The seven days apply once tracks reach into the archive. Until then a track reaches only the 48-hour hot window, and a longer range is clamped to it.

The MCP tool `get_vessel_track` ([#80](https://github.com/openwatersio/aiscast/issues/80)) calls the same handler with the same tier gate and bumps `mcpVersion` and `server.json`.

### `GET /v1/history?bbox&from&to`

Every position inside a box during a window, as a GeoJSON FeatureCollection of points ordered by time, with the same caps and thinning as the track. This is the playback query. It has no consumer until the last stage of the web client, it is the most expensive query per call, and its cost is bounded by the `cell` sort above. Ship it after tracks, with a budget of one square degree times 24 hours per request for the feeder tier and wider by arrangement.

### Station series and coverage

The fourth item of #32 splits in two:

- **Seven days** of hourly counts already exist. Every station, every source, and the network counters keep a 168-bucket hourly ring that persists across restarts. `?series=hourly` on `/v1/stats` and `/v1/stations/{id}` serializes the arrays. No new storage.
- **Longer** series come from a nightly aggregate in the packager: `ais.station_days`, one row per station per day with events, first copies, exclusive events, distinct vessels, and the bbox heard. About 13,000 rows a year. `GET /v1/stations/{id}/history?days=` reads it through the same R2 SQL client, cached for a day.
- **Coverage tiles** ([#30](https://github.com/openwatersio/aiscast/issues/30)) are a nightly job over `ais.receptions`: H3 cells per zoom tier with sources heard and distinct vessels, written as one PMTiles archive to a public R2 bucket and served through a Cloudflare-proxied hostname, the way the chart tiles are. Tens of megabytes per build. The web client fetches tiles in view. This follows [research/coverage-map-design.md](../research/coverage-map-design.md) and replaces the bounding rectangles.

## Metering

History costs money to run and the policy already says it is a metered capability. The gate is the tier, not the vessel:

| Tier | `/v1/vessels/{mmsi}` and `?q=` | Track | `/v1/history` |
| --- | --- | --- | --- |
| Anonymous | yes | hot window only, 200 points | no |
| Personal | yes | hot window only, 1,000 points | no |
| Feeder | yes | full archive | one square degree by 24 hours |
| Commercial | yes | full archive | by arrangement |

A request past the tier's reach gets a 403 with a body that says what tier reaches it and where to get a token, so the web client can show that as a normal state. Whether anonymous callers see the hot window at all is the one metering decision to make before the track ships. The recommendation above is yes: it is what makes a shared vessel link legible, and it costs a SQLite index scan. Until the archive stage lands, every tier reaches only the hot window, so a request past it is clamped to the window rather than refused, and the answer's `from` and `to` say what was covered. The 403 arrives with the archive, when a feeder can reach further than an anonymous caller.

## Opt-out

Opt-out is a separate feature, built when the first request arrives, and nothing in this plan waits on it. The policy commits to a suppression list applied at fan-out and in history. When it is built it is a table in the same SQLite file, a check in each read path and the fan-out, `noindex` on the vessel page, admin endpoints to manage the list, and a packager step that deletes the vessel's rows from the lake. A request should be checked by a person, so there is no self-service.

## Sizing the box

| Store | Contents | Steady size |
| --- | --- | --- |
| SQLite `vessels` | one row per MMSI ever heard, indexed on name and cell | under 100 MB |
| SQLite `tracks.db` | every accepted position for two to three days, one table per UTC day keyed by `(mmsi, ts)` | 70 to 105 M rows, 4 to 6 GB |
| SQLite `stations` | station names | negligible |
| Query cache | closed-day track and history results | capped at 2 GB, LRU |

Writes are about 400 positions a second, batched in one transaction a second. Tracks live in their own file, because the record is the file an operator copies when replacing the box. Each UTC day is its own table, so expiry drops a table instead of deleting 35 M rows. Measured on a laptop, a flush of 400 positions takes 6 ms against a day table of 400 k rows and 22 ms against 10 M rows. Each position lands at a random MMSI and dirties its own page, so the write-ahead log sees on the order of a page per position, over a megabyte a second. If disk writes on the box turn out to matter, the alternative is a per-vessel ring in memory, about 2 GB, saved on shutdown like the vessel snapshot. `modernc.org/sqlite` keeps the build pure Go. The 160 GB NVMe has room for ten times this.

## Order of work

Each step is one pull request with tests, `openapi.json`, the server README, and [docs/limits.md](../docs/limits.md) updated together, plus its monitoring:

- New routes register as mux patterns with wildcards, such as `/v1/vessels/{mmsi}/track`. The request counter labels by pattern, so this keeps one series per route instead of one per MMSI.
- Routes that do real work join the request latency histogram beside `/v1/vessels` and `/mcp`.
- The store exports write batch latency, rows written, and file size. The R2 SQL client exports query latency, bytes scanned, and cache hit rate.
- Alert rules go in [server/deploy/grafana/rules.yaml](../server/deploy/grafana/rules.yaml) with panels on the capacity dashboard. Bytes scanned per day gets a budget alert, because it is the one line on the bill that grows with traffic.


1. **The store and the durable vessel record.** SQLite behind an interface, the `vessels` table fed from the fold, `GET /v1/vessels/{mmsi}`, the `mmsi=` fallback and `max_age` on `/v1/vessels`, `GET /v1/vessels?q=`, and the matching MCP tool changes. This unblocks indexable vessel pages and the sitemap.
2. **Recent positions and the hot track.** `tracks.db`, `GET /v1/vessels/{mmsi}/track` over the hot window with thinning and GPX, the tier gate, and `get_vessel_track` on `/mcp`. No in-memory ring.
3. **The backfill.** `aiscast replay` over the raw archive from 2026-08-20 and a full packaging run, once #63 is deployed.
4. **The archive stage.** The R2 SQL client, a spike that measures latency for the three query shapes and records it in the README, the disk cache, tracks stitched over closed days, and `/v1/history`. It also lands the record import: `first_ts` and the last position in `ais.vessels`, and the nightly merge that backdates `first_seen` after every packaging run. This is what closes #31's verification and #32.
5. **Series and coverage.** `?series=hourly`, `ais.station_days`, `GET /v1/stations/{id}/history`, and the coverage tile job.
6. **More vessel filters.** `kind`, `class` (A or B), `type` as a category such as cargo, tanker, passenger, fishing, sailing, or pleasure, mapped from the ITU ship type codes, and `flag` as a country code, on `/v1/vessels` and in the MCP search tool. Columns and parameter handling come with step 1, so this step is the category mapping, validation, tests, and the OpenAPI document.

## What the web client still needs from the server

What the web client needs next, with what each needs stored. Items that need no server change are listed at the end.

| Client need | Endpoint | Storage | Size |
| --- | --- | --- | --- |
| Vessel page that always renders, sitemap | `GET /v1/vessels/{mmsi}`, `GET /v1/vessels?updated_since=` for the sitemap | SQLite `vessels` | under 100 MB |
| Global name search | `GET /v1/vessels?q=` | index on `vessels.name` | tens of MB |
| Seven-day charts on station and network pages | `?series=hourly` on `/v1/stats` and `/v1/stations/{id}` | none, the rings exist | 0 |
| Station names ([#51](https://github.com/openwatersio/aiscast/issues/51)) | `PUT /v1/stations/{id}` signed by the station's token, `name` in the list | SQLite `stations` | negligible |
| Heard-first per station ([#53](https://github.com/openwatersio/aiscast/issues/53)) | a field in `/v1/stations` | a counter in the existing usage file, or `ais.station_days` for the exact number | 0 |
| A map above the area cap | `GET /v1/vessels/summary?bbox` returning vessels per one-degree cell, read straight from the spatial index the cache already keeps | in memory | 0 |
| Recent track on the vessel page | `GET /v1/vessels/{mmsi}/track` | SQLite `tracks.db` | 4 to 6 GB |
| Time range, GPX and GeoJSON export | same endpoint over the lake | `ais.positions` with the bucket partition | 1 to 1.5 GB a day |
| Playback over a bbox | `GET /v1/history` | `ais.positions` with the `cell` column | same table |
| Real coverage cells ([#30](https://github.com/openwatersio/aiscast/issues/30)) | PMTiles from a public bucket | nightly over `ais.receptions` | tens of MB per build |
| Station history beyond seven days | `GET /v1/stations/{id}/history` | `ais.station_days` | 13,000 rows a year |
| `/v1/stations/{id}` scans the vessel map and sorts the station list twice | a station index in the pipeline | none | 0 |

No server work: the deploy of Astro beside the Go binary, retiring `viewer/index.html` and `viewer/token.html`, the coverage fallback above the area cap, live charts from the stream, the network page's vessel counts and `/health` (both exist), and the station list's source kind, vessels heard, and duplicates (all in `/v1/stations` already).

Accounts for the web client add tables to the same SQLite and is the reason to introduce it in step 1 rather than later.

## Decisions to make

1. **Go calls R2 SQL directly**, no Worker. Recommendation: yes, for the reasons above, behind an interface.
2. **Anonymous callers get the hot window.** Recommendation: yes, capped at 200 points.
3. **`/v1/history` waits.** It has no consumer before the last client stage. Recommendation: tracks first, history when the tile and playback work starts.
