# aisstream.io protocol and open-source building blocks

Researched 2026-08-20.

## Headline findings

- aisstream.io's wire format is `encoding/json` marshalling of [`github.com/BertoldVdb/go-ais`](https://github.com/BertoldVdb/go-ais) structs. Proof: the misspelled `VenderIDModel`/`VenderIDSerial` fields (alongside `VendorIDName`) appear in both aisstream's live output and [`go-ais/messages.go:308`](https://raw.githubusercontent.com/BertoldVdb/go-ais/master/messages.go); `Valid`, embedded `Header`, `CommunicationStateIsItdma`, and the lowercase `Fixtype` in `AidsToNavigationReport` all match. Using go-ais gives byte-exact wire compatibility.
- aisstream.io is degraded. 2026 record: TLS cert on the stream endpoint expired 2026-05-20 and sat unrenewed ([aisstream/aisstream#21](https://github.com/aisstream/aisstream/issues/21)); expired again 2026-07-19, auto-renewal did not run ([issues#229](https://github.com/aisstream/issues/issues/229); renewed, valid to 2026-09-18); 40+ hour blackout from 2026-08-05 ([issues#212](https://github.com/aisstream/issues/issues/212)); ~10 open "connects, subscribes, zero messages" reports since then, down ~14 of the last 20 days; repo unpushed since 2022-12-21, 196 open / 0 closed issues. No first-party status page; a third party runs <https://aisuptime.buttermilkgreen.fyi/>. Pattern: solo-maintainer operations failures, not capacity or architecture.
- Throttling is per API key, which matters for a shipped app bundling one key across every install; hold any bundled key server-side.
- There is no open-source aisstream equivalent: nothing on GitHub does bbox-subscribed WebSocket fan-out of a global AIS feed.

## aisstream.io protocol

Sources: [documentation](https://aisstream.io/documentation), [ais-message-models](https://github.com/aisstream/ais-message-models) (MIT; `type-definition.yaml` is authoritative), [example](https://github.com/aisstream/example).

Endpoint `wss://stream.aisstream.io/v0/stream`, WSS only, CORS deliberately unsupported (browsers must proxy).

### Subscription

```json
{
  "APIKey": "<key>",
  "BoundingBoxes": [[[25.835302, -80.207729], [25.602700, -79.879297]]],
  "FiltersShipMMSI": ["368207620", "367719770"],
  "FilterMessageTypes": ["PositionReport"]
}
```

- Coordinates are `[lat, lon]`; any two opposite corners. The official JS/TS examples use `[[-180,-90],[180,90]]` (`[lon, lat]`), which is wrong but invisible for a world box.
- Key field name is case-insensitive (`APIKey`, `Apikey`, `APIkey` all seen and all work, Go `encoding/json` semantics). A compatible server must accept all casings; same for `MetaData`/`Metadata`.
- `FiltersShipMMSI` max 50, 9-char strings. `FilterMessageTypes` duplicates are an error. `APIKey` and `BoundingBoxes` required.

| Rule | Value |
|---|---|
| Subscribe deadline after connect | 3 s, else close |
| Re-subscribe | resend on same socket, swap-and-replace |
| Subscription update rate | max 1/s |
| Expected throughput for world bbox | ~300 msg/s; server may close slow readers |
| Concurrent connections/key | undocumented; exceeded → HTTP 429 at connect ([#253](https://github.com/aisstream/issues/issues/253)) |

Error frames: `{"error": "Api Key Is Not Valid"}`, `{"error": "Subscription Object Is Malformed"}`, `{"error": "concurrent connections per user exceeded"}` (the last also fires on re-subscribe in some cases, [#77](https://github.com/aisstream/issues/issues/77)).

### Outbound message

```json
{
  "Message": {
    "PositionReport": {
      "Cog": 4.5, "CommunicationState": 59916,
      "Latitude": 55.990715, "Longitude": -3.183045,
      "MessageID": 3, "NavigationalStatus": 3,
      "PositionAccuracy": false, "Raim": false,
      "RateOfTurn": -128, "RepeatIndicator": 0,
      "Sog": 0, "Spare": 0, "SpecialManoeuvreIndicator": 0,
      "Timestamp": 36, "TrueHeading": 511,
      "UserID": 250013879, "Valid": true
    }
  },
  "MessageType": "PositionReport",
  "MetaData": {
    "MMSI": 250013879,
    "MMSI_String": 250013879,
    "ShipName": "DINOPOTES           ",
    "latitude": 55.990715,
    "longitude": -3.183045,
    "time_utc": "2023-10-22 22:47:36.94034384 +0000 UTC"
  }
}
```

Compatibility quirks:

1. `MMSI_String` is an unquoted JSON number (Go clients such as [seaspy](https://github.com/bbailey1024/seaspy/blob/main/aisstream/aisstream.go) type it `int`). Emit a number; parse leniently.
2. `time_utc` is Go `time.Time.String()`: `2006-01-02 15:04:05.999999999 -0700 MST` → `"2023-10-22 22:47:36.94034384 +0000 UTC"`. Not RFC3339; fractional digits vary.
3. `MetaData.ShipName` is untrimmed and padded; `""` when unknown.
4. `ShipName` and `MetaData.latitude/longitude` come from a server-side per-MMSI cache, present even for positionless messages (types 5, 24). This is how bbox filtering routes static data: the server must keep a last-position cache per MMSI. Recent captures round MetaData lat/lon to 5 decimals.
5. `Fixtype` (lowercase t) in `AidsToNavigationReport`; `FixType` elsewhere. `VenderIDModel`/`VenderIDSerial` vs `VendorIDName` in `StaticDataReport.ReportB`. go-ais names the type-12 struct `AddessedSafetyMessage` while aisstream's documented enum says `AddressedSafetyMessage`; which spelling aisstream actually emits on the wire is unverified (capture a type 12 from aisstream to settle it).
6. `MetaData` is schema-less (`map[string]interface{}` in the official Go client).
7. No raw NMEA, station ID, or signal metrics are exposed. We can add fields (clients ignore unknown keys).
8. `PositionReport` covers types 1, 2, 3 (`MessageID` disambiguates).

MessageType enum (24): `PositionReport`, `UnknownMessage`, `AddressedSafetyMessage`, `AddressedBinaryMessage`, `AidsToNavigationReport`, `AssignedModeCommand`, `BaseStationReport`, `BinaryAcknowledge`, `BinaryBroadcastMessage`, `ChannelManagement`, `CoordinatedUTCInquiry`, `DataLinkManagementMessage`, `DataLinkManagementMessageData`, `ExtendedClassBPositionReport`, `GroupAssignmentCommand`, `GnssBroadcastBinaryMessage`, `Interrogation`, `LongRangeAisBroadcastMessage`, `MultiSlotBinaryMessage`, `SafetyBroadcastMessage`, `ShipStaticData`, `SingleSlotBinaryMessage`, `StandardClassBPositionReport`, `StandardSearchAndRescueAircraftReport`, `StaticDataReport`.

Field lists (all share `MessageID`, `RepeatIndicator`, `UserID`, `Valid`):

| Type | Fields |
|---|---|
| `PositionReport` | `NavigationalStatus`, `RateOfTurn`, `Sog`, `PositionAccuracy`, `Longitude`, `Latitude`, `Cog`, `TrueHeading`, `Timestamp`, `SpecialManoeuvreIndicator`, `Spare`, `Raim`, `CommunicationState` |
| `ShipStaticData` | `AisVersion`, `ImoNumber`, `CallSign`, `Name`, `Type`, `Dimension{A,B,C,D}`, `FixType`, `Eta{Month,Day,Hour,Minute}`, `MaximumStaticDraught`, `Destination`, `Dte`, `Spare` |
| `StandardClassBPositionReport` | `Spare1`, `Sog`, `PositionAccuracy`, `Longitude`, `Latitude`, `Cog`, `TrueHeading`, `Timestamp`, `Spare2`, `ClassBUnit`, `ClassBDisplay`, `ClassBDsc`, `ClassBBand`, `ClassBMsg22`, `AssignedMode`, `Raim`, `CommunicationStateIsItdma`, `CommunicationState` |
| `ExtendedClassBPositionReport` | `Spare1`, `Sog`, `PositionAccuracy`, `Longitude`, `Latitude`, `Cog`, `TrueHeading`, `Timestamp`, `Spare2`, `Name`, `Type`, `Dimension`, `FixType`, `Raim`, `Dte`, `AssignedMode`, `Spare3` |
| `AidsToNavigationReport` | `Type`, `Name`, `PositionAccuracy`, `Longitude`, `Latitude`, `Dimension`, `Fixtype`, `Timestamp`, `OffPosition`, `AtoN`, `Raim`, `VirtualAtoN`, `AssignedMode`, `Spare`, `NameExtension` |
| `StaticDataReport` | `Reserved`, `PartNumber`, `ReportA{Valid,Name}`, `ReportB{Valid,ShipType,VendorIDName,VenderIDModel,VenderIDSerial,CallSign,Dimension,FixType,Spare}` |
| `BaseStationReport` | `UtcYear`, `UtcMonth`, `UtcDay`, `UtcHour`, `UtcMinute`, `UtcSecond`, `PositionAccuracy`, `Longitude`, `Latitude`, `FixType`, `LongRangeEnable`, `Spare`, `Raim`, `CommunicationState` |
| `LongRangeAisBroadcastMessage` | `PositionAccuracy`, `Raim`, `NavigationalStatus`, `Longitude`, `Latitude`, `Sog`, `Cog`, `PositionLatency`, `Spare` |
| `UnknownMessage` | `Error` |

Clients to test against: [aisstream/example](https://github.com/aisstream/example) (Go/Python/JS/TS/Java/Rust/Dart/C/C++/C#/browser); [`aisstream-ts`](https://www.npmjs.com/package/aisstream-ts) (MIT, 2026-05, zero deps, reconnects, Class-B static merge — best test target); `github.com/aisstream/ais-message-models/golang/aisStream`; [seaspy](https://github.com/bbailey1024/seaspy); [signalk-aisstream](https://www.npmjs.com/package/signalk-aisstream) (actively maintained 2026-08).

## Decoder libraries

| Library | Lang | License | Activity | Types | Multipart | TAG block | Notes |
|---|---|---|---|---|---|---|---|
| [BertoldVdb/go-ais](https://github.com/BertoldVdb/go-ais) | Go | MIT | 2024-10 | all 27 | yes (`aisnmea/assembler.go`) | decode+encode+merge | what aisstream runs; also encodes |
| [adrianmo/go-nmea](https://github.com/adrianmo/go-nmea) | Go | MIT | 2026-06 | VDM/VDO framing | — | yes | sentence layer under go-ais |
| [pyais](https://github.com/M0r13n/pyais) | Python | MIT | 2026-08 | all 27 | yes | c,d,n,r,s,t (no g) | decode+encode |
| [libais](https://github.com/schwehr/libais) | C++/py | Apache-2.0 | 2026-06 | all 27 | yes | partial | reference impl |
| [ais](https://crates.io/crates/ais) | Rust | Apache-2.0 | 2026-02 | most | yes | no | deps `nom`+`heapless` → `no_std`, clean WASM |
| [nmea-parser](https://crates.io/crates/nmea-parser) | Rust | Apache-2.0 | 2024-06 | AIS+GNSS | yes | no | WASM-clean, serde |
| [ggencoder](https://www.npmjs.com/package/ggencoder) | JS | Apache-2.0 | 2026-06 | 1,2,3,5,18,24 | partial | no | Signal K's decoder |
| [ais-web](https://github.com/felipecarrillo100/ais-web) | TS | MIT | 2025-07 | 1,2,3,5 | yes | no | too thin |
| [ais-stream-decoder](https://www.npmjs.com/package/ais-stream-decoder) | TS | MIT | 2021 | partial | yes | no | uses `node:stream`, not Worker-safe |
| [Ais.Net](https://github.com/ais-dotnet/Ais.Net) | C# | AGPL-3.0 | active | all | yes | yes | license blocker |
| [GFW ais-tools](https://github.com/GlobalFishingWatch/ais-tools) | Python | Apache-2.0 | active | — | out-of-order join | add-tagblock | ingest normalization |

For a Worker/V8 isolate: no pure-JS decoder is production-adequate; compile the Rust `ais` crate to wasm32. For a standalone binary: Go with go-ais + aisnmea (wire-identical to aisstream, all 27 types, TAG block handling, MIT).

Known go-ais bug to fix on adoption: `aisnmea/assembler.go` keys fragments by `(NumFragments, MessageID, lastChannel, VDO)` with no station identity, and expires by message count (32) not wall time. Wrong for a multi-station aggregator; wrap per-source or add station to the key.

## Existing open-source aggregators

| Project | Lang | License | Activity | What it does |
|---|---|---|---|---|
| [dma-ais/AisLib](https://github.com/dma-ais/AisLib) | Java | Apache-2.0 | active v2.8.7 Aug 2026 | decode/encode, TCP/UDP readers, `AisBus` fan-out, dedupe, downsample, filter DSL with bbox |
| [dma-ais/AisStore](https://github.com/dma-ais/AisStore) | Java | Apache-2.0 | dormant 2023 | Cassandra archive, dual-resolution geo-cell partitioning |
| [dma-ais/AisView](https://github.com/dma-ais/AisView) | Java | Apache-2.0 | dead 2019 | HTTP chunked `/stream?box=…`, closest prior art to aisstream |
| [AIS-catcher](https://github.com/jvde-github/AIS-catcher) | C++ | GPL-3.0 | very active | SDR→NMEA/JSON; UDP/TCP/HTTP/MQTT out; web server; community feed |
| [sdr-enthusiasts/docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) | shell | GPL-3.0 | active | fans one station to ~12 aggregators; best inventory of who accepts what |
| [kplex](https://github.com/stripydog/kplex) | C | GPL-3.0 | stale | NMEA mux; emits TAG blocks |
| [aismixer](https://github.com/iliyan85/aismixer) | Python | — | 2026 | AIS mux: reassembly, dedupe, TAG-block egress |
| [mhaberler/adsb-feeder](https://github.com/mhaberler/adsb-feeder) | — | — | — | only real dynamic-bbox-over-WebSocket implementation (ADS-B) |

AIS Dispatcher (aishub) is closed freeware; trivial open reimplementations exist. aiscatcher.org's server is closed; only the upload client is public (`COMMUNITY_HUB` over TCP, `-X <key>`). [AisVirtualNet](https://github.com/dma-ais/AisVirtualNet) can load-test fan-out with virtual transponders.

Worth copying: AisLib's stateful filter (static messages survive bbox filter via last-position memory); AisStore's dual-resolution cell partitioning (1° and 10° cells × 10-min blocks); AisBus's per-consumer bounded queue with overflow accounting; AIS-catcher's delta/cursor API (`/api/ships_array.json`, `/api/changes.json?since=`, SSE push).

## Dedupe, reassembly, TAG blocks

Dedupe references: AIS-catcher `unique_interval = 3` s, key = packed MMSI + channel + type + FNV-1a(payload) in a circular buffer ([Message.h](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Marine/Message.h)); AisLib `DuplicateFilter` window 10 s, key = full six-bit payload string (deliberately not hashed), amortized sweep; `ReplayDuplicateFilter` keys on TAG-block time. Recommended: key `(payload, channel)`, window 3–10 s, on TAG-block `c:` time not arrival time. Table size = peak rate × window (trivial).

Reassembly: the sequential message ID is 0–9 and scoped to one link; two stations will collide on `(count, seqid, channel)`. Key reassembly by `(station, channel, seqid, count)` per station with a few-seconds wall-clock expiry; empty channel field means "same as previous" (per-stream state). Order: reassemble → dedupe → decode → fan out.

TAG block format ([gpsd](https://gpsd.gitlab.io/gpsd/AIVDM.html)): `\g:1-2-73874,n:157036,s:r003669945,c:1241544035*4A\!AIVDM,...`; checksum XOR over chars between `\` and `*`. Keys: `c` unix time (s or ms, spec-ambiguous), `d` dest, `g` `<n>-<total>-<groupid>`, `n` line count, `r` relative time, `s` source/station, `t` text. AIS-catcher emits `\s:s0,c:1750955733.123456*4F\` (station prefixed with literal `s`, `c:` is a float); kplex emits `\s:kplex,c:1423133048*5F\`. Require `s:` and `c:` from feeders.

Observed from Kystverket (2026-08-20): `\s:2573405,c:1787233894*00\!BSVDM,1,1,,A,...` — integer station, integer epoch, ~38 sentences/s.

## AIS-catcher output formats

Common fields on every JSON message ([reference](https://jvde-github.github.io/AIS-catcher-docs/references/JSON-decoding/)): `class`, `device`, `version`, `driver`, `hardware`, `scaled`, `channel`, `nmea[]`, `type`, `repeat`, `mmsi`; conditional `rxtime` (`YYYYMMDDHHMMSS`), `rxuxtime` (float epoch), `signalpower`, `ppm`, `toa`, `station_id`, `country`.

HTTP POST (`-H`), default `AISCATCHER` protocol, posted every `interval` seconds (default 60):

```json
{
  "protocol": "jsonaiscatcher",
  "encodetime": "20221102171325",
  "stationid": "MyStation",
  "receiver": {"description": "AIS-catcher v0.39", "version": 39, "engine": "Base (non-coherent)", "setting": "droop ON fp_ds OFF "},
  "device": {"product": "FILE-RAW", "vendor": "", "serial": "", "setting": "rate 1536K file posterholt.raw format CU8"},
  "msgs": [
    {"class":"AIS","device":"AIS-catcher","rxtime":"20221102171324","scaled":true,"channel":"A","nmea":["!AIVDM,1,1,,A,13`fL1PP140KCELMBO7SS?wH0@Jv,0*50"],"ppm":0.0,"type":1,"mmsi":244030470,"status":0,"speed":6.8,"accuracy":false,"lon":5.964237,"lat":51.185970,"course":90.8,"repeat":0,"second":44,"maneuver":0,"raim":false,"radio":67262}
  ]
}
```

Other protocols: `MINIMAL`, `LIST` (one JSON per line), `AIRFRAMES`, `APRS`, `NMEA`. Options `gzip`, `userpwd`, `interval`, `lat`/`lon`. Example: `AIS-catcher -H https://host/ingest userpwd Station:Password gzip on interval 5`.

MQTT (`-Q`): `mqtt://`, `mqtts://`, `wsmqtt://`, `wssmqtt://`; `TOPIC ais/%station%/%type%/%channel%/%mmsi%`; `msgformat JSON_NMEA|JSON_FULL|NMEA|NMEA_TAG`.

## Sizing

| Metric | Value | Source |
|---|---|---|
| Global terrestrial unique rate | ~300 msg/s world bbox | aisstream docs |
| AISHub stations | 1,619 registered / 1,349 online | [aishub.net/stations](https://www.aishub.net/stations) 2026-08-20 |
| Distinct vessels / 24 h | 98,417 | same |
| Per-station concurrent ships | median ~10–50; busiest 236 | same |
| Decode throughput | ~30k msg/s single laptop (Go) | [Lenses writeup](https://lenses.io/blog/mqtt-kafka-connect-with-ais-data) |

Derived: a station tracking N vessels sees ~N/10 to N/3 msg/s (5–15 msg/s typical, 25–80 busy port). Plan 1,000–3,000 msg/s raw inbound, ~300–500 msg/s unique. Cost is fan-out, not ingest.

## Recommended path

1. Ingest AIS-catcher's UDP NMEA, TCP, HTTP POST (`LIST`), and MQTT. Require TAG blocks.
2. Per-station reassembly keyed `(station, channel, seqid, count)` → payload+channel dedupe 3–10 s on TAG-block time.
3. Decode with go-ais (free wire compat); Rust `ais` → WASM if decoding in a Worker.
4. Per-MMSI last position + name cache to populate `MetaData` and route positionless messages to bbox subscribers.
5. Bbox fan-out with per-connection bounded queues and drop accounting.
6. Compat surface: case-insensitive keys, numeric `MMSI_String`, Go-style `time_utc`, untrimmed `ShipName`, the typos, 3-second subscribe deadline. Test against `aisstream-ts` and the official examples.

## Decoder benchmarks (measured 2026-08-20, 998-sentence real-world corpus, Apple Silicon)

| Library | Lang | Throughput | Decoded | Types |
|---|---|---:|---:|---|
| nmea-kit 0.8.2 | Rust | 1,805k/s | 904/998 | 1–27 |
| ais 0.12.0 | Rust | 1,498k/s | 902/998 | 1–21, 24, 27 |
| go-ais `CodecNewFast(…, true)` | Go | 1,069k/s | — | 1–27 |
| go-ais reflection default | Go | 253k/s | — | 1–27 |
| nmea-parser 0.11.0 | Rust | 307k/s | 851/998 | 1–6, 9–27 (silently drops 7, 8) |

Inside V8 with no Node globals: nmea-kit→WASM 513k/s, 904/998, 62.7 KB (24.4 KB gz), runs in a bare isolate; ggencoder 557k/s but 809/998 and needs `Buffer` plus `fs`/`net`/`util` stubs; ais-nmea-decoder 337k/s, 706/998, 23.5 KB, isolate-clean but hard-matches `AIVDM`/`AIVDO` talker IDs (rejected 10.6% of the corpus: `BSVDM`, `ABVDM`, `ANVDM`) and only handles 2-fragment messages. No JS library supports TAG blocks (they split on commas and expect 7 fields).

go-ais gotcha: `ais.CodecNew()` uses reflection and is ~4× slower; call `ais.CodecNewFast(false, false, true)`. Raw packet decode 191 ns/op fast vs 2,685 ns/op reflection.

[nmea-kit](https://crates.io/crates/nmea-kit): zero deps, `unsafe_code = "forbid"`, types 1–27 plus encoders, bounded reassembly (10 slots/channel, ITU-derived caps), TAG block exposed as raw string. Risk: created 2026-04, one maintainer, API still moving. Best Worker-side decoder if needed.

Live check (2026-08-20): go-ais fast codec + `aisnmea` over 261 live Kystverket sentences: 216 decoded, 45 pending fragments, 0 errors; TAG block `s:`/`c:` parsed. Types seen: PositionReport, ShipStaticData, StandardClassBPositionReport, StaticDataReport, AidsToNavigationReport.

Test corpora with expected outputs: GPSD [`test/sample.aivdm`](https://gitlab.com/gpsd/gpsd/-/blob/master/test/sample.aivdm) (all 27 types, annotated, BSD-2); libais [`typeexamples.nmea/json`](https://github.com/schwehr/libais/blob/main/test/data/typeexamples.nmea) and [`tagblock.nmea/json`](https://github.com/schwehr/libais/blob/main/test/data/tagblock.nmea) (Apache-2.0); pyais [`tests/multi.txt`](https://github.com/M0r13n/pyais/blob/main/tests/multi.txt) multipart torture test (MIT). Field semantics reference: [GPSD AIVDM doc](https://gpsd.gitlab.io/gpsd/AIVDM.html).
