# Infrastructure evaluation

Limits and prices fetched from vendor docs 2026-08-20. Workload: 2k–10k msg/s ingest (upstream feeds we connect out to + volunteer stations pushing in), dedupe/decode, bbox-filtered WebSocket fan-out to thousands of clients, per-vessel latest state (~100k), archive every message (300–800M rows/day), later reporting APIs.

## The three findings that decide this

1. **Workers cannot accept inbound UDP, and cannot accept inbound TCP yet.** TCP Sockets doc: "Support for handling inbound TCP connections is coming soon" ([tcp-sockets](https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/)). Containers: "end-users cannot make non-HTTP TCP or UDP requests to a Container instance" ([platform details](https://developers.cloudflare.com/containers/platform-details/)). Spectrum UDP is Enterprise + paid add-on only ([protocols per plan](https://developers.cloudflare.com/spectrum/protocols-per-plan/)). Legacy UDP NMEA from volunteer stations cannot terminate on Cloudflare at any plan we'd buy.
2. **Cloudflare does not bill bandwidth for Worker/DO responses.** At 1,000 clients × 50 msg/s × 500 B we egress 64.8 TB/month: Cloudflare does not bill proxy bandwidth, but the origin still sends every byte to Cloudflare, so the host's egress price applies: $1,296/mo on Fly.io at $0.02/GB, inside Hetzner's included 20 TB (EU) with ~5:1 `permessage-deflate`. Strongest argument against Fly.io as the origin for production.
3. **Per-message pricing kills naive Cloudflare designs.** DO SQLite rows written are $1.00/million: 3,000 rows/s = 7.78B rows/month = $7,776/mo for the vessel-state table alone. Anything on Cloudflare must batch aggressively and keep hot state in memory.

## Comparison

| Design | Ingest UDP? | Fan-out capacity | Archive | Monthly cost @ target | Ops burden | SPOFs |
|---|---|---|---|---|---|---|
| A. Cloudflare-native | No (no inbound UDP/TCP; Spectrum UDP = Enterprise) | High but must shard: 32,768 WS/DO hard cap, ~500–1,000 clients/DO practical at 50 msg/s | Pipelines → R2 Iceberg, R2 SQL | ~$710 (~$380 with compact ~125 B records) | Low infra, high design complexity | DO shard hotspots |
| B. Single Go binary on Hetzner | Yes | One 4 vCPU box ≫ target; 10k msg/s in + 250k sends/s out with batching | Parquet → R2 (Data Catalog) or self-hosted ClickHouse | ~$130–180 | Medium | One process, one box (until 2-node) |
| B'. Same on Fly.io | Yes (dedicated IPv4) | Same | Same | ~$520, egress-dominated | Medium | Yes, mitigable |
| C. Hybrid: tiny UDP relay + Cloudflare-native | Yes via 1 tiny VM | As A | As A | ~$730 | Low-medium | UDP path only |
| D1. Ably / Pusher fan-out | No | Capped far below need | n/a | $65k–324k (Ably); Pusher 48× largest plan | Lowest | Vendor |
| D2. Upstash Redis | No | Not a fan-out tier | n/a | $15,552 PAYG at 3k msg/s | Low | Vendor |
| D3. NATS JetStream on VMs | Yes (in front) | Millions msg/s/node | JetStream → object store | ~$50–150 (3-node) | Medium-high | None with 3-node RAFT |

## Design A: Cloudflare-native details

### Outbound feed connections from a Durable Object

Outbound TCP (`connect()` from `cloudflare:sockets`) and outbound WebSocket both work from a DO. Since 2026-06-19, "Active outbound TCP sockets and outbound WebSocket connections now prevent a Durable Object from being evicted while the connection is open… for a maximum of 15 minutes" ([changelog](https://developers.cloudflare.com/changelog/post/2026-06-19-outbound-connections-keep-dos-alive/)). Baseline eviction is 70–140 s idle ([lifecycle](https://developers.cloudflare.com/durable-objects/concepts/durable-object-lifecycle/)). A feed-connector DO needs a ~60 s keep-alive alarm (inferred pattern, not documented as keep-alive). `connect()` can't target Cloudflare IPs, port 25, private IPs; max 6 simultaneous outgoing connections per request ([DO limits](https://developers.cloudflare.com/durable-objects/platform/limits/)) → one DO per upstream feed.

### DO as fan-out hub: numbers

| Limit | Value | Source |
|---|---|---|
| Max WebSockets per DO | 32,768 (hibernation API), CPU/memory may limit further | [api/state](https://developers.cloudflare.com/durable-objects/api/state/) |
| Requests/sec per object | soft 1,000/s | [DO FAQ](https://developers.cloudflare.com/durable-objects/reference/faq/) |
| Memory | 128 MB | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| CPU per request | 30 s default, up to 5 min | [DO limits](https://developers.cloudflare.com/durable-objects/platform/limits/) |

Pricing ([DO pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/)): requests $0.15/M after 1M (incoming WS messages billed 20:1; outgoing WS messages free); duration $12.50/M GB-s after 400k GB-s, billed at 128 MB whether used or not; SQLite writes $1.00/M rows after 50M. One always-hot DO = 331,776 GB-s/month = $4.15/mo.

One DO cannot broadcast 3,000 msg/s to 5,000 clients: 250k `ws.send()`/s and ~1 Gbps from a single isolate. What works: batch many messages per frame (docs recommend it: [best-practices/websockets](https://developers.cloudflare.com/durable-objects/best-practices/websockets/)), shard by geocell at ~500–1,000 clients/DO, feed ingest into DOs as batched RPC (4 calls/s, not 3,000). Per-client DOs = 1,000 hot DOs = $4,150/mo duration; don't.

### Latest vessel state

DO SQLite at full rate: $7,776/mo. D1: single-threaded, ~1,000 q/s, 10 GB max ([D1 limits](https://developers.cloudflare.com/d1/platform/limits/)) — can't take 3k writes/s. Works: 100k-vessel map in DO memory (sharded 8–16 ways, ~10 MB total), checkpoint each shard as one blob every 10 s (4.1M writes/mo, inside free tier). Hyperdrive → external Postgres/Timescale is free on Workers Paid ([pricing](https://developers.cloudflare.com/hyperdrive/platform/pricing/)); ClickHouse not supported.

### Dedupe

One DO request per message = $1,167/mo; batched 100:1 = $11.70/mo. Must be in-memory `Map` with TTL inside sharded DOs.

### Archive: Pipelines → R2 Data Catalog → R2 SQL

Status Aug 2026: Pipelines open beta, R2 Data Catalog public beta, R2 SQL open beta with published pricing.

Pipelines limits ([limits](https://developers.cloudflare.com/pipelines/platform/limits/)): 5 MB/s max ingest per stream (≈10k records/s at 500 B), 5 MB/request, 20 streams/sinks/pipelines per account. 10k msg/s peak is exactly the single-stream cap → shard streams or request a raise. Pricing ([pricing](https://developers.cloudflare.com/pipelines/platform/pricing/)): ingress free; SQL transforms $0.04/GB after 50 GB; Iceberg sink $0.06/GB after 50 GB. Sink ([r2-data-catalog sink](https://developers.cloudflare.com/pipelines/sinks/available-sinks/r2-data-catalog/)): Parquet only, zstd, roll interval min 60 s, exactly-once.

R2 Data Catalog pricing ([pricing](https://developers.cloudflare.com/r2-data-catalog/platform/pricing/)): catalog ops $9/M after 1M; compaction $0.005/GB + $2/M objects.

R2 SQL: JOINs shipped 2026-05-14 ([changelog](https://developers.cloudflare.com/changelog/post/2026-05-14-joins-subqueries-multi-table-queries/)); aggregations/GROUP BY since 2025-12; no writes, no OFFSET, no UNNEST/PIVOT, heavy queries may be rejected ([limitations](https://developers.cloudflare.com/r2-sql/reference/limitations-best-practices/)). $2.50/TB scanned, 10 GB/mo free ([pricing](https://developers.cloudflare.com/r2-sql/platform/pricing/)). Catalog exposes standard Iceberg REST so DuckDB/Spark/Trino/ClickHouse/PyIceberg can query it too ([data-catalog](https://developers.cloudflare.com/r2/data-catalog/)). Sane archive for 500M rows/day, with the per-stream cap as the caveat.

### Queues, Analytics Engine

Queues ([limits](https://developers.cloudflare.com/queues/platform/limits/), [pricing](https://developers.cloudflare.com/queues/platform/pricing/)): 5,000 msg/s/queue, $0.40/M ops billed per 64 KB, 3 ops per message. Per-message: $9,336/mo; batched 100:1: $93/mo. Not for the archive path.

Analytics Engine ([limits](https://developers.cloudflare.com/analytics/analytics-engine/limits/), [pricing](https://developers.cloudflare.com/analytics/analytics-engine/pricing/)): 1 index, 3-month retention, sampling, 250 points/invocation, $0.25/M after 10M (currently not billed). Excellent for per-station/feed health metrics, useless as the message archive.

### Cost at 3k msg/s, 1,000 clients, 300M rows/day

| Line | Arithmetic | $/mo |
|---|---|---|
| Workers Paid | | 5.00 |
| Worker requests/CPU | 77.8M req, 233M CPU-ms | 24.40 |
| DO requests | ~200M batched | 29.85 |
| DO duration | 24 hot DOs × 331,776 GB-s | 94.51 |
| Pipelines transforms + Iceberg sink | 4,450 GB × ($0.04 + $0.06) | 445.00 |
| R2 storage 12-mo retention | 5,400 GB × $0.015 | 81.00 |
| Catalog compaction, R2 SQL | | 32.45 |
| Client egress 64.8 TB | not billed | 0.00 |
| Total | | ~$712 |

63% is Pipelines billed per byte; compact records (~125 B) bring the total to ~$380.

## Design B: single binary on a VPS

One 4 vCPU / 8–16 GB box: UDP receive ~1–2% of a core at 10k msg/s, decode 1–5%, dedupe <1%, vessel state 10 MB. Fan-out is the real cost: 1,000 clients × 50 msg/s = 50k sends/s = 200 Mbps, comfortable; 5,000 clients = 1 Gbps = 324 TB/mo, CPU-feasible with frame batching but bandwidth-infeasible on per-GB hosts. Real per-client rates are far below 50 msg/s because of bbox filtering.

Compute and egress by host (list prices checked 2026-08-20, on-demand, Linux, ex-VAT; 1 TB = 1024 GB; "65 TB" and "13 TB" are the 1,000-client fan-out case uncompressed and with ~5:1 deflate, instance plus egress):

| Host | Instance (4 vCPU class) | $/mo | Included egress | Overage | 65 TB/mo | 13 TB/mo | Caveats |
|---|---|---|---|---|---|---|---|
| Hetzner Cloud EU | CCX23 4 ded. / 16 GB | €101.49 (Cloud API, hel1, 2026-08-20) | 20 TB | €1/TB | €146 | €101 | EU locations only; Singapore overage €7.40/TB |
| Hetzner Cloud EU | CX43 8 shared / 16 GB | €18.49 (Cloud API, hel1) | 20 TB | €1/TB | €63 | €18 | what `ais-hub-1` runs on; CAX31 (ARM 8/16) €24.99, CCX13 (2 ded./8 GB) €50.49 |
| Hetzner Cloud US | CCX23 (ASH/HIL) | €102.99 | 2 TB | €1/TB | €166 | €114 | US/APAC bundle only 2 TB |
| OVHcloud Public Cloud | b3-16 4 / 16 GB | $82.64 | unmetered, 1 Gbps | n/a outside APAC | $83 | $83 | US (Vint Hill, Hillsboro), CA, EU |
| OVHcloud VPS | VPS-2 4 / 8 GB | $8.50 | unmetered, 1 Gbps guaranteed | n/a outside APAC | $8.50 | $8.50 | no 16 GB VPS; APAC quota'd then throttled |
| Scaleway | PRO2-XS 4 / 16 GB | €81.90 | included, 700 Mbps cap | n/a | €82 | €82 | EU only (PAR/AMS/WAW) |
| Oracle OCI | E5.Flex 2 OCPU / 16 GB | $67.16 | 10 TB free | $0.0085/GB | $546 | $93 | NA/EU/UK rates; free ARM tier exists |
| Akamai/Linode | G6 Linode 8 GB | $48 | 5 TB | $0.005/GB | $355 | $89 | G8 plans bundle 0 TB |
| Vultr | vhp-4c-8gb | $48 | 6 TB | $0.01/GB | $652 | $120 | |
| DigitalOcean | Basic 8 GiB | $48 | 5 TiB pooled | $0.01/GiB | $664 | $131 | no regional variation |
| Fly.io | shared-cpu-4x 8 GB | $42.79 | none | $0.02/GB NA/EU | $1,374 | $309 | fine for the beta slice |
| AWS | m7i.xlarge / Lightsail 16 GB | $147 / $84 | 100 GB / 6 TB | $0.09→$0.05/GB | $5,617 / $5,521 | $1,321 / $729 | |
| GCP | n2-standard-4 | $142 | ~0 | $0.12→$0.08/GiB (Std tier $0.085→$0.045) | $5,784 (Std $4,656) | $1,524 (Std $1,195) | |
| Azure | D4as_v5 | $126 | 100 GB | $0.087→$0.05/GB (Internet routing $0.08→$0.04) | $5,484 ($4,513) | $1,263 ($1,134) | |

Sources: Hetzner from the Cloud API `GET /v1/server_types` on 2026-08-20 (the web price feed reading of $50.49 for CCX23 was CCX13), [OVH VPS](https://us.ovhcloud.com/vps/) and [Public Cloud](https://www.ovhcloud.com/en/public-cloud/prices/), [Scaleway](https://www.scaleway.com/en/pricing/virtual-instances/), [Oracle](https://www.oracle.com/cloud/networking/pricing/), [Akamai](https://www.akamai.com/cloud/pricing/north-america), [Vultr](https://docs.vultr.com/support/platform/billing/what-is-the-bandwidth-overage-rate), [DigitalOcean](https://docs.digitalocean.com/platform/billing/bandwidth/), [Fly.io](https://fly.io/docs/about/pricing/), [AWS](https://aws.amazon.com/ec2/pricing/on-demand/) and [Lightsail](https://aws.amazon.com/lightsail/pricing/), [GCP](https://cloud.google.com/vpc/network-pricing), [Azure](https://azure.microsoft.com/en-us/pricing/details/bandwidth/).

Reading: the hyperscalers are 5–10× on egress and nothing in this workload needs them. The mid-tier VPS vendors (DO, Vultr, Linode) are fine for a beta and get uncomfortable past ~20 TB/mo. The cheap-bandwidth tier is Hetzner EU, OVH, Scaleway, and Oracle's 10 TB free. For a US node, Hetzner's 1 TB bundle makes OVH (Vint Hill/Hillsboro/Beauharnois), Oracle, or Linode the ones to price; OVH VPS-2 at $8.50 unmetered is the outlier worth testing for real sustained throughput before trusting it.

Archive: Parquet → R2 ($0.015/GB-mo, free egress, [r2/pricing](https://developers.cloudflare.com/r2/pricing/)) registered in R2 Data Catalog → queryable by R2 SQL and DuckDB/Spark/Trino. Recommended. ClickHouse Cloud needs 24/7 ingest → Scale tier ~$500/mo floor ([billing](https://clickhouse.com/docs/cloud/manage/billing)). Self-hosted ClickHouse on one extra node handles 300–800M rows/day easily (~20–30 B/row compressed) but adds ops. TimescaleDB not recommended at this rate.

Cloudflare in front: WebSocket proxying on all plans ([network/websockets](https://developers.cloudflare.com/network/websockets/)); idle timeout unpublished → app-level ping/pong; Argo Smart Routing incompatible with WS; origin read timeout 125 s, proxy idle 900 s ([connection limits](https://developers.cloudflare.com/fundamentals/reference/connection-limits/)). UDP cannot go through the proxy; volunteer UDP hits the origin IP directly. TLS via Origin CA cert or `cloudflared` Tunnel for HTTP/WS (Tunnel won't carry UDP).

HA: Fly.io 2 machines in 2 regions, share over 6PN private network or NATS; UDP on Fly needs dedicated IPv4, bind `fly-global-services`, same external/internal port, ~1300 B MTU ([udp-and-tcp](https://fly.io/docs/networking/udp-and-tcp/)). Hetzner: 2 boxes + floating IP or DNS RR. Dedupe split-brain is benign (worst case a duplicate reaches a client).

## Design C: hybrid

Tiny Fly/Hetzner machine (~150 lines of Go) binds UDP, batches, POSTs to a Worker or holds one outbound WSS to a router DO. Adds ~$18/mo over A. Removes A's one hard blocker but pays both the ops tax and Cloudflare's per-message design complexity.

## Design D: managed pub/sub

Outbound 50k msg/s = 129.6B msgs/month. Ably $2.50/M ($0.50/M floor) → $65k–324k/mo; Pro plan caps at 10k msg/s anyway ([pricing](https://ably.com/pricing)). Pusher largest listed plan is 90M msgs/day vs 4.32B needed ([pricing](https://pusher.com/channels/pricing/)). Upstash Redis $2/M commands → $15.5k/mo ([pricing](https://upstash.com/pricing)); Upstash Kafka appears retired. NATS JetStream self-hosted is the only sane managed-streaming shape, as an internal bus, not a replacement for the Go binary.

## Recommendation

Design B for the hot path, Design A's storage for the cold path:

1. One Go binary on Hetzner CCX23-class hardware: UDP + TCP + HTTP ingest, dedupe, decode, in-memory vessel state, bbox-filtered WS fan-out. ~5–10% utilized at target.
2. Cloudflare orange-cloud in front of the WS endpoint (TLS, DDoS; proxy bandwidth unbilled by Cloudflare, origin egress still paid to the host); app-level ping/pong; Argo off; `permessage-deflate` on.
3. Volunteer UDP hits the origin IP directly; rate-limit per source IP; accepted risk as every AIS aggregator has today.
4. Archive as Parquet directly to R2, registered in R2 Data Catalog; query with R2 SQL or DuckDB/Spark/Trino via Iceberg REST.
5. HA when it matters: second node in a second region, share messages over NATS or a private link.

Estimated ~$130–180/mo vs ~$712 (A) and ~$730 (C).

Verify before committing: Cloudflare WS proxy idle timeout (unpublished); Pipelines 5 MB/s per-stream cap if A/C; DO duration assumes always-hot fan-out DOs.

Extrapolated rather than cited: single-DO send throughput, Parquet compression ratio.
