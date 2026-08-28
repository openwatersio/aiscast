# aisstream.io open issues: index and summary

Compiled 2026-08-20 from every open issue in the five `aisstream` GitHub repositories (305 open: `issues` 196, `aisstream` 31, `example` 15, `ais-message-models` 3, `Projects-Using-aisstream.io` 60; the 91 closed titles were scanned for context). Bodies and comment threads were read in full. This is the demand signal behind what aiscast prioritizes; the [index](#index) at the bottom lists every issue with a category, a signal score (comments, reactions, "me too"s), and what it implies for the hub.

## Headline

- **The product gap is operational, not functional.** Across the 245 technical issues, 84 are outages (36 "connects and subscribes but zero messages", 18 expired TLS certificates, 16 other, 14 login/OAuth) and a further 18 are people asking for a status page, an explanation, or a way to pay. Feature requests are 16. What users want from a replacement is a feed that stays up and tells them when it doesn't.
- **Silent failure is the signature failure mode.** aisstream sends no subscription acknowledgement, no heartbeat, and no close reason; a dead backend is indistinguishable from an empty ocean or a flipped bounding box, so every outage opens a dozen duplicate "is it me?" issues (#257 alone has 12 independent confirmations). The hub's `/health` already fails on a silent upstream; the client-visible half (ack, heartbeat, close codes, a public status page) is the cheapest high-value work left.
- **Coverage is the biggest single category (45 issues) and is really two problems:** no receiver in the region, and no way to tell. Users compare against MarineTraffic/VesselFinder and blame their own code for hours. Six different people offered to plug in their own receivers (Great Lakes, Seville, Varberg, southern Brazil, Cape Fear, anchored boats) and aisstream has never had an intake path; a coverage map driven by live per-station last-seen and a one-line feeder setup answers both halves.
- **People will pay and nobody has taken their money.** #260 "backup or paid service" is the highest-reaction issue in the tracker (16); #278 is an offer to buy the service; #35, #172, #261, #170 offer hosting, donations, or an enterprise subscription "if it had an SLA". This validates the open-data, paid-access model in [docs/policy.md](../docs/policy.md#funding).
- **Licensing silence blocks adopters who want to comply.** 18 issues ask for a data license, attribution wording, redistribution rules, or a commercial contact; none were ever answered. A terms page with the per-source license table, an attribution string, the free-tier limits, and a contact address is near-zero cost.
- **The decoded `/v0` JSON has known defects users catalogued for us** (`Valid: true` on out-of-range values across nine issues, message 27 labelled 23, Go-format `time_utc`, 15-decimal coordinates, MMSI filter ignored for static data, types 8/23/27 never emitted, 45 s–2 min delivery lag). `/v0` stays bug-for-bug; `/v1` should get each of these right, and the golden tests should pin which aisstream quirks are deliberately reproduced.
- **The three most-wanted features are already in the plan:** an HTTP snapshot (#76 plus five projects that poll by opening short sockets; shipped as `/v1/vessels`), history/replay (#1, #113, eleven projects, and #229's point that outage time is permanent data loss without backfill; Stage 2), and raw NMEA (#10 at 6 reactions, #98, two closed issues; `/v1/nmea` in Stage 1). Static data joined onto position reports (#79, pinged five times over 13 months, plus eleven projects) is the one frequently-asked thing not yet in `/v1` events.
- **Connection-model friction is self-inflicted load.** A 1-connection-per-IP cap (NAT-hostile), resubscribe counted as a new connection, undocumented 429s, and browser use banned push users into reconnect storms and world-bbox subscriptions; the maintainer attributes 2026's instability to "applications created by LLMs" subscribing to the whole world. Document limits, cap per key, answer 429 with `Retry-After`, and make in-band resubscribe free.
- **Free push streaming is the unfilled niche.** The 34-comment "Alternate feed?" thread (#273) inventories every alternative: all REST/credit-based, starting at roughly EUR 330–600 per 10–20k queries, or reciprocity-gated (AISHub, AISFriends). Only one user reported actually migrating (to Marinesia, EUR 13.99/quarter). The source is private and the GitHub repo is empty (#242), so nobody can fork it.

## Demand ranked against the hub

Signal = open issues in the theme; the strongest threads in parentheses. "Hub today" is the state of [`hub/`](../hub/README.md) on the compile date; "Action" names the concrete change it implies.

| # | Theme | Signal | What users ask for | Hub today | Action |
|---|---|---|---|---|---|
| 1 | Silent feed and "is it me or you" | 36 silent-feed + 16 other-outage issues (#257 24c, #15 22c, #32 19c, #178 15c, #23 14c, #136 12c, #208 26c) | Subscription ack, heartbeat or "no data" frame, close codes with reasons, per-connection counters, health endpoint, public status page with incident notes | `/health` 503 when an upstream is silent >2 min; `/metrics`; `/v0` frozen to aisstream semantics (no ack exists there) | Stage 0B: add `{"type":"subscribed"}` ack and a periodic heartbeat frame on `/v1/stream`; close with a code and reason on every server-initiated close; uptime monitor on the public endpoint (already a 0B checkbox) published as a status page with a short incident log. **P0** |
| 2 | TLS certificate expiry | 18 issues, four separate expiries 2024–2026 (#229 18c 10r, #236 17c, #187 7c 10r); in July a valid cert existed and was never reloaded | A certificate that renews itself | Cloudflare-proxied hostname with origin cert is a 0B checkbox, not yet done | Finish the 0B DNS/TLS item; add an origin-cert expiry alert to the monitor anyway. **P0** |
| 3 | Coverage gaps and feeder intake | 45 coverage issues + 6 unsolicited receiver offers (#9, #42, #59, #150, #241, projects #52/#16) and #251/#23 coverage maps | Live coverage map, per-station/per-region health, station id in metadata, station list, a way to contribute a receiver | `/v1` events carry `source` and `station`; per-source last-event age in `/metrics`; HTTP/UDP ingest works; Stage 1 has the registry, coverage map, and one-line setup | Pull the coverage page forward: a viewer layer over per-station last-seen plus the public source list; rest stays Stage 1 (blocked on feeder agreement). **P1** |
| 4 | Status, communication, money | 18 governance issues (#260 16r, #261 11c, #276 10c, #278 buy offer, #212 11r, #116 7r, #35, #242, #222) | Say something during outages, a donate/pay link, a contribution path, source that can be forked | Code is MIT and public; Sustainability plan exists; no status page, sponsor link, or CONTRIBUTING | Status page (shared with #1), sponsors link + commercial contact address, CONTRIBUTING.md; the paid-key path in Sustainability is validated, keep it manual until someone asks. **P1** |
| 5 | License and commercial terms | 18 issues, all unanswered (#16, #18, #24, #68, #138, #165, #181, #184, #198–#200, #204, #220, #223, #226–#228; projects #22, #32, #58) | A stated data license, attribution wording, redistribution/caching rules, paid tier with SLA, a human to email | Per-source license table in [docs/policy.md](../docs/policy.md); license tag on every archived reception | Publish a terms page at launch: per-source license, attribution string, free-tier limits, contact. **P1**, near-zero cost |
| 6 | Rate limits and connection model | 10 issues (#170 31c 9r, #67, #77, #253, #256, #158, #11, #7, #8, #217) | Documented limits, per-key not per-IP, resubscribe without reconnect, many small bboxes, 429 with `Retry-After`, browser policy | Per-IP WS connect limit and per-key ingest limit (fixed windows); `/v0` resend-to-replace and `/v1` re-subscribe are in-band | Document the limits; cap concurrent connections per key rather than per IP; `Retry-After` on 429; decide and state the browser policy (CORS is already open on `/v1/vessels`). **P1** |
| 7 | Decoded-data quality | 26 issues (#119–#127 `Valid` flag, #74 `time_utc`, #88 precision, #91 typos/escaping, #99/#197 MMSI filter on static, #152 types 8/23/27, #54 Class B, #104 null island, #176 latency, #92 plausibility, #145 static join, #206) | Honest per-field validity, RFC3339 times, sane precision, all message types, filters that apply uniformly, low latency | go-ais decode; `/v0` renders aisstream's envelope; `/v1` event carries the decoded struct | `/v0` bug-for-bug (golden tests name which quirks are reproduced on purpose); `/v1` gets RFC3339, rounded coordinates, range-checked validity, all types; MMSI filter routes static data through the position cache like bbox does; add an end-to-end latency metric. **P1/P2** |
| 8 | Login and key issuance | 14 issues (#28 24c, #230 7r, #238, #237, #233, #266, #33, #174, #225) | Getting a key must not depend on a fragile OAuth/DNS path; account deletion | Flat-file keys, `ALLOW_ANON` for local use; no issuance flow yet | Keep issuance static and independent of the stream process (a form or email → key file); consider an anonymous tier with tighter limits; a deletion procedure. **P2** decision before public beta |
| 9 | Raw NMEA | #10 (6r), #98 (3r), closed #22/#33; zero project posts ask for it | `!AIVDM` passthrough for simulators, testing, own decoders | Receptions archived verbatim; `/v1/nmea` planned Stage 1 as reciprocity-only | Stage 1 as planned; the access question (free for all vs feeders) stays open in Sustainability. **P2** |
| 10 | HTTP snapshot | #76; projects #16, #18, #28 (shared hosting cannot hold a socket), #53 (opens a socket every 15 min to poll), #62 | Current state of a box over plain HTTP | `GET /v1/vessels?bbox=` shipped | Done; document it as the answer to "follow N ships" and "what's here now". |
| 11 | History, replay, backfill | #1, #113, #26, #229; 11 projects (#7, #18, #22, #24, #26, #37, #40, #42, #44, #57, #58) | Track by MMSI and period, bulk export, replay, fill the gap after an outage | R2 reception archive; Stage 2 APIs | Stage 2 as planned; the natural paid product. **P2** |
| 12 | Static data on position reports | #79 (6c, pinged 5×), #102, #144, #81, #82, #103; 11 projects | Name, type, dimensions with every position; subtype; tonnage/owner (not in AIS) | Vessel cache has name/type/dims; `/v1/vessels` returns them; `/v1` events carry only the decoded message | Add cached `name`, `type`, `dims` to `/v1` event envelope (cheap, the cache exists); registry enrichment is out of scope. **P2** |
| 13 | More filters | type (#21; projects #1, #19, #40), name/regex (#135, #96), IMO (#99, example #23), bbox-match tag (#14), stationary dedupe (#47) | Server-side filtering by ship type and name, fewer redundant frames | `/v0` MMSI and message-type filters; `/v1` bbox only | `/v1` subscribe gains `types` and `mmsi`; name/IMO via `/v1/vessels?q=` lookup rather than stream filters; rest deferred. **P3** |
| 14 | Derived events and geofences | 9 projects (arrival/departure, area entry, stop, dwell, rendezvous); CPA/TCPA in #44/#47 | Alerts instead of raw positions | Nothing | Not planned; client-side or a Stage 2 query. **P3** |
| 15 | Client libraries and docs | 27 issues (bbox corner order, `APIKey`/`Apikey`, units, `Timestamp` semantics, binary payloads, broken examples, Node-RED/ArcGIS/Arduino/Spring) | Examples that run, units per field, one unambiguous bbox spec, copyable snippets | `aisstream-ts` and official examples verified; both key casings accepted | Docs page with units, coordinate order, and field semantics; run examples in CI. **P2** |
| 16 | Signal K | #277 (plugin reconnect loop leaks the key in logs) | A plugin that reconnects sanely | Stage 3 `signalk-aiscast` | As planned; never log keys. |

## What the issues reveal about aisstream

Useful for `/v0` compatibility and for not repeating its failure modes. Nothing here is documented by aisstream; it is reverse-engineered by users in the threads, with the issue cited.

### Protocol and limits

- Endpoint `wss://stream.aisstream.io/v0/stream`, single origin `136.243.173.177` (Hetzner, Germany), Envoy in front (`server: envoy`, overload 503s carry `x-envoy-overloaded: true`); the website is behind Cloudflare and returns 404 for `/v0/stream` (#23, #187, #236, #248).
- Subscribe within 3 s of connect or the server closes. No acknowledgement of a valid subscription; the only error frames are `{"error": "Api Key Is Not Valid"}` and `{"error": "Subscription Object Is Malformed"}`; bad keys close at ~1.2 s with 1006 and no close frame. Missing `BoundingBoxes` is accepted silently (#15, #84, #269, #257).
- `APIKey` and `Apikey` both work (Go's case-insensitive JSON); the landing page, docs, and examples disagree on spelling, and the docs say `FilterShipMMSI` while the working field is `FiltersShipMMSI` (#106, #269, example #10).
- Bounding boxes are `[[[lat, lon], [lat, lon]]]`; the JavaScript example shipped them in lon/lat order for a year, and flipped corners are behind a steady stream of "no data" reports (#147, #175, example #17).
- Connection caps: 1 per IP as "permanent" load shedding since 2024-01-30 (#67); in 2026 about 3 concurrent connections per IP, the 4th gets 429 even on a different key (#253); re-sending a subscription to change the bbox returns `concurrent connections per user exceeded` and closes the socket (#7, #77); rapid reconnects earn a temporary 429, sustained retry storms an IP block that key rotation does not clear (#256).
- Sockets are closed at exactly 2 minutes unless the client pings (every 110 s works; pongs can lag >4 min) (#140, #159). Documented backpressure: unread TCP queue above a threshold closes the connection; a world subscription is ~300 msg/s (#170).
- Browser clients are refused by policy; the maintainer's guidance is to proxy through your own server (#8).
- `FiltersShipMMSI` is silently ignored for `ShipStaticData` (#99, #197); `FilterMessageTypes` shipped 2023-06-29; type, name, and IMO filters never did (#21, #96, #99, #135).

### Output format quirks

- Backend is Go: `time_utc` is `time.Time.String()` with nine fractional digits (#74); `VenderIDModel`/`VenderIDSerial` typos, `ShipType` vs `Type` inconsistency, and an `integer`-for-`Number` rename in generated models (`Sequenceinteger`, `integerOfSlots`) (#91, #122, #123); `MMSI_String` is a number; `ShipName` is space-padded; coordinates have ~15 decimals (#88).
- `Valid: true` on out-of-range data throughout: `ImoNumber=1`, `SpecialManoeuvreIndicator=3`, `Spare=6`, `TrueHeading=456/399`, `MMSI=0`, 10-digit MMSIs, `UtcMonth=15`/`UtcYear=4161`, Interrogation `MessageID` echoing the destination MMSI; conversely every `DataLinkManagementMessage.Data[].Valid` is false (#119–#127). `LongRangeAisBroadcastMessage` is labelled message 23 (should be 27) and real message 23 is unhandled (#125). Types 8 and 27 are never observed on the wire (#152); Class B extended reports and static data appear absent (#54). Positions at 0,0 pass bbox matching (#104).
- SOG and draught are pre-scaled (knots, metres) without documentation; `Timestamp` is the AIS second-of-minute field; ETA is passed through as broadcast; binary payloads are an escaped byte string nobody can decode (#57, #58, #78, #30).
- No receiver or station identifier in `MetaData`; #141 wants one to cluster GPS-spoofed positions, and two papers cited in #162 argue the base-station reports do not reflect the actual receiving stations.
- Delivery latency regressed from ~65 ms (January 2026) to a consistent 45 s–2 min (March 2026) (#176).

### Sources and coverage

- Terrestrial only, no satellite, and thinner than MarineTraffic by the maintainer's own statement (#44). No service-level vessel filtering; missing small craft come from downsampling at individual stations, and there is no map of which stations filter what (#29).
- Sources are undocumented. Users repeatedly ask whether it is AISHub (#46, #162, #170); #278's 15-hour side-by-side found 99.7% overlap with AISHub in the Malacca Strait but zero Hormuz rows where AISHub had 904 hulls, plus Malaysian AtoN beacons AISHub lacks, so at least one non-AISHub upstream exists. The coverage map is believed to be years old (#202, #252).
- There is no volunteer feeder intake; the maintainer promised one for "late 2023, early 2024" (#42) and it never shipped (#150, #241, #276).

### Outage record visible in the tracker

TLS expiries 2023-07-21, 2023-10-19, 2024-02-16, 2026-05-20 (~2 days), 2026-06-20/23, 2026-07-19 22:34 UTC (a replacement cert issued 2026-06-20 was never loaded; a restart did not pick it up). Connection caps/503s March 2025, May 2025 (a race in the WebSocket server meant rejected accepts never reached the error metrics, so nothing auto-remediated for four days, #136), March 2026 (#170). Silent-feed outages April 2026 (sessions die after 20–35 min), 2026-06-21 17:02 → 06-24, 2026-08-05 13:31 → 08-17 07:49 UTC (11 d 18 h), again from 2026-08-19 06:18 UTC and ongoing at compile time; by 08-20 invalid keys were no longer being rejected. OAuth signup dead 2026-07-20 → ~07-22 (backend could not resolve github.com). The official status page said "operational" throughout August; `/usage` has returned "Could Not Retrieve Usage" since February. The maintainer (`@aisstream`, anonymous) last replied in the `aisstream` repo on 2024-06-11 and in `issues` on 2026-03-14; the only stated policy is "We make no guarantees and provide no SLA".

## Alternatives users named (#273, #278, #221, #260)

| Service | Model and price as quoted | Notes |
|---|---|---|
| Live-AIS.com | USD 200 / 20,000 credits, TER + SAT | invite-gated registration |
| VesselFinder API | EUR 330 / 10,000 credits (TER 1, SAT 10) | "the only independent service left" |
| Data Docked | USD 290 / 6,000 credits, TER + SAT | |
| Datalastic | EUR 600 / 20,000 credits, terrestrial | recommended for REST polling in #278 |
| MyShipTracking | ~USD 1,000/yr minimum | volunteer terrestrial network |
| Marinesia | EUR 13.99 / 3 months, terrestrial | the one reported migration |
| MarineTraffic (Kpler) | unpriced; its cheap API tier was withdrawn mid-June 2026 | |
| Shipdocs.app | paid, MMSI-list API, "3 sources with automatic fallback" | built by the author of #260 |
| AISHub, AISFriends | reciprocal: feed a receiver, get API access | the only free paths, hardware-gated |
| Kystverket, Digitraffic | open government feeds | named in #278; our upstreams |
| Kpler AIS, ORBCOMM, Windward | enterprise only | #221 |
| Named without prices | VesselTracker, ShipFinder, MariTrace, MarineRadar, ShipXplorer, GoRadar, VesselDove, TrackIPI, OceanLook, JsonCargo, SeaVantage, SeaRoutes | |

Community tooling built around the outages: uptime monitor `aisuptime.buttermilkgreen.fyi` ([repo](https://github.com/buttermilkgreen/AISStream-Uptime), cited in six issues, has an API); coverage heatmap from [MarKco/tracker-porti](https://github.com/MarKco/tracker-porti) (#251); LORAN open-source AIS+ADS-B viewer (#259); a USD 250 fixed-price failover prototype pitched in #260. Two data brokers spammed the threads by private email.

## Coverage gaps reported

Grouped by basin; issue refs in the index. Europe: Brittany (Douarnenez–Lorient) #202, Toulon #53/#85, Seville #59, Cádiz #139, Bilbao (Mendibil station dead) #254, Portugal #45, Gibraltar #159, Clyde #146/#167, Dover #151, West Sweden (Varberg, Visby) #150/#85, Baltic/North Sea/Biscay comparison #162, Istanbul/Marmara (Class B ferries) #183, Iceland #224. Americas: Great Lakes/St Lawrence #9, Puget Sound (Class B) #143, Columbia River #208, Ingleside TX #90, Cape Fear NC project #52, Martinique/St Lucia #22/#164, Rio de Janeiro #49, Río de la Plata #149, southern Brazil #241, Antofagasta #166. Asia/Pacific: Persian Gulf/Hormuz/Gulf of Oman #17/#153/#173/#179/#250, South Korea #118/#160/#168/#255, Hong Kong #161, Vietnam #31/#252, Indonesia #155, Malacca mid-strait #278, Asia/Pacific/Oceania generally #147, New Zealand #44, Fiji/Lau #217, Black Sea/Caspian #158, offshore/satellite generally #221.

## Who uses it (`Projects-Using-aisstream.io`, 60 posts)

By use case: research/analytics 9, live map/viewer 8, OSINT/security 8, integration into a non-marine product (TAK, WMS, Fabric, X-Plane, transit app, ERP, game) 8, ferry/port tracker 6, hobby 6, navigation app 5, commercial-permission request 3, other 7. By region: 23 global, 18 a named European water (Baltic/North Sea, France, NL, DE, SE, IE, IT, ES), 5 North America, 2 Africa, 2 Asia, 1 Oceania. Commercial: 11 clearly commercial, 10 likely (shipped consumer apps, pre-launch products), 34 non-commercial, 5 unknown; only three ever ask about a license.

Needs mentioned, by count: regional coverage 13; history/archive/replay 11; static data (type, dimensions, name, destination) 11; MMSI-list follow 10; bbox subscribe 10; derived events/geofences 9; uptime 6; attribution 5; REST snapshot 5; licensing 5; quotas 4; world firehose 4; right to cache 3; type filter 3; registry enrichment 3; CPA/TCPA 2; contributing receivers 2; raw NMEA 0.

## Index

Signal: `Nc` comments, `Nr` reactions, `+N me-too` explicit confirmations in the thread. Categories: outage-silent-feed (connects, subscribes, zero messages), outage-tls, outage-other, auth/login, rate-limit/connections, coverage-gap (region), data-quality, feature-request, licensing/commercial, client-sdk/docs, community/governance, spam/off-topic.

### aisstream/issues (196 open)

Links: `https://github.com/aisstream/issues/issues/<#>`.

| # | Date | Category | Summary | Signal | Hub implication |
|---|---|---|---|---|---|
| 280 | 2026-08-20 | outage-silent-feed | Subscription accepted, zero frames for 38 consecutive windows since ~08-18 | 1c 0r | Emit explicit "no data" heartbeat, not silence |
| 279 | 2026-08-19 | outage-silent-feed | Feed dies again 06:19 UTC two days after recovery; users hunting alternatives | 12c 1r +6 confirms | Publish incident status; users churn on second failure |
| 278 | 2026-08-16 | community/governance | charter.boats owner offers to buy or co-run aisstream; thread analyzes upstream sources | 5c 2r | Real acquisition demand; commercial users want continuity |
| 277 | 2026-08-15 | outage-silent-feed | SignalK plugin user sees 1006 reconnect loop; leaks API key in logs | 6c 0r | Ship a SignalK plugin; never log keys |
| 276 | 2026-08-14 | community/governance | "Please explain what's up" — 8-day silence, asks about paid tier | 10c 3r | Status page + comms are a product feature |
| 275 | 2026-08-13 | outage-silent-feed | Staged probe: TLS ok, handshake ok, zero frames Tokyo + global | 4c 0r | Ship a diagnostic/self-test endpoint |
| 274 | 2026-08-12 | outage-silent-feed | World bbox, no filter, zero messages; ruled out proxy, key, format | 6c 4r +6 me-too | Distinguish auth-ok-but-no-data from healthy silence |
| 273 | 2026-08-11 | community/governance | "Alternate feed?" — full inventory of paid AIS alternatives and prices | 34c 0r | Free WebSocket streaming is the unmet niche |
| 272 | 2026-08-11 | outage-silent-feed | Three keys, two networks, world bbox: 0 msgs where ~9,000 expected | 0c 0r | Document expected msg rate so clients self-check |
| 271 | 2026-08-11 | outage-silent-feed | Connection ok, sub accepted, valid key, no AIS message | 4c 3r +2 me-too | Offer to contribute came from this thread |
| 270 | 2026-08-11 | outage-tls | Expired TLS cert on wss endpoint blocks all connections | 0c 0r | Terminate TLS at Cloudflare; never hand-renew |
| 269 | 2026-08-10 | outage-silent-feed | Silent since 08-05 13:31; tested both APIKey/Apikey spellings | 8c 2r +4 me-too | Accept both field spellings; fix doc inconsistency |
| 268 | 2026-08-10 | outage-other | WebSocket fails/closes immediately; duplicate of ongoing outage | 1c 0r | Dedupe pressure: a status page prevents issue spam |
| 267 | 2026-08-08 | outage-other | Same connection-failure report, closed as duplicate of #257 | 1c 0r | Status page prevents duplicate reports |
| 266 | 2026-08-08 | auth/login | Cannot get API key: Cloudflare 524 timeout on signup page | 2c 0r | Key issuance must survive stream backend failure |
| 265 | 2026-08-07 | outage-tls | Expired SSL certificate, cannot connect to WebSocket | 0c 0r | Automate cert renewal + reload; alert on expiry |
| 263 | 2026-08-07 | outage-silent-feed | Brand-new GitHub account and key still receive zero messages | 0c 0r | New-key path must be provably working |
| 262 | 2026-08-07 | outage-silent-feed | Feed cut mid-minute at 650 msg/min; zero frames 43h later | 0c 0r | Per-key throughput metrics visible to the user |
| 261 | 2026-08-06 | community/governance | "The deafening silence is the most frustrating part" — comms, willingness to pay | 11c 2r | Communication is the top non-technical demand |
| 260 | 2026-08-05 | community/governance | "backup or paid service" — users organize to build/fund a replacement | 7c 16r | Highest-reaction issue: this is our exact market |
| 259 | 2026-08-05 | outage-silent-feed | Two valid keys, global + Rotterdam bbox, zero frames; invalid key rejected | 2c 1r +2 confirms | Auth path healthy while data path dead |
| 257 | 2026-08-05 | outage-silent-feed | Canonical outage thread: 12+ independent confirmations, controls, timeline | 24c 2r +12 confirms | Best spec for what "healthy" must mean |
| 256 | 2026-08-02 | rate-limit/connections | Self-reported retry storm got their IP 429-blocked; asks for unblock | 0c 0r | Publish limits; return 429 with Retry-After |
| 255 | 2026-08-02 | coverage-gap (South Korea) | Korea bbox silent 18h while Europe control bbox streams normally | 0c 0r | Expose per-region coverage/station health |
| 254 | 2026-07-27 | coverage-gap (Bilbao, Spain) | Mendibil base station offline; Bilbao harbour uncovered | 0c 0r | Station registry with per-station last-seen |
| 253 | 2026-07-26 | rate-limit/connections | Asks max concurrent connections per key; 429 at 3-4, undocumented | 2c 0r | Document connection limits; ideally per-account not IP |
| 252 | 2026-07-24 | coverage-gap (Vietnam) | No cargo AIS around Vietnam despite coverage map claiming it | 0c 0r | Don't advertise coverage you can't sustain |
| 251 | 2026-07-23 | community/governance | User builds public AIS density heatmap of aisstream's real coverage | 0c 3r | Ship an honest live coverage map ourselves |
| 250 | 2026-07-23 | outage-silent-feed | Strait of Hormuz bbox idle 15s; endpoint recovered from 503s | 1c 0r | Sparse regions look identical to outages |
| 248 | 2026-07-22 | outage-other | WebSocket stuck CONNECTING; envoy HTTP 503 overload spikes | 4c 0r | Capacity headroom; graceful overload responses |
| 246 | 2026-07-22 | outage-other | Korea platform asks whether account/plan blocks WebSocket access | 1c 0r | Document that there is no gated tier |
| 244 | 2026-07-22 | auth/login | GitHub OAuth fails: backend cannot resolve github.com | 0c 0r | Don't make login the single point of failure |
| 242 | 2026-07-21 | community/governance | "Are we ready to fork?" — discovers repo is empty, code is private | 2c 0r | Open source is our differentiator; forkability matters |
| 241 | 2026-07-21 | coverage-gap (southern Brazil) | Ham operator with receivers asks how to feed data in; no mechanism exists | 0c 0r | Accept volunteer feeders — aisstream never did |
| 238 | 2026-07-21 | auth/login | GitHub OAuth token exchange fails, cannot get API key | 4c 3r +3 me-too | Offer keyless/anonymous tier or email signup |
| 237 | 2026-07-21 | auth/login | Same OAuth DNS failure; multiple confirmations | 3c 3r +2 me-too | Same |
| 236 | 2026-07-20 | outage-tls | stream. subdomain cert expired 07-19; apex fine; OAuth down too | 17c 4r | Managed TLS removes an entire outage class |
| 235 | 2026-07-20 | auth/login | Cannot "Get Started"/GitHub login; DNS no-such-host | 1c 2r | Signup must not depend on outbound DNS |
| 234 | 2026-07-20 | auth/login | OAuth "operation not permitted" error, no workaround | 0c 0r | Same |
| 233 | 2026-07-20 | auth/login | OAuth state parameter invalid, then DNS failure; blocked from key | 3c 2r | Same |
| 232 | 2026-07-20 | auth/login | Sign-in redirect fails, server-side DNS resolution issue | 1c 2r | Same |
| 231 | 2026-07-20 | outage-tls | CERT_HAS_EXPIRED then handshake timeouts from Railway-hosted client | 4c 0r | Same |
| 230 | 2026-07-20 | auth/login | Cannot sign in to access API keys, multiple browsers/devices | 0c 7r | Login outage has huge silent reaction weight |
| 229 | 2026-07-19 | outage-tls | Cert expired 22:34 UTC; valid cert existed but was never reloaded | 18c 10r | Cert reload hooks + expiry monitoring, or Cloudflare |
| 228 | 2026-07-17 | licensing/commercial | Public emergency map asks permission to rebroadcast cached snapshots | 0c 0r | Publish an explicit redistribution licence |
| 227 | 2026-07-12 | licensing/commercial | Paid market-intelligence platform asks if commercial use is permitted | 0c 0r | Same |
| 226 | 2026-07-11 | licensing/commercial | Navigation app asks about commercial use, architecture, attribution, limits | 0c 0r | Publish licence, attribution wording, rate limits |
| 225 | 2026-07-10 | auth/login | Requests permanent account and API key deletion | 0c 0r | Self-serve account deletion |
| 224 | 2026-07-10 | coverage-gap (Iceland) | Asks whether Iceland coverage is possible | 0c 0r | Coverage map + "adopt a region" feeder recruiting |
| 223 | 2026-07-06 | licensing/commercial | Asks for ToS/licence: public display, commercial use, caching, redistribution | 0c 0r | Same |
| 222 | 2026-07-01 | community/governance | "Doesn't seem under active development" — asks if contributors wanted | 0c 2r | Bus-factor fear; make contribution path obvious |
| 221 | 2026-06-26 | coverage-gap (offshore/satellite) | Coastal coverage limited; asks what people use for satellite AIS | 1c 0r | Be honest: terrestrial only, document the gap |
| 220 | 2026-06-25 | licensing/commercial | Commercial derived-analytics product asks written permission for port index | 0c 0r | Permissive licence unlocks commercial adoption |
| 217 | 2026-06-23 | rate-limit/connections | Fiji non-profit requests quota lift; actually a service-wide outage | 5c 0r | Surface quota/limit state so users can self-diagnose |
| 216 | 2026-06-23 | outage-tls | June cert expiry report; unanswered, third recurrence noted | 2c 0r | Recurring failure mode; monitoring absent |
| 215 | 2026-06-23 | outage-silent-feed | Valid key, docs-verbatim subscription, world bbox: zero messages in 20s, no error frame | 6c 1r +5 me-too | Emit explicit subscription-ack and heartbeat so silence is diagnosable |
| 214 | 2026-06-23 | outage-silent-feed | Zero messages since 2026-06-21 17:02 UTC across keys, libraries, bboxes | 7c 3r +5 me-too | Health check must fail when upstream feed goes silent |
| 213 | 2026-06-23 | outage-silent-feed | "Cannot receive any AIS message from API" during June outage | 2c 1r +2 me-too | Public status page cheaper than duplicate outage issues |
| 212 | 2026-06-22 | community/governance | User builds third-party uptime monitor + API because no official status page | 5c 11r | Ship official status page and machine-readable health endpoint |
| 211 | 2026-06-22 | outage-silent-feed | Auth works, invalid keys rejected, global bbox yields zero PositionReports | 4c 1r +3 me-too | Distinguish auth-OK-but-no-data from auth failure in protocol |
| 210 | 2026-06-22 | outage-silent-feed | Connected + subscribed, counters all zero; asks whether account needs activation | 10c 0r +6 me-too | Per-connection message counters clients can query |
| 209 | 2026-06-22 | outage-silent-feed | UK/Ireland bbox silent since 21 June; new key also silent; HTTP 503 on upgrade | 2c 0r +1 me-too | Return 503 with retry-after, never accept-then-silence |
| 208 | 2026-06-21 | outage-silent-feed | Columbia River then global outage; long thread, alternatives and uptime monitor shared | 26c 8r +11 me-too | Outages become abandonment fears; communicate status proactively |
| 206 | 2026-06-14 | data-quality | Vessel MMSIs emit BaseStationReport (type 11), some with UtcYear 2006 | 0c 0r | Validate/flag type-11 from vessel MMSIs; don't trust UtcYear |
| 204 | 2026-06-12 | licensing/commercial | UK commercial API service asks whether redistribution permitted and if paid tier exists | 0c 0r | Publish explicit license + redistribution terms upfront |
| 203 | 2026-06-09 | outage-silent-feed | 101 handshake OK, global bbox, zero frames; usage dashboard also broken | 7c 1r +5 me-too | Keep usage/metrics endpoint independent of stream path |
| 202 | 2026-06-04 | coverage-gap (Brittany, FR) | Gap Douarnenez–Lorient (Audierne, Penmarc'h, Concarneau) despite coverage map claiming coverage | 0c 0r | Coverage map must be live per-receiver, not stale image |
| 201 | 2026-06-04 | client-sdk/docs | Connection reset (104) from inside Docker behind proxy; works from host shell | 0c 0r | Document proxy/Docker/TLS troubleshooting; avoid exotic handshake requirements |
| 200 | 2026-06-01 | licensing/commercial | Commercial iOS app asks about display, derived products, attribution, usage limits | 0c 0r | State attribution string and limits in machine-readable terms |
| 199 | 2026-06-01 | licensing/commercial | Paid trading terminal wants written commercial clarity, chokepoint bboxes, live map rights | 0c 0r | "Rather hear no quickly than maybe slowly" — answer licensing |
| 198 | 2026-05-30 | licensing/commercial | Wants to speak to someone about licensing data commercially | 1c 0r | Provide a contact channel, not just GitHub issues |
| 197 | 2026-05-27 | data-quality | FiltersShipMMSI returns nothing though same vessels stream unfiltered | 1c 0r | MMSI filter must be tested; document combination semantics |
| 196 | 2026-05-26 | outage-tls | Expired certificate blocks all WSS clients | 0c 0r | Automate cert renewal + expiry alerting (Cloudflare handles) |
| 194 | 2026-05-25 | client-sdk/docs | Asks units for rate of turn and meaning of ±127/-128 sentinels | 0c 0r | Document field units and AIS sentinel values |
| 193 | 2026-05-22 | outage-tls | CERTIFICATE_VERIFY_FAILED, repeat plea a month later during June outage | 1c 0r | Cert expiry is the recurring, most avoidable failure |
| 191 | 2026-05-21 | outage-tls | Node client CERT_HAS_EXPIRED, close 1006; cites Let's Encrypt R12 expiry | 1c 1r +1 me-too | Monitor leaf cert expiry as a first-class alert |
| 190 | 2026-05-21 | outage-tls | curl/openssl confirm expiry; fixed roughly two days later | 3c 0r +2 me-too | Two-day TLS outage is the baseline to beat |
| 189 | 2026-05-20 | outage-tls | Strict clients (Deno, Go, Java) refuse handshake; suspects certbot cron failure | 0c 1r | Strict-TLS clients break first; test with Go/Java clients |
| 188 | 2026-05-20 | outage-tls | Detailed expiry report with likely causes (cron, ACME challenge, edge not reloaded) | 3c 2r +3 me-too | Reload edge after renewal; verify served cert, not disk |
| 187 | 2026-05-20 | outage-tls | Expiry report; thread yields rejectUnauthorized/unverified-TLS workarounds and CF 403 finding | 7c 10r +3 me-too | Users disable verification to survive; avoid teaching that |
| 186 | 2026-05-18 | outage-other | Connect then immediate 1006 within 1–2s for five days; 14 MMSIs tracked | 0c 1r | Send close reason codes, never bare 1006 |
| 184 | 2026-05-12 | licensing/commercial | Asks permission to use data in a public showcase platform | 0c 0r | Clear license removes need to ask permission |
| 183 | 2026-05-04 | coverage-gap (Istanbul/Marmara) | Class B Istanbul ferries absent; asks for terrestrial receivers in inner harbor | 0c 0r | Class B needs dense local feeders; recruit by port |
| 182 | 2026-05-02 | licensing/commercial | Shares free public ship-tracking use case per README, asks for concerns | 0c 0r | Invite use-case sharing; publish a projects gallery |
| 181 | 2026-04-27 | licensing/commercial | Early-stage SaaS wants private licensing channel and attribution guidance | 0c 0r | Offer email/private contact for commercial questions |
| 180 | 2026-04-14 | outage-silent-feed | SF Bay bboxes silent after working; suspects reconnect throttle from deploys | 2c 0r +1 me-too | Reconnect storms must not silently blackhole a key |
| 179 | 2026-04-13 | outage-silent-feed | Hormuz bbox: zero messages, server closes with code 1005 after 30s | 0c 0r | Close with meaningful code+reason on idle disconnect |
| 178 | 2026-04-05 | outage-silent-feed | Long-running silent-stream thread; works ~20-35 min then dies, self-recovers | 15c 5r +8 me-too | Detect and restart stalled upstream sessions automatically |
| 177 | 2026-04-03 | outage-silent-feed | Zero messages, intermittent, 1006 every ~2 minutes, 35 port bboxes | 5c 0r +4 me-too | Support many bboxes per connection without degradation |
| 176 | 2026-03-24 | data-quality | Delivery lags MetaData.time_utc by 45s–2min; was milliseconds in January | 0c 0r | Track and publish end-to-end latency SLO per message |
| 175 | 2026-03-24 | outage-silent-feed | Silent stream with ping/pong alive; thread surfaces flipped lat/lon mistake | 2c 0r +1 me-too | Reject malformed/suspicious bboxes with an error message |
| 174 | 2026-03-24 | auth/login | Valid key rejected "Api Key Is Not Valid" then 1006; usage dashboard broken | 0c 0r | Auth store outages must not masquerade as bad keys |
| 173 | 2026-03-16 | coverage-gap (Hormuz/Persian Gulf) | Only 20–40 vessels visible where 300+ exist; jamming, AIS-off, blackouts | 2c 0r | Expose receiver liveness so gaps read as coverage |
| 172 | 2026-03-13 | outage-silent-feed | Total silence, no error frames; offers donation or subscription to help | 3c 3r +2 me-too | Users want to pay/donate; provide funding path |
| 171 | 2026-03-11 | outage-other | Python client times out while Java client works with same key and bbox | 3c 2r +2 me-too | Test against multiple client libraries in CI |
| 170 | 2026-03-06 | rate-limit/connections | Cloudflare/LB blocking WS upgrades; maintainer confirms load-balancer max-connection cap | 31c 9r +9 me-too | Size connection limits explicitly; document per-key caps |
| 168 | 2026-02-21 | coverage-gap (South Korea, Asia) | No South Korea coverage, thin China/Japan; asks about source and military filtering | 0c 0r | Document data sources and any filtering policy |
| 167 | 2026-02-20 | coverage-gap (Clyde, Scotland) | Glasgow to Firth of Clyde blackout except pocket near Greenock | 0c 0r | Per-receiver outage alerts surface regional blackouts |
| 166 | 2026-02-13 | coverage-gap (Antofagasta, Chile) | Coverage degraded then stopped; asks whether moderators read the tracker | 1c 0r | Answer coverage issues; silence reads as abandonment |
| 165 | 2026-02-06 | licensing/commercial | Asks how to contact team about commercial use; a second party joins | 1c 0r | Named maintainers and contact address build trust |
| 164 | 2026-02-03 | coverage-gap (Martinique/St Lucia) | Data stopped for Caribbean bbox two days running | 0c 0r | Regional feeder loss should be visible to users |
| 163 | 2026-01-28 | outage-silent-feed | Official example, world bbox, zero messages over hours; no maintainer response | 4c 0r +3 me-too | Ensure the documented example always works |
| 162 | 2026-01-07 | coverage-gap (Baltic/North Sea/Iberia) | Compares coverage to VesselFinder; asks about receiver network and sources | 4c 0r | Publish receiver locations and honest coverage expectations |
| 161 | 2025-12-13 | coverage-gap (Hong Kong) | Specific HK MMSIs never update while other HK vessels do | 1c 1r | Per-vessel gaps need receiver provenance to explain |
| 160 | 2025-12-10 | coverage-gap (South Korea) | World bbox floods data; Korea bbox returns nothing; asks if free tier limits region | 0c 0r | Never let coverage gaps look like tier restrictions |
| 159 | 2025-12-06 | coverage-gap (Gibraltar/Med) | No messages in busy Gibraltar zone; connection lasted only 2 minutes | 3c 1r +2 me-too | Long-lived connections; don't drop after minutes |
| 158 | 2025-11-08 | rate-limit/connections | 70 small bboxes but only 5–6 deliver daily; asks bbox count/area limits | 0c 0r | Document bbox count/area limits; support many small bboxes |
| 157 | 2025-11-03 | community/governance | Asks how to contact the team about a collaboration | 0c 0r | Publish contact and collaboration path |
| 156 | 2025-10-22 | outage-other | Timeout during opening handshake every attempt with documented example | 0c 0r | Handshake must succeed from common client stacks |
| 155 | 2025-10-18 | coverage-gap (Indonesia) | Sabang-to-Merauke area: no vessel identification (empty body) | 0c 0r | Static data joins matter as much as positions |
| 154 | 2025-09-19 | community/governance | Asks whether mapped AIS stations are self-operated or third-party | 0c 0r | Be transparent about feeders and upstream sources |
| 153 | 2025-09-09 | outage-other | Gulf of Oman bbox gives i/o timeout and EOF while global bbox works | 0c 0r | Regional subscription must not fail differently from global |
| 152 | 2025-08-31 | data-quality | Types 8, 23, 27 never delivered; docs mention undocumented message types | 0c 0r | Support all AIS types; keep raw NMEA passthrough |
| 151 | 2025-08-30 | coverage-gap (Dover, UK) | Ferries stop updating within a couple km of Dover, "force field" | 1c 0r | Near-shore dropouts need local feeder coverage |
| 150 | 2025-08-29 | community/governance | Suspects no receiver near Varberg; offers to host one, gets no reply | 1c 2r +1 me-too | Make feeder onboarding self-serve and documented |
| 149 | 2025-07-22 | coverage-gap (Río de la Plata) | No vessels between Argentina and Uruguay | 0c 0r | Recruit feeders for South American estuaries |
| 147 | 2025-06-24 | coverage-gap (Asia/Pacific/Oceania) | Month of data has no Asia/Pacific/Oceania; bbox corner-order confusion suspected | 4c 0r +1 me-too | Unambiguous bbox spec; validate and echo parsed bbox |
| 146 | 2025-06-18 | coverage-gap (Firth of Clyde) | Latitude band 55.76–55.93 N delivers nothing while north and south work | 0c 0r | Band-shaped gaps suggest per-receiver failure, expose it |
| 145 | 2025-06-13 | data-quality | Type 5 static data has no UserID matching types 1/2/3, blocking classification | 0c 0r | Guarantee MMSI join key across static and position |
| 144 | 2025-06-06 | data-quality | Documented dimensions/type absent from PositionReport payload | 0c 0r | Enrich positions from vessel state, or fix docs |
| 143 | 2025-06-04 | coverage-gap (Puget Sound) | Class A fine, Class B spotty; coverage map blobs overstate reality | 1c 0r +1 me-too | Class B coverage is the differentiator; measure separately |
| 141 | 2025-05-30 | feature-request | Wants base-station coordinates or UUID in metadata to filter GPS spoofing | 0c 0r | Include receiver/source id in every message's metadata |
| 140 | 2025-05-28 | outage-other | WebSocket silently closed after exactly 2 minutes, no close reason, across C/Node/Go clients | 4c 5r +4 me-too | Server-side ping/pong keepalive; document interval; never drop silently |
| 139 | 2025-05-12 | coverage-gap (Spain, Gulf of Cádiz) | Connects, subscribes, zero messages; works for San Francisco, nothing for Cádiz | 4c 0r | Coverage map must be live and per-bbox truthful |
| 138 | 2025-05-09 | licensing/commercial | Can a company use the API for commercial/for-profit work? Beta exit date? Pricing? | 1c 0r +1 me-too | Publish explicit data license and commercial-use terms upfront |
| 136 | 2025-05-02 | outage-other | HTTP 503 on connect for days; recurring; maintainer post-mortem after four days | 12c 0r +7 me-too | Health check must cover accept path, not error metrics |
| 135 | 2025-04-29 | feature-request | Filter by ship name (with regex) since users know names, not MMSIs; offers to implement | 0c 0r | Add name filter to subscription; accept outside contributions |
| 127 | 2025-03-19 | data-quality | ShipStaticData carries ImoNumber=1 yet `Valid: true`; static fields often null/empty | 1c 0r +1 me-too | Validity flag must reflect standard-range checks per field |
| 126 | 2025-03-19 | data-quality | SpecialManoeuvreIndicator=3 and Spare=6 (out of range) still marked `Valid: true` | 0c 0r | Range-check enums and spares before setting Valid |
| 125 | 2025-03-17 | data-quality | LongRangeAisBroadcastMessage labelled msg 23; should be 27. Msg 23 (Group Assignment) unhandled | 0c 0r | Correct type-ID mapping; implement message 23 |
| 124 | 2025-03-15 | data-quality | TrueHeading=456 / 399 across unrelated vessels, marked valid; suspected bit-decode fault | 0c 0r | Reject heading outside 0–359/511; audit bit offsets |
| 123 | 2025-03-14 | data-quality | BinaryAcknowledge with UserID=0 and MMSI=0 in metadata, still `Valid: true` | 0c 0r | Zero MMSI must invalidate message and metadata |
| 122 | 2025-03-14 | data-quality | DataLinkManagementMessage: all 4096 sampled messages have every Data entry `Valid: false` | 0c 0r | Test sub-structure validity flags against real fixtures |
| 121 | 2025-03-14 | data-quality | 10-digit destination IDs (1021812661) and UserIDs marked valid; MMSI must be 9 digits | 0c 0r | Enforce 9-digit MMSI range on all ID fields |
| 120 | 2025-03-14 | data-quality | BaseStationReport with UtcMonth=15, UtcYear=4161 marked valid | 0c 0r | Validate base-station clock fields; flag implausible time |
| 119 | 2025-03-13 | data-quality | Interrogation Station1Msg1.MessageID equals StationID; messages unusable, still valid | 0c 0r | Fix Interrogation bit layout; add golden-payload decoder tests |
| 118 | 2025-03-05 | coverage-gap (Korea, Malacca) | Busan/Ulsan data vanished; Malacca has none; still zero for South Korea in 2026 | 2c 0r +2 me-too | Per-region feed health alarms; surface upstream loss publicly |
| 116 | 2025-02-07 | community/governance | Wants red/yellow/green status indicator and load counter on main page | 0c 7r | Ship status page with live message-rate counter |
| 114 | 2025-02-07 | outage-other | Stream down; maintenance changed edge-server IP, clients pinned to old address broke | 4c 0r | Stable hostname, announced maintenance, no IP pinning |
| 113 | 2025-02-02 | community/governance | Third party announces AIS archive service built on aisstream; asks for feature input | 3c 0r | Historical archive with CSV/JSON export and date-range API |
| 109 | 2024-12-31 | coverage-gap (SE Asia) | Five actively-reporting MMSIs never arrive; ~30 of 60 tracked vessels silent | 3c 0r +1 me-too | Document filter/connection limits; avoid silent per-key subscription caps |
| 106 | 2024-11-09 | client-sdk/docs | Laravel/Ratchet client gets no usable data; uses `Apikey` casing from docs sample | 1c 0r +1 me-too | Accept case-insensitive keys; publish PHP example |
| 105 | 2024-10-22 | client-sdk/docs | Arduino cannot open the WebSocket; asks whether embedded clients are possible | 0c 0r | Support constrained clients: plain TLS, small frames, examples |
| 104 | 2024-10-07 | data-quality | Ships reported at 0,0 with heading 0 inside a UK bbox; nonexistent vessels | 1c 0r | Drop or flag null-island positions before bbox matching |
| 103 | 2024-10-07 | client-sdk/docs | Ship type is an undocumented integer; no mapping table provided | 1c 0r | Ship type/subtype lookup in docs and API |
| 102 | 2024-10-06 | data-quality | Dimension and Type still absent despite documentation promising them | 0c 0r | Merge static data into position stream or fix docs |
| 101 | 2024-09-26 | outage-silent-feed | Stream stops delivering every 3–5 days with no error | 0c 0r | Detect stalled sockets server-side; force close on staleness |
| 100 | 2024-09-13 | client-sdk/docs | ArcGIS GeoEvent only accepts a URL; can't send the JSON subscription frame | 1c 0r | Offer query-param auth/bbox so URL-only clients connect |
| 99 | 2024-09-10 | data-quality | FiltersShipMMSI silently ignored for ShipStaticData; also asks for IMO filter | 1c 0r +1 me-too | Filters must apply uniformly; add IMO filter |
| 98 | 2024-09-09 | feature-request | Wants the raw NMEA sentences behind the JSON, to drive an AIS simulator | 0c 3r | Raw NMEA endpoint wanted; we already archive it |
| 97 | 2024-08-17 | coverage-gap (Mediterranean) | Vessel lost 10 miles out of port though VesselFinder keeps updating | 1c 0r +1 me-too | Show per-vessel last-seen and receiving station |
| 96 | 2024-08-07 | feature-request | Users don't know MMSI; wants filter by vessel name to resolve MMSI | 0c 0r | Name→MMSI lookup endpoint plus name filter |
| 94 | 2024-08-01 | coverage-gap (unspecified) | One MMSI stops arriving for 8 hours while other vessels work | 0c 0r | Expose per-station uptime so gaps are explainable |
| 93 | 2024-08-01 | client-sdk/docs | Asks for owner, place of build, gross tonnage the docs claim ShipStaticData contains | 3c 0r | Never document fields AIS doesn't actually carry |
| 92 | 2024-07-31 | data-quality | Single position 500km off (Savannah→Norfolk) then back; wants to reject such reports | 1c 0r | Optional plausibility flag using speed/heading extrapolation |
| 91 | 2024-07-30 | data-quality | `VenderID*` typo, ShipType vs Type inconsistency, unescaped apostrophes break JSON parsers | 1c 0r | Consistent named schema; correct JSON escaping always |
| 90 | 2024-07-19 | coverage-gap (US Gulf, Texas) | No data for Ingleside, TX bbox; asks whether the location is covered | 0c 0r | Answer coverage questions with data, not a static map |
| 89 | 2024-07-18 | coverage-gap (unspecified) | Only 2 of 15 coastal MMSIs return anything; client hangs waiting forever | 1c 0r | Emit explicit "no data yet" heartbeat to unblock clients |
| 88 | 2024-07-12 | data-quality | Lat/long emitted with absurd precision; rounding would cut bandwidth | 1c 0r | Round coordinates to source precision (~6 decimals) |
| 87 | 2024-07-12 | coverage-gap (Indian Ocean/Gulf) | Vessels visible on MarineTraffic never arrive; maintainer says development is stalled | 4c 0r | Accept volunteer help; make coverage additive via feeders |
| 85 | 2024-06-23 | coverage-gap (France/Sweden) | Toulon area silent a month; Visby last ping 150 hours old | 1c 0r +1 me-too | Alert on per-station silence rather than waiting for reports |
| 84 | 2024-05-30 | client-sdk/docs | "Subscription Object Is Malformed" from wrong bbox nesting; user pasted a live API key | 3c 0r +1 me-too | Error must name the offending field; never echo keys |
| 82 | 2024-04-23 | feature-request | Wants gross tonnage / deadweight in static data; maintainer: development slow, won't fix | 2c 0r | Static enrichment from vessel registry is a differentiator |
| 81 | 2024-04-23 | feature-request | Wants vessel subtype (e.g. vehicle carrier within cargo=70) propagated | 0c 0r | Decode ship-type subcategories into a named field |
| 79 | 2024-03-25 | feature-request | Dimensions/type documented for PositionReport but never sent; pinged 5 times over 13 months | 6c 0r +2 me-too | Join static data onto position reports server-side |
| 78 | 2024-03-16 | client-sdk/docs | SOG and draught units differ from raw AIS; docs list no units at all | 2c 0r +1 me-too | Document units per field; keep scaling consistent |
| 77 | 2024-03-12 | rate-limit/connections | Re-subscribing with a new bbox returns "concurrent connections per user exceeded" and closes socket | 4c 0r +1 me-too | In-band re-subscribe must work — our /v1 events |
| 76 | 2024-03-01 | feature-request | Wants HTTP request/response for current vessel positions instead of a persistent socket | 1c 0r | Validates /v1/vessels?bbox= snapshot endpoint |
| 75 | 2024-02-28 | outage-other | Regular interruptions with graphs; user can't tell whether fault is client or server | 1c 0r | Status page so users can self-diagnose outages |
| 74 | 2024-02-17 | data-quality | `time_utc` is Go's default format with 9 fractional digits, not ISO 8601 | 1c 2r +1 me-too | Emit RFC3339; keep aisstream format only for /v0 |
| 70 | 2024-02-16 | client-sdk/docs | Sample Java client clashes with Spring Boot's SLF4J logger; unusable as a component | 0c 0r | Ship a library, not a demo app, per language |
| 68 | 2024-02-06 | licensing/commercial | No data license anywhere; MIT on models repo, silence on the rest | 0c 0r | State data license per upstream feed explicitly |
| 67 | 2024-01-30 | rate-limit/connections | Maintainer: load 4x normal from single clients; limiting to 1 connection per IP, permanently | 0c 0r | Limit per API key, not per IP; NAT-safe |
| 65 | 2024-01-19 | client-sdk/docs | Homepage JS sample has an unbalanced bracket in BoundingBoxes | 0c 0r | Test docs examples in CI |
| 64 | 2024-01-18 | client-sdk/docs | Code samples on the site can't be visibly selected in Firefox | 0c 0r | Plain copyable code blocks with a copy button |
| 60 | 2024-01-05 | client-sdk/docs | "Is it possible to follow a ship?"; answered a year later by another user | 1c 0r | Task-oriented docs: track one vessel, watch an area |
| 59 | 2024-01-02 | coverage-gap (Spain, Seville) | Has receivers in Seville, asks what's needed to get them into the feed | 0c 0r | Self-serve feeder onboarding closes coverage gaps |
| 58 | 2023-12-26 | client-sdk/docs | Can't decode SingleSlot/MultiSlot binary payloads; format undocumented, escaped-byte string | 4c 0r | Deliver binary payloads as base64/hex plus decoder |
| 57 | 2023-12-21 | client-sdk/docs | Timestamp is a small integer, not Unix time; AIS second-of-minute field | 1c 0r | Document AIS-native fields vs wall-clock metadata |
| 54 | 2023-11-21 | data-quality | No ExtendedClassBPositionReport or Class B static data ever received worldwide | 1c 0r | Verify all 27 message types end-to-end with fixtures |
| 53 | 2023-11-19 | coverage-gap (France, Toulon) | No data for Toulon; recovered a month later, broke again in March | 3c 0r | Per-station history so recovery/regression is visible |
| 52 | 2023-10-19 | coverage-gap (France, Brittany) | MMSI 311001311 never appears even with worldwide bbox | 1c 0r | MMSI filter needs "never seen" feedback to client |
| 51 | 2023-10-19 | community/governance | Requests a basic status/uptime page to distinguish outage from local bug | 0c 1r | Public uptime page is a repeatedly-requested feature |
| 49 | 2023-10-14 | coverage-gap (Brazil, Rio) | No data for Rio–Niterói bay; suspects a block, actually no coverage | 1c 1r | Reply "no station in range" instead of silence |
| 48 | 2023-09-21 | client-sdk/docs | Node-RED flow never connects; thread ends with users asking for other free AIS APIs | 5c 1r +2 me-too | Integration recipes; users actively shopping for alternatives |
| 47 | 2023-09-21 | feature-request | Wants redundant position reports filtered out when vessel is anchored/stationary | 0c 0r | Optional server-side dedupe/downsample for stationary vessels |
| 46 | 2023-09-16 | feature-request | Asks for receiver locations/ranges; asks directly whether the data comes from AISHub | 2c 0r | Publish station map and named upstream sources |
| 45 | 2023-09-07 | coverage-gap (Portugal) | Portuguese coast region went quiet; suspects one receiver died, wants receiver status | 2c 0r +1 me-too | Per-receiver status page; users infer outages themselves |
| 44 | 2023-08-28 | coverage-gap (New Zealand) | Auckland missing many vessels vs MarineTraffic; maintainer: no satellite, thinner ground coverage | 4c 0r +1 me-too | State coverage honestly; publish comparison methodology |
| 43 | 2023-08-24 | client-sdk/docs | Non-programmer asks how to print name, speed and course from the Python sample | 1c 0r | Fuller quickstart examples covering common fields |
| 42 | 2023-08-22 | community/governance | Offers to feed their home AIS station; maintainer promised support "late 2023, early 2024" | 3c 0r +2 me-too | Volunteer feeder intake is unmet demand — core wedge |
| 30 | 2023-07-19 | data-quality | AIS ETA frequently in the past; maintainer unsure if ship error or conversion bug | 3c 0r | Pass ETA through unmodified; document its unreliability |
| 29 | 2023-07-18 | data-quality | Vessels under ~13m absent from feed; maintainer: stations downsample, no map of which | 14c 0r +4 me-too | Don't downsample; publish per-station filtering policy |
| 26 | 2023-07-04 | feature-request | Wants origin port in addition to destination; maintainer says it's hard | 1c 0r | Derive port calls from archive, not live stream |
| 23 | 2023-05-30 | feature-request | Wants an interactive/live coverage map; maintainer: not in the next 12 months | 3c 1r +2 me-too | Live coverage map from actual reception density |
| 21 | 2023-05-26 | feature-request | Wants AIS ship-type filter in subscription; MMSI/message-type filters shipped, type filter never did | 3c 0r | Add vessel-type filter; reduces both server and client load |

### aisstream/aisstream, aisstream/example, aisstream/ais-message-models (49 open)

Links: `https://github.com/aisstream/<repo>/issues/<#>`.

| Repo | # | Date | Category | Summary | Signal | Hub implication |
|---|---|---|---|---|---|---|
| aisstream | 38 | 2026-08-19 | outage-silent-feed | Stream returns zero targets again two days after previous outage ended | 0c 0r | Repeat silent failures; need self-healing + public status |
| aisstream | 37 | 2026-08-17 | spam/off-topic | Promo recruiting SDR hosts for closed commercial marine-VHF audio product | 0c 0r | Feeder recruiting happens here; make onboarding one command |
| aisstream | 36 | 2026-08-14 | outage-silent-feed | Fresh valid key, world bbox, both field spellings, zero frames, no close | 2c 0r +1 me-too | No subscription ack; hub must ack and heartbeat |
| aisstream | 35 | 2026-08-14 | community/governance | Users offer free EU datacenter, hosting, and engineering to run service | 4c 3r +3 offers | Demand to co-fund/co-host; accept infra contributions |
| aisstream | 33 | 2026-08-08 | auth/login | Login host parked at Namecheap, OAuth state error, blocks key issuance | 2c 0r | Key issuance must not depend on fragile web/OAuth path |
| aisstream | 32 | 2026-08-05 | outage-silent-feed | Global 12-day silent outage from 2026-08-05 13:31 UTC, restored 08-17 | 19c 4r +13 me-too | Biggest pain: health must fail on silent upstream |
| aisstream | 31 | 2026-07-23 | coverage-gap (Vietnam) | Asks whether Cat Lai / Vung Tau southern Vietnam is covered | 2c 0r | Publish machine-readable coverage/receiver map per region |
| aisstream | 30 | 2026-07-23 | outage-silent-feed | Valid key, bbox subscription, connection opens, no messages ever arrive | 0c 1r | Distinguish "no vessels here" from "feed dead" |
| aisstream | 29 | 2026-07-22 | auth/login | GitHub login broken, empty body report | 0c 0r | Offer keyless/anonymous read tier |
| aisstream | 28 | 2026-07-20 | auth/login | OAuth token exchange fails server-side (DNS egress blocked); thread becomes fork discussion | 24c 4r +4 me-too | Auth outage blocks all new users; avoid OAuth-only signup |
| aisstream | 27 | 2026-07-20 | outage-other | Handshake timeout and ETIMEDOUT to 136.243.173.177 during cert outage | 1c 0r | Single-origin IP is SPOF; front with anycast CDN |
| aisstream | 26 | 2026-07-20 | outage-tls | Let's Encrypt cert for stream host expired 2026-07-19, service dead | 2c 2r +1 me-too | Automate TLS; Cloudflare-fronted edge removes this class |
| aisstream | 24 | 2026-07-08 | licensing/commercial | Asks if publishing derived aggregates with attribution is allowed; no ToS exists | 0c 0r | Ship explicit data license and attribution string |
| aisstream | 23 | 2026-06-22 | outage-silent-feed | Three-day silent outage; detailed probe proves key accepted, envoy proxy | 14c 5r +6 me-too | Status page and health endpoint explicitly demanded |
| aisstream | 22 | 2026-06-03 | coverage-gap (Caribbean) | No data for Martinique/Saint Lucia bbox while other sites show ships | 0c 0r | Per-region receiver coverage transparency |
| aisstream | 21 | 2026-05-22 | outage-tls | Cert expired 2026-05-20; Cloudflare Workers fetch returns 526 | 0c 0r | Users proxy via Workers; keep edge cert valid |
| aisstream | 20 | 2026-05-20 | outage-tls | Cannot connect, certificate chain NotTimeValid; workaround disables verification | 1c 0r | Cert lapses train users into insecure workarounds |
| aisstream | 19 | 2026-05-20 | outage-tls | Same 2026-05-20 cert expiry; Rust/tungstenite user has no bypass | 3c 3r +1 me-too | Non-JS clients cannot work around TLS failure |
| aisstream | 18 | 2026-05-15 | licensing/commercial | Paid B2B SaaS asks license, 500-2000 MMSI filters, SLA, paid tier | 1c 0r +1 interested | Document limits, large MMSI filter lists, paid/SLA tier |
| aisstream | 17 | 2026-03-24 | coverage-gap (Persian Gulf) | Persian Gulf coverage vanished; Rotterdam and Singapore still fine | 2c 0r +1 me-too | Per-region feed health, not just global up/down |
| aisstream | 16 | 2026-03-20 | licensing/commercial | Japanese commercial tracker asks license, attribution, connection and rate limits | 0c 0r | Publish limits and license; commercial users want certainty |
| aisstream | 15 | 2026-03-13 | outage-silent-feed | Longest-running silent-feed thread spanning March to August 2026 outages | 22c 0r +18 me-too | Chronic silent failure is the top demand signal |
| aisstream | 14 | 2026-03-11 | feature-request | Wants to know which subscribed bounding box a PositionReport matched | 1c 0r | Tag delivered messages with matching subscription id |
| aisstream | 13 | 2026-03-10 | outage-other | WSS endpoint not responding, TLS fine but no upgrade | 2c 0r +2 me-too | Upgrade path must fail loudly, not hang |
| aisstream | 11 | 2025-02-07 | rate-limit/connections | HTTP 429 returned on the WebSocket handshake itself | 1c 0r +1 me-too | Return machine-readable limit reason, not bare 429 |
| aisstream | 10 | 2024-10-24 | feature-request | Requests raw NMEA sentences as an API option | 0c 6r | Raw NMEA endpoint wanted; highest reaction-per-comment issue |
| aisstream | 9 | 2024-08-22 | coverage-gap (Great Lakes/St Lawrence) | Volunteers offer to add receivers; no path to contribute exists | 3c 1r +3 me-too | Accept volunteer feeders; publish feeder onboarding docs |
| aisstream | 8 | 2024-06-10 | rate-limit/connections | Browser WebSocket blocked by policy; maintainer explains per-account connection cap | 2c 0r | Decide browser policy; document connection caps clearly |
| aisstream | 7 | 2024-03-12 | rate-limit/connections | Resending subscription to change bbox triggers "concurrent connections exceeded" | 0c 0r | Support in-place resubscribe without counting new connection |
| aisstream | 5 | 2023-09-03 | coverage-gap (global) | Asks for list of AIS stations aisstream aggregates | 1c 0r | Publish station/source list as data |
| aisstream | 1 | 2023-05-26 | feature-request | Asks whether historical AIS data is available for backtesting/prediction | 3c 1r +2 me-too | R2 archive should be queryable by MMSI and period |
| example | 30 | 2026-08-07 | outage-silent-feed | August silent outage reproduced on Node, Python, Ubuntu, Windows | 6c 0r +4 me-too | Cross-stack repro proves server-side; publish incident timeline |
| example | 29 | 2025-07-10 | community/governance | Asks about accuracy, completeness, latency, and long-term sustainability | 0c 0r | Publish latency/completeness metrics and sustainability model |
| example | 27 | 2025-05-01 | client-sdk/docs | Go example uses deprecated `go get -d`; offers PR | 1c 0r | Keep examples current; accept community PRs |
| example | 24 | 2024-10-21 | client-sdk/docs | Python example README is empty | 0c 0r | Working per-language quickstarts matter |
| example | 23 | 2024-10-10 | data-quality | FiltersShipMMSI returns nothing while unfiltered global works; asks IMO filter | 1c 2r +1 me-too | MMSI filter must work; consider IMO filter |
| example | 22 | 2024-07-24 | coverage-gap (global) | Wants full station list to explain dead spots near covered ports | 0c 0r | Expose receiver positions and last-heard status |
| example | 19 | 2024-02-26 | client-sdk/docs | User cannot cleanly stop the asyncio WebSocket example | 0c 0r | Ship idiomatic client examples with clean shutdown |
| example | 18 | 2024-02-16 | outage-tls | 2024 cert expiry; revived in 2026 by users hitting it again | 4c 0r +2 me-too | Cert expiry is recurring, multi-year failure mode |
| example | 17 | 2024-01-14 | client-sdk/docs | JavaScript example has bounding box coordinates in lon/lat order | 0c 0r | Document coordinate order once; validate bbox input |
| example | 13 | 2023-12-18 | client-sdk/docs | Confusion over meaning of AIS `Timestamp` second-of-minute field | 0c 0r | Document AIS field semantics in message schema |
| example | 11 | 2023-11-22 | coverage-gap (global fleet) | Client-side filtering of 12 MMSIs yields positions for only two | 0c 0r | Server-side MMSI subscribe beats global firehose filtering |
| example | 10 | 2023-08-29 | client-sdk/docs | Docs say FilterShipMMSI, working field is FiltersShipMMSI | 0c 0r | Field naming must match docs; accept aliases |
| example | 8 | 2023-08-16 | outage-other | WebSocket not working; maintainer redirects to issues repo | 3c 0r +1 me-too | Single triage channel; last maintainer reply was 2024 |
| example | 7 | 2023-08-04 | client-sdk/docs | Wrong Go module import path in example; maintainer fixed | 1c 0r | Publish real, installable client modules |
| example | 4 | 2023-04-27 | outage-other | ECONNRESET before TLS established running the Node example | 0c 0r | Connection resets under load look like client bugs |
| models | 8 | 2025-09-07 | client-sdk/docs | Generated Java models import unresolvable `org.openapitools.client.JSON` | 0c 0r | Generated SDKs must compile standalone or be dropped |
| models | 5 | 2024-07-21 | client-sdk/docs | `DataLinkManagementMessageData` referenced but schema missing from OpenAPI yaml | 0c 2r | Schema must cover every AIS message type emitted |
| models | 4 | 2024-07-21 | client-sdk/docs | `SubscriptionMessage` missing from published npm TypeScript package | 0c 0r | Publish complete, tested typed clients |

### aisstream/Projects-Using-aisstream.io (60 open)

Links: `https://github.com/aisstream/Projects-Using-aisstream.io/issues/<#>`.

| # | Date | Project / who | Use case | Region | Commercial? | Features they depend on or ask for |
|---|---|---|---|---|---|---|
| 62 | 2026-08-13 | Skydex (browser flight sim) | integration / live map | global, player bboxes | no | bbox; PositionReport; type 5 static (type, dimensions); follow MMSIs; nav status, destination, ETA; port arrival/departure events |
| 61 | 2026-08-12 | Morgan PSI Control Centre | integration (ERP/logistics) | global | yes | vessel tracking in a procurement/container dashboard |
| 60 | 2026-08-11 | private "Buddy Overview" | hobby | Europe | no | positions and status of friends' MMSIs; also transmits AIS |
| 59 | 2026-08-09 | MS Fabric marine-protected-areas blog | research + integration (Fabric RTI) | Mediterranean | no | positions joined to MPA polygons; speed geofencing; asks for better stability |
| 58 | 2026-08-03 | Maranox Marine (IE) | commercial-permission / navigation app | Ireland | yes (paid app) | commercial license; relay to authenticated users; user/message/geography limits; caching and history retention rules; attribution; paid tier; regional subscription |
| 57 | 2026-07-23 | Tracker Porti (MarKco) | OSINT | Italy/Med, self-hosted | no (NGO) | live map; historical replay; berth-level tracking; rendezvous detection; watchlists + Telegram alerts; sanctions/PSC enrichment |
| 56 | 2026-07-23 | AIS-show (CesiumJS/MapLibre) | live map | global | no (OSS) | bbox + MMSI filters; course/speed/last-seen; low bandwidth; attribution |
| 55 | 2026-07-22 | "Flightradar for ships" | live map | unspecified | unknown | empty body |
| 54 | 2026-07-22 | OSINT Data Fabric (NextGen Advisors) | OSINT | global | yes | AIS as one feed in a multi-source fabric |
| 53 | 2026-07-16 | MerryGo Disney trip planner | integration (consumer app) | cruise routes | unclear | 8 fixed MMSIs; polls by opening a short socket every ~15 min (wants a snapshot); position freshness; attribution |
| 52 | 2026-07-02 | home dashboard (Cape Fear NC) | hobby | US | no | small local area; offers to run a receiver |
| 51 | 2026-06-23 | Belgian marine research institute | support (outage) + research | Belgium/North Sea | no | uptime; bbox; "can't afford MarineTraffic" |
| 50 | 2026-06-22 | The Reportr | OSINT | global | yes | AIS in a multi-domain intelligence wire |
| 49 | 2026-06-19 | XPAIS permission ask | commercial-permission (for a free plugin) | global, bbox follows aircraft | no (OSS) | bbox; PositionReport; StandardClassBPositionReport; static type + dimensions; latest-per-MMSI; ~1 Hz |
| 48 | 2026-06-18 | XPAIS Marine Traffic (X-Plane 12) | integration (flight sim) | global, moving bbox | no | same as #49 |
| 47 | 2026-06-15 | Pertuis-Nav | navigation app | France, Pertuis Charentais | no | narrow-channel traffic; CPA/TCPA alerts; estuary coverage |
| 46 | 2026-06-15 | commodity-vessel-tracker | research | Singapore | no | vessel/cargo type counts through a port |
| 45 | 2026-06-08 | Swintel | OSINT (situational awareness) | Sweden | unclear | live vessel layer; attribution |
| 44 | 2026-05-28 | moodSailor | navigation app | EU | no (free) | AIS in map + AR; CPA/TCPA; many concurrent bbox zones; offline caching |
| 43 | 2026-05-10 | SIST Ship Intelligence & Suspicion Tracker | OSINT | global | no (OSS) | realtime dashboard; anomaly detection; sanctions scoring |
| 42 | 2026-04-26 | Binnenvaart Live | port tracker | Netherlands, municipal harbor | no | enter/stay/leave logging; inland coverage; history as billing evidence |
| 41 | 2026-04-01 | Navig8 route planner | navigation app | unspecified | unclear | route planning + trip recording |
| 40 | 2026-03-13 | Seavec | research | North Sea + Baltic | unclear | trajectory archive; ship-type filter; heading; notes coverage only good in N. Sea/Baltic |
| 39 | 2026-03-10 | mariSecAI pipeline | support (WS timeout) | unspecified | unclear | reliability; Python websockets compat |
| 38 | 2026-03-07 | Ken's WorldView | hobby | global | no | world-events feed |
| 37 | 2026-03-06 | coverage/quality studies (moritzhuetten) | research (academic) | Baltic + Tokyo Bay | no | long-run capture; coverage + quality benchmark vs proprietary; publishes datasets |
| 35 | 2026-03-03 | Skana Robotics | other (autonomy) | global | yes | live AIS into an autonomy stack |
| 34 | 2026-02-14 | ShadowSight | OSINT | global | no (OSS) | multi-source aggregation |
| 33 | 2026-02-08 | Kiel Live ferries | ferry tracker + transit app | Kiel | no (OSS) | ferry positions by MMSI; local coverage |
| 32 | 2026-02-06 | web application (KyleV88) | commercial-permission | South Africa | yes | permission for production |
| 31 | 2026-02-02 | Ocean IQ | OSINT (subsea cables) | global | unclear | anomaly detection near cable corridors |
| 30 | 2026-02-02 | Openclaw test | integration / hobby | unspecified | no | movement tracking + notifications |
| 29 | 2025-12-10 | SailNav (Android) | navigation app | unspecified | no | could not get the API working (onboarding friction) |
| 28 | 2025-12-10 | OzCompFishing | live map + emergency | Australia | unclear | needs a non-WebSocket option (shared hosting); live map |
| 27 | 2025-10-29 | stream stopped | support | US | unclear | uptime; MMSI filter; quota clarity |
| 26 | 2025-08-12 | data request | other | global | unclear | wants a full past-year archive |
| 24 | 2025-06-22 | shipdocs port-dues checker | port tracker | unspecified | yes (internal) | port-stay durations; arrival/departure timestamps; history to audit invoices |
| 23 | 2025-04-18 | Njord "your own MarineTraffic" | live map | global | no (OSS) | whole-world firehose; own storage backend |
| 22 | 2025-04-04 | Pharo AIS viewer | live map + licensing question | FR | no (OSS) | asks the data license and whether storing for an offline demo is allowed |
| 21 | 2025-04-03 | 503 errors | support | unspecified | unclear | uptime/SLA |
| 20 | 2025-04-03 | 3D digital twin of the ocean (ICATMAR/OBSEA) | research + live map | Catalan coast | no | AIS fused with met-ocean + 3D tiles; local coverage |
| 19 | 2025-03-22 | MILSYM Web Map Service | integration (OGC WMS) | global | yes | AIS as WMS layers; ship type drives symbology |
| 18 | 2025-03-07 | AIS My Vessels Portfolio | hobby | global | no | PositionReport + static; own Postgres; last-known-position fallback |
| 17 | 2025-03-05 | yacht location tracking | hobby | coastal | no | realtime positions on a map |
| 16 | 2025-02-26 | anchor-watch iOS app | navigation app + data offer | unspecified | unclear | "pull boats in an area" (query, not stream); wants to upload anchored-boat positions |
| 15 | 2025-02-10 | AIS_Monitor | hobby (borderline OSINT) | Baltic | no | SOG; log vessels stopping or dropping below 10 kn |
| 14 | 2024-11-09 | Geoloc dufour34fanclub | hobby (club) | France | no | share members' positions by MMSI |
| 13 | 2024-08-19 | AIS Social (iOS) | other (social) | global | unclear | identity by MMSI; claim-your-vessel |
| 12 | 2024-08-05 | Vessel Tracker web app (KyleV88) | live map | South Africa | yes | PositionReport + static; notes missing tonnage/owner/flag |
| 11 | 2024-07-19 | SpaceShip (space-ship.world) | port logistics | global ports | yes | approach/ETA tracking |
| 10 | 2024-07-05 | Toulon AIS picture | research (air pollution) | Toulon | no | ship movements + type as emissions proxy |
| 9 | 2024-06-05 | OceanTwin (OceanHack4EU) | research | EU | no | seaglider monitoring alongside traffic |
| 8 | 2024-05-12 | Sea Spy | live map | global | no (OSS) | global feed of the world fleet |
| 7 | 2024-04-03 | OpenAIS | research (open data) | North Sea | no | archive/bulk history; Kafka ingest; gridded vessel-hours products |
| 6 | 2024-02-12 | Geoglify | live map | global / Portugal | no (OSS) | maritime data, planning, statistics |
| 5 | 2023-11-20 | Nominal Systems | integration (digital-twin sim) | global | yes | realtime AIS into simulations |
| 4 | 2023-07-09 | Red Hook WaterStories (PortSide NY) | other (education) | NY Harbor | no | local harbor coverage |
| 3 | 2023-05-29 | Ferry Watch (iOS) | ferry tracker | BC, Canada | unclear | ferry positions + status by MMSI |
| 2 | 2023-05-29 | BC Ferry Times | ferry tracker | BC, Canada | unclear | terminal-level ferry position/capacity |
| 1 | 2023-05-27 | tak-feeder-aisstream.io | integration (TAK) | global | no (OSS) | server-side ship-type filters; relay into a self-hosted TAK server |
