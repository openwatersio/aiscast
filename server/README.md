# aiscast server

AIS ingest → reassemble → dedupe → decode → bbox fan-out, with an aisstream.io-compatible WebSocket at `/v0/stream`. One Go binary. See [docs/architecture.md](../docs/architecture.md) for the design.

```sh
ALLOW_ANON=1 go run .          # Kystverket upstream on, HTTP :8080, UDP :10110, archive/ in cwd; no tokens needed
go test ./...
```

[openwaters.io/api/ais](https://openwaters.io/api/ais/) documents the endpoints. Operator-only: `GET /metrics` serves Prometheus text (events, duplicates, parse/decode failures, client and archive drops, rate-limit rejections, vessels, clients, per-source event counts and last-event age).

Environment:

- `ADDR` (`:8080`), `UDP_ADDR` (`:10110`).
- `KYSTVERKET` (`1`), `KYSTVERKET_ADDR`.
- `BARENTSWATCH_CLIENT_ID` + `BARENTSWATCH_CLIENT_SECRET` (set = BarentsWatch upstream on), `BARENTSWATCH_URL`.
- `DIGITRAFFIC` (`1`), `DIGITRAFFIC_URL`.
- `AISSTREAM_API_KEY` (set = aisstream.io upstream on), `AISSTREAM_BBOX` (world), `AISSTREAM_URL`.
- `AISHUB_FEED` (`data.aishub.net:<port>`): forward volunteer-station events to AISHub as plain `!AIVDM`. The server never forwards public feeds or synthesized events, per their terms.
- `AISHUB_USERNAME` (set = poll AISHub's aggregate snapshot), `AISHUB_INTERVAL` (`20s`, the limit AISHub set for our account).
- `ARCHIVE_DIR` (`archive`).
- `R2_BUCKET` + `R2_ACCOUNT_ID` + `R2_ACCESS_KEY_ID` + `R2_SECRET_ACCESS_KEY`: unset = archive stays local. Otherwise the server PUTs each hour to R2 over the S3 API on rotation and on shutdown. Use `S3_ENDPOINT`/`S3_REGION` for non-R2 targets.
- `ISSUER_PUBKEYS` (`kid:base64url-pubkey,...`): the issuers whose tokens aiscast accepts.
- `PERSONAL_ISSUER_KEY` (`kid:base64url-seed`): lets `POST /v1/keys` mint personal-tier tokens.
- `REVOKED_SUBS` (comma list).
- `ALLOW_ANON=1`: no tokens needed, local development only.
- `SNAPSHOT` (`vessels.json`): the server writes the vessel cache every 10 s and restores it on boot. The rolling 24 h/7 d counters behind `/v1/stats` live in `<name>-usage.json` beside it.
- `WS_CONNECTS_PER_MIN` (`60` per IP).
- `STATION_SALT`: keys the UDP station ids. Set it on a public host.
- `TRUST_CF_HEADERS=1`: use it only when Cloudflare proxies the hostname. It makes rate limits key on `CF-Connecting-IP`.

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

The token goes everywhere an API key went: aisstream `APIKey`, `Authorization: Bearer`, Basic-auth password (AIS-catcher `USERPWD x:ak1...`), or `?key=`. `POST /v1/keys {"pubkey": "<base64url ed25519 public key>"}` returns a personal token (no expiry) for that device key. The Signal K plugin and the chart plugin use this, and they bundle no secret. `/v1/stream` subscribe and `/v1/vessels` are open. `/v0/stream`, publishing, and `/v1/receive` need a token.

`aiscast-key issuer` makes an issuer keypair. `aiscast-key new -sub station-42 -role feeder -exp 8760h` mints a token. `aiscast-key inspect <token>` shows the claims.

Sources:

- Kystverket (Norway, NLOD, TCP NMEA).
- BarentsWatch when `BARENTSWATCH_CLIENT_ID` is set (Norway, NLOD, JSON stream, `synthesized`). The same AIS Norge network as Kystverket plus satellite and offshore receivers out to the EEZ and Svalbard. Events rebuilt from a non-NMEA source must advance the vessel's clock, so its copies of transmissions Kystverket already delivered raw are withheld, and it takes over a vessel from that vessel's next missed transmission onward.
- Digitraffic (Finland, CC BY 4.0). The server maps its MQTT JSON to go-ais structs and re-encodes it, and the events carry `synthesized: true`.
- aisstream.io when `AISSTREAM_API_KEY` is set. The server maps its `/v0` envelopes back to structs, also `synthesized`. Anything the open feeds already delivered dedupes.
- AISHub's aggregate snapshot when `AISHUB_USERNAME` is set (`synthesized`, source `aishub`, reciprocal with `AISHUB_FEED`). AISHub regenerates its world snapshot only every ~5 minutes, so positions from it are 1–6 minutes old. The server skips unchanged snapshots.

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received.
