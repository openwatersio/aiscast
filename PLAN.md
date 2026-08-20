# AIS service: scope and plan

A self-hosted replacement for aisstream.io: ingest AIS from open feeds and volunteer receivers, keep the full history, and stream live traffic to clients by bounding box at scale. Research behind every claim here is in [research/](research/).

## What the research settled

- **No drop-in alternative exists.** aisstream.io is the only free global bbox-subscribe push feed, it is a solo operation with repeated cert/outage incidents (down ~14 of the last 20 days), and there is no open-source equivalent of its server. Competitors are paid, regional, or hardware-gated. ([open-feeds.md](research/open-feeds.md), [protocol-and-building-blocks.md](research/protocol-and-building-blocks.md))
- **Open-licensed live feeds we may re-serve:** Norway Kystverket (raw tagged AIVDM over TCP, NLOD, ~38 msg/s), Finland Digitraffic (decoded JSON over MQTT/WSS, CC BY 4.0, ~43 msg/s), EuRIS inland Europe (REST bbox polling, anonymised positions with `mmsi: 0`, attribution string). Denmark's daily archive has no explicit license. Everything else is closed, non-commercial, or reciprocity-gated. US, Med, Caribbean, Asia, Pacific: no public feed anywhere; coverage there only comes from a volunteer receiver network.
- **Wire compatibility is tractable.** aisstream's JSON is Go `encoding/json` over `BertoldVdb/go-ais` structs (the `VenderIDModel` typo proves it), so go-ais gives struct-level compatibility for free; the envelope (cached name/position, rounding, time format, errors, filters, resubscribe) still has to be matched and proven by golden tests. Verified today: go-ais fast codec decodes the live Kystverket feed with 0 errors and parses its TAG blocks.
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
| Host | Hetzner CCX23-class (4 dedicated vCPU, 16 GB, 20 TB/mo EU traffic included; verify current price and overage) for anything public; Fly.io only for the local/beta slice | origin egress decides it: Fly bills $0.02/GB on every byte sent to Cloudflare, Hetzner includes 20 TB |
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
| Single-node fan-out capacity | 250k sends/s feasible with batching | extrapolated, not measured |
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

### Stage 0B, this week: public Nordic beta

- Digitraffic MQTT adapter (`Digitraffic-User` header, gzip) mapped to structs with `synthesized: true`; originals archived.
- Reconnect with backoff on every upstream; `/health` fails when any upstream is silent >2 min; `/metrics`; uptime monitor on the WS endpoint.
- Bounded client queues with drop accounting; payload/line limits on all inputs; per-IP and per-key rate limits on ingest and WS.
- Vessel-state snapshot/restore; `/v1/vessels` bbox snapshot.
- Tests against GPSD `sample.aivdm` and libais `tagblock.nmea`; load test with synthetic clients (AisVirtualNet or a Go generator) as the deployment gate.
- Deploy on Hetzner with systemd, Cloudflare DNS (`ais.<domain>` proxied, `ingest.<domain>` unproxied), Origin CA cert.
- aisstream.io as an upstream behind a flag for coverage while it lasts (pending the terms question); EuRIS overlay on `/v1` if anonymised positions are useful to the chart client.

### Stage 1: volunteer ingest

Blocked until the feeder agreement, per-source licensing, privacy policy, station registry, and deletion/opt-out procedures are settled (see below).

- Station registry issuing a key (and a UDP port for legacy setups); one-page setup (`AIS-catcher -H https://ais.<domain>/ingest USERPWD id:key GZIP on INTERVAL 15`, or `-u ingest.<domain> <port>`); per-station stats page, coverage map, offline alerts; opt-in only, stated plainly.
- PR to `sdr-enthusiasts/docker-shipfeeder` so existing multi-feed stations add us with one env var.
- Raw feed back to everyone: `/v1/nmea` WebSocket/TCP of deduped `!AIVDM` with TAG blocks, bbox-filterable, each sentence carrying its source's license tag.

### Stage 2: history and reporting APIs

- Normalized events to Parquet/Iceberg in R2 Data Catalog (nightly job first, streaming later); verify R2 SQL and DuckDB queries.
- `GET /v1/vessels/{mmsi}` last known; `GET /v1/vessels/{mmsi}/track?from&to`; `GET /v1/history?bbox&from&to`; station stats; coverage heatmap tiles. Served by a Worker over R2 SQL / Iceberg with caching; hot recent window from the hub's memory.
- Denmark backfill only after written confirmation of reuse rights from DMA (`sifa@brs2.dk`).

### Stage 3: Signal K plugin

- `signalk-<name>-ais` (working name): the vessel agent. Generates and stores a keypair, relays `!AIVDM` (targets) and `!AIVDO` (own ship) verbatim from `app.on('nmea0183')` and `app.on('nmea0183out')` (the latter is where N2K-derived AIS appears) to our ingest as signed events, batched, gzip, disk-buffered when offline, randomized schedule; optional plain-UDP mode. Own-ship `!AIVDO` sharing is off by default and separately consented. Optionally subscribes to our stream for traffic beyond VHF range and injects it as SK deltas with source `net` (model on `signalk-aisstream`). Pitfalls already hit by others: avoid `app.streambundle` (baconjs stall), reject Null Island, use AIS sentinels not zeros. Keywords `signalk-node-server-plugin`, `signalk-category-ais`.

### Stage 4: scale and federation

- Second node in another region, US node once US feeders exist, per-region WS routing via Cloudflare. Nodes peer over `/v1/stream` by geographic cell; a replayable log (Redpanda/WarpStream tiered to R2) replaces an ad-hoc bus if stream and archive should become one object.
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

- Service and repo name, and the public hostname.
- Hetzner account setup and current CCX23 price/overage.
- Whether EuRIS's anonymised positions are useful to the chart client.
- Whether aisstream.io's terms allow using it as an upstream during bootstrap.
- Event envelope: adopt Nostr NIP-01 semantics (ids, keys, signatures, tag filters, `g` geohash) so off-the-shelf libraries and relays interoperate, or a minimal envelope of our own. Lean: NIP-01-compatible envelope and subscription semantics, our own Go relay with bbox filters and the aisstream view.
- Legal review of the feeder agreement, per-source licensing, and privacy policy before Stage 1.
