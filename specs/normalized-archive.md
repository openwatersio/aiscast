# The normalized archive

The server decodes and deduplicates every reception in real time, and that code is the only complete definition of how. Anything downstream that re-decodes raw data is a second implementation of the same semantics, and five review rounds on the lake showed what that costs: every divergence between the two is a bug, the class is inexhaustible, and review keeps finding members of it at a steady rate. This layer removes the class. The server writes what it already knows, once, and everything downstream packages rather than reinterprets.

```
sources ──> pipeline: reassemble, decode, dedupe ──┬──> fan-out to subscribers (unchanged)
                                                   ├──> raw archive, source-native (unchanged)
                                                   └──> normalized archive: accepted events + reception copies
                                                              │  nightly packager (no decoders, no dedup rule)
                                                              ▼
                                                        Iceberg tables in R2 Data Catalog
```

## What gets written

One merged stream: a single hourly gzip file of line-delimited records, all sources together, written at emit time through the same rotation and upload machinery the raw archive uses, so straggler reopening, shutdown flushing, and the disk sweep apply unchanged. Merged is load-bearing, not cosmetic: the aggregate stream never has an idle hour, so files rotate and upload promptly every hour and the quiet-source open-hour gap that plagued per-source timing cannot exist. Dedup's output is cross-source anyway, and terms questions are answered by the license field on each copy, not by path layout.

Every line is a versioned envelope: `{"k": <event|copy|methyd>, "v": <schema version>, ...flags, "r": <record>}`. The envelope is what makes the archive's schema independent of the public stream's: `v1Event` rides inside it for accepted events, but a stream API change is a new envelope version here, not a silent format drift, and the flags that non-emitted events need (`implausible`, `stale`) live on the envelope rather than leaking into the public type. Three record kinds:

**Accepted events.** One record per message the pipeline accepts: the `v1Event` JSON the stream already sends subscribers. That encoding exists, is documented, is exercised by every stream client, and carries everything the derived tables need: `id`, canonical `time`, `source`, `station`, `channel`, `mmsi`, `msg_type`, the raw sentences, and the fully decoded `message`. The archive is the stream, persisted; there is no second opinion about what a field means.

**Reception copies.** One small record per copy heard, the accepted first copy included: the event's `id`, `source`, `station`, receive time, and license. Uniformity matters twice over: the event record's `time` is canonical rather than receive time, so the first copy's actual receive time has to live somewhere for receive-time deltas, and a license is a fact about a delivery, so it belongs on every copy or on none. Today the hot path only counts duplicates; recording copies is the one behavioral addition, and it is what makes the receptions table (coverage, station health, multilateration) possible without re-deriving dedup downstream.

**Weather observations.** BarentsWatch's decoded MetHyd broadcasts, stored with their field names as-is. They are not vessel traffic and never touch `ais.Packet`; whether they ever become a live stream event type is a separate decision.

Events the pipeline archives but does not emit (`Implausible`, `Stale`) are written with their envelope flags set, as the raw layer already retains them; whether the derived tables include them is a packager decision, not an ingest one.

The stream lands in its own bucket, private: R2 access control is per-bucket, the raw and derived layers may become public, and this layer is a candidate for APIs or paid access later. Publishing is a one-way door, so it starts closed.

## Identity and idempotency

`id` is the content hash the server already computes, so it is stable under replay by construction. Two genuine transmissions with identical payloads minutes apart share an `id` and are distinguished by time; an accepted-event record *is* a transmission by the server's own rule, so the packager never re-derives the 10 s window.

Restarts get two layers of protection. The dedup map holds about a minute of keys, so the server saves it on shutdown and restores it on start, riding the snapshot mechanism that already persists the vessel cache; that closes the window for every clean restart, deploys included. A crash runs no shutdown path, so the packager also collapses same-`id` records closer than the window when it assembles a day: an idempotency guard for unclean exits, not a reimplementation of dedup. Beyond the window the question is moot either way, because a copy arriving 10 s or more after its twin's acceptance is a new transmission under the rule, restart or not.

Multipart reassembly buffers die with the process too. A message whose sentences straddle a restart is lost from the live stream, but both sentences are in the raw archive, so replay recovers it; that stays a raw-layer property, not a reason to persist parser state.

## What the packager is

The nightly job keeps the operational shell built for the lake and loses everything semantic: fetch the closed day's normalized hours from R2 with size verification, sort, write day-partitioned Parquet, commit receptions and vessels then messages last in atomic per-table transactions, reconcile output against input, self-heal missing recent days from partition metadata, delete the fetched copy. No parsers, no sentinels, no license map, no dedup rule; nothing left in it can drift from the server.

Licenses ride each reception copy at write time using the server's own `licenseOf`, and no license wins at the message level: a license governs a delivery, not the broadcast it carried, so a message heard by three sources is available under each of the three terms independently, and the message record carries none. Terms questions become filters over receptions rather than judgment calls; a CC0-only subset is the set of messages with at least one CC0-1.0 reception, and the blanket contract stays as it is: credit the attribution-requiring sources when publishing derived work.

## Capture completeness

Writing the normalized stream is only worth it if the adapters capture everything the sources send, and "everything" has to be enforced, not assumed. Two mechanisms. Live, each adapter shadow-decodes a sample of records into a generic map beside its struct and exports any unmapped field names and unrecognized record types as metrics, so upstream schema drift surfaces in hours. In CI, the adapters run over fixture files sampled from the real archive, and every field present is either captured or named in a waiver ledger checked into the repo; declining data is a reviewed decision, never an accident. Field checks catch presence, not meaning, so a captured value transformed wrongly is still possible; that class is what the raw log's replay exists to repair.

The known gaps close as part of landing the writer: BarentsWatch SAR altitude gets mapped, and its MetHyd weather broadcasts (a few hundred an hour of decoded sea state, wind, and water level) are archived as their own record kind rather than dropped; whether weather ever becomes a live stream event type is a later decision.

## Replay and backfill

A raw archive line is a serialized adapter input: receive time, station, and the source-native body exactly as the adapter consumed it live. Backfill is therefore replay, not reimplementation: read each source's raw files for a range, merge lines across sources by archived receive time, and feed them to the same adapters through the same pipeline, with the normalized writer as the only output. The adapters that ran in production are the adapters that backfill history.

Three properties make replayed output trustworthy, each enforced rather than hoped:

- The normalized path takes every timestamp from the reception, never the wall clock. A determinism test replays the same fixture day at two different wall times and requires byte-identical output; anything reaching for `time.Now()` in that path fails it.
- Replay runs the pipeline with fan-out, the vessel snapshot, and stats disconnected, so its only side effect is normalized files for the requested range.
- Cross-source order is a merge on archived receive times, ties broken by source name. Live goroutine interleaving of near-simultaneous copies is not reproducible, so a live day and its replay can disagree about which source's copy of a message arrived first; the message sets match, first-copy attribution may not, and the diff harness compares accordingly.

The lake branch's Python decoders do not ship, but they are the executable record of every divergence review found, and diffing replay output against them over real days is a second, independent check.

## Rollout

1. The server dual-writes: raw as today, plus the normalized stream at emit, with the capture-completeness mechanisms landing alongside. Nothing downstream changes yet.
2. Confidence: for days with both a live normalized record and raw, replay the raw and diff against what live wrote. Run until the only divergences are the known first-copy races.
3. Backfill: replay the raw archive from its beginning to populate normalized history, then the packager builds the derived tables from the normalized layer alone.
4. Raw keeps writing indefinitely as the write-only input log: the replay substrate, and the rewind buffer for the one failure class coverage checks cannot see. Nothing downstream reads it.

## Implementation care

- The copy record is born in the dedup branch, which holds the pipeline mutex: hand it to the async archive channel, never do I/O under the lock, and recheck the channel sizing since archive throughput roughly doubles.
- The dedup map's prune cutoff uses the wall clock today; it moves to an event-time high-water mark so the determinism test can hold.
- Replay of a sub-range starts stateful pieces cold (the dedup map, aishub's last-emitted tracking, trust state, multipart buffers), so replay takes a warm-up lead-in that is run and discarded. Trust-derived flags are excluded from strict diffs; they are timing-dependent even live.
- Volume: roughly the raw layer's line count again, about 18M accepted events and 20M copies a day, JSON-gzipped, 1-2 GB/day against free egress and the existing sweep. Raw plus normalized accretes storage cost slowly; raw is the candidate for the infrequent-access class if it matters.
