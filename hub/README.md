# hub

AIS ingest → reassemble → dedupe → decode → bbox fan-out, with an aisstream.io-compatible WebSocket at `/v0/stream`. One Go binary; see [../PLAN.md](../PLAN.md) for the design.

```sh
go run .                       # Kystverket upstream on, HTTP :8080, UDP :10110, archive/ in cwd
go test ./...
```

Endpoints:

- `GET /v0/stream` WebSocket, aisstream.io protocol (subscribe JSON with `APIKey`, `BoundingBoxes`, `FiltersShipMMSI`, `FilterMessageTypes`). Frozen; nothing here may deviate.
- `GET /v1/stream` WebSocket, bidirectional. Frames in: `{"type":"subscribe","bbox":[[minLat,minLon,maxLat,maxLon],...]}` (empty bbox = everything), `{"type":"publish","nmea":["!AIVDM,..."]}` (needs Basic auth or `?key=id:secret`). Frames out: `{"type":"event", id, time, source, station, channel, nmea, mmsi, msg_type, lat, lon, message, synthesized}`.
- `POST /v1/receive` AIS-catcher HTTP output (`-H http://host:8080/v1/receive USERPWD id:key GZIP on`): `jsonaiscatcher` JSON or plain NMEA lines, optional gzip.
- UDP `:10110` raw NMEA datagrams.
- `GET /health` 503 when no event for 2 minutes.

Environment: `ADDR` (`:8080`), `UDP_ADDR` (`:10110`), `KYSTVERKET` (`1`), `KYSTVERKET_ADDR`, `ARCHIVE_DIR` (`archive`), `R2_BUCKET` (unset = local only; uploads via `npx wrangler` on hourly rotation), `V0_API_KEYS` (comma list; unset = any non-empty key), `FEEDER_KEYS` (`id:secret,...`; unset = any).

Archive layout: `<license>/<source>/YYYY/MM/DD/HH.gz`, one record per line: receive time, station, body as received.
