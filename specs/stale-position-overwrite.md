# Stale positions overwrite fresher ones

A position that reaches the server minutes after its report time replaces a fresher position for the same vessel. This happens in the vessel cache, the `/v1/vessels` snapshot, and the viewer. AISHub does this today, and any replayed or slow source can do it tomorrow. The vessel visibly jumps backwards along its track, then forward again on the next live report.

## Evidence

We measured this on 2026-08-21 from six minutes of `/v1/stream` per region. The count covers position events whose `time` was older than the newest already delivered for that MMSI:

| Region | Stale source | Overwrote | Times | Moved back (median / max) |
|---|---|---|---|---|
| Cape Cod | aishub | `v1:mmsi:368168720/n2k` | 16 | 38 s / 180 s |
| Lysekil | aishub | `v1:ed25519:GzNIQN…` | 27 | 42 s / 182 s |
| Oslofjord | aishub | kystverket | 66 | 105 s / 238 s |

AISHub rows arrive 72–89 s after their own `TIME` at the median, with p99 around 9 minutes (archive hours 10–11 UTC). Local feeders deliver in 80 ms, and Kystverket/Digitraffic in 0.5–1.5 s. Wherever a local station also hears an AISHub vessel, the AISHub copy is the stale one almost every time.

## Mechanism

- `updateVessel` ([server/vessels.go](../server/vessels.go)) folds every event into the per-MMSI cache unconditionally. It sets `Seen` to `ev.Time` even when `ev.Time` is earlier than the current `Seen`. The `/v1/vessels` snapshot and the on-disk snapshot inherit the regression.
- The viewer's `onEvent` ([viewer/index.html](../viewer/index.html)) does the same: `v.lat/v.lon` and `seen` come from whichever event arrived last.
- Dedupe misses these rows. AISHub re-encodes them from downsampled positions, so the payload never matches the VHF reception of the same report.
- Canonical time is correct on every event (`ingestPacketAt` keeps the row time), so consumers that order by `time` stay correct. Only "last arrival wins" consumers regress.

## Change

Server, in `updateVessel`: when the cache already holds a position and `ev.Time` is before `v.Seen`, skip the position and motion fields. Those fields are lat, lon, cog, sog, heading, and nav status. Leave `Seen`, `Source`, `Station`, and `MsgType` unchanged. Still fold static fields (name, ship type, kind), since a late static report is still correct.

Server, in `emit`: a stale event never reaches subscribers or the AISHub feeder. It is archived, its static fields fold into the cache as above, and it counts in `aiscast_stale_total`. Each vessel's stream is monotonic in `time`. Snapshot replays are exempt: a subscription's initial replay delivers the retained last position and last static, and the static may be older.

Viewer, in `onEvent`: the same rule, keyed on `Date.parse(ev.time) < v.seen`, guarding against out-of-order snapshot replays and the `/v1/vessels` fallback. Static fields still apply.

All rules tolerate equal times and times within 1 s (Kystverket, Digitraffic and AISHub stamp whole seconds).

## Checks

- Unit test: two position events for one MMSI, the second with an older `Time` and a different position. The cache keeps the first position and `Seen`, and it takes a name from the second event if the second event carries one.
- Unit test: the same pair through `emit` with a subscriber. The stale event is not delivered and `aiscast_stale_total` increments.
- Live: rerun the probe above for a region with both AISHub and a local feeder. The backwards-jump count against `/v1/vessels` is zero (poll it each second, compare per-MMSI `seen`), and `/v1/stream` never delivers an event older than the newest already delivered for that MMSI, beyond the 1 s tolerance.
