# Competitor and alternative pages for /ais

Drafted 2026-09-04. Data lives in [competitors.yaml](competitors.yaml); this file is the page set and the copy. Pages are Astro pages in the openwaters.io monorepo under `website/src/pages/ais/`, hero pattern of `/ais`, TOC pattern of `/charts/seamap`.

## Page set plan

| Priority | URL | Format | Target queries | Why |
|---|---|---|---|---|
| P1 | `/ais/alternatives/aisstream/` | Alternatives (plural, with a singular-intent opener) | "aisstream alternative", "aisstream.io alternatives", "aisstream down", "aisstream not working", "free AIS websocket api" | The only query in this category with visible demand: aisstream's own tracker has a 34-comment "Alternate feed?" thread, "Dead project, the need is a FOSS alternative" (#218), and a fresh outage cycle (#291, #292, Sep 1–2). Nothing ranks for it except Similarweb and a listicle. One page covers both singular and plural; split when Search Console shows both. |
| P2 | `/ais/vs/aisstream/` | You vs competitor | "aiscast vs aisstream", "aisstream compatible", "aisstream drop-in" | Sales enablement for the migration: the hostname swap, the differences table, what breaks. Linked from P1's aiscast entry and from `/api/ais`. |
| P3 | `/ais/feed/` | Alternatives, feeder side | "where to send AIS data", "AIS feeder", "AIS-catcher share data", "AISHub alternative", "MarineTraffic station data license" | The other audience. Nobody publishes what feeders give away; the docker-shipfeeder crowd multi-homes and will add one line. The comparison is on what you get back and what license you sign, which is aiscast's real differentiator. |
| P4 | `/ais/vs/aishub-vs-aisstream/` | Competitor vs competitor | "aishub vs aisstream", "free ais api" | The two free options people already compare; the third-option section is aiscast. Cheap once P1 exists. |

Not doing: pages against Kpler/MarineTraffic, VesselFinder, Datalastic. Different buyer, no shared search intent, and they have the authority to outrank a comparison page on their own name.

Hub: add an "Alternatives and comparisons" row to the `/ais` TOC linking P1–P4; link P1 from the `/api/ais` intro ("Coming from aisstream.io?") and from the token page.

Schema: FAQPage on P1 and P4 (questions below). Keep `data-nosnippet` off the comparison tables.

Expectation setting: these pages will be cited by AI answers for "aisstream alternative" (the category has almost no offsite consensus), but the recommendation in those answers will follow reviews and forum mentions, which aiscast does not have yet. The GitHub issue thread is the one place to earn that: one reply on #273 and #218 with the hostname swap, no marketing.

---

## P1: `/ais/alternatives/aisstream/`

**Title:** aisstream.io alternatives: free and paid AIS feeds that stay up (2026)
**Meta description:** aisstream.io down again? Six real alternatives compared on protocol, coverage, price, and license, including one that speaks aisstream's protocol so your code keeps working.
**H1:** aisstream.io alternatives

### TL;DR

If your aisstream.io client is sitting on a connection that delivers nothing, point it at `wss://ais.openwaters.io/v0/stream` with an aiscast token as the `APIKey` and it starts receiving again. Same subscribe message, same frames. If you need global coverage with an SLA, that is a paid product (Kpler, VesselFinder, Datalastic) and none of them stream. If you run a receiver, AISHub's snapshot is free and unrestricted. The rest of this page is the honest version of those three sentences.

### Why people look for an alternative

aisstream.io is the only free service that pushes global AIS over a WebSocket, and it is a one-person project that has been failing the way one-person projects fail. Its issue tracker had 190 open issues on 4 September 2026. The most recent, from the first two days of September, describe connections stuck for days blocking new ones with 429s, and every connection attempt closing instantly with code 1006. In August the service was down roughly 14 days out of 20. Earlier outage cycles were expired TLS certificates, four times since 2024.

The failures are silent. aisstream sends no subscription acknowledgement, no heartbeat, and no close reason, so a dead backend looks exactly like an empty ocean or a flipped bounding box. Every outage produces a dozen "is it me?" issues.

There are also no terms. Twenty issues ask for a data license, attribution wording, or a commercial contact, the latest two on 29 and 30 August 2026, and none have been answered. The server is closed source and the public repository has been empty since December 2022, so nobody can fork it.

None of that makes aisstream a bad idea. It proved that free, push-based, bounding-box AIS is what developers want. It is just not something you can build on.

### What to look for in a replacement

- **Push or pull.** aisstream is push: subscribe once, receive forever. Almost everything else is a REST API you poll, which changes your architecture and your bill.
- **Coverage you can see.** Terrestrial AIS reaches roughly 40 nautical miles from a receiver. Ask where the receivers are, and whether the service shows you per-station health so a gap is distinguishable from an outage.
- **A license.** If you display, store, or redistribute the data, you need written terms. Most commercial APIs forbid public display or redistribution outright.
- **Failure that announces itself.** An ack, a heartbeat, a close code, a status page.
- **Someone to email.**

### The alternatives

#### 1. aiscast (Open Waters)

An open AIS network that speaks aisstream's protocol. We built it because we had the same problem: our chart app streamed from aisstream and went dark with it.

- **Protocol:** WebSocket push, bounding box or MMSI list. `/v0/stream` is aisstream-compatible: change the hostname, use your aiscast token as `APIKey`, and `aisstream-ts`, the official Go/Python/JS examples, and `signalk-aisstream` run unmodified. `/v1/stream` is the native API and needs no token to subscribe. `GET /v1/vessels?bbox=` returns a GeoJSON snapshot so a view fills instantly instead of waiting for the next position report.
- **Coverage:** live from the Norwegian and Finnish coastal authorities and from volunteer receivers; worldwide terrestrial via the AISHub snapshot, which is 1 to 6 minutes old; aisstream.io itself as an upstream when it is up. Every event names its source and station, and `/v1/stations` shows each source's message rate and age, so you can tell a gap from a fault.
- **Price:** free without a token (2 streams, 20 msg/s, about 10°×10°); free personal token in one click, never expires (2 streams, 50 msg/s, about 20°×20°, or 50 vessels by MMSI anywhere); feeder tier is earned by feeding a receiver; commercial by arrangement.
- **License:** per source, published, with the attribution string you must carry. Norway NLOD, Finland CC BY 4.0, volunteer receptions CC0 with the aggregate under ODbL, AISHub with attribution. Code is MIT.
- **Operations:** subscription ack, heartbeat, close reasons, documented limits per token rather than per IP, a public status page at status.openwaters.io, and hello@openwaters.io answers.
- **Honest limits:** it is a beta with no SLA, launched in 2026. Live sub-minute coverage outside Norway, Finland, and volunteer stations depends on the AISHub snapshot. No history or bulk export yet. No satellite coverage outside the Norwegian EEZ.

Best for: anyone with an aisstream client that needs to keep running today, Signal K boats, dashboards, hobby and research projects, and anyone who needs to know where a message came from and under what license.

#### 2. AISHub

The reciprocal exchange: run a receiver, feed it, get the aggregate.

- **Protocol:** REST snapshot (JSON, XML, CSV) polled at most once a minute. No push. The worldwide snapshot regenerates about every 5 minutes, so positions are 1 to 6 minutes old.
- **Coverage:** the largest volunteer network with published numbers: 1,600+ stations in 83 countries, ~98,000 vessels a day.
- **Price:** free, but only if your receiver meets the bar (10+ vessels average, 90% uptime, 60 s max downsampling, 10 s max delay). No receiver, no API.
- **License:** no written terms document. AISHub confirmed to us in writing (22 August 2026) that use, commercial use, and redistribution are unrestricted. The key is revocable at will.
- **Honest limits:** polling only, minutes old, and the gate is a working station.

Best for: station operators who want a global "who is out there" view. Not for close-quarters use or anyone without a receiver.

#### 3. Open government feeds directly (Digitraffic, Kystverket, BarentsWatch, EuRIS)

The sources aiscast is built on, available to anyone.

- **Protocol:** Finland pushes over MQTT/WSS; Norway is a raw TCP socket of tagged NMEA; BarentsWatch is JSON and Kafka behind a free OAuth client; EuRIS is a REST bounding-box endpoint for EU inland waterways.
- **Coverage:** Finnish waters, the Norwegian coast to about 60 nm plus the EEZ and Svalbard, and 13 countries of European rivers and canals (anonymised: no MMSI or name).
- **Price:** free, no registration except BarentsWatch.
- **License:** CC BY 4.0, NLOD 2.0, and an attribution string for EuRIS. Redistribution explicitly allowed.
- **Honest limits:** three protocols, three reconnect loops, your own dedupe, and nothing outside those waters.

Best for: Nordic-only projects and anyone who wants to own the whole pipeline.

#### 4. VesselFinder API

- **Protocol:** REST, credit-based, built for per-vessel lookups. A raw TCP/UDP feed exists on quote, priced by area and traffic density.
- **Coverage:** global terrestrial plus satellite.
- **Price:** €330 for 10,000 credits, €625 for 20,000, €1,470 for 50,000. A terrestrial position is one credit, satellite ten.
- **License:** the terms ban competing vessel-tracking services and AI training; external display is allowed only where your order says so.
- **Honest limits:** polling a bounding box through credits gets expensive fast.

Best for: looking up a handful of known vessels, or satellite positions on demand.

#### 5. Datalastic

- **Protocol:** REST with credits; historical data included.
- **Price:** Starter €199/month for 20,000 credits, Growth €569 for 80,000, Developer Pro €679 unlimited. Two-week money-back trial.
- **License:** the terms ban exposing the data in a public-facing application.
- **Honest limits:** no WebSocket, no public maps.

Best for: a backend that needs history and is willing to pay for it.

#### 6. Kpler AIS (MarineTraffic, Spire Maritime, exactEarth, FleetMon)

The commercial layer consolidated into one company between 2021 and 2025.

- **Protocol:** GraphQL, raw NMEA streams, Snowflake.
- **Coverage:** the best on earth: 13,000+ receivers plus a satellite constellation with sub-minute latency.
- **Price:** not published. The wholesale figure in Kpler's own SEC filing is $437,500 a month for a global feed; regional live satellite has gone for about $10,000 a year in public procurement.
- **License:** no redistribution, no access through any third-party software or aggregator.

Best for: fleets and analysts with procurement. Not for individuals or anything open.

Also seen: MyShipTracking (REST from €90/month, public or third-party use of the data prohibited) and Marinesia (the cheapest paid API found, about €14 a quarter as of August 2026, REST polling only, and the one service an aisstream user reported actually migrating to).

### At a glance

| | Push stream | Snapshot API | Coverage | Free tier | Redistribution | Status page |
|---|---|---|---|---|---|---|
| aiscast | yes, aisstream-compatible | yes | Nordic live, world via AISHub (1–6 min), volunteers | yes, no account | yes, per-source license published | yes |
| aisstream.io | yes | no | global terrestrial, when up | only tier | no terms | no |
| AISHub | no (≤1/min) | yes | global, 1,600+ stations, 1–6 min | feeders only | yes, confirmed by email | no |
| Government feeds | MQTT/TCP | some | Finland, Norway, EU inland | yes | yes | per agency |
| VesselFinder | quote | credits | global + satellite | no | no | no |
| Datalastic | no | credits | global | trial | no public display | no |
| Kpler | raw NMEA | GraphQL | global + satellite | no | no | enterprise |

### Which one

- **Your aisstream client stopped and you want it back this afternoon:** aiscast `/v0/stream`. One hostname.
- **You run a receiver and want the world:** AISHub for the snapshot, aiscast for the live stream, and feed both. aiscast already feeds AISHub for you.
- **Finland or Norway only:** go direct to Digitraffic or Kystverket, or use aiscast for one protocol and the license carried through.
- **A few known ships, occasionally:** VesselFinder credits.
- **History and a budget:** Datalastic.
- **A fleet and a procurement department:** Kpler.

### Switching from aisstream.io to aiscast

1. Get a token at openwaters.io/ais/token (one click, stays in your browser, never expires).
2. Replace `wss://stream.aisstream.io/v0/stream` with `wss://ais.openwaters.io/v0/stream`.
3. Put the token in `APIKey`. Everything else stays: `BoundingBoxes`, `FiltersShipMMSI`, `FilterMessageTypes`, and the `MessageType` / `MetaData` / `Message` frame shape.

What changes: the limits are per token rather than per IP (2 concurrent streams, 50 messages a second, about 20°×20°), so a world-sized bounding box is refused with a message that says so instead of silently throttled. Where aisstream's decoder was wrong (`Valid: true` on out-of-range values, message 27 labelled 23), `/v0` reproduces it on purpose so nothing breaks; `/v1` gets it right. The full list is in the API reference.

When you have a minute, move to `/v1/stream`: no token for subscribing, one JSON event per message with `source`, `station`, the raw sentence and the decoded fields, an ack when your subscription is live, a heartbeat, and a close reason when we close.

### FAQ (FAQPage schema)

**Is aisstream.io down?** Check the aisstream issue tracker; there is no status page. As of early September 2026 the newest issues report 429 lockouts and instant connection closes. (Cheap win before publishing: add a `ws` check on `wss://stream.aisstream.io/v0/stream` to `.upptimerc.yml` so status.openwaters.io can answer this question and the page can link to it.)

**Is there a free alternative to aisstream.io?** aiscast is free without an account and speaks the same protocol. AISHub is free if you run a receiver. The Finnish and Norwegian government feeds are free and open.

**Can I use aisstream.io data commercially?** aisstream publishes no terms, and its licensing issues have gone unanswered since 2023. aiscast publishes the license of every source it re-serves and carries it on every event.

**Does aiscast have the same coverage as aisstream.io?** Not yet in the same way. aiscast is live in Norway, Finland, and wherever volunteer receivers are, and carries worldwide terrestrial positions from AISHub that are one to six minutes old. It also uses aisstream.io as an upstream when aisstream is up, so you lose nothing by switching.

---

## P2: `/ais/vs/aisstream/`

**Title:** aiscast vs aisstream.io
**Meta description:** Same WebSocket protocol, different everything else. A field-by-field comparison of aiscast and aisstream.io: uptime, coverage, limits, license, and what changes when you switch.
**H1:** aiscast vs aisstream.io

### TL;DR

aiscast speaks aisstream.io's protocol, so the choice is not about rewriting code. It is about whether you want a feed that tells you when it fails, names its sources and licenses, and answers email, in exchange for a beta whose live coverage outside the Nordics still leans on minutes-old snapshots. aisstream's own upstream is one of aiscast's inputs, so switching costs you no coverage.

### At a glance

| | aiscast | aisstream.io |
|---|---|---|
| Protocol | aisstream-compatible `/v0/stream` plus native `/v1/stream` | `/v0/stream` |
| Token | one click, no account, never expires | GitHub/Google OAuth key |
| Snapshot API | `GET /v1/vessels?bbox=` GeoJSON | none |
| Limits | documented, per token: 2 streams, 50 msg/s, ~20°×20° personal; more for feeders | 1 connection per IP, undocumented 429s, browsers banned |
| Failure signalling | ack, heartbeat, close reason, `/health`, public status page | none |
| Coverage | Norway and Finland live, volunteers live, world via AISHub 1–6 min, aisstream.io when up | global terrestrial when up |
| Source on each event | yes, with station | no |
| Data license | per source, published, attribution strings given | none |
| Contribute a receiver | yes: HTTP, UDP, Signal K | no |
| Raw NMEA | feeders and commercial | no |
| History | not yet (archive exists) | no |
| Code | MIT, public | closed; repo empty |
| Uptime | beta, no SLA; monitored publicly | down ~14 of 20 days in Aug 2026 |
| Cost | free; commercial by arrangement | free; no way to pay |

### Protocol and compatibility

aisstream's JSON is Go's `encoding/json` over the go-ais structs, typo and all (`VenderIDModel`). aiscast uses the same decoder, so `/v0/stream` frames are structurally identical; golden tests pin the envelope, rounding, time format, error messages, and resubscribe behaviour against captured aisstream output. Verified clients: `aisstream-ts`, the official Go, Python and JavaScript examples, and `signalk-aisstream`. Where aisstream's decoder is known wrong, `/v0` reproduces the quirk deliberately and `/v1` fixes it; the API reference lists each one.

Bottom line: the migration is a hostname and a token.

### Coverage

aisstream claims a global network of stations about 200 km off coastlines and publishes no map, no station list, and no per-region health. Its coverage issues (45 open) are people comparing against MarineTraffic and debugging their own code for hours before learning there is simply no receiver.

aiscast publishes every source: Kystverket and BarentsWatch for Norway, Digitraffic for Finland, volunteer receivers wherever they are, the AISHub aggregate for the rest of the world, and aisstream.io itself when it is delivering. `/v1/stations` gives each source's message count and age, and the live map draws the same data. Where aisstream is honest by accident (no data means no data), aiscast is honest on purpose (no data means this source has been silent for this long).

The gap: outside the Nordics and the volunteer footprint, aiscast's positions are AISHub's, regenerated every five minutes. That is fine for "what is out there" and wrong for close quarters. It closes as receivers join.

### Limits and connection model

aisstream caps one connection per IP, counts a resubscribe as a new connection, returns undocumented 429s, and forbids browsers, which pushes NATed users into reconnect storms and whole-world subscriptions. Its maintainer blames "applications created by LLMs" for the 2026 instability.

aiscast limits per token, publishes the numbers, thins a stream that exceeds its rate rather than dropping the connection, refuses an oversize area with a message that says so, and allows in-band resubscribe for free. CORS is open on `/v1/vessels`.

### License and terms

aisstream has none. aiscast does not relicense the aggregate: each event is re-served under its source's terms, and the terms page gives you the attribution string to carry. If you are one of the twenty people who asked aisstream for a license and heard nothing, this is the answer.

### Who aiscast is for

aisstream clients that need to keep running; Signal K boats; dashboards and hobby projects; research that needs provenance; products that need a license and a contact; receiver operators who want the network's data back.

### Who aisstream.io is for

Honestly: a free key to try the category, and nothing that runs unattended. When it is up it is fine, and aiscast consumes it too.

### Migration

See the steps on the alternatives page. Support: hello@openwaters.io, or an issue on the aiscast repository.

---

## P3: `/ais/feed/` (outline)

**Title:** Where to send your AIS receiver's data: feeder programs compared
**H1:** Where should your AIS station feed?

1. **The default is not a choice.** AIS-catcher shares with aiscatcher.org by default and docker-shipfeeder fans one receiver to 18 aggregators. Multi-homing is normal; the question is what each one gives back and what you sign.
2. **What you get back** (table from `feeder_programs` in competitors.yaml): aiscast returns the live deduplicated aggregate as raw NMEA and JSON, a station page with coverage and dedupe stats, and the feeder tier; AISHub a once-a-minute snapshot; MarineTraffic and VesselFinder a free web plan; ShipXplorer a plan and sometimes hardware; aiscatcher.org a map popup.
3. **What you sign.** MarineTraffic: perpetual, transferable, irrevocable, royalty-free, data becomes Kpler's exclusive property. VesselFinder: transferable, sublicensable license to commercialize, benefits discretionary. FleetMon promised owners would keep their data, was sold, and is gone. aiscast: CC0 on receptions, ODbL on the aggregate, not transferable to an acquirer, station location never published precisely. (Mark the feeder agreement as draft until it is signed off.)
4. **Setup** (reuse the `/ais` contribute cards): AIS-catcher `-H`, UDP, Signal K, docker-shipfeeder.
5. **Feed everyone.** aiscast forwards volunteer receptions to AISHub as its reciprocal feed, so feeding aiscast also counts toward AISHub if you bind your station.

---

## P4: `/ais/vs/aishub-vs-aisstream/` (outline)

**Title:** AISHub vs aisstream.io: the two free AIS APIs compared
1. Overview: AISHub is a reciprocal snapshot exchange; aisstream is a push stream with no gate and no terms.
2. Compare: access (receiver required vs OAuth key), transport (poll ≤1/min vs WebSocket), freshness (1–6 min vs live), coverage (1,600+ stations vs unpublished), terms (unrestricted by email vs none), reliability (stable vs 190 open issues).
3. Who each is for.
4. The third option: aiscast is an AISHub contributor and an aisstream consumer, serves both over one protocol, with the source on every event.
5. Three-way table, CTA to the token page.
