# History in ClickHouse

History the app serves moves to ClickHouse, a self-hosted analytical database on the box's local disk. The server writes positions to it as they are accepted and reads every history feature from it: vessel tracks over any range, and in later steps station series, coverage cells, and area playback. A new history feature becomes a table, a materialized view, or a projection, not new infrastructure. ClickHouse is the source of truth for history, as [historical-sources.md](historical-sources.md#where-it-lands) sets out. The normalized archive on R2 is a log of live ingest, and the lake stays the public dataset, but the server stops reading the lake to answer requests.

The plan starts with a spike on the box that decides go or no-go from measurements, then moves tracks, then the other history features.

## Why

The lake is Iceberg on R2. It suits batch work and the public dataset, but every interactive read pays a catalog round trip and R2 latency per file. Measured from the box for one vessel, with the server's DuckDB settings:

| Read | First | Again |
| --- | --- | --- |
| Local SQLite, 3 days of every position | 2 to 10 ms | |
| Lake, a calendar week, 8 daily files | 1.8 to 2.0 s | 0.6 to 1.0 s |
| Lake, a compacted month, 1 to 2 files | 1.5 to 3.6 s | 0.3 s |
| Lake, the current month, 31 daily files | 5.5 to 13 s | 0.3 to 1.9 s |

Every feature that reads the lake needs its own workaround to be fast: a cache per vessel-day, a rollup table, compaction, warming the catalog at startup, a local tier. ClickHouse keeps data sorted on local disk, merges small parts in the background, maintains rollups as data arrives, and expires or moves old data by rule, so those workarounds become settings.

## Where data lives

The rule: state the server needs to start or to answer live requests stays in SQLite on the box. History over time goes to ClickHouse. The server keeps working, minus history, when ClickHouse is down.

| Data | Store |
| --- | --- |
| Vessel record, the latest state per vessel | SQLite |
| Station records: names, owners, bound addresses | SQLite |
| Tokens, usage counters, dedupe state | SQLite and files, as now |
| Receptions, the `positions` view, `positions_1m` | ClickHouse, the source of truth |
| Weather | ClickHouse, in a later step |
| Station series, coverage cells | ClickHouse rollups, as [station-page.md](station-page.md#rollups) plans |
| Every reception as received | R2, the raw archive, which `aiscast replay -clickhouse` rebuilds days of the record from |

Backups, the R2 cold tier, and recovery from losing the box are in [historical-sources.md](historical-sources.md#durability).

## Schema

This section is the spike's schema. [historical-sources.md](historical-sources.md#tables) holds the current one.

Positions use the lake's integer encodings, so loading from the lake and comparing answers is direct.

```sql
CREATE TABLE positions (
    mmsi    UInt32,
    ts      DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
    lat6    Int32 CODEC(Delta, ZSTD),
    lon6    Int32 CODEC(Delta, ZSTD),
    sog10   UInt16 CODEC(ZSTD),
    cog10   UInt16 CODEC(ZSTD),
    heading UInt16 CODEC(ZSTD),
    navstat UInt8,
    source  LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (mmsi, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY DELETE
SETTINGS non_replicated_deduplication_window = 1000;
```

Sorted by vessel and time, one vessel's range is a few granules in each part it touches. Repeated positions from a moored vessel compress to almost nothing under the delta codecs, so the rollups keep every slot and need no rule for dropping repeats.

Two rollups keep each vessel's first position in each epoch-aligned window, which is what the server's thinning keeps, so a step that is a whole multiple of a rollup's window gets the same answer from the rollup as from every position:

```sql
CREATE TABLE positions_15m (
    mmsi  UInt32,
    slot  DateTime('UTC'),
    first AggregateFunction(argMin, Tuple(DateTime64(3, 'UTC'), Int32, Int32, UInt16, UInt16, UInt16, UInt8, String), DateTime64(3, 'UTC'))
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(slot)
ORDER BY (mmsi, slot)
TTL slot + INTERVAL 13 MONTH DELETE;

CREATE MATERIALIZED VIEW positions_15m_mv TO positions_15m AS
SELECT mmsi, toDateTime(toStartOfInterval(ts, INTERVAL 15 MINUTE), 'UTC') AS slot,
       argMinState((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), ts) AS first
FROM positions GROUP BY mmsi, slot;
```

`positions_1h` is the same at one hour, kept with no TTL. A report relayed hours late lands in its own slot, and the aggregate keeps the earliest position whenever it arrives.

## Sizing

Row counts are measured on 2026-09-25. Bytes per row are the original research's estimate of 20 to 30 bytes compressed, which the spike replaces.

| Table | Rows a day | Kept | Size |
| --- | --- | --- | --- |
| `positions` | 54 M | 30 days | 32 to 48 GB |
| `positions_15m` | 4.9 M | 13 months | 48 to 72 GB |
| `positions_1h` | 1.4 M | always | 13 to 19 GB a year |

The box has 126 GB free on local disk. If the spike's bytes per row run high, `positions` moves days older than 7 to an R2-backed disk by TTL instead of keeping them all local, and the rollups the app reads stay local.

## Writes

The server already queues each accepted position for the track store under the cache lock it holds. A second writer drains the same positions into ClickHouse once a second as one batch over the native protocol (`clickhouse-go`). ClickHouse being slow or down never blocks ingest: the queue is bounded, a full queue drops its oldest positions, and drops are counted in `/metrics`, as the track store counts its own. A failed batch is sent again whole under the same `insert_deduplication_token`, and `positions` keeps a deduplication window, because an insert can fail after ClickHouse committed it. A batch ClickHouse keeps refusing for 10 minutes is dropped and counted, so one it refuses every time cannot stop the ones behind it, while overload, which ClickHouse also answers with refusals, has time to pass. Lost connections and timeouts never drop a batch. Shutdown sends a failed batch and then the queue. The deploy installs ClickHouse once, restarts it only when its config changes, and never fails because of it, since the server runs without it. `CLICKHOUSE_URL` unset keeps the server as it is.

## Reads

The track endpoint picks the table from the step it already computes:

| Step | Table |
| --- | --- |
| Under 15 minutes | `positions`, within its 30 days |
| 15 minutes to under 1 hour | `positions_15m` |
| 1 hour and up | `positions_1h` |

A range that starts before what the chosen table holds takes the next coarser table. Thinning is one query: the first position per vessel per step, grouped by `intDiv` of the time. Spikes are judged in Go after thinning, as now. The tier rules stay: anonymous and personal tokens reach the last 48 hours, and feeder and commercial tokens reach the rest, up to 366 days per request.

## The spike

Run on the box, with a go or no-go decision from the numbers.

1. Install ClickHouse from the official apt repository as a systemd service, listening on localhost only, with `max_server_memory_usage` at 4 GB and its data on local disk. Its config goes under `server/deploy/rootfs/` with the other managed files. Alloy scrapes its Prometheus endpoint.
2. Create the schema above.
3. Load positions from 2026-08-20 to now from the lake with `server/deploy/clickhouse-load.py`, which exports each day with DuckDB, which reads the lake's current snapshot, and inserts it with `clickhouse-client`. The rollups fill from every day, and `positions` expires all but the last 30.
4. Write live positions to ClickHouse alongside the track store for at least three days.
5. Measure.

| Measure | Go |
| --- | --- |
| One vessel, a day from `positions` | under 50 ms first read |
| One vessel, a month from `positions_15m` | under 200 ms first read |
| One vessel, a year from `positions_1h` | under 300 ms first read |
| One cell, one hour, every position in it | under 1 s, as a reference for area playback |
| ClickHouse CPU under live ingest | under 1 vCPU on average |
| ClickHouse resident memory | under 4 GB |
| Server ingest and fan-out | no change in event rate, client drops, or request latency |
| Bytes per row in each table | inside the sizing above, or a plan to move older parts to R2 |
| Active parts per partition | steady, not growing |

Read times include the busiest vessel of a day, about 1,440 rows a day in a rollup, and are measured with the query cache off.

## After a go

1. **Tracks.** The track endpoint reads ClickHouse. The server stops reading the lake for tracks, and `tracks.db`, which holds the 48-hour track store and the lake cache, goes with it. `positions` answers every range. The web client's 30-day and 12-month ranges ship with this step.
2. **Station series.** Rollups of receptions, first copies, and distinct vessels per station per hour and per station and vessel, rebuilt from receptions, as [station-page.md](station-page.md#rollups) plans. The in-memory rings stay for the access tiers.
3. **Coverage cells.** A rollup per station, H3 cell, and day of distinct vessels, using ClickHouse's H3 functions, feeds the coverage tiles and each station's footprint, as [station-page.md](station-page.md#rollups) plans.
4. **Area playback.** A projection of `positions` ordered by cell and time answers `/v1/history`.
5. **No lake.** ClickHouse is the record, replay rebuilds it from the raw archive, and the vessel record imports its history from ClickHouse.

## Decisions before starting

- **#162 does not merge.** It moves track reads onto the lake's `ais.tracks`, which this plan replaces. Its one-minute floor and 366-day limit carry into step 1 above.
- **#157 waits for step 1.** It carries the 30-day and 12-month ranges, which need history past 7 days that only step 1 serves at speed. The alternative is to take those ranges back out of #157 and ship the rest now.

## Risks

- **Memory beside the live server.** The box has 15 GB, and the server uses 1 to 2 GB. A 4 GB cap leaves room, and the spike measures it under real ingest.
- **One more service.** Upgrades, config, and alerts on disk use, insert failures, and part counts. The config lives in the repo with the other managed files.
- **One node.** ClickHouse's nightly backup to a separate R2 bucket covers loss of the box (see [Durability](historical-sources.md#durability)).
- **R2 as a ClickHouse disk.** `receptions` moves to R2 after 30 days. The tier is tested against an S3-compatible store, not yet against R2 itself.
