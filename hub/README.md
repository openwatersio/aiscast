# hub

AIS ingest → reassemble → dedupe → decode → bbox fan-out, with an aisstream.io-compatible WebSocket at `/v0/stream`. One Go binary; see [../PLAN.md](../PLAN.md) for the design.

```sh
ALLOW_ANON=1 go run .          # Kystverket upstream on, HTTP :8080, UDP :10110, archive/ in cwd; no tokens needed
go test ./...
```

Endpoints (full reference in [docs/API.md](../docs/API.md)):

- `GET /v0/stream` WebSocket, aisstream.io protocol (subscribe JSON with `APIKey`, `BoundingBoxes`, `FiltersShipMMSI`, `FilterMessageTypes`). Frozen; nothing here may deviate.
- `GET /v1/stream` WebSocket, bidirectional. Frames in: `{"type":"subscribe","bbox":[[minLat,minLon,maxLat,maxLon],...]}` (empty bbox = everything; open), `{"type":"publish","nmea":["!AIVDM,..."]}` (needs a feeder/peer token via `?key=` or `Authorization: Bearer`). Frames out: `{"type":"event", id, time, source, station, channel, nmea, mmsi, msg_type, lat, lon, message, synthesized}`.
- `GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon` GeoJSON of current positions (all vessels without `bbox`); vessels unseen for 30 minutes are dropped. CORS open.
- `POST /v1/receive` AIS-catcher HTTP output (`-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on`): `jsonaiscatcher` JSON or plain NMEA lines, optional gzip; needs a feeder token.
- UDP `:10110` raw NMEA datagrams; the station id is a keyed hash of the sender address (`udp:<hex>`, `STATION_SALT` keeps it stable), never the address itself; a sender that transmits `!AIVDO` own-ship sentences is keyed by that MMSI instead (`mmsi:<n>`, self-reported).
- `GET /v1/stations` per-source event counts and seconds since last event (public; how a contributor sees their feed arriving).
- `GET /health` 503 when any configured upstream has been silent for 2 minutes.
- `GET /metrics` Prometheus text: events, duplicates, parse/decode failures, client and archive drops, rate-limit rejections, vessels, clients, per-source event counts and last-event age.

Environment: `ADDR` (`:8080`), `UDP_ADDR` (`:10110`), `KYSTVERKET` (`1`), `KYSTVERKET_ADDR`, `DIGITRAFFIC` (`1`), `DIGITRAFFIC_URL`, `AISSTREAM_API_KEY` (set = aisstream.io upstream on), `AISSTREAM_BBOX` (world), `AISSTREAM_URL`, `AISHUB_FEED` (`data.aishub.net:<port>`: forward received events to AISHub as plain `!AIVDM`), `AISHUB_USERNAME` (set = poll AISHub's aggregate snapshot), `AISHUB_INTERVAL` (`65s`; never below their one-minute limit), `ARCHIVE_DIR` (`archive`), `R2_BUCKET` + `R2_ACCOUNT_ID` + `R2_ACCESS_KEY_ID` + `R2_SECRET_ACCESS_KEY` (unset = archive stays local; otherwise each hour is PUT to R2 over the S3 API on rotation and on shutdown; `S3_ENDPOINT`/`S3_REGION` for non-R2 targets), `ISSUER_PUBKEYS` (`kid:base64url-pubkey,...`: issuers whose tokens are accepted), `PERSONAL_ISSUER_KEY` (`kid:base64url-seed`: lets `POST /v1/keys` mint personal-tier tokens), `REVOKED_SUBS` (comma list), `ALLOW_ANON=1` (no tokens needed; local development only), `SNAPSHOT` (`vessels.json`, vessel cache written every 10 s and restored on boot), `WS_CONNECTS_PER_MIN` (`60` per IP), `STATION_SALT` (keys the UDP station ids; set it on a public host), `TRUST_CF_HEADERS=1` (only when Cloudflare proxies the hostname; makes rate limits key on `CF-Connecting-IP`).

## Access tokens

Every credential is an Ed25519-signed claims token, `ak1.<claims>.<sig>`, minted offline with `go run ./cmd/aiscast-key` and verified by the hub with the issuer's public key; the hub holds no key that can mint more than personal-tier tokens. Claims: `sub` (station id, partner, device key), `role` (`personal` subscribe with small limits, `feeder` publish, `peer` publish+subscribe, `partner` subscribe, `admin` all), `exp`, optional `bbox` (subscriptions must fit inside), `cidr` (source addresses), `conns` (concurrent WebSockets). The token goes wherever a key went: aisstream `APIKey`, `Authorization: Bearer`, Basic-auth password (AIS-catcher `USERPWD x:ak1...`), or `?key=`. `POST /v1/keys {"pubkey": "<base64url ed25519 public key>"}` returns a 30-day personal token for that device key (the Signal K plugin and the chart plugin use this; no secret is bundled). `/v1/stream` subscribe and `/v1/vessels` are open; `/v0/stream`, publishing, and `/v1/receive` need a token. `aiscast-key issuer` makes an issuer keypair; `aiscast-key new -sub station-42 -role feeder -exp 8760h` mints; `aiscast-key inspect <token>` shows claims.

Sources: Kystverket (Norway, NLOD, TCP NMEA), Digitraffic (Finland, CC BY 4.0, MQTT JSON mapped to go-ais structs and re-encoded; events carry `synthesized: true`), aisstream.io when `AISSTREAM_API_KEY` is set (its `/v0` envelopes mapped back to structs, also `synthesized`; anything the open feeds already delivered dedupes), and AISHub's aggregate snapshot when `AISHUB_USERNAME` is set (`synthesized`, source `aishub`; reciprocal with `AISHUB_FEED`). AISHub regenerates its world snapshot only every ~5 minutes, so positions from it are 1–6 minutes old; unchanged snapshots are skipped.

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received.
