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
- `ALLOW_ANON=1`: no tokens needed, local development only.
- `SNAPSHOT` (`vessels.json`): the server writes the vessel cache every minute and on shutdown, and restores it on boot. The rolling 24 h/7 d counters behind `/v1/stats` live in `<name>-usage.json` beside it.
- `STORE` (`aiscast.db`): the vessel record, in SQLite. `off` runs without it, and without tracks. See [Vessel record](#vessel-record).
- `TRACKS` (`tracks.db`): every position of the last 48 hours, in SQLite, for tracks. `off` runs without it. See [Recent tracks](#recent-tracks).
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

The token goes everywhere an API key went: aisstream `APIKey`, `Authorization: Bearer`, Basic-auth password (AIS-catcher `USERPWD x:ak1...`), MQTT CONNECT password (AIS-catcher `-Q wssmqtt://x:ak1...@`), or `?key=`. `POST /v1/keys {"pubkey": "<base64url ed25519 public key>"}` returns a personal token (no expiry) for that device key. The Signal K plugin and the chart plugin use this, and they bundle no secret. `/v1/stream` subscribe and `/v1/vessels` are open. `/v0/stream`, publishing, and `/v1/receive` need a token.

`aiscast-key issuer` makes an issuer keypair. `aiscast-key new -sub station-42 -role feeder -exp 8760h` mints a token. `aiscast-key inspect <token>` shows the claims.

Sources:

- Kystverket (Norway, NLOD, TCP NMEA).
- BarentsWatch when `BARENTSWATCH_CLIENT_ID` is set (Norway, NLOD, JSON stream, `synthesized`). The same AIS Norge network as Kystverket plus satellite and offshore receivers out to the EEZ and Svalbard. Events rebuilt from a non-NMEA source must advance the vessel's clock, so its copies of transmissions Kystverket already delivered raw are withheld, and it takes over a vessel from that vessel's next missed transmission onward.
- Digitraffic (Finland, CC BY 4.0). The server maps its MQTT JSON to go-ais structs and re-encodes it, and the events carry `synthesized: true`.
- aisstream.io when `AISSTREAM_API_KEY` is set. The server maps its `/v0` envelopes back to structs, also `synthesized`. Anything the open feeds already delivered dedupes.
- AISHub's aggregate snapshot when `AISHUB_USERNAME` is set (`synthesized`, source `aishub`, reciprocal with `AISHUB_FEED`). AISHub regenerates its world snapshot about once a minute, so positions from it run about a minute behind (`sources.aishub.delay` in `/v1/stats`). The server skips unchanged snapshots.

Volunteer stations feed over `/v1/stream` as MQTT (a socket that negotiates the `mqtt` subprotocol gets a receive-only MQTT 3.1.1 session: the token is the CONNECT password or the request's, each PUBLISH payload is newline-separated NMEA on any topic, QoS 0 to 2 acknowledged, SUBSCRIBE refused), `/v1/receive` (AIS-catcher HTTP output), or `/v1/stream` publish frames. All three name the station by the token's `sub` alone, `station:<sub>`, so a feeder can switch transports without changing identity. UDP senders are `udp:<hash>`, or `mmsi:<n>` once their own `!AIVDO` names the vessel.

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received. A station followed by ` buffered` marks a sender's offline backlog, which live withheld from the stream when stale and replay withholds the same way. A station followed by ` published` marks a line published over `/v1/stream`, which replay feeds to the NMEA parser rather than trying as an AIS-catcher envelope. A record can carry both marks, ` published` first.

## Vessel record

The cache drops a vessel 30 minutes after its last report. The vessel record keeps one row per MMSI ever heard, with its particulars and last known position, in the SQLite file `STORE` names. It answers what the cache cannot: `GET /v1/vessels/{mmsi}` for any vessel ever heard, a followed MMSI's last position on `/v1/vessels?mmsi=`, `/v1/vessels?bbox=&max_age=` past 30 minutes, the `?q=` search, and the MCP `get_vessels` and `search_vessels_by_name` tools.

The fold marks each vessel it updates, and a writer upserts the marked vessels once a second in one transaction, so the fold never waits on the disk. The upsert merges with the fold's rules: a vessel that returns after the cache dropped it arrives without its name or position, and a blank field keeps the stored value. On boot every vessel in the snapshot is written, which seeds an empty record. `aiscast replay` never attaches the record, so a replayed day cannot overwrite a live position. A record that will not open leaves the server running without it, and `aiscast_store_up` drops to 0.

`/metrics` reports `aiscast_store_up`, flushes, flush failures and seconds, rows written, and the file size.

## Recent tracks

`GET /v1/vessels/{mmsi}/track` and the MCP `get_vessel_track` tool answer where a vessel has been over the last 48 hours. Every position report the pipeline accepts is kept in the SQLite file `TRACKS` names, which runs only beside the vessel record. A report the cache withholds as stale or implausible is withheld here too.

Each UTC day is one table keyed by MMSI and time, so a track is a range read in each day it spans, and expiry drops a whole table once the window has left it. That keeps two to three days, several gigabytes. Positions use the lake's integer encodings, and each row names its source kind for attribution. The fold appends each accepted position to a queue under the cache lock it already holds, and the vessel record's writer drains the queue once a second. The queue holds about eight minutes of traffic; past that, a stalled disk costs new positions, counted in `aiscast_tracks_points_dropped_total`, rather than memory. `aiscast replay` never attaches the store.

A request past 48 hours is clamped to the window, and the answer's `from` and `to` say what was covered. The newest `limit` positions are returned when more match, capped by tier: 200 anonymous, 1,000 with a personal token, 5,000 for feeder and above. `/metrics` reports positions written and dropped, write failures and seconds, and the file size.
