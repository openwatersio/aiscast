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

Every source here is published for anyone to download, and none forbids storing or redistributing it. DMA publishes under the Danish public sector information act. Its [data management policy](https://www.dma.dk/safety-at-sea/navigational-information/ais-data/ais-data-management-policy-) has one condition: do not combine the data so that private individuals become identifiable. The vessel opt-out in [docs/policy.md](../docs/policy.md#privacy) already covers that for small craft. MarineCadastre's FAQ forbids retransmission under a Coast Guard instruction that the Coast Guard cancelled in 2021, and its 2024 release is labeled CC0. HAIS, BSH, and the Finnish set have open licenses. The DMA row in `docs/policy.md` changes from blocked to re-served under the PSI act when DMA loads.

## Where it lands

ClickHouse is the record of history. Every copy of every transmission is a row in `receptions`, from live feeds and archives alike. An archive row is a reception like any other, delivered days or months late by a feed the network does not run, just as AISHub delivers its rows a minute late. `positions` is a view that keeps the earliest copy of each transmission. Static data goes to `vessel_statics`. Nothing goes to the lake.

```
live writer ────────────────────────────────────────────────┐
source file → ClickHouse table function → history_in (Null) ┴→ receptions (local, then R2) ─┬→ positions_15m, positions_1h (accepted copies)
                                                │                                           ├→ coverage, coverage_stations
                                                └→ vessel_statics                           └→ positions (view)
```

### Tables

```sql
CREATE TABLE receptions (
    mmsi         UInt32,
    ts           DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
    tx           UInt64,
    recv_ts      DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD),
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
    clock_bad    Bool DEFAULT false
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (mmsi, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY TO VOLUME 'cold'
SETTINGS storage_policy = 'tiered', non_replicated_deduplication_window = 1000;
```

- `tx` names the transmission a copy belongs to: the accepted copy's event id, a payload hash, XORed with its time in milliseconds. Reads group a vessel's copies by it across up to a month, so it must not collide among a month of one vessel's transmissions. At 32 bits, a vessel reporting every 10 seconds would see several collisions a month, each merging two reports, so it is 64 bits. A random hash does not compress. Raw copies share `tx` through the payload hash. Rebuilt copies and archive rows take it from the copy they match (see [Deduplication](#deduplication)).
- `accepted` marks the copy that arrived first, the one the live server accepts today. The rollups read only accepted copies, so they get exactly what they get from `positions` now. A rebuilt or archive copy that matches an earlier one is not accepted. The flag is set at write time and is not updated after a purge. Every read that has to survive a purge uses the `positions` view, which recomputes the earliest copy.
- `station` is the station for a live copy and the archive's name for an archive copy. `source` sits beside it because redundancy classes come from the source kind.
- `corroborated` carries the live writer's corroboration rule, the one `/v1/nmea` and the AISHub feed apply, so bulk re-serving can leave out uncorroborated reports. Archive rows are corroborated by their source.
- `implausible` and `clock_bad` mark copies that history leaves out (see [Validity](#validity)). They are stored, not dropped, so a purge or a changed rule never needs a reload.

Measured on the box on 2026-10-02 over 44 days, `positions` costs 6.8 bytes per row. With `tx`, `recv_ts`, `station`, and the flags, a reception should cost 17 to 22 bytes.

The `tiered` policy has a local volume and a cold volume on R2, with a 10 to 20 GB local filesystem cache in front of R2. Merges on the cold volume are turned off, so R2 is written once per part. Partitions are monthly, because a table kept forever would collect thousands of daily partitions. Every part spans every vessel, because the sort is `(mmsi, ts)`, so a one-vessel read of a cold month touches every part in it. The number of parts per cold month is therefore what decides how fast a cold read is. Live parts merge locally for 30 days before they move. Archive rows are older than the TTL on arrival, so loads control their own moves (see [Loading](#loading)).

`positions` is a parameterized view over `receptions`. It keeps the earliest-received copy of each transmission in a vessel's time range:

```sql
CREATE VIEW positions AS
SELECT mmsi, tx, t AS ts, la AS lat6, lo AS lon6, src AS source FROM (
    SELECT mmsi, tx, argMin(ts, recv_ts) AS t, argMin(lat6, recv_ts) AS la, argMin(lon6, recv_ts) AS lo, argMin(source, recv_ts) AS src
    FROM receptions
    WHERE mmsi = {mmsi:UInt32} AND NOT clock_bad
      AND ts BETWEEN {from:DateTime64(3, 'UTC')} - INTERVAL 10 SECOND AND {to:DateTime64(3, 'UTC')} + INTERVAL 10 SECOND
    GROUP BY mmsi, tx
    HAVING NOT max(implausible)
) WHERE t BETWEEN {from:DateTime64(3, 'UTC')} AND {to:DateTime64(3, 'UTC')};
```

The motion columns follow the same pattern. The parameters put the vessel and time filter inside the grouping, so a query reads only that vessel's range. The 10 second margin catches copies of one transmission on either side of the boundary. Measured in ClickHouse 26.8 against 104 M test receptions: a vessel-day of 17,280 copies returned 8,640 transmissions, one each, and read 32,768 rows in 4 of 4,516 granules. A materialized view cannot do this job. It sees one insert at a time, but copies of a transmission arrive in different inserts, sometimes months apart, and it does not see deletes.

`positions_15m` and `positions_1h` fill from `receptions` through the same views as now, reading `WHERE accepted AND NOT implausible AND NOT clock_bad`. Each keeps the first position in its window. Reading only accepted copies matters: AISHub stamps are the main reason `despike()` exists, and if every copy reached the rollups, a mis-stamped AISHub copy could win a window and then be dropped on read, leaving the window empty. `positions_15m` keeps 13 months, and `positions_1h` keeps everything. Measured on the box, both cost about 20 bytes per row.

`history_in` is a `Null` table with every column a source can give: position, motion, and static data. One insert into it fills `receptions` and `vessel_statics` through two views, so each source file is downloaded and parsed once.

`vessel_statics` is an `AggregatingMergeTree` keyed by `(mmsi, source)`. It keeps each vessel's earliest report, its latest position, and the latest non-empty value of each static field. Archives feed it first. Once the live writer feeds it too, it replaces `ais.vessels` as the record import's input.

### Loading

The aiscast server runs the loads. It already holds the ClickHouse connection, the R2 client, a daily job, and `/metrics`, so loading needs no new service, timer, or runner. Once a day, after the record import, it lists each source's index and compares it with `history_loads`. That table has one row per source file: size, checksum or ETag, rows read, rows dropped by reason, rows matched to a stored transmission, new transmissions, and load time. Each new or changed file is one `INSERT INTO history_in SELECT ... FROM <table function>`. ClickHouse downloads, decompresses, and parses the file itself. A changed file first deletes its rows.

Loads must never starve the live writer. The writer treats any ClickHouse error as a refusal and drops a refused batch after 10 minutes, so a load that runs ClickHouse out of memory would lose live positions. Loads therefore run as their own ClickHouse user with a settings profile: `max_memory_usage` about 1.5 GB, `max_threads` and `max_insert_threads` 1 to 2, and the default insert block size of about a million rows. Default blocks keep each view's aggregation small. `max_server_memory_usage` keeps headroom above the profile. Step 1 also tries ClickHouse 26's `CREATE WORKLOAD` scheduling.

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
2. **A copy matches a stored one** with the same MMSI and the same position within 5 seconds, and takes its `tx`. It takes `implausible` too when any stored copy of that transmission has it, as the live writer flags dedupe's copies, because views that see one insert at a time, such as `coverage`, filter copy by copy. Feeds stamp the same transmission a second or two apart. A vessel underway reports every 2 to 10 seconds and moves between reports, and a moored one reports every 3 minutes, so the same position within 5 seconds is the same transmission. DMA's six decimal places round back to the exact wire value. MarineCadastre's five do not, so its match allows three wire units, about 5 m. Step 1's fixture files confirm each source's precision before the match is fixed.
3. **An unmatched copy** gets its own `tx`, a hash of its source and decoded fields, and is accepted.

**Rebuilt live copies** come from AISHub, aisstream, and BarentsWatch, which re-serve transmissions that Kystverket and stations deliver raw. AISHub runs about a minute behind, so by the time its copy arrives, a vessel underway has sent 6 to 30 newer positions, and `staleFor`'s comparison with the last position misses. The server therefore keeps a ring of each active vessel's accepted positions from the last 5 minutes, as `(ts, lat6, lon6, tx)`. That is about 60,000 vessels, 30 positions, and 24 bytes, or about 45 MB. A rebuilt copy that matches the ring takes that `tx` and is written with `accepted` false. Today these copies are withheld and never written. Writing them is what lets a purge fall back to them.

**Archive copies** are matched by the loader in SQL. The 5 second window and MarineCadastre's position tolerance cannot be equality keys or a single `ASOF` join. The loader joins on MMSI plus a time bucket and its neighbors, then filters. The other side is one day of `receptions` for the file's MMSIs, about 1 to 2 M rows for Danish waters, which fits in the loader's memory. Matching runs for every day that holds receptions from another source, which for live data means from 2026-08-20. Backfills of earlier days skip it. MarineCadastre is two months behind, so each of its quarterly loads overlaps live data and is matched.

A wrong match costs nothing permanent: both copies are stored, and only the choice between them depends on it.

`history_loads` records matched rows and new transmissions per file. In the overlap, the matched share measures how much of what an archive heard the live network also heard. On 2026-09-30, DMA heard 3,177 vessels between 54.5 and 57.8° N and 8 and 13° E, 1,596 class A and 1,581 class B. The live network heard 2,721 there in the 24 hours to 2026-10-03 19:35 UTC, almost all through AISHub.

Nothing is thinned at load.

### Validity

The `positions` view detects nothing, but it judges a transmission as a whole: one implausible copy keeps every copy of it out, since they all carry its position. Checks run in two places, as they do now: when a row is written, and when a track is drawn. The view and the rollups choose only among rows the write step has already marked.

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

`stale` is not a validity flag. The live stream withholds a report older than the vessel's newest so that the map never moves a vessel backward. A copy of a transmission already heard is a duplicate, handled in [Deduplication](#deduplication). A genuine report that arrives late, such as a satellite pass hours behind, is a valid position. `receptions` keeps it for history as an accepted copy, and the rollups put it in its own window, because they keep the earliest position whenever it arrives. The stream's rule is unchanged.

### Coverage

Coverage is where the network has data for a period, whatever the source. The `coverage` view from [#170](https://github.com/openwatersio/aiscast/pull/170) fills from `receptions` as rows are inserted, skipping implausible and bad-clock rows, so archive rows count like any other. It counts distinct vessels per cell, so duplicate copies change nothing. DMA's daily load, three days behind, fills in Danish waters for the days it covers. The view already skips positions older than `coverage`'s 13 months, so a backfill of older years computes no cells that `coverage` would then delete.

Per-station work reads aggregates, not `receptions`: a `coverage_stations` table of distinct vessels per resolution, day, cell, and station, filled from `receptions` by a view as `coverage` is. An archive's `station` is the archive itself, and it counts toward redundancy like any other source. A recent week shows mostly the live network. A period the archives cover fills in with them. If raw per-station reads are ever needed, a projection ordered by `(station, ts)` serves them without changing the sort key. Range outlines will need each copy's distance and bearing from its station. Those are two columns added when that work starts, unknown by default.

### Purging a source

Purging deletes the source's rows from `receptions` and `vessel_statics` with a mutation. The `positions` view then keeps each transmission's next-earliest copy, so a transmission another source also heard is never lost. The rollups and `coverage` keep one value per window or a set per cell, chosen when rows were inserted. `accepted` is not updated by a purge. So the months the source touched are rebuilt: drop each month's partition in the rollups and `coverage`, then insert it again from `receptions`. The rollups take the earliest copy per transmission, as the `positions` view does, instead of reading `accepted`. `coverage` uses `chCoverageSelect`. Purged months on R2 are read back through the cache. Purges are rare.

### Durability

ClickHouse holds the only copy of archive data. If it is lost, archive history comes back by reloading from the sources that still publish it, and live history by replaying the normalized archive.

The cold volume is a plain S3 disk on R2, with its metadata on the box. Hetzner's nightly backups cover the box, metadata included. A restored backup can point at R2 objects that a merge, purge, or reload deleted after it was taken, which leaves broken parts. Disabling merges on the cold volume narrows that risk, and it is accepted for now. Backups are revisited separately. Options include ClickHouse's incremental `BACKUP` to R2 and `s3_plain_rewritable`, which keeps metadata in the bucket but has no hard links for mutations and moves.

No R2 disk has been tested yet. Everything measured so far ran on local MergeTree. Step 1 tests R2 explicitly before anything depends on it: the endpoint, `region` auto, `s3_check_objects_after_upload`, multipart upload, `CopyObject`, a purge, a reload delete, and `MOVE PARTITION` both ways, on 26.8.

### Reads

The track endpoint keeps its rule: the coarsest table that holds the range and whose window divides the step. `receptions` holds every range, so `chRawKeep` goes away.

- **Steps of 15 minutes and up** read the rollups, which stay on local disk, so month and year tracks never touch R2. Measured on the box, cold-cache reads take 35 to 40 ms for a day, 25 to 29 ms for a month, and 29 to 37 ms for a year.
- **Steps under 15 minutes, including every position**, read the `positions` view, so each transmission appears once. The span is capped at 31 days. A busy vessel's year at full rate is millions of rows, partly on R2, and seconds per request. A longer range takes a 15-minute step. Fine steps work for any 31-day window in history.

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

Measured on the box on 2026-10-02: about 28 M accepted positions a day, `positions` at 6.8 bytes per row (about 190 MB a day), and both rollups at about 20 bytes per row.

| Store | One year | Notes |
| --- | --- | --- |
| Archive rows in `receptions`, on R2 | 150 to 170 GB | About $2.50 a month. DMA is about 20 M rows a day and MarineCadastre about 9 M, at 17 to 22 bytes each |
| Live rows in `receptions`, on R2 | 28 M a day times copies per transmission, at 17 to 22 bytes | Measure copies per transmission from the lake first: `ais.receptions` rows over `ais.positions` rows per day. At 1.5 copies, about 230 GB a year, $3.50 a month |
| `positions_15m`, local | 34 GB live plus 17 GB archive | Kept 13 months |
| `positions_1h`, local | 10 GB live plus 4 GB archive, each year | Kept indefinitely |
| R2 cache, local | 10 to 20 GB | Fixed |

The local rollups and the cache come to about 75 GB in the first year and grow by about 14 GB a year after, against the box's 126 GB free. A 100 GB Hetzner volume costs about €5 a month if they outgrow it. Part counts per partition are not yet measured: `SELECT table, partition, count() FROM system.parts WHERE active AND database = 'aiscast' GROUP BY 1, 2 ORDER BY 1, 2`.

## Order of work

Each step is one pull request. Each includes tests, the server README, `openapi.json` where a response changes, policy and attribution docs, and monitoring.

1. **`receptions` as the record.** Merges onto `main` ahead of [#197](https://github.com/openwatersio/aiscast/pull/197), which then rebases onto it to make ClickHouse the only track source. Until then the SQLite track store keeps answering the last 48 hours, and ClickHouse answers everything older.
   - Add the `tiered` storage policy with R2 and the cache disk, and run the R2 tests in [Durability](#durability).
   - `chSchema` runs `CREATE ... IF NOT EXISTS` at every start and cannot turn a table into a view, so step 1 is an explicit migration. It drops the rollup views, renames the `positions` table to `positions_old`, creates `receptions`, recreates the rollup views over it, and creates the `positions` view. `chConn.history` calls the view as `positions(mmsi = ..., from = ..., to = ...)`.
   - [#170](https://github.com/openwatersio/aiscast/pull/170) merges after this and is rebuilt on it: its `coverage` view reads `receptions` with the usable-copy filter, not `accepted`, and its backfill reads `receptions` too, since days loaded from the lake before it deploys never pass through its view.
   - The live writer writes every copy with `tx`, `accepted`, `station`, and the flags, and keeps the 5-minute ring for rebuilt copies.
   - Load the days since 2026-08-20 from the lake's `ais.receptions`, joined to `ais.positions` for the position fields, so ClickHouse holds the network's whole history.
   - Track reads follow [Reads](#reads), and `chRawKeep` goes.
   - Measure one overlap day before switching the rollups: count the 15-minute windows whose winner differs between accepted-only and all copies, and whether the all-copies winner fails `despike()`. That confirms reading `accepted` is needed.

   This stands on its own: live history stops expiring, and the lake is no longer needed for positions.
2. **The loader and MarineCadastre's last year.** `history_in`, `vessel_statics`, `history_loads`, the loader's ClickHouse user and profile, and the month moves. The source registry entries. Metrics for the latest loaded day per source, rows loaded, and load failures, and an alert when a daily source is more than a week behind. Load 2025-10-01 to 2026-06-30, then check daily for new quarters. Check rows per day against the source files, and check drop rates. Render one vessel each in Puget Sound, the Gulf of Mexico, and the Great Lakes. This is the first step anyone can see.
3. **Vessel record import from `vessel_statics`.** Vessels known only from MarineCadastre get pages, appear in search, and appear in the sitemap.
4. **DMA.** The SQL for both schema eras. Load 2025-10-01 to now, then daily. Change DMA's row in `docs/policy.md`.
5. **HAIS and BSH.** Order HAIS for 2025-10-01 to 2026-08-19 and load it through staging. Load BSH daily.
6. **Further back.** MarineCadastre 2015 to 2025, DMA from 2006, HAIS year by year back to 2011, and the Finnish set. These are formats the earlier steps already read, so this step needs time, not code. MarineCadastre before 2015 is left out: it is in file geodatabases, and its MMSIs are scrambled.

`get_coverage`, `/v1/stats`, and the coverage page then say what history exists for each region and from when.

## Decisions to make

1. **Who sees history.** Anonymous and personal tokens reach only the last 48 hours of a track. DMA is three days behind and MarineCadastre two months, so under the current tiers only feeder and commercial tokens see any of this. The vessel record shows everyone the last historical position and the backdated `first_seen`. Undecided. One option is to open tracks older than 48 hours to every caller at one-hour steps, which `positions_1h` answers cheaply, and keep finer steps for feeder and commercial tokens.
