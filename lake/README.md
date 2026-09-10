# Derived archive (the lake)

The nightly derive job turns the raw per-source archive into deduplicated, decoded Iceberg tables that anyone can build on. The raw log (`<license>/<source>/YYYY/MM/DD/HH.gz` in the `ais-archive` bucket, one line per reception, source-native payloads) is the lossless source of truth; everything here is a pure function of it, so a parser fix or a backfilled source is a rerun, never a migration. Only the coverage map and the history APIs ship as part of this project; other analytics are external consumers reading the same tables.

```sh
./derive.py                                                 # every closed day of the past week missing from the catalog
./derive.py --date 2026-08-29                               # fetch that day from R2 and derive it
./derive.py --archive ../server/archive --date 2026-08-29   # derive from a local archive tree
./derive.py --archive ../server/archive --all               # every closed day in that tree
```

Without `--archive` the job fetches the day's hour files from the `ais-archive` bucket into `raw/`, derives them, and deletes them, so it does not depend on what any box still holds on disk. That needs `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY` (the same S3 keys the server uploads with) and works one day at a time. A day is refused if it is not over yet, or if fewer than 20 distinct hours arrived (`--min-hours` covers a day the archive genuinely started mid-way through; under `--all` a short day is skipped, not fatal). A source directory that contributed raw files but decoded to zero position rows also fails the day, so a decoder regression that drops a whole source is caught at derive time rather than by a consumer.

The local catalog is SQLite at `warehouse/catalog.db` with data files under `warehouse/`. Set `LAKE_CATALOG_URI`, `LAKE_WAREHOUSE`, and `LAKE_CATALOG_TOKEN` to write to R2 Data Catalog instead (the token needs the R2 Data Catalog permission; add R2 SQL Read to the same token for `wrangler r2 sql` queries); everything else is identical. The production catalog is bucket `ais-lake`, warehouse `7822da9c68cfce969e63d07534969359_ais-lake`.

In production the box runs this nightly: `derive.timer` fires at 01:30 UTC, after the closed day's last hour files have rotated into R2, and `derive.service` runs `/opt/aiscast/derive.py` with no arguments, which derives every closed day of the past week still missing from the catalog (so a failed night heals on the next run), reading the `LAKE_*` and `R2_*` variables from `/etc/aiscast.env`. Both units live in [server/deploy/rootfs](../server/deploy/rootfs/etc/systemd/system), and `derive.py` ships in the same bundle as the server binary, so a deploy updates the script and the units together. `LAKE_HOME` points the staging and warehouse directories at `/var/lib/aiscast/lake`.

## Schema

All coordinates and kinematics are stored at AIS wire precision so the same transmission is byte-identical no matter which source carried it: lat/lon as 1/600000 degree integers (`lat6`, `lon6`), SOG in 0.1 kn (`sog10`, 1023 = n/a), COG in 0.1 degree (`cog10`, 3600 = n/a), heading in degrees (511 = n/a), draught in 0.1 m (`draught10`).

**ais.messages** — one row per transmission, deduplicated across sources. Position reports (types 1, 2, 3, 18, 19, 27 and their JSON-source equivalents). Identity: same (mmsi, lat6, lon6, sog10, cog10, heading) within a 10 s window of canonical time, matching the server's hot-path rule. The window is measured from the last copy accepted, not from the previous copy seen, so reports at 0 s, 9 s and 18 s are two transmissions rather than one and a transponder repeating a frozen position stays one message per window. `id` is the md5 of that identity. Rows are sorted by (mmsi, ts) within each day's files so per-vessel track scans prune well.

| column | type | notes |
| --- | --- | --- |
| id | string | md5 of identity, stable across reruns |
| mmsi | int | |
| ts | timestamp | canonical time: source's own claim when within 30 s of receive time, else first receive time |
| msg_type | int | 0 for JSON sources that do not say |
| lat6, lon6 | int | 1/600000 degree |
| sog10, cog10, heading, navstat | int | wire units, n/a sentinels as above, navstat -1 when absent |
| day | date | UTC day of ts; the Iceberg partition column, so a scan for one day reads one partition |

**ais.receptions** — one row per copy heard: `msg_id`, `source`, `station`, `recv_ts`, `license`, `day`. Deduplicated on (msg_id, source, station) with the earliest receive time, which collapses aishub re-reporting the same state in consecutive snapshots. Sorted by (source, station, recv_ts).

**ais.vessels** — latest-wins static data per MMSI, overwritten each run: `mmsi`, `name`, `callsign`, `ship_type`, `draught10`, `cls` (A or B, from message types seen), `updated_ts`. Latest-wins applies per field: a type 24 part B or a source's metadata can carry a callsign and no name, and it must not erase a name learned earlier.

## Source handling

| Source | Format | Station | Event time |
| --- | --- | --- | --- |
| kystverket | tag-blocked NMEA | `s:NNN` tag | tag `c:` (seconds) |
| feeders | tag-blocked NMEA, or AIS-catcher JSON envelopes over HTTP | source column (`v1:mmsi:...`, `udp:...`, `http:...`) | tag `c:` (unit by magnitude), envelope `rxtime` |
| digitraffic | multi-line JSON per topic | none (source-wide) | `time` field |
| aisstream | JSON per line | none (source-wide) | MetaData `time_utc` |
| aishub | whole-network snapshot per line | none (source-wide) | record `TIME` |

aishub snapshots are state dumps, not receptions: a record is kept only when its `TIME` is within 120 s of the snapshot, so each transmission enters once when fresh instead of once per snapshot for hours.

Hygiene applied here so every consumer inherits it: out-of-range and null-island positions dropped, net-buoy MMSI blocks (unallocated MID gap 578-599 and out-of-range MIDs) dropped.

## Contract with consumers

- Schema changes are additive: a column's meaning and units never change; new columns and tables may appear. A breaking change means a new table family name, with the old one kept until consumers move.
- A day partition is written once, after the UTC day closes. Its presence in `ais.messages` means the day is done: Iceberg has no cross-table transaction, so receptions and vessels commit first and messages last. Reruns and backfills replace whole days.
- Hour files are named by receive time, so the job also reads the hours either side of midnight and keeps whatever its own canonical-time window claims. A transmission never falls between two days, though one whose copies straddle midnight surfaces once in each.
- `license` mirrors the server's mapping: exact source name first, then the prefix before `:`, else `unspecified`. Volunteer feeder receptions are CC0-1.0 per the contributor agreement.
- A damaged hour file fails the day rather than shortening it, and a day is refused unless it has closed and arrived with a plausible number of hours.
- Consumers publishing derived work must credit kystverket (NLOD-2.0) and digitraffic (CC-BY-4.0); those licenses require attribution. All sources are cleared for use and redistribution.
- The tables contain per-MMSI data because AIS is per-MMSI. Publish aggregates (grids, counts, flows), not tracks of identifiable pleasure craft.

## Reading it

DuckDB, straight from the catalog metadata:

```sql
INSTALL iceberg; LOAD iceberg;
SELECT count(*) FROM iceberg_scan('warehouse/ais.db/messages');
```

The files themselves are plain Parquet under `warehouse/`, readable by anything, catalog or not.
