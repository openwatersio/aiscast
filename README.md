# aiscast

Live AIS vessel traffic, streamed by bounding box, from open government feeds and volunteer receivers. Part of [Open Waters](https://openwaters.io). The public instance is `ais.openwaters.io`; a live map is at [openwatersio.github.io/aiscast](https://openwatersio.github.io/aiscast/).

This is a beta. No SLA, coverage is uneven, and the terms under which some sources are re-served are still being settled (see [Coverage](#coverage)).

## Usage

**aisstream.io clients.** aiscast speaks aisstream.io's protocol on `wss://ais.openwaters.io/v0/stream`: the same subscribe message, the same `MessageType` / `MetaData` / `Message` frames. Change the hostname, use your aiscast token as the `APIKey`, and existing code keeps working ([aisstream-ts](https://www.npmjs.com/package/aisstream-ts), the official Go/Python/JS examples, `signalk-aisstream`).

**Native API** ([full reference](docs/API.md)).

- `wss://ais.openwaters.io/v1/stream`: send `{"type":"subscribe","bbox":[[minLat,minLon,maxLat,maxLon]]}` or `{"type":"subscribe","mmsi":[368168720]}` (or both) and receive one JSON event per decoded AIS message with its source, station, receive time, raw sentence, and the decoded fields. Subscribing needs no token.
- `GET https://ais.openwaters.io/v1/vessels?bbox=minLat,minLon,maxLat,maxLon` (or `?mmsi=a,b,c`): GeoJSON of every vessel currently in view (last position, name, type, course, speed, heading, when and from where it was last heard). No token.
- `GET https://ais.openwaters.io/v1/stations`: every source aiscast is hearing, with message counts and age.

**Tokens.** `/v0/stream` needs a token. A personal token is self-serve and never expires: use the [token page](https://openwatersio.github.io/aiscast/token.html), or generate an Ed25519 keypair and `POST https://ais.openwaters.io/v1/keys` with `{"pubkey":"<base64url public key>"}`; the response is your token. It carries the personal tier (2 streams, 50 messages/s, a 20°×20° area); feed data from the same token and it becomes a feeder token by itself. Tiers and limits are in [docs/limits.md](docs/limits.md); for more than the feeder tier, or for commercial use, write to hello@openwaters.io.

Events carry a `source` so you can tell a live volunteer receiver from a government feed or an aggregate, and `synthesized: true` marks messages that were re-encoded from a non-NMEA source.

## Contributing data

If you run an AIS receiver, send it here and it is re-served to everyone, deduplicated against the open feeds and every other station, and forwarded to AISHub as part of our reciprocal feed (only volunteer receptions go there; public feeds are never relayed, per their terms). Volunteer terms proper come with the next stage; until then treat this as a beta in both directions.

- **AIS-catcher** (preferred: authenticated HTTP, works behind any NAT): get a token at [openwatersio.github.io/aiscast/token.html](https://openwatersio.github.io/aiscast/token.html) (one click, stays in your browser), then `AIS-catcher ... -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`. Your data appears as `source: http:<station id>`. Named stations with higher limits: ask.
- **UDP** (no token): AIS-catcher `-u ais.openwaters.io 10110`, [docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) with host `ais.openwaters.io` port `10110`, or any NMEA forwarder sending plain `!AIVDM` / `!AIVDO` sentences (TAG blocks welcome). Your station appears as `udp:<id>`, a keyed hash of your address, never the address itself; a sender whose `!AIVDO` sentences identify the vessel is keyed by that MMSI instead.
- **Signal K**: add a UDP target `ais.openwaters.io:10110` in [`ais-forwarder`](https://github.com/hkapanen/ais-forwarder) (forward AIVDM and AIVDO). Or install the [`signalk-aiscast`](signalk-plugin/README.md) plugin: no token to paste, shares what your receiver hears (and your own position), and shows aiscast traffic when you have no receiver.

Your station page is the [map](https://openwatersio.github.io/aiscast/) with `?station=<your id>`: vessels heard, coverage extent, message counts, how many were heard elsewhere first; the same numbers are at `GET /v1/stations/{id}`. Feeders get the deduplicated raw stream back on `wss://ais.openwaters.io/v1/nmea`.

## Coverage

| Source                 | Where                                                                     | Freshness                                       |
| ---------------------- | ------------------------------------------------------------------------- | ----------------------------------------------- |
| Kystverket             | Norwegian coast, 40–60 nm out                                             | live                                            |
| Fintraffic Digitraffic | Finnish waters                                                            | live                                            |
| Volunteer receivers    | wherever they are (Buzzards Bay / Vineyard Sound as of the first station) | live                                            |
| AISHub aggregate       | worldwide terrestrial, ~50k vessels                                       | 1–6 min (their snapshot refreshes every ~5 min) |
| aisstream.io           | worldwide, when it is up                                                  | live; frequently down                           |

Every event says which of these it came from. What is deliberately not pulled in, and why, is in [PLAN.md](PLAN.md#stage-0c-more-sources-some-on-borrowed-terms).

## Licensing and attribution

Licensing is per source. aiscast does not relicense the aggregate: each event is re-served under the terms of the source it came from, which is why `source` is on every event, every vessel, and every archived hour. If you display or redistribute the data, carry the source's attribution through.

| Source                 | License                                                                                                                                                                                                                                                             | What you must do                                                                                                                                                                                  |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Kystverket             | [NLOD 2.0](https://data.norge.no/nlod/en/2.0)                                                                                                                                                                                                                       | Credit: "Contains data under the Norwegian licence for Open Government data (NLOD) distributed by the Norwegian Coastal Administration." NLOD is not sublicensable: you are bound by it directly. |
| Fintraffic Digitraffic | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)                                                                                                                                                                                                           | Credit: "Source: Fintraffic / digitraffic.fi, license CC 4.0 BY."                                                                                                                                 |
| Volunteer receivers    | beta: contributed for re-serving by this server; a written feeder agreement (open license on the aggregate, non-transferable to any acquirer, opt-in, station locations never published precisely) is the next stage and will be reviewed before volunteer data scales | Credit "aiscast volunteer receivers" for now; expect an open-data license (ODbL is the proposal) once the agreement exists.                                                                       |
| AISHub aggregate       | AISHub membership; their published terms grant "use" only                                                                                                                                                                                                           | Treat as view-only: fine to display, do not build a product on these events alone. They may be withdrawn; `source: aishub` and the `aishub-terms/` archive tag make that a clean cut.             |
| aisstream.io           | no published terms                                                                                                                                                                                                                                                  | Same as AISHub: best effort, may disappear. `source: aisstream`.                                                                                                                                  |

aiscast's own code is [MIT](LICENSE). Volunteer station locations are never published with precision, UDP stations are identified by a keyed hash rather than an address, and the archive keeps raw receptions per source so any source can be purged. The full position, including the feeder agreement draft, the privacy rules, and how the project intends to fund itself without relicensing data, is in [PLAN.md](PLAN.md#licensing-and-attribution).

## Contributing

Developers and operators: see [CONTRIBUTING.md](CONTRIBUTING.md).
