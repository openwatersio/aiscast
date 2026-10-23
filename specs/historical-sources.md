# Historical sources

Tracks and the vessel record reach back only to 2026-08-20, when the network's own archive starts. Several governments publish their own AIS archives, and some of them keep publishing new days. This plan loads at least the last year from those archives, 2025-10 to now, and keeps loading what they publish, so history covers coasts the live network hears weakly or not at all. It builds on [clickhouse.md](clickhouse.md), which serves tracks from ClickHouse, and on the record import in [history-api.md](history-api.md#backdating-the-record-from-history).

## Sources

Checked on 2026-10-03.

| Source | Covers | Available now | Updates | Rate | Identity | License | Size a year |
| --- | --- | --- | --- | --- | --- | --- | --- |
| US MarineCadastre (USCG NAIS) | US coasts, Great Lakes, Hawaii, Puerto Rico, Canadian spillover in the Salish Sea, Great Lakes, and Bay of Fundy. No Alaska after 2021-03-28 | 2009 to 2026-06-30 | Quarterly, about two months behind | One position per vessel per minute | MMSI, name, IMO, call sign, type, dimensions | Public domain; the 2024 GeoParquet release says CC0 | 90 GB zstd CSV, 3 B rows |
| Denmark DMA | Danish waters, Skagerrak, Kattegat, western Baltic | 2006-03 to 2026-09-30 | Daily, three days behind | Every report, to the second | MMSI, name, IMO, call sign, type, dimensions, destination | None stated | 240 GB zipped CSV |
| Norway Kystverket HAIS | Norwegian waters | 2011 to yesterday | Daily | Every report | MMSI and motion only | NLOD 2.0 | Not published; ordered a year at a time |
| Germany BSH | Three shore receivers: Elbe at Wedel, Kiel, Bremerhaven | 2016 to two days ago | Daily | Every report | Full, including class B | dl-de/by-2.0 | 25 GB text |
| US MarineCadastre, Guam and CNMI | Guam and the Northern Mariana Islands | 2015 to 2025 | Yearly | One a minute | As US | As US | 40 MB |
| Finland, Åbo Akademi ([Zenodo 8112336](https://zenodo.org/records/8112336)) | Finnish seas, rivers, and lakes | 2021-04 to 2022-12 only | None | Every report; a Digitraffic capture | MMSI, name, IMO, call sign | CC BY 4.0 | 43.6 GB for the whole set |

The live network covers Norway from Kystverket and BarentsWatch, Finland from Digitraffic, and everywhere else from AISHub snapshots and volunteer stations. So the US archive adds the most: the live network has no US government feed. Denmark is second, because AISHub's snapshots are the only live Danish source. HAIS repeats the live Kystverket feed, so it is loaded only for the time before 2026-08-20 and needs no daily updates.

Not loaded:

- Datasets that replace the MMSI with a pseudonym, because history is keyed on MMSI: Piraeus, Korea's main MOF set, Australia's AMSA craft tracking.
- Datasets under a non-commercial license: AMSA derivatives, Brest and Ushant, the AegeaNET Syros set, Global Fishing Watch.
- Fishing-fleet VMS from Mexico and Chile, which is not AIS and has no MMSI.
- HELCOM raw data, because its agreement forbids redistribution.
- Sweden's RAIS, which is paid per extract.
- Every commercial archive, because every one forbids redistribution.
- Mirrors on Kaggle and GitHub, because their provenance is unknown.

Korea's set with real identities runs to November 2025. Its size is unverified, and it is a candidate once one file has been downloaded and checked.

## Licensing

Every source here is published for anyone to download, and none forbids storing or redistributing it. DMA publishes under the Danish public sector information act. Its [data management policy](https://www.dma.dk/safety-at-sea/navigational-information/ais-data/ais-data-management-policy-) has one condition: do not combine the data so that private individuals become identifiable. aiscast meets it by what it joins: the vessel record adds vessel facts and never an individual's name. The vessel opt-out in [docs/policy.md](../docs/policy.md#privacy) is a separate feature ([history-api.md](history-api.md#opt-out)) that this plan does not wait on. MarineCadastre's FAQ forbids retransmission under a Coast Guard instruction that the Coast Guard cancelled in 2021, and its 2024 release is labeled CC0. HAIS, BSH, and the Finnish set have open licenses. The DMA row in `docs/policy.md` changes from blocked to re-served under the PSI act when DMA loads.

## Where it lands

ClickHouse is the source of truth for history: what it holds is the network's history, and no second decoded copy is kept beside it. This is the one place the specs say so. The raw archive on R2 is what live ingest received, and `aiscast replay -clickhouse` rebuilds days of the record from it; it is not the record. Every copy of every transmission is a row in `receptions`, from live feeds and archives alike. An archive row is a reception like any other, delivered days or months late by a feed the network does not run, just as AISHub delivers its rows a minute late. `positions` is a view that keeps the earliest copy of each transmission. Static data goes to `vessel_statics`, and each distinct state to `statics`.

```
live writer ────────────────────────────────────────────────┐
source file → ClickHouse table function → history_stage → loader (Go) ┴→ receptions (local, then R2) ─┬→ positions_1m (accepted copies)
                                                     │                                                   ├→ station_coverage
                                                     └→ vessel_statics                                   └→ positions (view)
```

### Tables

```sql
CREATE TABLE receptions (
    mmsi         UInt32,
    ts           DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
    tx_off       Int32 CODEC(T64, ZSTD),
    tx_disc      UInt8 CODEC(ZSTD),
    recv_delay   Int32 CODEC(T64, ZSTD),
    lat6         Int32 CODEC(Delta, ZSTD),
    lon6         Int32 CODEC(Delta, ZSTD),
    sog10        UInt16 CODEC(ZSTD),
    cog10        UInt16 CODEC(ZSTD),
    heading      UInt16 CODEC(ZSTD),
    navstat      UInt8,
    source       LowCardinality(String),
    station      LowCardinality(String),
    accepted     Bool DEFAULT true,
    corroborated Bool DEFAULT true,
    implausible  Bool DEFAULT false,
    clock_bad    Bool DEFAULT false,
    moving       Bool DEFAULT true
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (mmsi, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY TO VOLUME 'cold'
SETTINGS storage_policy = 'tiered', non_replicated_deduplication_window = 1000;
```

- A transmission is named by its vessel, the time its accepted copy was stamped, `ts` plus `tx_off`, and `tx_disc`, the low byte of that copy's event id, which tells apart a vessel's transmissions stamped in the same millisecond. Most copies are their transmission's accepted one, so `tx_off` is almost always 0 and costs nothing. On three hours of production receptions, the time and the id's byte alone merged 83 of 5 M transmissions, all a vessel's reports in one millisecond, so a transmission takes the next byte free among the vessel's recent ones in its millisecond. A random 64-bit hash cost 8 bytes a row, 41% of the table, and did not compress. Raw copies share the name through the payload hash. Rebuilt copies and archive rows take it from the copy they match (see [Deduplication](#deduplication)).
- `recv_delay` is when the copy arrived, in milliseconds after `ts`, where a second timestamp cost 3 bytes a row.
- `moving` is reported speed above half a knot, or more than 50 m from where the vessel was last moving, which catches drift. It decides how `positions_1m` keeps the copy. A copy carries its transmission's verdict, so `positions_1m` rebuilt from it after a purge keeps the track's shape.
- `accepted` marks the copy that arrived first, the one the live server accepts today. `positions_1m` reads only accepted copies. A rebuilt or archive copy that matches an earlier one is not accepted. The flag is set at write time and is not updated after a purge. Every read that has to survive a purge uses the `positions` view, which recomputes the earliest copy.
- `station` is the station for a live copy and the archive's name for an archive copy. `source` sits beside it because redundancy classes come from the source kind.
- `corroborated` carries the live writer's corroboration rule, the one `/v1/nmea` and the AISHub feed apply, so bulk re-serving can leave out uncorroborated reports. Archive rows are corroborated by their source.
- `implausible` and `clock_bad` mark copies that history leaves out (see [Validity](#validity)). They are stored, not dropped, so a purge or a changed rule never needs a reload.

The schema changes in numbered steps recorded in `schema_migrations`, each run once at start. A step only adds a table, a column, or a view, so a server from before a step still writes after it and a rolled-back deploy keeps history flowing. No step renames a table: a materialized view stays on the table it was created over, so a rename silently stops `coverage` and every other view on it. Steps that drop or rewrite data are commands run by hand. The production table came from the first layout, which named a transmission by a 64-bit `tx` and stored `recv_ts`. A step added the columns above beside those, `aiscast convert-receptions` rewrites the old rows in place, and `aiscast clickhouse-cleanup` then drops the old rows and columns, with `positions_15m`, `positions_1h`, and `positions_old`.

Measured on three hours of production receptions, this layout costs 11.4 bytes a row, where a 64-bit `tx` and `recv_ts` cost 19.8.

The `tiered` policy has a local volume and a cold volume on an `s3` disk on R2, with a 10 to 20 GB local filesystem cache in front of R2. The disk keeps its part metadata on the box, and a nightly backup carries it off the box (see [Durability](#durability)). A `plain_rewritable` disk, which keeps metadata in the bucket, does not work here: on ClickHouse 26.8 a table with one in its policy refuses every `ALTER` but settings and comments, so no column could be added and no purge could run. Merges on the cold volume are turned off, so R2 is written once per part. Partitions are monthly, because a table kept forever would collect thousands of daily partitions. Every part spans every vessel, because the sort is `(mmsi, ts)`, so a one-vessel read of a cold month touches every part in it. The number of parts per cold month is therefore what decides how fast a cold read is. Live parts merge locally for 30 days before they move. Archive rows are older than the TTL on arrival, so loads control their own moves (see [Loading](#loading)).

`positions` is a parameterized view over `receptions`. It keeps the earliest-received copy of each transmission in a vessel's time range:

```sql
CREATE VIEW positions AS
SELECT mmsi, f.1 AS ts, f.2 AS lat6, f.3 AS lon6, f.8 AS source FROM (
    SELECT mmsi, min(toUnixTimestamp64Milli(ts) + tx_off) AS sent,
           argMinIf((ts, lat6, lon6, sog10, cog10, heading, navstat, toString(source)), toUnixTimestamp64Milli(ts) + recv_delay, NOT clock_bad) AS f
    FROM receptions
    WHERE mmsi = {mmsi:UInt32}
      AND ts BETWEEN {from:DateTime64(3, 'UTC')} - INTERVAL 5 MINUTE AND {to:DateTime64(3, 'UTC')} + INTERVAL 5 MINUTE
    GROUP BY mmsi, toUnixTimestamp64Milli(ts) + tx_off, tx_disc
    HAVING NOT max(implausible) AND countIf(NOT clock_bad) > 0
) WHERE sent BETWEEN toUnixTimestamp64Milli({from:DateTime64(3, 'UTC')}) AND toUnixTimestamp64Milli({to:DateTime64(3, 'UTC')});
```

The motion columns follow the same pattern. The parameters put the vessel and time filter inside the grouping, so a query reads only that vessel's range. A copy is stamped up to 5 minutes from its transmission, the reach of the rebuilt-copy match, so copies are read 5 minutes either side and a transmission is in a range by its own time. A page boundary then never splits one transmission into two. Measured in ClickHouse 26.8 against 104 M test receptions: a vessel-day of 17,280 copies returned 8,640 transmissions, one each, and read 32,768 rows in 4 of 4,516 granules. A materialized view cannot do this job. It sees one insert at a time, but copies of a transmission arrive in different inserts, sometimes months apart, and it does not see deletes.

`positions_1m` fills from `receptions` through a view, reading accepted copies that are neither implausible nor `clock_bad`. A moving vessel keeps its latest position in each minute. One sitting still keeps one row every 30 minutes for each place it sits, a cell of about a kilometer, its latest report there: a heartbeat that says it was heard there then, on the 30 minutes that make a vessel active on the live map. A range finds a moored vessel in every 30 minutes it overlaps but the last, and its moving rows bound when it arrived and left to the minute, so no first or last time per window is kept. On 2026-10-02 it kept 12.4 M rows of 53 M accepted positions: 10.2 M minutes underway and 2.2 M heartbeats, where one a day would keep 0.15 M and lose a moored vessel from any range that ended before its last report of the day. Plain columns cost 12.4 bytes a row where a window kept as an aggregate state cost 25. Any step of a minute or more groups its rows by the step, so it answers every step the 15-minute and hourly rollups did, and a ferry's coarse track is its first position in each step rather than an hourly sample that lands at random along its crossings. Rollups at 15 minutes or an hour can be added later and filled from it.

`history_stage` holds one file at a time, in every column a source can give: position, motion, and static data. The loader writes its positions to `receptions`, and one insert from it fills `vessel_statics`, so each source file is downloaded and parsed once.

`vessel_statics` is an `AggregatingMergeTree` keyed by `(mmsi, source)`. It keeps each vessel's earliest report, its latest position, and the latest non-empty value of each static field. Archives feed it first. Once the live writer feeds it too, it replaces `ais.vessels` as the record import's input.

### Loading

The aiscast server runs the loads. It already holds the ClickHouse connection, the R2 client, a daily job, and `/metrics`, so loading needs no new service, timer, or runner. Once a day, after the record import, it lists each source's index and compares it with `history_loads`. That table has one row per source file: size, checksum or ETag, rows read, rows dropped by reason, rows matched to a stored transmission, new transmissions, and load time. For each new or changed file, ClickHouse downloads, decompresses, and parses the file itself into `history_stage`, a MergeTree table sorted by vessel and time. The loader then reads the staged day in that order, beside the day's live transmissions for the same vessels, and writes through the live writer's own insert, so an archive row gets its transmission name, its byte, its anchor verdict on moving, and its clamped arrival exactly as a live copy would. A single SQL statement cannot carry the anchor through a vessel's day. A changed file first deletes its day's rows, and its day of `positions_1m` is then rebuilt, since rows written from the earlier file stay there otherwise.

Loads must never starve the live writer. The writer treats any ClickHouse error as a refusal and drops a refused batch after 10 minutes, so a load that runs ClickHouse out of memory would lose live positions. Loads therefore run as their own ClickHouse user, `loader`, with a settings profile: `max_memory_usage` about 1.5 GB, `max_threads` and `max_insert_threads` 1 to 2, and the default insert block size of about a million rows. Default blocks keep each view's aggregation small. `max_server_memory_usage` keeps headroom above the profile. Step 1 also tries ClickHouse 26's `CREATE WORKLOAD` scheduling.

Default blocks produce many parts, and archive rows are past the TTL, so left alone they would move to R2 unmerged. A bulk load therefore moves its own months:

1. `SYSTEM STOP MOVES receptions`.
2. Load the month's files.
3. `OPTIMIZE TABLE receptions PARTITION <month> FINAL`, on local disk.
4. `ALTER TABLE receptions MOVE PARTITION <month> TO VOLUME 'cold'`, then `SYSTEM START MOVES receptions`.

Live moves wait while a month loads, which costs nothing, because live rows move only after 30 days. A daily DMA or BSH load lands in the current month, which is still local and merges with live parts. A quarterly MarineCadastre load lands in months already on R2. It loads those months back to local disk, merges them, and moves them again, so each month stays a few parts.

There is one SQL file for each source schema era, embedded in the binary:

- **MarineCadastre `csv2`**: `url()` over `.csv.zst`, one schema for 2015 to 2026. Longitude comes before latitude, and timestamps are separated by a space. The Azure host ignores range requests and serves about 2.4 MB/s per stream, so a day takes about two minutes and a few days load at once.
- **DMA**: ClickHouse reads the CSV inside each zip from DMA's public bucket with `s3()`, `NOSIGN`, and the archive syntax (`aisdk-2026-09-30.zip :: *.csv`). There are 26 columns from 2019 and 22 before, timestamps are day-first, and keys follow three layouts. `Type of mobile` separates vessels from base stations and aids to navigation. A day is about 780 MB zipped, 4.5 GB of CSV, and about 20 M rows.
- **HAIS and the Finnish set**: these arrive by hand, as an emailed HAIS order or one 43.6 GB Zenodo zip. They are uploaded to `history-staging/<source>/` in the private bucket. The loader picks up anything there, loads it with `s3()`, and deletes it after the load.
- **BSH**: `LineAsString`, with fields extracted by pattern from a semicolon-delimited text format that has units in the values. If the patterns prove unworkable, a Go parser feeds the same insert.

Each SQL file applies the same rules. It keeps only valid MMSIs and mobile stations: no base stations, no aids to navigation, and no MMSIs that the live map's validity rule rejects ([#183](https://github.com/openwatersio/aiscast/issues/183)). It turns the sources' not-available values into the server's: latitude 91, longitude 181, heading 511, COG 360, and SOG 102.3. Rows with no usable position go only to `vessel_statics`.

ClickHouse sorts its own parts, so the loader writes no Parquet and needs no Python or runner. Parsing a year of DMA, the largest source, takes one or two CPU-hours on the box.

### Deduplication

Nothing is dropped for being a duplicate. Each copy is assigned to a transmission, the `positions` view keeps the earliest copy, and `accepted` marks it at write time.

Raw receptions share a transmission when their payloads hash the same. Two kinds of copy carry decoded fields only, so their payload cannot be rebuilt, because the radio state bits in every position report are lost in decoding. Both match on decoded content with one rule:

1. **Exact repeats within a file** of `(mmsi, ts, lat6, lon6)` collapse to one row. They are the same row twice, not two receptions. DMA has them.
2. **A copy matches a stored transmission** of the same vessel at the same position, within 4 wire units (about 0.75 m), the nearest in time within 5 minutes, and takes its name and its verdict on moving. This is the live writer's rule for rebuilt copies. AISHub stamps run tens of seconds off the raw copy's, so a window of seconds would make every AISHub-covered archive row a second transmission and zigzag the track. A vessel underway moves between reports, so its position names one; a moored one repeats its position, and any of those transmissions is the same point. The copy takes `implausible` too when its transmission has it, because views that see one insert at a time, such as `coverage`, filter copy by copy. DMA's six decimal places round back to the exact wire value. MarineCadastre's five are off by up to 3 units, and converting the float back can add one.
3. **An unmatched copy** is its own transmission, accepted, with the byte of a hash of its source and decoded fields, moved to the next free one in its millisecond.

**Rebuilt live copies** come from AISHub, aisstream, and BarentsWatch, which re-serve transmissions that Kystverket and stations deliver raw. AISHub runs about a minute behind, so by the time its copy arrives, a vessel underway has sent 6 to 30 newer positions, and `staleFor`'s comparison with the last position misses. The server therefore keeps a ring of each active vessel's accepted positions from the last 5 minutes, as `(ts, lat6, lon6, tx)`. That is about 60,000 vessels, 30 positions, and 24 bytes, or about 45 MB. A rebuilt copy that matches the ring takes that `tx` and is written with `accepted` false. Today these copies are withheld and never written. Writing them is what lets a purge fall back to them.

**Archive copies** are matched by the loader in Go, a vessel at a time, against the accepted copies of the day's live transmissions for the file's vessels, read with 5 minutes either side. An archive copy's arrival is when it loaded, clamped to `recv_delay`'s 24.8 days, so it never arrives before the live copies of its transmission and the view keeps serving them. Matching runs for every day that holds receptions from another source, which for live data means from 2026-08-20. Backfills of earlier days skip it. MarineCadastre is two months behind, so each of its quarterly loads overlaps live data and is matched.

A wrong match costs nothing permanent: both copies are stored, and only the choice between them depends on it.

`history_loads` records matched rows and new transmissions per file. In the overlap, the matched share measures how much of what an archive heard the live network also heard. On 2026-09-30, DMA heard 3,177 vessels between 54.5 and 57.8° N and 8 and 13° E, 1,596 class A and 1,581 class B. The live network heard 2,721 there in the 24 hours to 2026-10-03 19:35 UTC, almost all through AISHub.

Nothing is thinned at load.

### Validity

The `positions` view detects nothing, but it judges a transmission as a whole: one implausible copy keeps every copy of it out, since they all carry its position. Checks run in two places, as they do now: when a row is written, and when a track is drawn. The view and `positions_1m` choose only among rows the write step has already marked.

| Check | Live copies | Archive rows |
| --- | --- | --- |
| Invalid position: latitude 91, longitude 181, (0, 0), out of range | The decoder leaves the position out | Loader SQL, the same rule |
| Invalid MMSI | The live map's rule ([#183](https://github.com/openwatersio/aiscast/issues/183)) | Loader SQL, the same rule |
| Future time | Clamped to the receive time, as now | Dropped when outside the file's UTC day |
| Wrong clock | `clock_bad` when `ts` is more than a day before `recv_ts`. A device with a reset clock stamps years back, and satellite passes arrive at most about ten hours late | Dropped when outside the file's UTC day |
| Implausible jump: more than 10 NM and more than 120 kn | `implausible`, set by the fold as now, against the vessel's last accepted position. A stale report, which the fold does not test, is tested against the vessel's position nearest it in time. Dedupe's copies of an implausible transmission are flagged with it | `implausible`, set by the loader against both neighbors in time |
| Late report from a volunteer station | `implausible`: a station's backlog never reaches the fold, so it has no late reports to deliver, and anyone can run one, token or not, and stamp a report into any vessel's past | Not applicable |
| Spikes in a drawn track: over 25 kn and twice the reported speed | `despike()` after thinning, as now | The same, because it runs on whatever a track reads |

The live rule compares each report with the last accepted one. A single SQL statement cannot walk rows that way, and comparing each row only with the one before flags a lone spike and also the good row after it. The loader therefore flags a row only when it is implausible against both its previous and its next row for the vessel, using `lagInFrame` and `leadInFrame`. A lone spike is far from both neighbors. A good row beside a spike is far from only one. Runs of two or three bad rows pass the loader, and `despike()` removes them when a track is drawn, as it does for live data now.

`stale` is not a validity flag. The live stream withholds a report older than the vessel's newest so that the map never moves a vessel backward. A copy of a transmission already heard is a duplicate, handled in [Deduplication](#deduplication). A genuine report that arrives late, such as a satellite pass hours behind, is a valid position. `receptions` keeps it for history as an accepted copy, and `positions_1m` puts it in its own window whenever it arrives. The stream's rule is unchanged.

### Coverage

Coverage is where the network has data for a period, whatever the source. The `coverage` view from [#170](https://github.com/openwatersio/aiscast/pull/170) fills from `receptions` as rows are inserted, skipping implausible and bad-clock rows, so archive rows count like any other. It counts distinct vessels per cell, so duplicate copies change nothing. DMA's daily load, three days behind, fills in Danish waters for the days it covers. The view already skips positions older than `coverage`'s 13 months, so a backfill of older years computes no cells that `coverage` would then delete.

Per-station work reads aggregates, not `receptions`: `station_coverage`, distinct vessels per station, resolution, day, and cell, filled from `receptions` by a view ([station-page.md](station-page.md#rollups)). An archive's `station` is the archive itself, and it counts toward redundancy like any other source. A recent week shows mostly the live network. A period the archives cover fills in with them. If raw per-station reads are ever needed, a projection ordered by `(station, ts)` serves them without changing the sort key. Range outlines will need each copy's distance and bearing from its station. Those are two columns added when that work starts, unknown by default.

### Purging a source

Purging deletes the source's rows from `receptions`, `vessel_statics`, and `statics` with a mutation. The `positions` view then keeps each transmission's next-earliest copy, so a transmission another source also heard is never lost. `positions_1m` and the station rollups keep one row per window or a set per cell, chosen when rows were inserted, and a purge does not move `accepted`. So the days the source touched are rebuilt: `aiscast rebuild-positions-1m -from -to` rebuilds `positions_1m` from each transmission's earliest copy, as the view picks it, and the station rollups rebuild each day the source touched ([station-page.md](station-page.md#rollups)). A copy carries its transmission's verdict on moving, so the rebuilt track keeps its shape. Purges are rare.

### Durability

ClickHouse is the source of truth, so its own backups are what keep history. Hetzner's nightly images of the box are not enough: they live in the same account as the box, and a cold part's metadata on the box can point at R2 objects a purge or merge deleted after the image was taken.

- **Cold tier.** `receptions` moves a part to the `cold` volume, an `s3` disk on R2, 30 days after its rows' time. `positions_1m` stays local. Archive months load straight into the cold volume (see [Loading](#loading)).
- **Backups.** A nightly `BACKUP DATABASE aiscast TO S3(...)` writes to a separate R2 bucket, incremental against the last full backup through `base_backup`, with a new full backup each month. A backup holds the part metadata the `s3` disk keeps on the box and a copy of every part, cold ones included, so the backup bucket alone rebuilds the database. Its cost is a second copy of cold data on R2, about $15 a month per TB.
- **Losing the box.** A new box gets the same ClickHouse config and restores from the backup bucket, one month at a time and oldest first: `RESTORE TABLE aiscast.receptions PARTITIONS '<month>' FROM S3(...) SETTINGS allow_non_empty_tables = true`. A restored part lands on local disk and the TTL moves an old month back to R2 within seconds, so the local disk only ever holds one month in flight. The objects the lost box's cold parts used are orphaned; delete their prefix once the restore checks out. Live data since the last backup is lost unless it is replayed from the normalized archive, and no tool does that yet.
- **What was tested.** On ClickHouse 26.8 against an S3-compatible store: a TTL move to the `s3` volume, `ADD COLUMN`, a lightweight `DELETE`, and an `ALTER ... DELETE` purge on cold parts; a full and an incremental `BACKUP`; removing the server and its disk; and a `RESTORE` on a fresh server, whole and one partition at a time, with every row and the later column back, and the old month moved back to the cold volume. A `plain_rewritable` disk, which keeps metadata in the bucket, was ruled out: a table with one in its storage policy refuses every `ALTER` but settings and comments. R2 itself is not yet tested: the endpoint, `region` auto, `s3_check_objects_after_upload`, multipart upload, and server-side `CopyObject` between buckets for `BACKUP`. That needs a bucket and token.

### Reads

The track endpoint picks a table from the step:

- **Steps under a minute, including every position**, read the `positions` view, so each transmission appears once. The span is capped at 31 days, because a busy vessel's year at full rate is millions of rows, partly on R2. A longer range takes a one-minute step. Every-position reads work for any 31-day window in history.
- **Steps of a minute and up** read `positions_1m`, grouped by the step. It stays on local disk, so these tracks never touch R2.

Credit lines come from the `source` of each position kept, as they do now. The despike anchor, the extra row past the limit, works as now, because every read returns one row per transmission.

The record import in [import.go](../server/import.go) also reads `vessel_statics` and applies the fill-only rule it applies to `ais.vessels`. An empty name or particular is filled, `first_seen` moves earlier, and the last position is replaced only by a newer one. A vessel known only from history gets a record. Its vessel page shows its last historical position and its real `seen` date.

A bulk history product is an export from the `positions` view: ClickHouse writes Parquet for a source, area, and range to R2 with `INSERT INTO FUNCTION s3(...)`, on request.

### Source registry

Each source gets a `source` value, an entry in `licenses` in [archive.go](../server/archive.go) with its attribution string, an env flag, and a row in [docs/policy.md](../docs/policy.md). None of them joins the health gate.

| `source` | License tag | Attribution |
| --- | --- | --- |
| `marinecadastre` | `us-public-domain` | U.S. Coast Guard NAIS, via NOAA MarineCadastre.gov |
| `dma` | `DK-PSI` | Danish Maritime Authority |
| `hais` | `NLOD-2.0` | Contains data under the Norwegian licence for Open Government data (NLOD) distributed by Kystverket |
| `bsh` | `dl-de/by-2.0` | Bundesamt für Seeschifffahrt und Hydrographie (BSH), dl-de/by-2-0 |
| `abo-finland` | `CC-BY-4.0` | Åbo Akademi University, from Fintraffic / digitraffic.fi, CC BY 4.0 |

## Keeping it current

The daily load finds new files on its own:

- DMA: normally one new day, three days behind.
- BSH: the same, two days behind.
- MarineCadastre: the container listing gains a quarter about two months after it closes, so most days find nothing.

Older ranges for a source load through an admin endpoint that queues that range's files for the same loader. HAIS needs no daily load, because the live Kystverket feed covers Norway from 2026-08-20.

## Sizing

Measured on production on 2026-10-02: 53 M accepted positions a day, with 3% more later copies of a transmission. Measured on three hours of production receptions: 11.4 bytes a row in `receptions` and 12.4 in `positions_1m`, which keeps 12.4 M rows a day, about a quarter of accepted positions.

| Store | One year | Notes |
| --- | --- | --- |
| Live rows in `receptions` | about 225 GB | 55 M a day at 11.4 bytes. Local for 30 days, about 19 GB, then R2 at about $3.40 a month per year kept |
| Archive rows in `receptions`, on R2 | about 120 GB | DMA about 20 M rows a day and MarineCadastre about 10 M, at about 11 bytes |
| `positions_1m` | about 56 GB live plus about 15 GB archive | Local |
| Backup bucket | about the size of everything above | A second copy of cold data |
| R2 cache, local | 10 to 20 GB | Fixed |

Until the cold tier exists, nothing moves: both tables stay on local disk and grow by about 0.75 GB a day, which the box's 150 GB disk, shared with the archive staging and SQLite, holds for about two months. With the tier, the local disk holds 30 days of receptions, `positions_1m`, and the cache: about 110 GB after a year. No Hetzner volume is added until the local set needs one, so the disk alerts are what warn of it. Part counts per partition are not yet measured: `SELECT table, partition, count() FROM system.parts WHERE active AND database = 'aiscast' GROUP BY 1, 2 ORDER BY 1, 2`.

## Columns to capture

Each is one numbered `ADD COLUMN` step, which costs nothing for the rows before it, read as the default. In order:

1. **`msg_type UInt8`**, the AIS message type, 0 when unknown. It tells class A from class B, keeps aids to navigation, base stations, and SAR aircraft out of vessel tracks, and lets privacy rules treat small craft differently. Near 0 bytes a row.
2. **`utc_second UInt8`**, the time stamp field of position reports, 0 to 59, 60 to 63 for not available. It pins the second a transmission was sent, so copies and archive rows match exactly instead of by position, and it measures each source's stamp skew. About 0.1 byte a row.
3. **A `statics` table** of name, call sign, IMO, type, dimensions, destination, ETA, and draught: each distinct state a source sent for a vessel each day, with the first and last time it was heard, from the live writer and archives ([station-page.md](station-page.md#static-data)). It enables port calls, voyages, ETA accuracy, and renames. A few MB a day.
4. **`rssi Int8`, `snr Int8`, `ppm Int8`, and `channel UInt8`**, from stations that send them in AIS-catcher JSON or NMEA tag blocks, unknown otherwise. Range models, station health, interference. A byte or two on station copies only.
5. **A `station_positions` table** of each station's position over time. Distance and bearing per copy are computed at read time, so `receptions` gains nothing. It enables range outlines.

## Order of work

Each step is one pull request. Each includes tests, the server README, `openapi.json` where a response changes, policy and attribution docs, and monitoring.

1. **`receptions` as the record.** Merges onto `main` ahead of [#197](https://github.com/openwatersio/aiscast/pull/197), which then rebases onto it to make ClickHouse the only track source. Until then the SQLite track store keeps answering the last 48 hours, and ClickHouse answers everything older.
   - Add the `tiered` storage policy with R2 and the cache disk, the nightly backup, and the R2 tests in [Durability](#durability).
   - `chSchema` runs `CREATE ... IF NOT EXISTS` at every start and cannot turn a table into a view, so step 1 is an explicit migration. It drops the rollup views, renames the `positions` table to `positions_old`, creates `receptions`, recreates the rollup views over it, and creates the `positions` view. `chConn.history` calls the view as `positions(mmsi = ..., from = ..., to = ...)`.
   - [#170](https://github.com/openwatersio/aiscast/pull/170) merges after this and is rebuilt on it: its `coverage` view reads `receptions` with the usable-copy filter, not `accepted`, and its backfill reads `receptions` too, since days loaded from the lake before it deploys never pass through its view.
   - The live writer writes every copy with `tx`, `accepted`, `station`, and the flags, and keeps the 5-minute ring for rebuilt copies.
   - Load the days since 2026-08-20 from the lake's `ais.receptions`, joined to `ais.positions` for the position fields, so ClickHouse holds the network's whole history.
   - Track reads follow [Reads](#reads), and `chRawKeep` goes.

   This stands on its own: live history stops expiring, and the lake is no longer needed for positions.
   - **Then a compact layout and `positions_1m`.** The first layout measured 19.2 bytes a row in production, about 1 GB a day. `receptions` names transmissions by time and one byte and stores the receive delay, at 11.4 bytes a row. `positions_1m`, a minute while moving and a heartbeat every 30 minutes per place while still, replaces `positions_15m` and `positions_1h`. A vessel is moving when it reports more than half a knot or is more than 50 m from where it was last moving, which catches drift. The schema moves in numbered steps that only add; the new columns join the first layout's in place, `aiscast convert-receptions` rewrites the old rows a day at a time through the live writer's own code, and `aiscast clickhouse-cleanup` drops the old rows, columns, rollups, and `positions_old` once every day is converted.
2. **The loader and MarineCadastre's last year.** `history_stage`, `vessel_statics`, `history_loads`, the loader's ClickHouse user and profile. The month moves wait for the R2 cold tier. The source registry entries. Metrics for the latest loaded day per source, rows loaded, and load failures, and an alert when a daily source is more than a week behind. Load 2025-10-01 to 2026-06-30, then check daily for new quarters. Check rows per day against the source files, and check drop rates. Render one vessel each in Puget Sound, the Gulf of Mexico, and the Great Lakes. This is the first step anyone can see.
3. **Vessel record import from `vessel_statics`.** Vessels known only from MarineCadastre get pages, appear in search, and appear in the sitemap.
4. **DMA.** The SQL for both schema eras. Load 2025-10-01 to now, then daily. Change DMA's row in `docs/policy.md`.
5. **HAIS and BSH.** Order HAIS for 2025-10-01 to 2026-08-19 and load it through staging. Load BSH daily.
6. **Further back.** MarineCadastre 2015 to 2025, DMA from 2006, HAIS year by year back to 2011, and the Finnish set. These are formats the earlier steps already read, so this step needs time, not code. MarineCadastre before 2015 is left out: it is in file geodatabases, and its MMSIs are scrambled.

`get_coverage`, `/v1/stats`, and the coverage page then say what history exists for each region and from when.

## Decisions to make

1. **Who sees history.** Anonymous and personal tokens reach only the last 48 hours of a track. DMA is three days behind and MarineCadastre two months, so under the current tiers only feeder and commercial tokens see any of this. The vessel record shows everyone the last historical position and the backdated `first_seen`. Undecided. One option is to open tracks older than 48 hours to every caller at one-hour steps, which `positions_1h` answers cheaply, and keep finer steps for feeder and commercial tokens.
