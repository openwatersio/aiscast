# signalk-aiscast

Signal K plugin for [aiscast](https://github.com/openwatersio/aiscast), the open AIS network from [Open Waters](https://openwaters.io). It does two things over one connection:

- **Share**: every AIS sentence your receiver hears (`!AIVDM` on NMEA 0183, or NMEA 2000 AIS PGNs re-encoded as sentences) is sent to aiscast as it arrives, so the places only boats can hear get coverage. Your own transponder's position (`!AIVDO`) is shared too, and when an AIS transponder is not available the plugin falls back to class B reports built from the Signal K position, marked self-reported. Each part has its own checkbox.
- **Receive**: when the boat hears no AIS of its own (no receiver, receiver off, server running ashore), the plugin subscribes to aiscast around your position and injects the traffic as Signal K targets with `$source` `signalk-aiscast.net`, so Freeboard and friends show them. `Always` mode also fills in beyond VHF range; locally heard targets win.

No account. On first start the plugin generates an Ed25519 keypair in its data directory and requests its own access token from aiscast (sent as an `Authorization: Bearer` header); receptions are credited to that key. Paste an operator-issued token into the config to publish as a named station with higher limits.

## Install

Signal K App Store → `signalk-aiscast`, or `npm install signalk-aiscast` in `~/.signalk`. Enable it in Plugin Config.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| Share → AIS targets I receive | on | publish AIS heard by the receiver (NMEA 0183 `!AIVDM`, NMEA 2000 AIS) |
| Share → My own ship's AIS transponder data | on | forward what the transponder broadcasts (`!AIVDO`); your position becomes public open data on aiscast (it is already broadcast on VHF) |
| Share → Fallback to self-reported AIS position | on | when an AIS transponder is not available, build class B reports from Signal K: position every 60 s while moving, static data every 6 min, tagged `s:self`; paused for 5 min after any real `!AIVDO`. Disabled until an MMSI is set in Vessel settings |
| Receive → Show traffic from aiscast | auto | `Off`, `Auto` (only while nothing is heard locally for 90 s), `Always` (also beyond local VHF range; local reception wins per target) |
| Receive → Radius | 50 nm | subscription box around the vessel (5–200) |
| Advanced → Server | `https://ais.openwaters.io` | aiscast base URL |
| Advanced → Access token | empty | optional operator-issued token; empty = self-minted personal token |

## Behaviour worth knowing

- Sentences go out as received, each stamped with a NMEA TAG block carrying the receive time. Anything not acknowledged, or heard while offline, waits in `<data dir>/queue/` (cap 100 MB) and replays oldest-first on reconnect; aiscast archives replayed sentences older than a minute without putting them on the live map.
- Reconnects with jittered backoff (5 s → 5 min; 30 min after a refusal) and a 60 s silence watchdog. Status line in Plugin Config shows the key prefix, send rate, queue depth, targets, and link state.
- Injected targets come through the server's own AIS parser, so they are shaped exactly like VHF-received ones; the server's *Maximum age of inactive vessels* setting expires them. Your own vessel and echoes of your own receptions are never injected, and payloads received from aiscast are never published back.
- NMEA 2000 AIS needs nothing extra: PGNs 129038/129039/129041/129794/129809/129810 are re-encoded to `!AIVDM` (own ship to `!AIVDO`) and tagged `s:n2k`, since N2K carries decoded fields rather than the VHF bits. `signalk-n2kais-to-nmea0183` is not required and not listened to.
- The self-reported position fallback is what `@signalk/aisreporter` does for MarineTraffic, aimed at aiscast: position from `navigation.position` with SOG, COG, and heading (true, or magnetic plus variation) when present and the AIS "not available" values when not; name, callsign, ship type, and dimensions from `name`, `communication.callsignVhf`, `design.aisShipType`, `design.length`/`design.beam`, and `sensors.gps.fromBow`/`fromCenter`. aiscast marks these events `synthesized: true`, keeps them out of its AISHub feed, and shows them apart from VHF receptions. Nothing is sent without a fix, while the GPS sits at Null Island, or while the position has not changed.

## Data and licensing

What you share is published by aiscast under its open data terms (see the [aiscast README](https://github.com/openwatersio/aiscast#readme)). The plugin never shares anything other than AIS sentences and, if you enable it, your own position (from the transponder, or built from Signal K when an AIS transponder is not available). Removal requests and the privacy policy are on the aiscast site.

## Development

```sh
npm install
npm test          # vitest against a fake aiscast server
npm run build     # dist/
```

Released to npm by the repo's `release.yml` when a GitHub release tagged `signalk-plugin-v*` is created.
