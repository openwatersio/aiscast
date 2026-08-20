# AIS service: scope and plan

A self-hosted replacement for aisstream.io: ingest AIS from open feeds and volunteer receivers, keep the full history, and stream live traffic to clients by bounding box at scale. Research behind every claim here is in [research/](research/).

## What the research settled

- **No drop-in alternative exists.** aisstream.io is the only free global bbox-subscribe push feed, it is a solo operation with repeated cert/outage incidents (down ~14 of the last 20 days), and there is no open-source equivalent of its server. Competitors are paid, regional, or hardware-gated. ([open-feeds.md](research/open-feeds.md), [protocol-and-building-blocks.md](research/protocol-and-building-blocks.md))
- **Open-licensed live feeds we may re-serve:** Norway Kystverket (raw tagged AIVDM over TCP, NLOD, ~38 msg/s), Finland Digitraffic (decoded JSON over MQTT/WSS, CC BY 4.0, ~43 msg/s), EuRIS inland Europe (REST bbox polling, anonymised positions with `mmsi: 0`, attribution string). Denmark's daily archive has no explicit license. Everything else is closed, non-commercial, or reciprocity-gated. US, Med, Caribbean, Asia, Pacific: no public feed anywhere; coverage there only comes from a volunteer receiver network.
- **Wire compatibility is tractable.** aisstream's JSON is Go `encoding/json` over `BertoldVdb/go-ais` structs (the `VenderIDModel` typo proves it), so go-ais gives struct-level compatibility for free; the envelope (cached name/position, rounding, time format, errors, filters, resubscribe) still has to be matched and proven by golden tests. Verified today: go-ais fast codec decodes the live Kystverket feed with 0 errors and parses its TAG blocks.
- **Users want uptime and honesty before features.** Of 305 open issues across the aisstream.io repositories, 84 are outages (36 of them "connects and subscribes, zero messages"), 45 are coverage gaps, 18 ask for a status page or a way to pay, and 18 ask for a license; feature requests are 16, and the three most wanted (HTTP snapshot, history, raw NMEA) are already in this plan. Priorities derived from it: subscription ack and heartbeat on `/v1`, close reasons, a status page, a terms page, a live coverage/station map, documented per-key limits. ([aisstream-issues.md](research/aisstream-issues.md))
- **Scale is small on ingest, real on fan-out.** Global unique terrestrial AIS is ~300 msg/s; raw inbound across overlapping stations 1–3k msg/s. One 4-vCPU box handles ingest at ~5% utilization; fan-out is N clients × bbox hit rate, with origin bandwidth the binding limit. ([infrastructure.md](research/infrastructure.md))
- **No open community network exists, and feeders get almost nothing today.** aiscatcher.org (1,259 stations, the largest volunteer network) returns a map popup; AISHub a once-a-minute snapshot; MarineTraffic a raw bounce-back under a perpetual, transferable license on your data. AIS-catcher even feeds aiscatcher.org by default, so the existing fleet is used to multi-homing and one config line away from feeding us. Nobody publishes the aggregate under an open license or has a plain-language feeder agreement. ([community-networks.md](research/community-networks.md))
- **Commercial and satellite AIS is closed to us.** The market consolidated into Kpler (MarineTraffic, FleetMon, Spire Maritime, exactEarth) and S&P Global (ORBCOMM AIS); a global wholesale feed leaked via SEC filing at ~$437,500/month; every retail API forbids redistribution; no academic satellite tier exists; Global Fishing Watch is CC BY-NC and itself downstream of Kpler. Satellite/commercial data only ever enters as bring-your-own-key. ([commercial-providers.md](research/commercial-providers.md))
- **Cloudflare can't terminate the ingest side.** Workers/Containers accept no inbound UDP/TCP; Spectrum UDP is Enterprise-only; per-message pricing makes DO-based dedupe/state/fan-out expensive unless heavily batched. Cloudflare's strengths here are TLS/cert ops, DDoS on the WS path, unbilled proxy bandwidth, and R2 + Data Catalog + R2 SQL for the archive. The origin still sends every byte to Cloudflare, so origin egress pricing decides the host.

## Architecture

One Go binary on a VM, Cloudflare in front for clients, R2 for history.

```
  open feeds ──TCP/MQTT/REST──▶ ┌────────────────────────────────────┐
  volunteer stations ─UDP/HTTP─▶ │  hub (Go, one process)             │ ──WS──▶ Cloudflare proxy ──▶ clients
  peers ─────────/v1/stream────▶ │  receptions → reassemble → dedupe  │ ──HTTP▶ /v1/vessels snapshot, /health
                                │  → decode → events → vessel state  │
                                │  bbox fan-out (per-client queues)  │ ──source-native receptions hourly .gz──▶ R2
                                └────────────────────────────────────┘         └─▶ normalized Parquet/Iceberg (stage 2)
```

### Data model

Two records, kept separately:

| Record | Contents | Purpose |
|---|---|---|
| Reception (immutable) | source id, station key, receive time (ours), source time (theirs, if any), the exact body as received (tagged NMEA sentence, Digitraffic JSON, EuRIS track, AIS-catcher JSON), validation result, license tag of the source | the archive; coverage analysis; spoof detection; re-decoding with better decoders later |
| Event (normalized) | `id = hash(canonical payload)`, origin (vessel key if IP-native, else station key), canonical time, geo cell, reassembled payload, decoded message, list of contributing reception ids, signature if present, `synthesized` flag when the payload was re-encoded from non-NMEA input | dedupe, fan-out, vessel state, `/v0` rendering, peering |

Timestamp precedence for the canonical time: validated source time (TAG `c:`, Digitraffic `timestamp`) when within ±30 s of receive time → feeder-supplied time when within skew → receive time. Both source and receive time are kept; out-of-skew or malformed timestamps are counted per station.

### Choices

| Concern | Choice | Why |
|---|---|---|
| Hot path | single Go process: per-source reassembly, dedupe on `(payload, channel)` over a 10 s window of canonical time, go-ais fast decode, in-memory vessel map, bbox fan-out with bounded per-client queues and drop accounting | simplest thing that handles 10× target; AisLib/AIS-catcher prove the patterns |
| Client protocol | aisstream-compatible WebSocket at `/v0/stream`, frozen (see launch requirement); `/v1/stream` is a bidirectional event stream (publish + subscribe on one socket) carrying events with station, raw body, receive time | existing clients work unchanged; `/v1` is the peer protocol, so feeders, hubs, boats, and chart clients all speak the same thing |
| Snapshot | `GET /v1/vessels?bbox=` returns current state | aisstream lacks this; clients otherwise wait minutes to fill a view |
| Non-NMEA sources | Digitraffic JSON is mapped to go-ais message structs (and optionally re-encoded to AIVDM for `/v1/nmea`) with `synthesized: true`; EuRIS is a separate overlay on `/v1` only, never in `/v0` (no MMSI, minutes stale) | `/v0` stays faithful to what a VHF receiver would hear; originals are archived as received |
| Feeder ingest | HTTP POST of AIS-catcher's `jsonaiscatcher` envelope with gzip + Basic auth (`-H https://… USERPWD id:key GZIP on INTERVAL 15`), content-sniffing `LIST`/`NMEA` bodies, key = station identity; `/v1/stream` publish for peers; UDP NMEA on a per-station port as a legacy adapter mapped to a station key; TCP push and MQTT later | matches what every existing receiver emits; HTTP/WS work through Cloudflare, UDP goes direct to the VM; identity is a key from day one |
| Parsing tolerances | TAG blocks on every transport (`s`, `c`, `g`, `n`, `d`, `t`, `r`; any `s:` form; checksum mismatch = warn and count), `c:` unit heuristic (`.`→s, ≤12 digits→s, else ms/µs), channel `1`/`2`/`CD` accepted, fixed-size messages up to 5 bits over length (AISHub type-5 fill-bit bug), USCG trailing fields after the checksum; payload size and line-length limits on all inputs | each of these silently drops a slice of real feeds if handled strictly; limits keep a hostile feeder from exhausting memory |
| Archive | every reception, source-native, hourly gzip per source to R2 with the license tag in the object path; normalized events to Parquet/Iceberg via R2 Data Catalog in stage 2 | receptions are the lossless record (pre-dedupe, so sized on raw overlapping traffic); R2 has no egress fee and is queryable by R2 SQL and DuckDB through Iceberg |
| Host | Hetzner Cloud EU (20 TB/mo included, €1/TB over): `ais-hub-1` is a CX43 (8 shared vCPU, 16 GB, €18.49/mo) in Helsinki because measured fan-out is ~12% of a core per 1,000 clients; CCX23 (dedicated, €101.49/mo) is the resize if CPU ever shows. "Hetzner-class" means any cheap-bandwidth VPS with a dedicated IP (OVH, Scaleway, Oracle are the alternatives; hyperscalers are 5–10× on egress) | origin egress decides it: Fly bills $0.02/GB on every byte sent to Cloudflare, Hetzner includes 20 TB ([host table](research/infrastructure.md#design-b-single-binary-on-a-vps)) |
| Edge | Cloudflare proxied hostname for WS/HTTP (Origin CA cert or Tunnel), unproxied A record for UDP ingest; app-level ping/pong for the proxy idle timeout; Argo off; `permessage-deflate` on | cert ops become a non-event; compression cuts origin egress ~5× |
| State on restart | vessel map snapshot to disk every 10 s; reload on boot | restart without a blank map |
| Keys | API key required on `/v0/stream` (aisstream semantics); feeders and peers identified by a key (HTTP Basic secret now, Ed25519 public key when events are signed); flat file/SQLite table; the hub has its own keypair | compatibility and abuse control; identity model that survives federation; no accounts system until needed |
| Metrics and health | Prometheus `/metrics` (per-source rate and last-message age, dedupe ratio, clients, queue drops, invalid timestamps per station); `/health` returns non-200 when any configured upstream is silent >2 min | the aisstream failure mode was a healthy-looking empty service |

Decisions deliberately deferred: second node and the bus between nodes (until one box is the bottleneck), Parquet at ingest (raw gz first), MQTT broker (HTTP/UDP cover the fleet), accounts/billing.

### Assumptions (measured vs inferred)

| Quantity | Value | Basis |
|---|---|---|
| Unique global terrestrial rate | ~300 msg/s | aisstream docs (measured by them) |
| Raw inbound incl. station overlap | 1,000–3,000 msg/s | inferred from AISHub station counts |
| 3,000 msg/s | 259.2 M receptions/day | arithmetic |
| Tagged NMEA sentence | ~90–110 B raw; ~25–35 B gzip'd | measured on Kystverket sample (raw); compression inferred |
| Reception archive | ~25 GB/day raw, ~7 GB/day gzip'd at 3k msg/s | arithmetic on the above |
| aisstream JSON per message | ~450–500 B | measured from samples |
| Client bbox hit rate | unknown; 50 msg/s/client used for sizing | assumption; world-bbox clients see ~300 msg/s |
| 1,000 clients × 50 msg/s × 500 B | 25 MB/s origin egress, 64.8 TB/mo uncompressed, ~13 TB/mo with deflate | arithmetic; compression ratio inferred |
| Single-node fan-out capacity | 1,000 clients × 68 msg/s = 68k sends/s, 37.7 MB/s, zero queue drops, 57 MB RSS, ~12% of one laptop core | measured 2026-08-20 with `cmd/loadtest` against the hub on an M-series laptop, uncompressed frames; 250k sends/s with batching still extrapolated |
| Hetzner CCX23 price and overage | not machine-readable | verify before committing |

Load testing with synthetic clients is a gate before the public beta, not a later nicety.

## Design constraint: one peer in a network

Long term the hub is one peer in a network where vessels, shore receivers, hubs, and chart clients exchange AIS as signed events over the internet, alongside VHF. Nothing in that future is built now, but three cheap early decisions keep it reachable:

1. `/v1/stream` is direction-agnostic: one WebSocket, publish and subscribe frames, bbox/cell subscriptions. Hub-to-hub peering is then just a client that also publishes.
2. The internal record is the event above, with a content hash id, an origin key, a geo cell, contributing receptions, and room for a signature. Dedupe by id; provenance by signature chain. The hub relays; it signs only its own observations and attestations.
3. Identity is a key. The per-station UDP port and HTTP secret are adapters that map to a key.

What the network looks like later: vessels run the Signal K plugin as an agent (keypair, signs Signal K deltas or its own `!AIVDO`, publishes to one or more relays, subscribes to cells around it, buffers offline, syncs boat-to-boat over WiFi/LoRa when there is no internet); relays federate by geographic cell; the archive is a mirrorable event log so anyone can run a peer with full history; a bootstrap relay list ships in the plugin and chart client with us as the first entry. Trust is tiered and shown in the UI: self-reported (any key), corroborated (the key's internet reports matched independent VHF receptions N times, which our receiver network can attest), attested (flag-state/MCP certificate or an organization vouching). Privacy is consent-based: sender chooses public, delayed, coarse, or encrypted to a group. IP-sourced targets reach a plotter only via Signal K with source marked `net`, never via VHF transmit, and are never sold as collision avoidance. Precedents: APRS-IS (federated filter relays), Nostr (signed events, dumb relays, tag filters, `g` geohash tag), Secure Scuttlebutt (offline-first gossip), IALA MCP/MRN (official maritime identity and messaging over IP).

## Launch requirement: aisstream.io API compatibility

Hard requirement for initial launch. Definition of done: an existing aisstream.io client works against us with only the hostname changed. Concretely:

- `wss://<host>/v0/stream`, same subscribe JSON (`APIKey` any casing, `BoundingBoxes` as `[lat, lon]` corner pairs in any corner order, `FiltersShipMMSI` ≤50, `FilterMessageTypes` with duplicates rejected), resend-to-replace semantics, 3 s subscribe deadline, 1/s update rate, same error frames (`Api Key Is Not Valid`, `Subscription Object Is Malformed`), slow-client disconnect.
- Same outbound JSON: `MessageType` / `MetaData` / `Message` envelope, go-ais field names and typos (`VenderIDModel`, `Fixtype`), numeric `MMSI_String`, Go-format `time_utc`, untrimmed `ShipName`, `MetaData.latitude/longitude` from the last-position cache for positionless messages (so static data reaches bbox subscribers), all 24 `MessageType` values.
- Golden tests: per-type fixtures for every supported type built from captured aisstream output and the `ais-message-models` schema; subscription validation cases (coordinate order, malformed boxes, filter limits, resubscribe, key casing, errors); static-message routing through the position cache; slow-client behavior.
- Verified by: `aisstream-ts`, the official Go/Python/JS examples, and `signalk-aisstream` running unmodified.
- Additive only: extra fields and `/v1` endpoints may exist, but nothing in `/v0` may deviate. Peer/event-model work lives under `/v1`.

Until the golden tests pass, the claim is "struct-compatible", not "byte-identical".

## Plan

### Stage 0A, today: local vertical slice

Goal: the whole pipeline running locally on one real source, with the data model and protocol fixtures in place.

1. Repo `hub/` (name TBD), Go module: `source/` (Kystverket TCP), `ingest/` (HTTP POST, UDP), `pipeline/` (reception → reassembly per source → dedupe → decode → event → state), `stream/` (WS server, bbox filter, `/v0` rendering, bidirectional `/v1/stream`), `archive/` (hourly source-native gz → R2 via S3 API).
2. Decode with `ais.CodecNewFast(false,false,true)` + `aisnmea`; wrap reassembly per source to avoid go-ais's station-less fragment key; reassembly keyed `(station, channel, seqid, count)` with wall-clock expiry.
3. Reception and event records as above; reception archive written before dedupe.
4. `/v0/stream` golden fixtures from captured aisstream samples; conformance harness running `aisstream-ts` against localhost.
5. Verify: subscribe with a bbox over Oslo fjord, see positions; POST an AIS-catcher batch and send a UDP sentence, see both come out; hourly gz lands in R2.

Checklist: [x] repo + decode [x] Kystverket source [x] reception + event records [x] dedupe + state [x] `/v0/stream` + golden tests [x] `/v1/stream` bidirectional [x] HTTP + UDP ingest [x] R2 archive (hourly rotation uploads to `ais-archive`) [x] `aisstream-ts` works locally

Code: [`hub/`](hub/README.md). Simplifications to revisit in 0B: UDP station = sender IP (not per-station ports/keys), R2 upload shells out to wrangler, the open hour is not uploaded on shutdown, no `/metrics` or rate limits yet.

### Stage 0A½: viewer

A static map of live traffic, the first client of the hub other than aisstream-compatible ones. Lives in [`viewer/`](viewer/) at the repo root, not in the hub: the hub stays a pure API, the page is a client like the chart plugin, it deploys to Cloudflare Pages for free, and it becomes the public "what's on the feed" and coverage page later.

- One `index.html`, vanilla JS + MapLibre GL, no build step. `?hub=` selects the hub (default `localhost:8080`). OpenFreeMap basemap until the Open Waters tiles are used. `hash: true` for shareable positions.
- On load and debounced `moveend`: `GET /v1/vessels?bbox=` fills the viewport, then `subscribe` on `/v1/stream` with the same bbox; live events update a per-MMSI map, `setData` at ≤2 Hz.
- Triangles rotated by heading (COG fallback), colored by ship-type class; dot when no heading; opacity by age, dropped after 30 min. Drawn where reported, never extrapolated (the lookout-marine rule). Click → name, MMSI, type, SOG/COG/HDG, nav status, last seen, source/station, last raw NMEA. Corner stats: WS state, events/s, vessels in view. Per-source license attribution in the map attribution.
- Hub side, pulled forward from 0B: vessel cache carries COG/SOG/heading/type/nav status/last seen/source/station and evicts idle vessels; `GET /v1/vessels?bbox=` returns GeoJSON with `Access-Control-Allow-Origin: *`.
- Skipped until asked: tracks (Stage 2 history), clustering, outlines to scale, search.

Checklist: [x] vessel cache + eviction [x] `/v1/vessels` [x] `viewer/index.html` [x] verified against Kystverket in a browser [x] published at https://openwatersio.github.io/aiscast/ against `ais.openwaters.io` (repo public, GitHub Pages from Actions)

Code: [`viewer/`](viewer/README.md). Not yet deployed to Pages.

### Stage 0B, this week: public Nordic beta

- [x] Digitraffic MQTT adapter (`Digitraffic-User` header) mapped to structs with `synthesized: true`, re-encoded to AIVDM so `/v1` events still carry a sentence; originals archived under `CC-BY-4.0/digitraffic/`.
- [x] Reconnect with backoff on every upstream; `/health` fails when any open-feed upstream is silent >2 min; `/metrics`. [ ] Uptime monitor on the public WS endpoint.
- [x] Bounded client queues with drop accounting; payload/line limits on all inputs; per-IP WS connect and per-key ingest rate limits (fixed one-minute windows).
- [x] Vessel-state snapshot every 10 s, restored on boot; SIGTERM snapshots and flushes/uploads the open archive hours.
- [x] Tests against GPSD `sample.aivdm` (USCG trailing-field tolerance came out of it) and libais `tagblock.nmea`; `cmd/loadtest` Go generator; load test result in Assumptions.
- [x] Box: `ais-hub-1` on Hetzner (hel1, CX43), systemd, firewall, hub live at `http://2.29.0.215:8080` and `udp://2.29.0.215:10110` with Kystverket + Digitraffic ([deploy/README.md](hub/deploy/README.md)). [x] `https://ais.openwaters.io` live: Cloudflare proxied → Caddy (Let's Encrypt) → hub on localhost; 8080 closed. [x] Archive hours upload to R2 from the box over the S3 API (hand-rolled SigV4 PUT). [x] `AISSTREAM_API_KEY` on the box (aisstream connected but delivered nothing during setup; it is best effort and outside the health gate). [x] `ais.openwaters.io` is DNS-only (no proxy) for the beta, so the same name serves UDP ingest; `TRUST_CF_HEADERS` gates Cloudflare header trust if proxying is ever turned on. [x] Access tokens replace static keys (below).
- [x] aisstream.io as an upstream when `AISSTREAM_API_KEY` is set (envelopes mapped back to structs, `synthesized`, dedupes against the open feeds; archived under `aisstream-io-terms/`). [ ] Confirm it actually delivers frames (first attempts with the real key connected but received nothing in 30 s; check again when aisstream is up). [ ] EuRIS overlay on `/v1` if anonymised positions are useful to the chart client.

Kystverket allows one TCP connection per source IP (a second connection makes both reconnect every few seconds): one hub per public IP, and the second node in Stage 4 must not also pull Kystverket from the same IP.

### Stage 0C: openwaters.io pages

The public face of the service lives on [openwaters.io](https://openwaters.io) (repo `openwatersio/openwaters.io`, Astro + Tailwind, `website/src/pages/`, nav in `components/layout/Header.astro`, footer in `Footer.astro`, API docs built from OpenAPI at build time in `pages/api.astro` + `utils/openapi.ts` + `components/api/ApiEndpoint.astro`). This repo stays the source of truth for anything the site pulls in (viewer, OpenAPI spec, plugin); the site only renders.

Website repo:

- **Nav**: `AIS` → `/ais` in `navItems` (after Charts) and the footer Explore list.
- **`/ais`** (`pages/ais/index.astro`, same hero pattern as `/charts`): hero (plain about coverage: Norway and Finland from open feeds today, volunteer receivers later), `<iframe src="https://openwatersio.github.io/aiscast/#8/59.5/10.6">` full-width under the hero (Pages sends no frame-blocking headers; the hash sets the initial framing), then sections with a TOC like `/charts/seamap`: **Use the data** (`GET /v1/vessels?bbox=` and a `/v1/stream` subscribe frame as copy-paste `curl`/`websocat` examples, `/v0/stream` for existing aisstream.io clients with the hostname swap, link to `/api#ais`), **Signal K** (the plugin card; see blockers), **Contribute** (run the hub, code on GitHub, feed a receiver once Stage 1 opens), **Sources and license** (per-source table from [Licensing](#licensing-and-attribution) with the attribution strings consumers must carry; the aggregate is not relicensed), a "not for navigation" callout (positions are drawn where reported, minutes stale, no collision avoidance), and a link to the repo. No live counters on the page (the hub's `/v1/vessels` has CORS open, so a vessel count is one fetch away when wanted).
- **`/api`**: AIS endpoints as a second tag group on the same page, built from this repo's spec fetched at build time from `raw.githubusercontent.com/openwatersio/aiscast/main/hub/openapi.json` (not from the hub itself, so a hub outage cannot fail a site build). `ApiEndpoint` takes the host from the spec's `servers[0].url` instead of `API_HOST`; WebSocket operations carry `x-websocket: true` and render a `WS` badge with the frame schemas from `components`. `utils/openapi.ts` types against `@neaps/api`'s literal spec; loosen `extractEndpoints` to a structural OpenAPI type so a fetched JSON spec passes.
- **Homepage**: an AIS card next to Charts/Tides/Bathymetry.

This repo:

- **`hub/openapi.json`**: OpenAPI 3.1 for `/v0/stream` (WS, aisstream.io subscribe and envelope), `/v1/stream` (WS, subscribe/publish/event frames), `/v1/vessels` (GeoJSON), `/v1/receive` (AIS-catcher POST), `/health`; `servers: [{url: "https://ais.openwaters.io"}]`; examples from `hub/testdata`. Served by the hub at `/openapi.json` via `go:embed` so the deployed binary describes itself. Golden test: the spec parses and every `mux.HandleFunc` path appears in it.
- **Viewer**: `cooperativeGestures: window.self !== window.top` so the embedded map doesn't capture page scroll; nothing else changes.

Checklist: [ ] `hub/openapi.json` + `/openapi.json` + route-coverage test [ ] viewer `cooperativeGestures` [ ] site: nav + footer + homepage card [ ] site: `/ais` page [ ] site: `/api` AIS group from the fetched spec, host from `servers`, WS badge [ ] verify `signalk-aisstream` against `wss://ais.openwaters.io/v0/stream`, then link it from `/ais` [ ] swap the Signal K link to `signalk-aiscast` on npm when Stage 3 ships.

Blockers and decisions before the pages can say what they need to say:

1. **`/v0` keys are hand-issued** (`V0_API_KEYS` in `/etc/hub.env`); a docs page cannot hand out one. Decide: accept any key on `/v0` (per-IP and rate limits already enforce; `/v1` subscribe is anonymous anyway) or "email for a key". Lean: accept any key, log it, keep `V0_API_KEYS` as a later allowlist.
2. **Signal K link target**: `signalk-aiscast` does not exist yet; `signalk-aisstream` (npm 0.9.1) is the launch-requirement client but is not yet verified against the hub. Until verified, `/ais` links the Stage 3 section of this plan, not a package.
3. **Contribute-a-receiver docs** are blocked on Stage 1 (feeder agreement, `FEEDER_KEYS`, registry); the page says so and offers an email, nothing more.
4. **Terms line**: the free-tier wording ("free for non-commercial use; commercial use needs a paid key", see Sustainability) is undecided, so the page states source licenses and attribution only and carries no terms line until it is.
5. **Name**: repo and viewer say `aiscast`, the nav says AIS, the hostname is `ais.openwaters.io`; pick the public product name before the page copy is written (open question below).
6. **Uptime**: no status page or monitor exists (the top aisstream.io complaint); the hero should not promise availability, and `/health` can be linked as the interim status check.

### Stage 0C: more sources, some on borrowed terms

Decision (2026-08-20): pull in sources whose terms are unclear to get coverage now, keep every one of them separable, and walk them back as volunteer and licensed data arrive. Separable means: its own `source` value on every event, its own license tag in the archive path (purgeable), its own env flag, and never in the health gate.

- [x] AISHub, reciprocal: the hub feeds its received (non-synthesized) stream to the assigned UDP port (`AISHUB_FEED`), and polls the aggregate snapshot once a minute (`AISHUB_USERNAME`): global terrestrial coverage (~51k vessels per snapshot, ~100k events per new snapshot), `synthesized`, source `aishub`, archive `aishub-terms/`. Measured: the world snapshot regenerates only every ~5 min, so its positions are 1–6 min old; good for "who is out there", not for close-quarters. Their current page grants "use" only (the 2021 "publish freely or commercially" sentence is gone); they can revoke at any time. Walk-back: unset the flag, purge `aishub-terms/`. [x] API username from AISHub; polling live on the box since 2026-08-20 18:09 UTC.
- Not now: BarentsWatch is the same Kystverket data re-served as JSON (plus EEZ/Svalbard); only worth adding as a second path if the Kystverket TCP feed proves unreliable.
- [ ] Ask Sjöfartsverket (Sweden; 2019 price sheet: 5,000–25,000 SEK/yr distributor tiers) and DMA (Denmark live; paid subscription) for current terms.
- [ ] EuRIS inland overlay on `/v1` if the viewer wants inland Europe (anonymised; never in `/v0`).
- Not doing: MarineTraffic/VesselFinder/ShipXplorer feeder programs (web plans, not data; perpetual licenses over what is fed), scraping any commercial map (circumvention, not a grey area), aprs.fi (no caching), GFW (CC BY-NC, aggregated), Kystverket restricted tier and HELCOM raw (explicit agreements required).

### Access tokens (done 2026-08-20)

Identity and authorization are separate. Identity is a device-generated Ed25519 keypair (the Signal K and chart plugins; Stage 3 signs events with it). Authorization is an Ed25519-signed claims token (`ak1.<claims>.<sig>`): `sub`, `role` (`personal`, `feeder`, `peer`, `partner`, `admin`), `exp`, optional `bbox`, `cidr`, `conns`. The issuer private key lives offline (`cmd/aiscast-key`, seed in the untracked `.env`); the hub verifies with public keys (`ISSUER_PUBKEYS`) and holds only a personal-tier issuer key so `POST /v1/keys` can hand a 30-day personal token to any device key without a bundled secret. The token is accepted wherever a key was: aisstream `APIKey`, Bearer, Basic password, `?key=`. `/v1/stream` subscribe and `/v1/vessels` stay open; `/v0/stream`, publish, and `/v1/receive` need a token. Not done: proof of possession at connect (personal tokens are bearer tokens naming the device key), holder attenuation (macaroon/Biscuit-style caveats) — add when a partner needs to issue sub-keys.

### Stage 1: volunteer ingest

Blocked until the feeder agreement (including the funding terms in Sustainability), per-source licensing, privacy policy, station registry, and deletion/opt-out procedures are settled (see below).

- Station registry issuing a feeder token (`aiscast-key new -role feeder`; and a UDP port for legacy setups); one-page setup (`AIS-catcher -H https://ais.<domain>/ingest USERPWD id:key GZIP on INTERVAL 15`, or `-u ingest.<domain> <port>`); per-station stats page, coverage map, offline alerts; opt-in only, stated plainly.
- PR to `sdr-enthusiasts/docker-shipfeeder` so existing multi-feed stations add us with one env var.
- Raw feed back to feeders: `/v1/nmea` WebSocket/TCP of deduped `!AIVDM` with TAG blocks, bbox-filterable, each sentence carrying its source's license tag. Reciprocity only in this stage: a feeder key unlocks it. Wider access (free for all vs. free for non-commercial use) is an open question; see Sustainability.

### Stage 2: history and reporting APIs

- Normalized events to Parquet/Iceberg in R2 Data Catalog (nightly job first, streaming later); verify R2 SQL and DuckDB queries.
- `GET /v1/vessels/{mmsi}` last known; `GET /v1/vessels/{mmsi}/track?from&to`; `GET /v1/history?bbox&from&to`; station stats; coverage heatmap tiles. Served by a Worker over R2 SQL / Iceberg with caching; hot recent window from the hub's memory.
- Denmark backfill only after written confirmation of reuse rights from DMA (`sifa@brs2.dk`).

### Stage 3: Signal K plugin

`signalk-aiscast`, the vessel agent, in [`signalk-plugin/`](signalk-plugin/) of this repo. One plugin, one socket to the hub, both directions: it **broadcasts** what the boat's receiver hears and **consumes** the hub's traffic when it has nothing of its own to hear. Survey of every existing AIS plugin and the server API facts this relies on: [research/signalk-plugins.md](research/signalk-plugins.md). Nothing in the ecosystem does both directions well; the failure modes they share (no reconnect, leaked listeners, silent subscriptions, retry storms, own ship echoed back as a target) are the checklist below.

#### Identity and self-authentication

No registration step. Identity is an Ed25519 keypair on each side; both runtimes have it in the standard library (`node:crypto`, Go `crypto/ed25519`). NIP-01 would give us off-the-shelf clients but signs with secp256k1 Schnorr, a dependency on both sides; the envelope below is NIP-01-shaped (pubkey, time, content, sig, content-hash id) so swapping the curve later is contained, but it is not Nostr-compatible.

- **Plugin key**: generated on first `start()`, stored in `<getDataDirPath()>/identity.json`, never leaves the boat. The public key (hex, 64 chars) is the station id; shown in the plugin status line and at `GET /plugins/signalk-aiscast/identity` (readonly). Claiming it on a stats page, trust promotion, and revocation come with the Stage 1 registry; none of that blocks the plugin.
- **Publish frame** on `/v1/stream`: `{"type":"publish","pubkey":<hex>,"time":<unix ms>,"nmea":[...],"sig":<hex>}` with `sig = Ed25519(time + "\n" + nmea.join("\n"))`. The hub verifies the signature, rejects `time` more than 5 min from its clock, and answers `{"type":"ack","time":<frame time>,"n":<accepted>}`. Station and source become `key:<pubkey>`; the whole signed frame is archived as one reception (source-native, signature preserved) alongside the per-sentence receptions. Basic-auth publish and `FEEDER_KEYS` stay for AIS-catcher.
- **Hub key**: `HUB_KEY` file, generated on first run. On connect the hub sends `{"type":"hello","pubkey","time","sig"}` with `sig = Ed25519(time)`; the plugin pins the key on first contact (TOFU) and turns the status line red if it changes. TLS already authenticates the hostname; the hello is what makes hub identity survive a hostname move and is the seed for Stage 4 relay signatures.
- **Trust**: self-signed keys are accepted by default and labeled trust `self`; keys cost nothing, so the abuse controls are per-IP connect limits and per-key publish limits, not the key itself. Promotion to `corroborated`/`attested` is the Stage 1/4 attestation work; the hub only needs a place for a trust tier on the station now.
- **Timestamps**: the plugin prepends `\c:<unix ms>*hh\` (TAG block, checksummed) to every sentence that lacks one, at the moment `nmea0183` fired. The hub already parses `c:`. On the publish path, a sentence whose `c:` is more than 60 s old is archived and counted but not emitted live and not folded into the vessel cache; that is how an offline backlog replays without stale positions jumping onto the live map.

#### Plugin behavior

- **Collect**: `app.on('nmea0183')` and `app.on('nmea0183out')` (the latter is where `signalk-n2kais-to-nmea0183` puts N2K-derived AIS), regex `^(\\[^\\]*\\)?[!$]..VD[MO],`. `VDM` always (when sharing is on), `VDO` only with the separate own-ship consent. Verbatim except for the TAG block; the hub decodes, dedupes, and archives. Listeners are removed on the same emitter in `stop()`; `start()` calls `stop()` first.
- **Send**: online, each sentence goes out as received, one publish frame per sentence (multipart fragments share a frame), so the hub's live stream sees the boat's receptions with no added latency; `permessage-deflate` keeps the per-frame cost small. Frames the hub does not `ack` within 30 s, and anything collected while the socket is down, go to the disk queue.
- **Queue**: `<dataDir>/queue/<ms>.json` files of up to 500 sentences each; on reconnect they drain oldest-first, one frame in flight, deleted on `ack`, before live sending resumes. Cap 100 MB, oldest dropped and counted. Per-sentence `c:` carries the real receive time, so the hub's 60 s rule sorts live from replay without a flag.
- **Socket**: `ws` (Node 20 has no global `WebSocket`), `permessage-deflate` on (no separate gzip), exponential backoff 5 s → 5 min with ±20 % jitter, 5 → 30 min after a 429/403 close, reset on the first `ack`/`event`; silence watchdog: no frame or pong for 60 s → terminate and reconnect.
- **Loop guards**: drop inbound events whose `station` is our own key (the hub echoes what we publish); keep payloads received from the hub in a 5 min set and never publish them back (`signalk-n2kais-to-nmea0183` would otherwise turn our injected deltas into `!AIVDM` on `nmea0183out`, and the hub would credit the boat with receptions it never made).
- **Consume** (`receive.mode`): `off` | `auto` (default: subscribe only while no local AIS sentence has been heard for 90 s, so a boat with a receiver is not fed redundant traffic and a boat without one, or a server at home, gets the picture) | `always` (also fill beyond VHF range; a target whose `navigation.position` has a non-net `$source` younger than 60 s is left alone, and the server's own `sourcePriorities` setting remains the user's knob).
- **Bbox**: own position from `app.getSelfPath('navigation.position')` polled every 10 s (no `streambundle`), ± `receive.radiusNm` (default 50, max 200); re-subscribe when the boat has moved more than a quarter radius or the radius changed. No position → no subscription, status "waiting for position"; an empty bbox means the whole world on `/v1/stream` and must never be sent.
- **Inject**: each event's `nmea` sentences go through `@signalk/nmea0183-signalk`'s `Parser`, the server's own AIS parser, so contexts, paths, and value types are identical to VHF-received AIS (this is what avoids the bare-string `name` memory leak and the `eta` type that freezes Freeboard). Each delta gets `$source: "signalk-aiscast.net"` and `timestamp` from the hub event's canonical `time`. Dropped before injection: own MMSI, MMSI 0, events whose `msg_type` is a position report but carry no `lat`/`lon` (hub rejected the position), own echoes. Stale targets expire via the server's `pruneContextsMinutes`; the plugin has no TTL of its own.
- **Status**: one line, refreshed at most every 5 s: `key 3f9a…  ↑ 42 msg/min (queue 0)  ↓ 118 targets  hub ok 2 s ago`; `setPluginError` on signature rejection, pinned-key mismatch, or a queue that has been draining for more than an hour.
- **Config** (JSON schema, no webapp): `hub` (WebSocket URL, default the public hub), `share.targets` (default on), `share.ownShip` (default off, its own checkbox with the consent text: where it goes, that it is public, how to stop), `receive.mode`, `receive.radiusNm`. Everything else is a constant.
- **Package**: TypeScript + vitest like `signalk-tides`; deps `ws`, `@signalk/nmea0183-signalk`; dev `@signalk/server-api`, `typescript`, `vitest`, `@types/ws`. `engines.node >= 20`. Keywords `signalk-node-server-plugin`, `signalk-category-ais`; `signalk-plugin-enabled-by-default` unset. Files: `src/index.ts` (plugin, schema, status), `src/identity.ts` (keys, sign, verify), `src/uplink.ts` (collect, queue, socket, backoff), `src/downlink.ts` (bbox, subscribe, inject), `test/` with a fake hub (`ws` server) and a fake `app`. Released to npm by [`.github/workflows/release.yml`](.github/workflows/release.yml) via trusted publishing (OIDC, no token) when a GitHub release is created with a `signalk-plugin-v*` tag; the package on npmjs.com needs the repo and workflow file name registered as its trusted publisher once.

#### Hub work for this stage

- `hub/identity.go`: Ed25519 key file, `hello` frame, signed-publish verification, `ack`, station/source `key:<pubkey>`, trust tier on the station, per-key publish limits.
- Publish path: whole-frame reception archived with the signature; sentences with `c:` older than 60 s archived only.
- `!AIVDO` already decodes through the NMEA path (go-nmea `VDMVDO` covers both talkers); own-ship receptions are attributed like any other.
- `/v1/stream` already bbox-routes positionless static messages through the vessel cache's last position, so names reach the plugin without further hub work.

#### Checklist

Hub: [ ] hub keypair + `hello` [ ] signed publish + `ack` + per-key limits [ ] frame archived with signature [ ] replay rule (`c:` > 60 s → archive only) [ ] tests: good/bad signature, skewed `time`, replay frame not emitted, echo carries `station == key`.

Plugin: [ ] identity + status + `/identity` route [ ] collect + TAG block + live publish + disk queue + ack [ ] backoff, watchdog, pinned hub key [ ] consume: modes, bbox follow, parser injection, drop rules [ ] loop guards [ ] tests: signature verifies with `node:crypto`; offline → files → drain on reconnect → deleted on ack; `auto` flips on/off with local AIS; injected delta has the expected context/`$source`/timestamp and own MMSI and own echoes are dropped; hub-received payload is never republished.

Verify end to end: Signal K with `--sample-nmea0183-data` + local hub → receptions attributed to the plugin's key in the viewer and the archive; stop the sample data → plugin flips to consumer mode and Kystverket targets appear under `vessels.urn:mrn:imo:mmsi:*` with `$source` `signalk-aiscast.net`; pull the network for a minute → queue files appear, drain on reconnect, land in the archive and stay off the live map.

Deferred, with the trigger that brings each back: plain-UDP mode (a user on a link where WS overhead matters; so far nobody); per-vessel downsampling for metered links (`ais-forwarder` #12 shows the demand; needs MMSI extraction on the boat); more than one hub (federation, Stage 4); privacy dials for own ship beyond on/off (delayed, coarse, encrypted to a group); filtering GPS-jammed far targets (hub-side, once station positions exist); `/v1/vessels` snapshot fill on subscribe (if waiting a few minutes for static data is a real complaint); boat-to-boat mesh sync.

### Stage 4: scale and federation

- Second node in another region, US node once US feeders exist (Hetzner US bundles only 1 TB, so price OVH Vint Hill/Hillsboro, Oracle, or Linode for it), per-region WS routing via Cloudflare. Nodes peer over `/v1/stream` by geographic cell; a replayable log (Redpanda/WarpStream tiered to R2) replaces an ad-hoc bus if stream and archive should become one object.
- Federation: publish the peer protocol and a bootstrap relay list; VHF corroboration as an attestation service; encrypted group events; local mesh sync in the plugin.
- Cached-tile read path: vessel state emitted as `/v1/tiles/{z}/{x}/{y}.json` every few seconds, `s-maxage=5` through Cloudflare, clients refetch viewport tiles; makes fan-out O(tiles) instead of O(clients) for viewport consumers. Lower priority than the stream paths.
- Commercial/satellite augmentation only as bring-your-own-key; no aggregator data relayed (ToS). The one unpriced lead for open-ocean coverage is Kinéis/Space AIS (raw NMEA over TCP, 3-min latency, terms unpublished); the GFW/ORBCOMM sublicense is the only precedent for a nonprofit publicly displaying satellite AIS (visualizations only, non-commercial, 90-day termination). Expect $150k–300k/yr for a global license that still forbids redistribution.

## Licensing and attribution

Licensing is per source, carried on every reception and surfaced on every output; the aggregate is not relicensed.

| Source | License | Obligations | Can we re-serve? |
|---|---|---|---|
| Kystverket | NLOD 2.0 | attribution; non-sublicensable, so downstream users are bound by NLOD directly | yes, under NLOD |
| BarentsWatch | NLOD | "Data delivered by BarentsWatch"; high-traffic use may be charged, clear in writing | yes, under NLOD |
| Digitraffic | CC BY 4.0 | `Source: Fintraffic / digitraffic.fi, license CC 4.0 BY` | yes, under CC BY |
| EuRIS | open data with attribution | literal string `API/Service [name] incorporated from EuRIS (eurisportal.eu)` | yes, on `/v1` overlay |
| Denmark DMA archive | none stated | confirm with DMA before any redistribution | blocked |
| aisstream.io | no terms exist | ask before relying on it | unclear |
| Volunteer stations | our feeder agreement | per agreement | ODbL for the contributed aggregate and CC0 for contributed history are the proposal, only where the feeder agreement explicitly grants it |

The feeder agreement (plain language: non-exclusive license limited to running the commons, not transferable to an acquirer, opt-in, station location never published precisely without consent) and the governance line (the project cannot be sold or unilaterally taken over) need legal review before Stage 1. Never relay paid aggregator data.

Reception and redistribution of AIS is lawful where it matters (US 47 USC §605(a) exempts broadcasts "for the use of the general public, which relates to ships"; Germany § 5 TDDDG; UK grey on text, settled in practice). IMO MSC 79's 2004 condemnation is non-binding and universally ignored.

## Sustainability

Goal: cover infrastructure (Hetzner box plus a growing R2 archive, on the order of $50–150/mo in year one) and make the time feel worthwhile, not build a company. Model: **open data, paid access.**

- **The data is never relicensed.** NLOD forbids added terms, CC BY 4.0 forbids downstream restrictions, and ODbL on the volunteer aggregate permits commercial use by anyone. A non-commercial restriction on the data is unenforceable and breaks the feeder pitch. The permissive license is also the differentiator: no Kpler/S&P API allows redistribution.
- **What is sold is the hosted service:** reliable bbox fan-out, history queries, SLA, and permission to use *this service* commercially. Precedents: Open-Meteo (open weather data, free API for non-commercial use, paid commercial plans), OpenSky Network (community ADS-B, free for non-commercial research, commercial license), OSM hosted by Mapbox/MapTiler.
- **Tiers are metered on cost axes, not on judging intent:** bbox area, concurrent connections per key, update rate, history depth. A plotter needs 50 nm around the boat; a fleet tracker needs an ocean. The free tier carries a plain terms line ("free for non-commercial use; commercial use needs a paid key"); the technical limits do the enforcing.
- **Feeders get the commercial tier free** (AISHub and FlightAware reciprocity). Growing the network is the coverage problem anyway.
- **History is the natural paid product:** live stream at the free tier, `/v1/history`, tracks, and bulk queries paid. The raw reception archive on R2 can still be a public bucket (no egress fees); the query API is what costs money to run.
- **The feeder agreement states the funding terms plainly:** the aggregate is open-licensed, the project charges for hosted access to fund operations, the feeder's license to the project is non-transferable to an acquirer. The ADS-B Exchange sale and feeder exodus is the failure mode this guards against.
- **No global tier until coverage is global.** Nordic plus a few volunteers priced as global is a refund request.
- **Sequence:** free tier with code-enforced limits, a terms line, a contact address for commercial keys, and a sponsors link now; keys hand-issued and invoiced manually until someone actually asks to pay; a merchant of record (Lemon Squeezy or Paddle, for EU VAT) issuing keys when they do; history tier and dedicated nodes later. Sole proprietor or LLC is fine at this scale; the open data and non-transferable feeder license protect the commons, not the entity type. NLnet NGI Zero is a plausible grant for the federation work.

Open: whether the deduped global raw feed (`/v1/nmea`) is free for everyone or free for feeders and non-commercial use with commercial users paying. If free for all, the paid product is decoded stream, history, and SLA only. Until decided, `/v1/nmea` is reciprocity-only.

## Privacy, before volunteer data

Decided before Stage 1, not after:

- Vessel opt-out: documented request path, suppression list applied at fan-out and in history queries; precedent is Norway's open feed excluding fishing <15 m and leisure <45 m, which we treat as a policy dial with a stated default (publish all, honor opt-outs).
- Retention: reception archive retained indefinitely for open-licensed sources; volunteer-contributed receptions per the feeder agreement; deletion procedure for opt-out vessels in history.
- Station locations: coarse by default, precise only on opt-in.
- Own-ship `!AIVDO` from Signal K: off by default, separate consent.
- Abuse response: per-key revocation, per-IP limits, and a contact address.
- GDPR basis for Class B small-craft data is documented (legitimate interest, broadcast data) and reviewed with the feeder agreement.

## Chart plugin implications

The Open Waters chart plugin speaks bbox-subscribe over WebSocket regardless of who serves it; its core seams (`VIEW_CHANGED` event, `net` provenance flag) are provider-agnostic, so pointing it at this hub needs no core work. The bundled aisstream key moves server-side (aisstream throttles per key). The `net.ws` manifest allowlist holds up to 8 hosts, so the hub hostname and a backup can be pre-listed to avoid re-consent later.

## Open questions

- Service and repo name. Hostname: `ais.openwaters.io` for the hub (a host-only change keeps aisstream clients working; a path under `api.openwaters.io` would not) and `ingest.openwaters.io` DNS-only for UDP, pending confirmation.
- Whether EuRIS's anonymised positions are useful to the chart client.
- Whether aisstream.io's terms allow using it as an upstream during bootstrap.
- Event envelope: adopt Nostr NIP-01 semantics (ids, keys, signatures, tag filters, `g` geohash) so off-the-shelf libraries and relays interoperate, or a minimal envelope of our own. Lean: NIP-01-compatible envelope and subscription semantics, our own Go relay with bbox filters and the aisstream view.
- Legal review of the feeder agreement, per-source licensing, privacy policy, and free-tier terms before Stage 1.
- Access terms for `/v1/nmea` beyond feeders (see Sustainability).
