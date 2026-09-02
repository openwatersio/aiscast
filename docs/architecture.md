# Architecture

aiscast is one Go process on one VM, with Cloudflare in front of clients and R2 behind for history.

```
  open feeds ──TCP/MQTT/REST──▶ ┌────────────────────────────────────┐
  volunteer stations ─UDP/HTTP─▶ │  aiscast (Go, one process)         │ ──WS──▶ clients
  peers ─────────/v1/stream────▶ │  receptions → reassemble → dedupe  │ ──HTTP▶ /v1/vessels, /health
                                │  → decode → events → vessel state  │
                                └────────────────────────────────────┘ ──hourly .gz──▶ R2
```

Read this before changing how data flows.

## Two records

A **reception** is immutable and exactly what arrived: source id, station key, our receive time, the source's own time if it had one, the verbatim body (tagged NMEA sentence, Digitraffic JSON, EuRIS track, AIS-catcher JSON), the validation result, and the license tag of the source. Receptions are the archive, the basis for coverage analysis and spoof detection, and what lets a better decoder re-read history later. aiscast writes them before dedupe, so the archive is sized on raw overlapping traffic.

An **event** is the normalized message: `id = hash(canonical payload)`, origin (the vessel key when the source is IP-native, otherwise the station key), canonical time, geo cell, reassembled payload, decoded message, the reception ids that contributed, a signature if one was present, and `synthesized: true` when the payload was re-encoded from non-NMEA input. Events drive dedupe, fan-out, vessel state, `/v0` rendering, and peering.

Canonical time comes from the source's own timestamp (TAG `c:`, Digitraffic `timestamp`) when it is within ±30 s of receive time, then a feeder-supplied time within skew, then our receive time. Both times are kept either way, and out-of-skew or malformed timestamps are counted per station.

## Choices

| Concern | Choice | Why |
| --- | --- | --- |
| Hot path | One process: per-source reassembly, dedupe on `(payload, channel)` over a 10 s window of canonical time, go-ais fast decode, in-memory vessel map, bbox fan-out with bounded per-client queues and drop accounting | Simplest thing that handles 10× the target rate. AisLib and AIS-catcher prove the patterns |
| Client protocol | aisstream.io-compatible WebSocket at `/v0/stream`, frozen. `/v1/stream` is a bidirectional event stream (publish and subscribe on one socket) carrying station, raw body, and receive time | Existing aisstream clients work unchanged, and `/v1` is also the peer protocol, so feeders, nodes, boats, and chart clients all speak one thing |
| Snapshot | `GET /v1/vessels?bbox=` returns current state | Without it a client waits minutes for a view to fill |
| Non-NMEA sources | Digitraffic JSON maps to go-ais structs and is re-encoded to AIVDM with `synthesized: true`. EuRIS would be a `/v1`-only overlay | `/v0` stays faithful to what a VHF receiver would hear. aiscast archives the originals as received |
| Feeder ingest | HTTP POST of AIS-catcher's `jsonaiscatcher` envelope with gzip and Basic auth, content-sniffing `LIST`/`NMEA` bodies. `/v1/stream` publish for peers. UDP NMEA as a legacy adapter | Matches what existing receivers already emit. HTTP and WebSocket work through any NAT, and UDP goes direct |
| Parsing tolerances | TAG blocks on every transport (`s`, `c`, `g`, `n`, `d`, `t`, `r`, any `s:` form, checksum mismatch warns and counts), `c:` unit heuristic (`.`→s, ≤12 digits→s, else ms/µs), channels `1`/`2`/`CD`, fixed-size messages up to 5 bits over length (the AISHub type-5 fill-bit bug), USCG trailing fields after the checksum, payload and line-length limits everywhere | Each strictness here silently drops a slice of real feeds. The limits keep a hostile feeder from exhausting memory |
| Archive | Every reception, source-native, hourly gzip per source to R2 with the license tag in the object path | Receptions are the lossless record, and R2 has no egress fee |
| Host | Hetzner Cloud EU (20 TB/mo included, €1/TB over): `ais-server-1`, a CX43 in Helsinki. "Hetzner-class" means any cheap-bandwidth VPS with a dedicated IP. Hyperscalers are 5–10× on egress | Origin egress decides it: every byte to Cloudflare is billed at most hosts and included here |
| Edge | Cloudflare in front for WebSocket and HTTP, app-level ping/pong for the proxy idle timeout, `permessage-deflate` on | Certificate operations become a non-event, and compression cuts origin egress about 5× |
| State on restart | Vessel map snapshot to disk every 10 s, reloaded on boot | Restart without a blank map |
| Identity | Devices generate Ed25519 keypairs. Authorization is a signed claims token (`ak1.<claims>.<sig>`) carrying `sub`, `role`, `exp`, and optional `bbox`, `cidr`, `conns` | An identity model that survives federation, with no accounts system |
| Metrics and health | Prometheus `/metrics` (per-source rate and last-message age, dedupe ratio, clients, queue drops, invalid timestamps per station). `/health` fails when a loopback subscriber has received nothing for 2 minutes | The aisstream failure mode was a healthy-looking empty service |

## Adding a source

Every source keeps its own `source` value on each event, its own license tag in the archive path, and its own environment flag. A source stays out of the health gate unless it is an open-licensed feed the project commits to. That is what makes a source purgeable: unset the flag, delete its archive prefix. Sources on unclear terms are only acceptable because of it.

## The `/v0` compatibility contract

`/v0/stream` is frozen to aisstream.io's wire format. An existing aisstream.io client must work against aiscast with only the hostname changed. That means the same subscribe message (`APIKey` in any casing, `BoundingBoxes` as `[lat, lon]` corner pairs in any corner order, `FiltersShipMMSI` up to 50, `FilterMessageTypes` rejecting duplicates), resend-to-replace semantics, a 3 s subscribe deadline, a 1/s update rate, the same error frames, and the same slow-client disconnect. Outbound it means the `MessageType` / `MetaData` / `Message` envelope with go-ais field names and their typos (`VenderIDModel`, `Fixtype`), a numeric `MMSI_String`, Go-format `time_utc`, an untrimmed `ShipName`, and `MetaData.latitude/longitude` filled from the last-position cache so static messages still reach bbox subscribers.

Golden tests hold the line: per-type fixtures built from captured aisstream output and the `ais-message-models` schema, subscription validation cases, static-message routing through the position cache, and slow-client behavior. Anything new goes under `/v1`, where additive changes are fine.

## One peer in a network

Long term aiscast is one peer in a network where vessels, shore receivers, nodes, and chart clients exchange AIS as signed events over the internet, alongside VHF. Three decisions keep that reachable without building it now:

1. `/v1/stream` is direction-agnostic. One WebSocket, publish and subscribe frames, bbox and cell subscriptions. Node-to-node peering is then just a client that also publishes.
2. The internal record is the event above: content-hash id, origin key, geo cell, contributing receptions, and room for a signature. Dedupe by id, provenance by signature chain. aiscast relays, and it signs only its own observations and attestations.
3. Identity is a key. The UDP port and the HTTP secret are adapters that map onto one.

Trust in that network is tiered and shown in the UI rather than assumed: self-reported (any key), corroborated (the key's internet reports matched independent VHF receptions, which a receiver network can attest), attested (a flag-state or MCP certificate, or an organization vouching). Privacy is consent-based, with the sender choosing public, delayed, coarse, or encrypted to a group. IP-sourced targets reach a plotter only through Signal K with the source marked `net`, never via VHF transmit, and are never presented as collision avoidance. The precedents worth copying are APRS-IS (federated filter relays), Nostr (signed events, dumb relays, tag filters, the `g` geohash tag), Secure Scuttlebutt (offline-first gossip), and IALA MCP/MRN (official maritime identity over IP).

One discipline carries into any peering: never forward `synthesized` or aggregate-sourced events to a peer without saying so. Receiver-only networks block relayed aggregates, and `source` provenance is what lets them take just what they want.

## Measurements

| Quantity | Value | Basis |
| --- | --- | --- |
| Unique global terrestrial rate | ~300 msg/s | aisstream's own measurement |
| Raw inbound including station overlap | 1,000–3,000 msg/s | Inferred from AISHub station counts |
| Tagged NMEA sentence | ~90–110 B raw, ~25–35 B gzipped | Measured on a Kystverket sample, compression inferred |
| Reception archive at 3k msg/s | ~25 GB/day raw, ~7 GB/day gzipped | Arithmetic on the above |
| aisstream JSON per message | ~450–500 B | Measured from samples |
| Client bbox hit rate | Unknown, 50 msg/s per client used for sizing | Assumption. World-bbox clients see ~300 msg/s |
| 1,000 clients × 50 msg/s × 500 B | 25 MB/s origin egress, 64.8 TB/mo uncompressed, ~13 TB/mo with deflate | Arithmetic, compression ratio inferred |
| Single-node fan-out capacity | 1,000 clients × 68 msg/s = 68k sends/s, 37.7 MB/s, zero queue drops, 57 MB RSS, ~12% of one core | Measured 2026-08-20 with `cmd/loadtest` on an M-series laptop, uncompressed frames |

Ingest is small and fan-out is the real load. One 4-vCPU box handles ingest at a few percent utilization. Fan-out scales with clients × bbox hit rate, and origin bandwidth binds first. Load testing with synthetic clients gates any capacity claim.

## Operational constraints

Kystverket allows one TCP connection per source IP. A second connection makes both reconnect every few seconds, so only one server per public IP may pull it.

Cloudflare cannot terminate the ingest side. Workers and Containers accept no inbound UDP or raw TCP. Spectrum UDP is Enterprise-only. Per-message pricing makes Durable-Object dedupe and fan-out expensive unless heavily batched. Cloudflare is good here for TLS and certificate operations, DDoS protection on the WebSocket path, unbilled proxy bandwidth, and R2 with Data Catalog and R2 SQL for the archive. The origin still sends every byte to Cloudflare, which is why origin egress pricing picks the host.

Deeper background on every claim here is in [research/](../research/).
