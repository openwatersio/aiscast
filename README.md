# aiscast

Open AIS aggregation hub: ingest live vessel traffic from open feeds and volunteer receivers, keep the full history, and stream it to clients by bounding box. A self-hosted replacement for aisstream.io, with a wire-compatible `/v0/stream` so existing aisstream clients work unchanged. Part of [Open Waters](https://openwaters.io).

## Layout

- [hub/](hub/) — the server, one Go binary: ingest → reassemble → dedupe → decode → bbox fan-out, plus hourly archive to R2. See [hub/README.md](hub/README.md) for endpoints and configuration.
- [viewer/](viewer/) — static map of live traffic; the first non-aisstream client of the hub.
- [PLAN.md](PLAN.md) — scope, architecture, staged plan, licensing, sustainability.
- [research/](research/) — the research behind every claim in the plan: open feeds, community networks, commercial providers, protocol, infrastructure.

## Quick start

```sh
cd hub
ALLOW_ANON=1 go run .   # Kystverket feed in, WebSocket on :8080, UDP NMEA on :10110
```

Then `cd viewer && python3 -m http.server 8089` and open http://localhost:8089/, or subscribe with any aisstream.io client pointed at `ws://localhost:8080/v0/stream`.

## License

Code is [MIT](LICENSE). Re-served data carries its source license (Kystverket NLOD, Digitraffic CC BY 4.0, …); see [PLAN.md](PLAN.md#licensing-and-attribution).
