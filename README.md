# aiscast

Live AIS vessel traffic, streamed by bounding box, from open government feeds and volunteer receivers. Part of [Open Waters](https://openwaters.io). The public instance is `ais.openwaters.io`. A live map is at [openwatersio.github.io/aiscast](https://openwatersio.github.io/aiscast/).

This is a beta. There is no SLA. Coverage is uneven. The terms for re-serving some sources are still unsettled (see [Coverage](#coverage)). Live status and uptime history are at [status.openwaters.io](https://status.openwaters.io).

## Usage

**aisstream.io clients.** aiscast speaks aisstream.io's protocol on `wss://ais.openwaters.io/v0/stream`: the same subscribe message, the same `MessageType` / `MetaData` / `Message` frames. Change the hostname and use your aiscast token as the `APIKey`. Existing code keeps working: [aisstream-ts](https://www.npmjs.com/package/aisstream-ts), the official Go/Python/JS examples, and `signalk-aisstream`.

**Native API** ([full reference](https://openwaters.io/api/ais/)).

- `wss://ais.openwaters.io/v1/stream`: send `{"type":"subscribe","bbox":[[minLat,minLon,maxLat,maxLon]]}` or `{"type":"subscribe","mmsi":[368168720]}` (or both). You receive one JSON event per decoded AIS message. Each event carries its source, station, receive time, raw sentence, and the decoded fields. Add `"snapshot":true` to get the last known messages for every vessel already tracked in the subscription first, then live traffic. Subscribing needs no token.
- `GET https://ais.openwaters.io/v1/vessels?bbox=minLat,minLon,maxLat,maxLon` (or `?mmsi=a,b,c`): GeoJSON of every vessel currently in view. Each vessel carries its last position, name, type, course, speed, heading, and when and from where aiscast last heard it. No token.
- `GET https://ais.openwaters.io/v1/stations`: every source aiscast is hearing, with message counts and age.

**Tokens.** `/v0/stream` needs a token. A personal token is self-serve and never expires. Use the [token page](https://openwatersio.github.io/aiscast/token.html), or generate an Ed25519 keypair and `POST https://ais.openwaters.io/v1/keys` with `{"pubkey":"<base64url public key>"}`. The response is your token. It carries the personal tier: 2 streams, 50 messages/s, and a 20°×20° area. Feed data from the same token and it becomes a feeder token by itself.

Tiers and limits are in [docs/limits.md](docs/limits.md). For more than the feeder tier, or for commercial use, write to hello@openwaters.io.

Events carry a `source`, so you can tell a live volunteer receiver from a government feed or an aggregate. `synthesized: true` marks messages that aiscast re-encoded from a non-NMEA source.

## Contributing data

If you run an AIS receiver, send its data here. aiscast re-serves it to everyone, deduplicates it against the open feeds and every other station, and forwards it to AISHub as part of our reciprocal feed. Only volunteer receptions go to AISHub, and aiscast never relays public feeds there, per their terms. The terms for contributing are the [contributor agreement](docs/contributor-agreement.md).

- **AIS-catcher** (preferred: authenticated HTTP, works behind any NAT): get a token at [openwatersio.github.io/aiscast/token.html](https://openwatersio.github.io/aiscast/token.html) (one click, stays in your browser), then `AIS-catcher ... -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`. Your data appears as `source: http:<station id>`. Ask for a named station with higher limits.
- **UDP** (no token): AIS-catcher `-u ais.openwaters.io 10110`, [docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) with host `ais.openwaters.io` port `10110`, or any NMEA forwarder sending plain `!AIVDM` / `!AIVDO` sentences (TAG blocks welcome). Your station appears as `udp:<id>`, a keyed hash of your address, never the address itself. If a sender's `!AIVDO` sentences identify the vessel, aiscast keys the station by that MMSI instead.
- **Signal K**: add a UDP target `ais.openwaters.io:10110` in [`ais-forwarder`](https://github.com/hkapanen/ais-forwarder) (forward AIVDM and AIVDO). Or install the [`signalk-aiscast`](signalk-plugin/README.md) plugin. It needs no token. It shares what your receiver hears, and your own position, taken from the transponder or built from Signal K when an AIS transponder is not available. It shows aiscast traffic when you have no receiver.

Your station page is the [map](https://openwatersio.github.io/aiscast/) with `?station=<your id>`. It shows vessels heard, coverage extent, message counts, and how many messages another station heard first. `GET /v1/stations/{id}` returns the same numbers. Feeders get the deduplicated raw stream back on `wss://ais.openwaters.io/v1/nmea`.

## Coverage

| Source                 | Where                                                                     | Freshness                                       |
| ---------------------- | ------------------------------------------------------------------------- | ----------------------------------------------- |
| Kystverket             | Norwegian coast, 40–60 nm out                                             | live                                            |
| BarentsWatch           | Norwegian EEZ, Svalbard, Jan Mayen (satellite + offshore receivers)       | live                                            |
| Fintraffic Digitraffic | Finnish waters                                                            | live                                            |
| Volunteer receivers    | wherever they are (Buzzards Bay / Vineyard Sound as of the first station) | live                                            |
| AISHub aggregate       | worldwide terrestrial, ~50k vessels                                       | 1–6 min (their snapshot refreshes every ~5 min) |
| aisstream.io           | worldwide, when it is up                                                  | live, but frequently down                       |

Every event names the source it came from. [docs/policy.md](docs/policy.md#sources-deliberately-not-used) lists the sources this project leaves out on purpose, and why.

## Licensing and attribution

Licensing is per source. aiscast does not relicense the aggregate. aiscast re-serves each event under the terms of the source it came from. That is why `source` is on every event, every vessel, and every archived hour. If you display or redistribute the data, include the source's attribution.

| Source                 | License                                                                                                                                               | What you must do                                                                                                                                                                              |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Kystverket             | [NLOD 2.0](https://data.norge.no/nlod/en/2.0)                                                                                                         | Credit: "Contains data under the Norwegian licence for Open Government data (NLOD) distributed by the Norwegian Coastal Administration." NLOD is not sublicensable, so it binds you directly. |
| BarentsWatch           | [NLOD](https://data.norge.no/nlod/en/2.0) per the [BarentsWatch API terms](https://www.barentswatch.no/en/articles/api-terms-and-conditions/)          | Credit: "Data delivered by BarentsWatch." It is the same AIS Norge data as Kystverket, so the Kystverket credit applies too.                                                                   |
| Fintraffic Digitraffic | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)                                                                                             | Credit: "Source: Fintraffic / digitraffic.fi, license CC 4.0 BY."                                                                                                                             |
| Volunteer receivers    | The [contributor agreement](docs/contributor-agreement.md) dedicates receptions to the public domain under CC0 and publishes the aggregate under ODbL | Credit "Open Waters AIS volunteer receivers (https://openwaters.io/ais/)". Individual receptions are CC0. The aggregate carries ODbL.                                                         |
| AISHub aggregate       | AISHub membership: contributors "are allowed to use the aggregated data for free". No stated restriction on publication or commercial use             | Credit "AISHub" and keep the `source: aishub` value on the data.                                                                                                                              |
| aisstream.io           | no published terms                                                                                                                                    | Best effort, may disappear. `source: aisstream`.                                                                                                                                              |

aiscast's own code is [MIT](LICENSE). aiscast identifies UDP stations by a keyed hash instead of an address (a station that sends `!AIVDO` is keyed by that MMSI, and its position is in the data it shares). The archive keeps raw receptions per source, so the project can purge any source. [docs/policy.md](docs/policy.md) states the full position: what the [contributor agreement](docs/contributor-agreement.md) says, the privacy rules, and how the project intends to fund itself without relicensing data.

## Contributing

Developers and operators: see [CONTRIBUTING.md](CONTRIBUTING.md).
