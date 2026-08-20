# aiscast

Open AIS aggregation hub: ingest live vessel traffic from open feeds and volunteer receivers, keep the full history, and stream it to clients by bounding box. A self-hosted replacement for aisstream.io, with a wire-compatible `/v0/stream` so existing aisstream clients work unchanged. Part of [Open Waters](https://openwaters.io).

## Layout

- [hub/](hub/) — the server, one Go binary: ingest → reassemble → dedupe → decode → bbox fan-out, plus hourly archive to R2. See [hub/README.md](hub/README.md) for endpoints and configuration.
- [viewer/](viewer/) — static map of live traffic; the first non-aisstream client of the hub.
- [PLAN.md](PLAN.md) — scope, architecture, staged plan, licensing, sustainability.
- [research/](research/) — the research behind every claim in the plan: open feeds, community networks, commercial providers, protocol, infrastructure, Signal K plugins, and an index of every open aisstream.io issue as demand signal.

## Quick start

```sh
cd hub
ALLOW_ANON=1 go run .   # Kystverket feed in, WebSocket on :8080, UDP NMEA on :10110
```

Then `cd viewer && python3 -m http.server 8089` and open http://localhost:8089/, or subscribe with any aisstream.io client pointed at `ws://localhost:8080/v0/stream`.

## Feeding the hub

The public instance is `ais.openwaters.io`. Data you send is re-served under the terms in [PLAN.md](PLAN.md#licensing-and-attribution) and forwarded to AISHub as part of our reciprocal feed; volunteer terms proper come with Stage 1.

- **AIS-catcher** (preferred: authenticated HTTP, works behind any NAT): `AIS-catcher ... -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`. Ask for a feeder token (it carries your station name); your data appears as `source: http:<station>`.
- **UDP** (no token; AIS-catcher `-u ais.openwaters.io 10110`, [docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) with host `ais.openwaters.io` port `10110`, or any NMEA forwarder): plain `!AIVDM` sentences, TAG blocks welcome. Appears as `source: udp:<id>`, where `<id>` is a keyed hash of your address, never the address itself.
- **Signal K**: add a UDP target `ais.openwaters.io:10110` in [`ais-forwarder`](https://github.com/hkapanen/ais-forwarder) or [`@signalk/aisreporter`](https://github.com/SignalK/aisreporter) (they forward AIVDM verbatim). A dedicated plugin that uses a token over HTTP and reports the boat's own position is planned (PLAN.md Stage 3).

Check it's arriving: `curl https://ais.openwaters.io/v1/stations` lists every source with its event count and seconds since the last message, and the [viewer](https://openwatersio.github.io/aiscast/) shows `source`/`station` on every vessel. Received data is deduplicated against the open feeds and other stations, so a vessel already heard by Kystverket or another receiver does not count twice.

Clients: point any aisstream.io client at `wss://ais.openwaters.io/v0/stream` with a token as the `APIKey`, or use `wss://ais.openwaters.io/v1/stream` (subscribe is open, no token) and `GET /v1/vessels?bbox=`. Tokens: see [hub/README.md](hub/README.md#access-tokens).

## License

Code is [MIT](LICENSE). Re-served data carries its source license (Kystverket NLOD, Digitraffic CC BY 4.0, …); see [PLAN.md](PLAN.md#licensing-and-attribution).
