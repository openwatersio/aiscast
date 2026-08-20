# Community and volunteer AIS networks

Compiled 2026-08-20 from upstream source, live pages, DNS/socket probes. Station counts are live counters read that day.

## Aggregators that accept volunteer feeds

| Aggregator | Operator | Transport | Feeder gets | Network size | Open re-exposure |
|---|---|---|---|---|---|
| AISHub | Astra Paging / VesselFinder Ltd | UDP NMEA → `data.aishub.net`, per-feeder port | aggregated REST snapshot (1 req/min) | 1,619 stations (1,349 online), 98.5k vessels/24 h | members only |
| VesselFinder | same group | UDP/TCP → `ais.vesselfinder.com` | free Premium | 1,446 stations | none |
| MarineTraffic | Kpler (since 2023) | UDP/TCP → `listener.marinetraffic.com` / `5.9.207.224`, per-station port | free plan + raw NMEA bounce-back | unpublished | none |
| ShipXplorer | AirNav (RadarBox) | closed `sxfeeder` (local UDP 34995, wraps AIS-catcher) or UDP → `hub.shipxplorer.com` | free Business plan, station page, ranking, free hardware near ports | ≥1,075 | none |
| MyShipTracking | Greece | UDP/TCP → `178.162.215.175` | tier + relay service | unpublished | API from €90/mo |
| ShipFinder.co | Pinkfroot (UK) | UDP/TCP `ais.shipfinder.co.uk:4001`, no key | apps | dormant since 2014 | none |
| aiscatcher.org | jvde-github | raw TCP `185.77.96.227:4242`, `COMMUNITY_HUB` binary framing | a map popup, no data back | 1,259 online / 843 public, 113k ships, 245M msgs/24 h per live `/stats`; `/addstation` marketing copy says 575+ stations / 73 countries (stale floor) | none: `/api/` robots-blocked, Turnstile-gated |
| AIS Friends | — | UDP → `ais.aisfriends.com` | quality-gated API | 465 stations | members only |
| SDRMap | Chaos Consulting | HTTP POST `https://ais.feed.sdrmap.org/` | community map | ~340 | undocumented |
| aisstream.io | — | does not accept feeds | — | undisclosed | free wss, no ToS at all |
| aprs.fi | Heikki Hannikainen | HTTP multipart `jsonais` → `aprs.fi/jsonais/post/$KEY` | map + free API (non-commercial) | — | no cloning |
| Airframes | community | HTTP → `feed.airframes.io:5599`, no signup | leaderboard | aviation-focused | — |
| BoatBeacon | Pocket Mariner (UK) | UDP/TCP `boatbeaconapp.com:5322` | station page | — | none |
| ShippingExplorer, VesselTracker, HPRadar, MLAT.uk, RadarVirtuel | — | UDP/HTTP | account | — | none |
| MastChain | DePIN startup 2026 | forked AIS-catcher | $MAST tokens | early | enterprise API planned |
| FleetMon | JAKOTA → Kpler | dead, no A record | — | — | gone |
| OpenSeaMap | OSM | no AIS network; "AIS" layer is MarineTraffic raster tiles | — | — | — |

Best single map of this landscape: [`sdr-enthusiasts/docker-shipfeeder`](https://github.com/sdr-enthusiasts/docker-shipfeeder), one container fanning one receiver to ~18 aggregators with a host/port/protocol table.

Dominant pattern: plain UDP NMEA on a per-feeder-assigned port, no auth; the port number is the identity. Newer entrants (aiscatcher.org, SDRMap, aprs.fi, Airframes, RadarVirtuel) use TCP/HTTP with keys.

## What feeders give away

- AISHub ([join-us](https://www.aishub.net/join-us)): feed required; API gate = ≥10 vessels, ≥90% uptime, ≤60 s downsampling, ≤10 s delay; no terms page at all.
- MarineTraffic ([terms](https://www.marinetraffic.com/en/p/terms)): "perpetual, transferable, irrevocable and royalty-free licence"; data "shall remain the exclusive property of Kpler".
- VesselFinder ([terms](https://www.vesselfinder.com/terms), 2026-01-15): §11.2 "transferable, sublicensable license to … commercialize"; §11.3 "AIS Station data is not submitted in confidence"; §11.5 benefits "discretionary"; §7.4 bans AI training and competing services.
- ShipXplorer, MyShipTracking, aiscatcher.org, aisstream.io: no feeder data agreement at all.
- FleetMon: help desk promised "you will be the owner of your data"; enforceable ToS said perpetual/transferable; sold to Kpler; gone.

## Is there an open-licensed community AIS network? No.

Not one volunteer AIS network applies CC/ODbL/any open license to its aggregate. aiscatcher.org is the largest and explicitly closed (no terms, no privacy page, `robots.txt` disallows `/api/`, `/hub/`, `/stations`, `/livemap`, `/tiles/`, `Content-Signal: ai-train=no`, all API paths 403). The "community feed overlay" that once returned data was replaced by a `window.open()` to aiscatcher.org ([community.js](https://github.com/jvde-github/AIS-catcher/blob/main/frontend/src/overlays/community.js)). No OpenSky-equivalent academic project. Contrast ADS-B: [adsb.lol](https://www.adsb.lol/docs/open-data/api/) publishes under ODbL 1.0 with history ODbL + CC0 and daily GitHub releases.

## Feeder software

AIS-catcher (GPLv3, v0.70) is the de-facto standard. `msgformat` values: `NMEA`, `NMEA_TAG` (`-o 7`), `FULL`, `JSON_NMEA`, `JSON_SPARSE`, `JSON_FULL`, `JSON_ANNOTATED`, `BINARY_NMEA`, `COMMUNITY_HUB`. Sinks: `-u host port` UDP (default `127.0.0.1:10110`), `-P host port` TCP client (persist on), `-S port` TCP server (5010, 64 clients), `-H [url]` HTTP POST (interval 60, `PROTOCOL AISCATCHER`), `-Q url` MQTT (`JSON_FULL`, topic `ais/data`), `-N` web + `/metrics`, `-X` community feed.

HTTP POST `PROTOCOL` variants: `AISCATCHER` (envelope, gzip optional), `MINIMAL`, `AIRFRAMES` (forces gzip, interval 30), `LIST` (one JSON per line), `NMEA` (newline-joined raw), `APRS` (multipart `jsonais`, gzip off). Empty POSTs are heartbeats carrying station lat/lon and receiver info. Keys: `url`, `userpwd` (HTTP Basic), `ssl_verify`, `stationid`/`id`, `interval` 1–86400, `timeout`, `gzip`, `response`, `protocol`, `lat`, `lon`, device metadata. Real-world: `-H https://ais.feed.sdrmap.org/ USERPWD $ID:$PASS GZIP on INTERVAL 15 RESPONSE off`.

Per-message JSON (`getNMEAJSON()`): `class`, `device`, `version`, `driver`, `hardware`, `channel`, `repeat`, `rxuxtime`, `toa`, `uuid`, `signalpower`, `ppm`, `station_id`, `mmsi`, `type`, `nmea[]`.

aiscatcher.org feed (`Engine.h`): TCP client to `185.77.96.227:4242`, `COMMUNITY_HUB` = every 100th message as `JSON_NMEA` keyframe, rest as `BINARY_NMEA` (`0xAC 0x00 flags rxtime_us[8] [sigpower ppm] channel bitlen payload [crc] 0x0A`, byte-stuffed). Sharing is on by default despite docs saying otherwise (`Engine.cpp:95`: "Currently ON by default"). Any station with any output configured and no `-X` is already feeding aiscatcher.org; existing stations are used to multi-homing.

Other software: AIS Dispatcher (AISHub, closed; UDP only, polygon/type filters, Linux web UI emits NMEA 4.10 tags; downsampling 0–300 s on types 1/2/3/18/19, no compressed format); rtl-ais (UDP `127.0.0.1:10110`, `-U`, `-T` TCP); dAISy family (serial 38400 only, DT-06 WiFi module for UDP broadcast); gpsd (ignores TAG blocks); kplex (emits `\s:kplex,c:…\`, strips on input); OpenCPN (ignores TAG blocks); Signal K (parses `s`, `c` only, no checksum); ShipXplorer `sxfeeder` (closed, armhf-only broke Pi 5 users).

On-boat receiver/gateway network defaults (for the Signal K stage): IANA `nmea-0183` is 10110 tcp/udp; Digital Yacht/Quark-elec/Navionics convention 2000; Vesper XB-8000 39150; em-trak 5000; Yacht Devices 1456; Actisense 60001–60003; Shipmodul MiniPlex-3 10110 and the only hardware emitting TAG blocks; Raymarine AIS700 has no network output; 4800 baud standard, 38400 for AIS. Signal K already reads all of these, so the plugin only needs SK's VDM/VDO events.

## Wire format details that matter

- Talker IDs: `AI` mobile, `AB`/`BS` base, `AN` AtoN, `AR` receiving station, `SA` shore; channel `A`/`B`, also `1`/`2` in the wild, and long-range `CD` (docker-shipfeeder relabels CD→AB because some aggregators choke).
- Padding: gpsd warns the most common error on AISHub is fill bits 2 too small (type 5 decoding as 426 bits not 424); accept fixed-size messages up to 5 bits over.
- TAG block checksum = XOR strictly between `\` and `*`; many published examples are wrong; validate but warn, don't drop.
- `c:` units ambiguous: contains `.` → fractional seconds; ≤12 digits → seconds; >12 → ms; >1e11 → µs. Keep both feeder time and receipt time.
- AIS-catcher emits `\s:s0,c:1777739134.027*69\` and `g:` only on multipart; on input it accepts `s:` only in its own `sN` form (don't replicate; Kystverket sends `s:2573515`, kplex `s:kplex`).
- USCG extended AIVDM appends after checksum: `,s1234,d-119,T12.34567123,r003669958,1085889680` (RSSI, dBm, TOA, station, epoch). Parse it.
- Reassemble on (seq ID, channel, station, arrival window); IDs cycle 0–9.

## Station economics and range

RTL-SDR V4 is end-of-line; V4L ($37.95, R828S) shipped Aug 2026. Budget station ~$78 (V4L + DIY dipole + Pi Zero 2 W); decent ~$178; dAISy-catcher build ~$394. Range d(nm) ≈ 1.23 × (√h_rx(ft) + √h_tx(ft)): 15 m rooftop → ~23 nm to Class A, ~13 nm to Class B; 100 m hill → 36 nm; 500 m headland → 64 nm. Coverage scales with range², so gap-targeted hilltop placements beat blanket rooftops (MarineTraffic/ShipXplorer siting rules: within 5–10 km of coast/port, clear sea view).

How ADS-B networks bootstrapped, ranked: free premium accounts (FlightAware/FR24; ShipXplorer proves it for AIS); gap-targeted free hardware; feeder-only data tiers; one-line installers and Pi images (ADSB.im); stats/leaderboards/coverage maps; shared Discord. ADSBexchange's 2023 sale to JETNET lost 15–20% of 9,000 feeders in a week ([Forbes](https://www.forbes.com/sites/cyrusfarivar/2023/02/02/adsb-exchange-flight-tracking-elonjet/)); airplanes.live wrote "cannot be sold, transferred or taken over by any one individual" into its [terms](https://airplanes.live/about/). Stable equilibrium is "open to contributors", designed deliberately.

## Signal K plugins that forward AIS

Two mechanisms exist: raw NMEA0183 relay over UDP, and decoded deltas over HTTPS POST.

| Package | Last publish | Target | Transport | AIS targets? |
|---|---|---|---|---|
| [`ais-forwarder`](https://github.com/hkapanen/ais-forwarder) | 2023-03 | MarineTraffic/AISHub/VesselFinder | UDP verbatim `!AIVDM`/`!AIVDO` | yes |
| [`@signalk/aisreporter`](https://github.com/SignalK/aisreporter) | 2026-06 | any aggregator | UDP, synthesized type 18/24 | own only |
| [`@meri-imperiumi/signalk-aprsfi-ais-reporter`](https://github.com/meri-imperiumi/signalk-aprsfi-ais-reporter) | 2026-06 | aprs.fi | HTTPS multipart `jsonais` | targets only |
| [`kahu-signalk`](https://github.com/KAHU-radar/radarhub-signalk) | 2026-05 | kahu.earth | TCP + Avro | AIS + ARPA |
| [`signalk-cloud`](https://github.com/sbender9/signalk-cloud) | 2024-08 | cloud.signalk.org | WS deltas | opt-in |
| [`signalk-saillogger`](https://github.com/Saillogger/signalk-saillogger) | 2026-07 | saillogger | HTTPS POST | own + targets |
| [`signalk-web-tracker`](https://github.com/ofernander/signalk-web-tracker) | 2026-08 | self-hosted | HTTPS POST, HMAC-SHA256 | own |
| [`crowd-depth`](https://github.com/openwatersio/crowd-depth) | 2026-08 | api.openwaters.io | HTTPS POST | own |
| [`@signalk/udp-nmea-plugin`](https://github.com/SignalK/udp-nmea-plugin) | 2023-09 | any UDP | UDP | both events |
| [`signalk-to-boating`](https://github.com/OpenFairWind/signalk-to-boating) | 2026-08 | Garmin/Navionics UDP 2000 | synthesizes AIVDM | yes |

No plugin hardcodes an aggregator host/port (all default `0.0.0.0:12345`) because the incumbents issue per-station ports. `ais-forwarder` is ~15 lines: `app.on('nmea0183', send)` filtered by `/!AIVDM/` / `/!AIVDO/`, `dgram` send to each endpoint.

Data-acquisition mechanisms: `app.on('nmea0183')` (raw inbound, 0183 providers only, N2K AIS never appears), `app.on('nmea0183out')` (where N2K-derived AIS appears via `signalk-n2kais-to-nmea0183`), `app.on('N2KAnalyzerOut')`, `app.subscriptionmanager.subscribe({context:'vessels.*'})`, `app.getPath('vessels')` on a timer. Lessons: default events to `'nmea0183,nmea0183out'` (aprs.fi reporter does; `ais-forwarder`'s 0183-only default is a support burden); avoid `app.streambundle` (baconjs version stall documented by aisreporter); randomize upload schedule (noforeignland); reject Null Island, suppress unchanged positions, use real AIS sentinels (SOG 1023, COG 3600, HDG 511). Appstore keywords: `signalk-node-server-plugin`, `signalk-category-ais`.

## Legal and ethical position

- USA: 47 USC §605(a) exempts radio communication "transmitted by any station for the use of the general public, which relates to ships". Clean.
- Germany: § 5 TDDDG permits reception of messages intended "für die Allgemeinheit oder für einen unbestimmten Personenkreis". Clean.
- UK: Wireless Telegraphy Act 2006 s.48 is grey on text but UK aggregators operate openly; no known prosecution. Low residual risk.
- No jurisdiction found restricting AIS retransmission. IMO MSC 79 (2004) "condemned those who irresponsibly publish AIS data" — non-binding, 22 years old, universally ignored.
- Privacy: Class B on pleasure craft ties a name/MMSI/position to an individual (GDPR-relevant). BarentsWatch's open feed excludes fishing <15 m and leisure/sail <45 m, a state authority's precedent. Default: publish everything, offer a documented vessel opt-out, treat small-craft handling as a policy dial. Don't publish precise station coordinates without opt-in (VesselFinder's norm).
- Security: mitigations are transmit-side; the receive-side response is provenance and spoof detection, not withholding.

## Implications for our ingest and feeder program

Accept on day one, by leverage per effort: (1) UDP raw NMEA with TAG-block prefixes and USCG trailing fields, per-station port; (2) HTTP POST `jsonaiscatcher` envelope with gzip + Basic auth + empty-batch heartbeat, content-sniffing `LIST`/`NMEA` too; (3) TCP client push; (4) our Signal K plugin; (5) MQTT. Do not build `COMMUNITY_HUB` binary framing or a closed feeder binary.

What we can offer that nobody does: the aggregate feed back to everyone live (raw `!AIVDM` with TAG blocks, bbox-filterable); an explicit open license on the aggregate (ODbL, history CC0, published before data exists); a plain-language feeder agreement (non-exclusive, only what's needed to run the commons, not transferable to an acquirer); a governance commitment that the project can't be sold; station pages, coverage maps, uptime alerts; a one-line installer and Docker image that multi-homes and a PR to `docker-shipfeeder`; gap-targeted free hardware later; multi-station provenance and spoof detection (AIS's MLAT analogue).

Operational cautions: be opt-in and say so (contrast AIS-catcher's default-on); seed the map from Finland/Norway on day one so it recruits.

Unresolved: whether Norway's raw TCP applies the same small-craft filter as the API (Class B is present in the raw stream, ~44 of 447 in a 15 s sample); ShipXplorer upstream host; MarineTraffic/VesselFinder hiding policies (Cloudflare-gated).
