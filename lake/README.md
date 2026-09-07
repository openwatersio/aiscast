# Derived archive (the lake)

The nightly derive job turns the raw per-source archive into deduplicated, decoded Iceberg tables that anyone can build on. The raw log (`<license>/<source>/YYYY/MM/DD/HH.gz` in the `ais-archive` bucket, one line per reception, source-native payloads) is the lossless source of truth; everything here is a pure function of it, so a parser fix or a backfilled source is a rerun, never a migration. Only the coverage map and the history APIs ship as part of this project; other analytics are external consumers reading the same tables.

```sh
./derive.py --archive ../server/archive --date 2026-08-29   # one day
./derive.py --archive ../server/archive --all               # every day present
```

The local catalog is SQLite at `warehouse/catalog.db` with data files under `warehouse/`. Set `LAKE_CATALOG_URI`, `LAKE_WAREHOUSE`, and `LAKE_CATALOG_TOKEN` to write to R2 Data Catalog instead (the token needs the R2 Data Catalog permission; add R2 SQL Read to the same token for `wrangler r2 sql` queries); everything else is identical. The production catalog is bucket `ais-lake`, warehouse `7822da9c68cfce969e63d07534969359_ais-lake`.

## Schema

All coordinates and kinematics are stored at AIS wire precision so the same transmission is byte-identical no matter which source carried it: lat/lon as 1/600000 degree integers (`lat6`, `lon6`), SOG in 0.1 kn (`sog10`, 1023 = n/a), COG in 0.1 degree (`cog10`, 3600 = n/a), heading in degrees (511 = n/a), draught in 0.1 m (`draught10`).

**ais.messages** — one row per transmission, deduplicated across sources. Position reports (types 1, 2, 3, 18, 19, 27 and their JSON-source equivalents). Identity: same (mmsi, lat6, lon6, sog10, cog10, heading) within a 10 s window of canonical time, matching the server's hot-path rule. `id` is the md5 of that identity. Rows are sorted by (mmsi, ts) within each day's files so per-vessel track scans prune well.

| column | type | notes |
| --- | --- | --- |
| id | string | md5 of identity, stable across reruns |
| mmsi | int | |
| ts | timestamp | canonical time: source's own claim when within 30 s of receive time, else first receive time |
| msg_type | int | 0 for JSON sources that do not say |
| lat6, lon6 | int | 1/600000 degree |
| sog10, cog10, heading, navstat | int | wire units, n/a sentinels as above, navstat -1 when absent |
| day | date | UTC day of ts; one append per day, so file stats prune like partitions |

**ais.receptions** — one row per copy heard: `msg_id`, `source`, `station`, `recv_ts`, `license`, `day`. Deduplicated on (msg_id, source, station) with the earliest receive time, which collapses aishub re-reporting the same state in consecutive snapshots. Sorted by (source, station, recv_ts).

**ais.vessels** — latest-wins static data per MMSI, overwritten each run: `mmsi`, `name`, `callsign`, `ship_type`, `draught10`, `cls` (A or B, from message types seen), `updated_ts`.

## Source handling

| Source | Format | Station | Event time |
| --- | --- | --- | --- |
| kystverket | tag-blocked NMEA | `s:NNN` tag | tag `c:` (seconds) |
| feeders | tag-blocked NMEA | source column (`v1:mmsi:...`, `udp:...`) | tag `c:` (ms when >12 digits) |
| digitraffic | multi-line JSON per topic | none (source-wide) | `time` field |
| aisstream | JSON per line | none (source-wide) | MetaData `time_utc` |
| aishub | whole-network snapshot per line | none (source-wide) | record `TIME` |

aishub snapshots are state dumps, not receptions: a record is kept only when its `TIME` is within 120 s of the snapshot, so each transmission enters once when fresh instead of once per snapshot for hours.

Hygiene applied here so every consumer inherits it: out-of-range and null-island positions dropped, net-buoy MMSI blocks (unallocated MID gap 578-599 and out-of-range MIDs) dropped.

## Contract with consumers

- Schema changes are additive: a column's meaning and units never change; new columns and tables may appear. A breaking change means a new table family name, with the old one kept until consumers move.
- A day partition is written once, after the UTC day closes. Its presence means the day is done; reruns and backfills replace whole days.
- Consumers publishing derived work must credit kystverket (NLOD-2.0) and digitraffic (CC-BY-4.0); those licenses require attribution. All sources are cleared for use and redistribution.
- The tables contain per-MMSI data because AIS is per-MMSI. Publish aggregates (grids, counts, flows), not tracks of identifiable pleasure craft.

## Reading it

DuckDB, straight from the catalog metadata:

```sql
INSTALL iceberg; LOAD iceberg;
SELECT count(*) FROM iceberg_scan('warehouse/ais.db/messages');
```

The files themselves are plain Parquet under `warehouse/`, readable by anything, catalog or not.
