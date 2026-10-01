# aiscast server

AIS ingest → reassemble → dedupe → decode → bbox fan-out, with an aisstream.io-compatible WebSocket at `/v0/stream`. One Go binary. See [docs/architecture.md](../docs/architecture.md) for the design.

```sh
ALLOW_ANON=1 go run .          # Kystverket upstream on, HTTP :8080, UDP :10110, archive/ in cwd; no tokens needed
go test ./...
```

[openwaters.io/api/ais](https://openwaters.io/api/ais/) documents the endpoints. `/mcp` is the MCP (Model Context Protocol) endpoint for AI assistants: Streamable HTTP, stateless, six read-only tools over the vessel cache, the vessel record, recent tracks, and station stats (`mcp.go`, `track_api.go`), the same claims and rate limit as `/v1/vessels`. `server.json` at the repo root is its registry listing; its version and `mcpVersion` move together. Operator-only: `GET /metrics` serves Prometheus text (events, duplicates, parse/decode failures, client drops, rate-limit rejections, unmapped source fields and record types, vessels, open streams by protocol and tier, fan-out sends and bytes, HTTP requests by route and status with latency histograms for the vessel routes and `/mcp`, per-source event counts, last-event age, and delay percentiles, archive upload failures and staged bytes, `aiscast_build_info` with the git revision of the binary, and the standard `process_` series). The alert rules and dashboard in [deploy/grafana/](deploy/grafana/) query these names.

Environment:

- `ADDR` (`:8080`), `UDP_ADDR` (`:10110`): comma-separated UDP listeners as `[label=]host:port`, labels unique. `/metrics` counts datagrams per label, so a listener per address shows which name feeders send to.
- `KYSTVERKET` (`1`), `KYSTVERKET_ADDR`.
- `BARENTSWATCH_CLIENT_ID` + `BARENTSWATCH_CLIENT_SECRET` (set = BarentsWatch upstream on), `BARENTSWATCH_URL`.
- `DIGITRAFFIC` (`1`), `DIGITRAFFIC_URL`.
- `AISSTREAM_API_KEY` (set = aisstream.io upstream on), `AISSTREAM_BBOX` (world), `AISSTREAM_URL`.
- `AISHUB_FEED` (`data.aishub.net:<port>`): forward volunteer-station events to AISHub as plain `!AIVDM`. The server never forwards public feeds or synthesized events, per their terms.
- `AISHUB_USERNAME` (set = poll AISHub's aggregate snapshot), `AISHUB_INTERVAL` (`20s`, the limit AISHub set for our account).
- `ARCHIVE_DIR` (`archive`).
- `NORMALIZED_DIR` + `NORMALIZED_BUCKET`: the normalized archive, off unless one of them is set. With only the bucket, staging defaults to `normalized`; with only the directory, it stays local and nothing reclaims it. It is written at emit as one merged hourly gzip of versioned JSON envelopes: an `event` record per accepted message (the `/v1` event, persisted), a `copy` record per delivery heard with its license (the first copy included), and BarentsWatch `methyd` weather broadcasts verbatim. Uploads to `NORMALIZED_BUCKET`, normally the raw archive's own bucket, under `normalized/v1/`, with the same R2 credentials. Every raw key starts with a license tag, so the stream sits beside them without ever reading as one, and `aiscast replay` walks past it. `aiscast replay -archive <raw> -out <dir> -from YYYY-MM-DD -to YYYY-MM-DD` regenerates it from archived raw days through the same adapters, deterministically; `-warmup` (90m, the corroboration window plus the vessel TTL) replays a lead-in for dedupe, trust, vessel, and per-source state without writing it. `aiscast normdiff -live <dir> -replay <dir>` compares a tree written live against one replayed from the same days and exits non-zero on any divergence beyond the two that carry no data: which source won a copy race, and the per-process multipart sequence id.
- `DEDUPE` (`dedupe.json`): the dedupe window, saved on shutdown and restored on boot so a restart cannot re-accept a copy inside the 10 s window.
- `R2_BUCKET` + `R2_ACCOUNT_ID` + `R2_ACCESS_KEY_ID` + `R2_SECRET_ACCESS_KEY`: unset = archive stays local. Otherwise the server PUTs each hour to R2 over the S3 API on rotation and on shutdown. Use `S3_ENDPOINT`/`S3_REGION` for non-R2 targets.
- `ISSUER_PUBKEYS` (`kid:base64url-pubkey,...`): the issuers whose tokens aiscast accepts.
- `PERSONAL_ISSUER_KEY` (`kid:base64url-seed`): lets `POST /v1/keys` mint personal-tier tokens.
- `REVOKED_SUBS` (comma list).
- `LOCKED_STATIONS` (comma list of station ids): moderation. A locked station shows no operator or vessel name and refuses new names, and keeps publishing.
- `ALLOW_ANON=1`: no tokens needed, local development only.
- `STATION_VESSELS` (`station-vessels.json`): each station's vessels from the last 24 hours, behind `vessels_24h` and `vessels_exclusive_24h` on `/v1/stations`. Written on shutdown and restored on boot.
- `USAGE` (`vessels-usage.json`): the rolling 24 h/7 d counters behind `/v1/stats`, written every minute and on shutdown, and restored on boot.
- `STORE` (`aiscast.db`): the vessel record, in SQLite. The vessel cache is restored from it on boot. `off` runs without it, and without tracks, and a restart starts with an empty map. It also holds the `stations` table: names, coverage labels, and which keys must sign their token requests. A signed request is saved there before its token is issued, so without the record those locks last only as long as the process. See [Vessel record](#vessel-record).
- `TRACKS` (`tracks.db`): every position of the last 48 hours, in SQLite, for tracks. `off` runs without it. See [Recent tracks](#recent-tracks).
- `WIKIDATA` (`1`): sync vessel particulars from Wikidata weekly into the vessel record. `0` turns it off. `WIKIDATA_URL` (`https://query.wikidata.org/sparql`) names the query endpoint. See [Vessel particulars](#vessel-particulars).
- `LAKE_CATALOG_TOKEN` (set = tracks reach the lake past 48 hours, and the record imports its history), `LAKE_BUCKET` (`ais-lake`), `LAKE_DUCKDB_DIR` (`duckdb`, where DuckDB keeps its extensions). The token needs R2 Data Catalog read and object read on the bucket; the account is `R2_ACCOUNT_ID`.
- `WS_CONNECTS_PER_MIN` (`60` per IP).
- `STATION_SALT`: keys the UDP station ids. Set it on a public host.
- `TRUST_CF_HEADERS=1`: use it only when Cloudflare proxies the hostname. It makes rate limits key on `CF-Connecting-IP`.
- `PPROF_ADDR` (`127.0.0.1:6060`): serves `net/http/pprof` on this address, never on `ADDR`. `off` turns it off.

## Access tokens

Every credential is an Ed25519-signed claims token, `ak1.<claims>.<sig>`. You mint it offline with `go run ./cmd/aiscast-key`, and aiscast verifies it with the issuer's public key. aiscast holds no key that can mint more than personal-tier tokens.

Claims:

- `sub`: station id, partner, or device key.
- `role`: `personal` subscribes and publishes as the device key with small limits, `feeder` publishes, `peer` publishes and subscribes, `partner` subscribes, `admin` does all.
- `exp`.
- optional `bbox`: subscriptions must fit inside it.
- `cidr`: source addresses.
- `conns`: concurrent WebSockets.
- `rate`: messages/s per connection, excess thinned.
- `area`: total subscribed square degrees.

`tiers.go` holds the defaults, and [docs/limits.md](../docs/limits.md) documents them: anonymous 2 per address / 20 / 100 (subscribe only), personal 2 / 50 / 400 with no expiry, feeder 5 / 200 / unlimited plus `/v1/nmea`. A personal token earns the feeder tier when its stations deliver 1,000 events in 24 h. You can also mint a feeder token directly. The cap is 8 streams per address across tokens.

The token goes everywhere an API key went: aisstream `APIKey`, `Authorization: Bearer`, Basic-auth password (AIS-catcher `USERPWD x:ak1...`), MQTT CONNECT password (AIS-catcher `-Q wssmqtt://x:ak1...@`), or `?key=`. A browser page on another origin can send the header too: every JSON endpoint answers the CORS preflight, so a token never has to go on the query string. `POST /v1/keys {"pubkey": "<base64url ed25519 public key>", "ts": <unix seconds>, "sig": "<signature>"}` returns a personal token (no expiry) for that device key. `sig` signs the request with the key, so nobody else can mint a token for a station; once a key has signed, unsigned requests for it are refused, and a request that names the station must be signed. The signed lines and an `openssl` recipe are in [openapi.json](openapi.json). The Signal K plugin and the chart plugin use this, and they bundle no secret. `/v1/stream` subscribe and `/v1/vessels` are open. `/v0/stream`, publishing, and `/v1/receive` need a token.

`aiscast-key issuer` makes an issuer keypair. `aiscast-key new -sub station-42 -role feeder -exp 8760h` mints a token. `aiscast-key inspect <token>` shows the claims.

Sources:

- Kystverket (Norway, NLOD, TCP NMEA).
- BarentsWatch when `BARENTSWATCH_CLIENT_ID` is set (Norway, NLOD, JSON stream, `synthesized`). The same AIS Norge network as Kystverket plus satellite and offshore receivers out to the EEZ and Svalbard. Events rebuilt from a non-NMEA source must advance the vessel's clock, so its copies of transmissions Kystverket already delivered raw are withheld, and it takes over a vessel from that vessel's next missed transmission onward.
- Digitraffic (Finland, CC BY 4.0). The server maps its MQTT JSON to go-ais structs and re-encodes it, and the events carry `synthesized: true`.
- aisstream.io when `AISSTREAM_API_KEY` is set. The server maps its `/v0` envelopes back to structs, also `synthesized`. Anything the open feeds already delivered dedupes.
- AISHub's aggregate snapshot when `AISHUB_USERNAME` is set (`synthesized`, source `aishub`, reciprocal with `AISHUB_FEED`). AISHub regenerates its world snapshot about once a minute, so positions from it run about a minute behind (`sources.aishub.delay` in `/v1/stats`). The server skips unchanged snapshots.

Volunteer stations feed over `/v1/stream` as MQTT (a socket that negotiates the `mqtt` subprotocol gets a receive-only MQTT 3.1.1 session: the token is the CONNECT password or the request's, each PUBLISH payload is newline-separated NMEA on any topic, QoS 0 to 2 acknowledged, SUBSCRIBE refused), `/v1/receive` (AIS-catcher HTTP output), or `/v1/stream` publish frames. All three name the station by the token's `sub` alone, `station:<sub>`, so a feeder can switch transports without changing identity. UDP senders are `udp:<hash>`, or `mmsi:<n>` once their own `!AIVDO` names the vessel.

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received. A station followed by ` buffered` marks a sender's offline backlog, which live withheld from the stream when stale and replay withholds the same way. A station followed by ` published` marks a line published over `/v1/stream`, which replay feeds to the NMEA parser rather than trying as an AIS-catcher envelope. A record can carry both marks, ` published` first.

## Vector tiles

`GET /v1/vessels/tiles/{z}/{x}/{y}` answers a Mapbox Vector Tile of last known positions with one point layer, `vessels` (`tiles.go`). It answers by the same age rules as `/v1/vessels` (`ageRules` in `vessels.go`): the cache for the last 30 minutes, and the vessel record for vessels last heard up to 7 days ago whose last report was stationary. `/v1/vessels/tiles.json` is its TileJSON and carries its query string, filters and token alike, into the tile URL. The encoder handles points only and uses the standard library. Tiles are built on demand, the record read first and the cache under its read lock, and gzipped once. A build is shared by every request for the same tile and filters for 10 seconds. The response carries its own `Content-Encoding`, so Caddy passes it through. At 60,000 vessels a z0 tile takes about 20 ms to build and a busy z8 tile under 1 ms. With 300,000 rows in the record, a z0 tile takes about 400 ms and a z8 tile still under 1 ms (`BenchmarkTile*`). [openapi.json](openapi.json) documents the properties, the filters, and how clients refresh.

## Vessel record

The cache drops a vessel 30 minutes after its last report. The vessel record keeps one row per MMSI ever heard, with its particulars and last known position, in the SQLite file named by `STORE`. It answers what the cache cannot: `GET /v1/vessels/{mmsi}` for any vessel ever heard, a followed MMSI's last position on `/v1/vessels?mmsi=`, the vessels an area on `/v1/vessels` holds past 30 minutes, the `?q=` search, the vector tiles, and the MCP `get_vessels` and `search_vessels_by_name` tools.

The fold marks each vessel it updates, and a writer upserts the marked vessels once a second in one transaction, so the fold never waits on the disk. The upsert merges with the fold's rules: a vessel that returns after the cache dropped it arrives without its name or position, and a blank field keeps the stored value. On boot the cache is filled with every vessel the record heard in the last 30 minutes, so the record is the one vessel state that survives a restart. `aiscast replay` never attaches the record, so a replayed day cannot overwrite a live position. A record that will not open leaves the server running without it, starting from an empty map that the feeds refill within minutes, and `aiscast_store_up` drops to 0.

History reaches the record through the lake. Once a day after 03:00 UTC, when the packager's night is over, and after a restart when the last import is more than a day old, the server reads `ais.vessels` a page at a time and merges every row. The packager records there, per MMSI, the earliest report of any kind and the latest position across every day it has packaged, so every archive it packages backdates `first_seen` with nothing source-specific here. The import's merge has its own rule, because history is usually older than the record: a stored name or particular is only filled when blank, `first_seen` takes the earlier value, and a position is taken only when it is newer. A vessel only history knows gets a row, with its last position and the source kind that delivered it. An import that finds the lake empty does not count, so it retries at the next check. It needs the lake, so it runs only with `LAKE_CATALOG_TOKEN`.

`/metrics` reports `aiscast_store_up`, flushes, flush failures and seconds, rows written, and the file size. `/metrics` also reports import runs, failures, and rows merged, and when the last import succeeded.

## Vessel particulars

`GET /v1/vessels/{mmsi}` and the MCP `get_vessels` tool add a `wikidata` object for a vessel whose IMO number has a Wikidata item: builder, year built, gross tonnage, deadweight, registered length and beam, country of registry, and former names, with the item's ID and URL. A bot import gave most IMO-registered ships an item; about two in three vessels with an IMO have one. Wikidata is CC0, so the object carries `license: CC0-1.0` and adds no credit line to `attribution`.

The particulars live in the `wikidata` table of the vessel record, keyed by IMO, and requests never call Wikidata. Once a week ([wikidata.go](wikidata.go)) the server reads every item with an IMO (P458) from the Wikidata Query Service, about 96,000 of them. It sends one query per field, ten seconds apart, and then names the builders and registries, about 1,500 items, a thousand per query. Each query takes 1 to 20 seconds. One query for every field takes half a minute, close to the service's 60-second limit, and joining names onto every ship's statements runs past it. A sync takes about three minutes and replaces the table in one transaction. An IMO that fails its check digit is skipped on both sides, so a mistyped IMO in AIS never picks up another ship's particulars. An IMO on more than one item takes the lowest QID. A sync that finds fewer than half the ships already stored is refused, and a failed sync is retried at the next hourly check. The fields and properties are in [openapi.json](openapi.json) under `VesselWikidata`.

`/metrics` reports syncs, failures, the IMO numbers stored, and when the last sync succeeded.

## Recent tracks

`GET /v1/vessels/{mmsi}/track` and the MCP `get_vessel_track` tool answer where a vessel has been over the last 48 hours. Every position report the pipeline accepts is kept in the SQLite file named by `TRACKS`, which runs only beside the vessel record. A report the cache withholds as stale or implausible is withheld here too.

Each UTC day is one table keyed by MMSI, time, and position, so a track is a range read in each day it spans, and expiry drops a whole table once the window has left it. Position is in the key because raw reports with equal stamps survive dedupe as distinct data; a second report at the same time and place is the same point. That keeps two to three days, up to about 10 GB. Positions use the lake's integer encodings, and each row names its source kind for attribution. The fold appends each accepted position to a queue under the cache lock it already holds, and the vessel record's writer drains the queue once a second. The queue holds about eight minutes of traffic; past that, a stalled disk costs new positions, counted in `aiscast_tracks_points_dropped_total`, rather than memory. `aiscast replay` never attaches the store.

Positions older than the window come from the lake, packaged daily into R2 Data Catalog, read in process by DuckDB with its iceberg extension ([lake_duckdb.go](lake_duckdb.go), the one part of the server that needs cgo). DuckDB reads only the Parquet row groups a query needs and caches file metadata between queries. The catalog attaches on first use; a failure is retried after a minute, and the rest of the server runs without it. Reads past the window cost server time and R2 requests, so reaching past 48 hours is a feeder and commercial capability: an anonymous or personal request that overlaps the window is clamped to it, and one wholly before it gets a 403 saying which tier reaches it. A request into the lake covers at most 7 days and pages back by moving `to`. Each vessel-day is read once, with the source that delivered each position, and cached in `tracks.db`, up to 2 GB, dropping the least recently fetched days first. The lake partitions by the day a position arrived, so the partition after a range's last day is read too, for reports relayed late. A day within the packager's repackaging week is read again after six hours, and so is a day with no positions, which may not be packaged yet; an older day with positions never is. Positions from both stores are thinned with the same interval buckets. Without `LAKE_CATALOG_TOKEN` there is no lake, and a range past the window is clamped to it. The answer's `from` and `to` say what was covered. Without an `interval`, the server picks the round step, from 5 seconds to a day, that spreads `limit` positions over the whole range, so a dense range is thinned rather than cut to its newest positions; the answer reports the interval it used, and `interval=0` asks for every position. The newest `limit` positions are returned when more match, capped by tier: 200 anonymous, 1,000 with a personal token, 5,000 for feeder and above. `/metrics` reports positions written and dropped, write failures and seconds, and the file size, and for the lake its queries, failures, seconds, and cache hits and misses.
