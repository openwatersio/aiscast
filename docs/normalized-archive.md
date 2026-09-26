# Normalized archive

The server decodes and deduplicates every reception as it arrives, and that code is the only definition of how. The normalized archive records what the server decided: every accepted message, every copy of it heard, and every weather broadcast. Everything downstream packages this stream. Nothing downstream decodes raw data or re-derives dedupe, so nothing downstream can disagree with the server.

```
sources ──> pipeline: reassemble, decode, dedupe ──┬──> fan-out to subscribers
                                                   ├──> raw archive, source-native
                                                   └──> normalized archive: events, copies, weather
                                                              │  nightly packager (no decoders, no dedupe rule)
                                                              ▼
                                                        Iceberg tables in R2 Data Catalog
```

The raw archive is the input log and the normalized archive is derived from it. `aiscast replay` regenerates the normalized stream from raw, so raw is always enough to rebuild history, and a bug in an adapter can be fixed and replayed.

## Layout

One merged stream holds every source. Each hour is one gzip file of line-delimited JSON at `normalized/v1/YYYY/MM/DD/HH.gz` in the `ais-archive` bucket, beside the raw archive. The server stages it locally under `NORMALIZED_DIR`, which mirrors the bucket's layout, and uploads it through the same rotation, shutdown flush, and disk sweep as raw. `NORMALIZED_BUCKET` names the bucket; unset, the stream stays local.

A merged stream is never idle, so every hour rotates and uploads on time. Per-source files would leave a quiet source's hour open indefinitely.

The stream shares raw's bucket and access class because it is a pure function of raw and public code. Every raw key starts with a license tag, so the `normalized/` prefix never reads as a source, and replay skips it. The derived tables live in their own bucket, `ais-lake`, because R2 access control is per bucket and they are the layer most likely to open up.

## Envelopes

Every line is a versioned envelope:

```json
{"k": "event", "v": 1, "t": "2026-09-01T12:00:00.123Z", "implausible": true, "r": { ... }}
```

- `k` is the record kind: `event`, `copy`, or `methyd`.
- `v` is the schema version. A reader that meets a version it does not know must fail, never skip.
- `t` is the server's receive time.
- `implausible` and `stale` flag events the server archived but withheld from the stream. They live on the envelope so they never leak into the public event type.
- `r` is the record.

The envelope keeps the archive's schema independent of the public stream's. A change to the stream API is a new envelope version here, not silent drift.

### Events

One record per message the pipeline accepts. The record is the `/v1` event the stream sends subscribers: `id`, canonical `time`, `source`, `station`, `channel`, `mmsi`, `msg_type`, `lat`, `lon`, the `nmea` sentences, the decoded `message`, and `synthesized`. The archive is the stream, persisted. There is no second opinion about what a field means.

`source`, `station`, `license`, `attribution`, and the envelope's `t` all describe the first copy to arrive.

### Copies

One record per copy heard, the accepted first copy included:

| Field | Meaning |
|---|---|
| `id` | The event's id |
| `time` | This copy's own canonical time |
| `tx` | Canonical time of the accepted transmission this copy belongs to |
| `source`, `station` | Who delivered it |
| `license` | The license of this delivery, from the server's `licenseOf` |

Receive time is the envelope's `t`. A copy joins its transmission on `id` and `tx`. Proximity is not enough: ids repeat when identical payloads are sent minutes apart, and two transmissions of one id can both sit within the dedupe window of one copy.

Every copy carries a license because a license governs a delivery, not the broadcast it carried. A message heard by three sources is available under each of the three terms independently. The event record carries no license of its own. A CC0-only subset is the set of messages with at least one CC0-1.0 copy.

### Weather

BarentsWatch MetHyd broadcasts, verbatim with their own field names: decoded sea state, wind, air, and water level from AIS weather stations. They are not vessel traffic and never pass through `ais.Packet`.

## Identity and restarts

`id` is the content hash the server computes for dedupe, so replay reproduces it. Two transmissions with identical payloads minutes apart share an `id` and differ by time. An event record is a transmission by the server's own rule, so downstream never re-derives the 10 s window.

The dedupe window survives clean restarts. The server saves it to `DEDUPE` on shutdown and restores it on start, so a deploy cannot re-accept a copy inside the window. A crash skips that, so the packager also folds a same-`id` event inside the window of the last kept one into that row.

Multipart reassembly buffers do not survive a restart. A message whose fragments straddle one is lost from the live stream, but both fragments are in raw, so replay recovers it.

## Write guarantees

Raw and normalized must hold the same receptions, or replay cannot regenerate the stream. Several rules keep them in step:

- Both writers block when their queue is full instead of dropping. The writer only touches local disk, and uploads run beside it, so a full queue means the disk has stalled. Ingest waits.
- A write, open, or flush the disk refuses stops the process. The stream goes down, `/health` reports it, and systemd restarts the server once the disk recovers. The next sweep uploads files left open.
- Shutdown turns away new receptions, waits for the ones in flight, and then drains both writers. No reception is recorded in one archive and not the other.
- An event and its first copy are one queue item, so a drain never splits them.
- A raw record from a sender's offline backlog carries ` buffered` after its station id. Station ids never contain a space. Live withholds a stale backlog from the stream, and replay reads the mark and withholds it the same way.

## Capture completeness

The stream is only as complete as the adapters, so completeness is checked, not assumed.

Live, each adapter shadow-decodes a sample of records into a generic map beside its struct. Any field or record type outside the adapter's capture set and the waiver ledger is counted in `/metrics`, so upstream schema drift shows up within hours.

In CI, the fixture corpus in `server/testdata/sources/`, sampled from the real archive, runs through the same check with sampling off. Every field present must be captured or named in the waiver ledger in `server/coverage.go`. Declining a field is a reviewed decision. The check catches a missing field, not a wrong value; replay exists to repair that class.

## Replay

A raw line is a serialized adapter input: receive time, station, and the body exactly as the adapter consumed it. Replay reads each source's raw files for a range, merges them by receive time with ties broken by source name, and feeds them to the same adapters through the same pipeline. The normalized writer is its only output. The adapters that ran in production are the adapters that rebuild history.

Replayed output is trustworthy for these reasons:

- The normalized path takes every time from the reception, never the wall clock. The dedupe map prunes on an event-time high-water mark. A test replays one day at two wall times and requires identical output.
- Replay runs with fan-out, the vessel snapshot, and stats disconnected.
- A range starts with a warm-up lead-in, 30 minutes by default, that rebuilds dedupe, AISHub, trust, and multipart state and is not written.
- Replay stops on anything it cannot read: an unreadable directory, a truncated hour, a line with no record header, a header with no station. History never comes out shorter than the archive.
- Replay refuses a non-empty output tree, because hour files open for append.

Two differences between live and replay are expected. Live goroutines interleave near-simultaneous copies in an order replay cannot reproduce, so the two can disagree about which source's copy arrived first. Re-encoded multipart sentences carry a sequence id from a per-process counter. Neither is data.

## Verifying replay

`aiscast normdiff` compares a live tree against a replay of the same raw hours. It exits non-zero on any real divergence:

- a transmission, copy, or weather record present on one side only, counted per occurrence
- an event whose decoded message, sentences, type, MMSI, channel, position, `synthesized`, or withholding flags differ
- a copy whose transmission, source, station, license, or receive time differs
- a weather record whose content or receive time differs

It reports first-copy races and resequenced multipart ids as expected and compares the rest of each sentence. It refuses records it cannot read.

## Packager

The nightly [packager](../packager/README.md) turns closed days of the stream into Iceberg tables: `ais.positions`, `ais.receptions`, `ais.vessels`, and `ais.weather`. It contains no parsers, no license map, and no dedupe rule.

A row belongs to the UTC day the server received it, not the day of its transmission. Satellite relays and aggregate snapshots arrive hours late, BarentsWatch up to about ten, so a day keyed on transmission time could never be complete when written. Keyed on arrival, a day is exactly its own hours of the stream. A copy that arrives just after midnight joins its transmission in the previous day's partition.

Each day commits vessels, weather, and receptions, then positions last. Iceberg has no cross-table transaction, so the positions commit is the completion marker. It records a fingerprint of the day's input hours and their sizes. Each night the packager re-lists the past seven days and repackages any day whose inputs changed, so an upload that landed late is picked up. A day that fails is reported, and the rest of the week still runs.

The packager fails a day rather than write a short one. An unknown envelope version fails, and so does a position without a reception, since every transmission has a first copy.

## Operations

Replay a range from a local copy of the raw archive:

```sh
aiscast replay -archive <raw tree> -out <empty dir> -from 2026-09-01 -to 2026-09-02
```

Check replay against what the server wrote live for the same hours:

```sh
aiscast normdiff -live <normalized tree> -replay <replay output>
```

A clean run means the only differences left are first-copy attribution and multipart sequence ids.

Backfill replays the raw archive from its beginning into the normalized layout, then the packager builds the tables from the normalized layer alone. Raw keeps being written indefinitely. It is the replay input and the way back from a wrong value that capture checks cannot see.
