# signalk-aiscast

Signal K plugin for [aiscast](https://github.com/openwatersio/aiscast), the open AIS network from [Open Waters](https://openwaters.io). It does two things over one connection:

- **Share**: the plugin sends every AIS sentence your receiver hears to aiscast as it arrives (`!AIVDM` on NMEA 0183, or NMEA 2000 AIS PGNs re-encoded as sentences), so the places only boats can hear get coverage. The plugin also shares your own transponder's position (`!AIVDO`). When an AIS transponder is not available, the plugin builds class B reports from the Signal K position and marks them self-reported. Each part has its own checkbox.
- **Receive**: when the boat hears no AIS of its own (no receiver, receiver off, server running ashore), the plugin subscribes to aiscast around your position. It injects the traffic as Signal K targets with `$source` `signalk-aiscast.net`, so Freeboard and friends show them. `Always` mode also adds traffic beyond VHF range, and locally heard targets win.

No account. On first start the plugin generates an Ed25519 keypair in its data directory and requests its own access token from aiscast, sent as an `Authorization: Bearer` header. aiscast credits receptions to that key. Paste an operator-issued token into the config to publish as a named station with higher limits.

## Install

Signal K App Store → `signalk-aiscast`, or `npm install signalk-aiscast` in `~/.signalk`. Enable it in Plugin Config.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| Share → AIS targets I receive | on | publish AIS heard by the receiver (NMEA 0183 `!AIVDM`, NMEA 2000 AIS) |
| Share → My own ship's AIS transponder data | on | forward what the transponder broadcasts (`!AIVDO`). Your position becomes public open data on aiscast, and the transponder already broadcasts it on VHF |
| Share → Fallback to self-reported AIS position | on | when an AIS transponder is not available, build class B reports from Signal K: position every 60 s while moving, static data every 6 min, tagged `s:self`. Synthesis pauses for 5 min after any real `!AIVDO`. The setting stays disabled until an MMSI is set in Vessel settings |
| Receive → Show traffic from aiscast | auto | `Off`, `Auto` (only while nothing is heard locally for 90 s), `Always` (also beyond local VHF range, and local reception wins per target) |
| Receive → Radius | 50 nm | subscription box around the vessel (5–200) |
| Advanced → Server | `https://ais.openwaters.io` | aiscast base URL |
| Advanced → Access token | empty | optional operator-issued token. Empty = self-minted personal token |

## Behaviour worth knowing

- The plugin sends sentences as received, each stamped with a NMEA TAG block that carries the receive time. Anything unacknowledged, or heard while offline, waits in `<data dir>/queue/` (cap 100 MB) and replays oldest-first on reconnect. aiscast archives replayed sentences older than a minute and keeps them off the live map.
- The plugin reconnects with jittered backoff (5 s → 5 min, and 30 min after a refusal) and a 60 s silence watchdog. The status line in Plugin Config shows the key prefix, send rate, queue depth, targets, and link state.
- Injected targets come through the server's own AIS parser, so they are shaped exactly like VHF-received ones. The server's *Maximum age of inactive vessels* setting expires them. The plugin never injects your own vessel or echoes of your own receptions, and it never publishes payloads from aiscast back to aiscast.
- NMEA 2000 AIS needs nothing extra. The plugin re-encodes PGNs 129038/129039/129041/129794/129809/129810 to `!AIVDM` (own ship to `!AIVDO`) and tags them `s:n2k`, since N2K carries decoded fields rather than the VHF bits. The plugin works without `signalk-n2kais-to-nmea0183` and ignores its output.
- The self-reported position fallback does for aiscast what `@signalk/aisreporter` does for MarineTraffic. Position comes from `navigation.position`, with SOG, COG, and heading (true, or magnetic plus variation) when present, and the AIS "not available" values when absent. Name, callsign, ship type, and dimensions come from `name`, `communication.callsignVhf`, `design.aisShipType`, `design.length`/`design.beam`, and `sensors.gps.fromBow`/`fromCenter`. aiscast marks these events `synthesized: true`, keeps them out of its AISHub feed, and shows them apart from VHF receptions. The plugin sends nothing without a fix, while the GPS sits at Null Island, or while the position stays unchanged.

## Data and licensing

aiscast publishes what you share under its open data terms (see the [aiscast README](https://github.com/openwatersio/aiscast#readme)). The plugin shares only AIS sentences and, if you enable it, your own position. That position comes from the transponder, or the plugin builds it from Signal K when an AIS transponder is not available. Removal requests and the privacy policy are on the aiscast site.

## Development

```sh
npm install
npm test          # vitest against a fake aiscast server
npm run build     # dist/
```

The repo's `release.yml` releases to npm when someone creates a GitHub release tagged `signalk-plugin-v*`.
