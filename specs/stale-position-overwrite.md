# Stale positions overwrite fresher ones

A position that reaches the server minutes after it was reported (AISHub today; any replayed or slow source tomorrow) replaces a fresher position for the same vessel in the vessel cache, the `/v1/vessels` snapshot, and the viewer. The vessel visibly jumps backwards along its track, then forward again on the next live report.

## Evidence

Measured on 2026-08-21 from six minutes of `/v1/stream` per region, counting position events whose `time` was older than the newest already delivered for that MMSI:

| Region | Stale source | Overwrote | Times | Moved back (median / max) |
|---|---|---|---|---|
| Cape Cod | aishub | `v1:mmsi:368168720/n2k` | 16 | 38 s / 180 s |
| Lysekil | aishub | `v1:ed25519:GzNIQN…` | 27 | 42 s / 182 s |
| Oslofjord | aishub | kystverket | 66 | 105 s / 238 s |

AISHub rows arrive 72–89 s after their own `TIME` at the median, p99 around 9 minutes (archive hours 10–11 UTC), while local feeders deliver in 80 ms and Kystverket/Digitraffic in 0.5–1.5 s. Wherever an AISHub vessel is also heard locally, the AISHub copy is the stale one almost every time.

## Mechanism

- `updateVessel` ([server/vessels.go](../server/vessels.go)) folds every event into the per-MMSI cache unconditionally; `Seen` is set to `ev.Time` even when that is earlier than the current `Seen`. The `/v1/vessels` snapshot and the on-disk snapshot inherit the regression.
- The viewer's `onEvent` ([viewer/index.html](../viewer/index.html)) does the same: `v.lat/v.lon` and `seen` come from whichever event arrived last.
- Dedupe does not help: AISHub rows are re-encoded from downsampled positions, so the payload never matches the VHF reception of the same report.
- Canonical time is right on every event (`ingestPacketAt` keeps the row time), so consumers that order by `time` are fine; only "last arrival wins" consumers regress.

## Change

Server, in `updateVessel`: when the cache already holds a position and `ev.Time` is before `v.Seen`, skip the position/motion fields (lat, lon, cog, sog, heading, nav status) and leave `Seen`, `Source`, `Station`, `MsgType` alone. Still fold static fields (name, ship type, kind), since a late static report is not wrong. Still stamp the event with the cache's name and its own position, so the event delivered to clients is unchanged.

Viewer, in `onEvent`: same rule, keyed on `Date.parse(ev.time) < v.seen`; static fields still apply. Skip the pulse ring for skipped positions.

Both rules tolerate equal times and times within 1 s (Kystverket, Digitraffic and AISHub stamp whole seconds).

Out of scope: changing what `/v1/stream` delivers. Clients get every event; ordering is their job, and the `time` field already lets them do it. A later option is a per-subscriber "newest per MMSI only" filter, not needed now.

## Checks

- Unit test: two position events for one MMSI, the second with an older `Time` and a different position; the cache keeps the first position and `Seen`, and picks up a name from the second if it has one.
- Live: rerun the probe above for a region with both AISHub and a local feeder; the backwards-jump count against `/v1/vessels` (poll it each second, compare per-MMSI `seen`) is zero.
