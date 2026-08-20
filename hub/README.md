# hub

AIS ingest → reassemble → dedupe → decode → bbox fan-out, with an aisstream.io-compatible WebSocket at `/v0/stream`. One Go binary; see [../PLAN.md](../PLAN.md) for the design.

```sh
ALLOW_ANON=1 go run .          # Kystverket upstream on, HTTP :8080, UDP :10110, archive/ in cwd; any key accepted
go test ./...
```

Endpoints:

- `GET /v0/stream` WebSocket, aisstream.io protocol (subscribe JSON with `APIKey`, `BoundingBoxes`, `FiltersShipMMSI`, `FilterMessageTypes`). Frozen; nothing here may deviate.
- `GET /v1/stream` WebSocket, bidirectional. Frames in: `{"type":"subscribe","bbox":[[minLat,minLon,maxLat,maxLon],...]}` (empty bbox = everything), `{"type":"publish","nmea":["!AIVDM,..."]}` (needs Basic auth or `?key=id:secret`). Frames out: `{"type":"event", id, time, source, station, channel, nmea, mmsi, msg_type, lat, lon, message, synthesized}`.
- `GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon` GeoJSON of current positions (all vessels without `bbox`); vessels unseen for 30 minutes are dropped. CORS open.
- `POST /v1/receive` AIS-catcher HTTP output (`-H http://host:8080/v1/receive USERPWD id:key GZIP on`): `jsonaiscatcher` JSON or plain NMEA lines, optional gzip.
- UDP `:10110` raw NMEA datagrams.
- `GET /health` 503 when any configured upstream has been silent for 2 minutes.
- `GET /metrics` Prometheus text: events, duplicates, parse/decode failures, client and archive drops, rate-limit rejections, vessels, clients, per-source event counts and last-event age.

Environment: `ADDR` (`:8080`), `UDP_ADDR` (`:10110`), `KYSTVERKET` (`1`), `KYSTVERKET_ADDR`, `DIGITRAFFIC` (`1`), `DIGITRAFFIC_URL`, `ARCHIVE_DIR` (`archive`), `R2_BUCKET` (unset = local only; uploads via `npx wrangler` on hourly rotation), `V0_API_KEYS` (comma list), `FEEDER_KEYS` (`id:secret,...`), `ALLOW_ANON=1` (accept any key/feeder; local development only), `SNAPSHOT` (`vessels.json`, vessel cache written every 10 s and restored on boot), `WS_CONNECTS_PER_MIN` (`60` per IP).

Kystverket allows one TCP connection per source IP: a second hub on the same IP makes both reconnect every few seconds. Run one hub per IP; for a second local instance set `KYSTVERKET=0`.

Load test: `go run ./cmd/loadtest -clients 1000 -duration 30s` against a hub started with `WS_CONNECTS_PER_MIN=100000` (all clients come from one IP). SIGTERM snapshots the vessel cache and flushes and uploads the open archive hours before exit.

Sources: Kystverket (Norway, NLOD, TCP NMEA) and Digitraffic (Finland, CC BY 4.0, MQTT JSON mapped to go-ais structs and re-encoded; events carry `synthesized: true`).

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received.
