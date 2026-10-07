# Station page from ClickHouse

The station page answers two questions: what does this station add to the network, and can it be relied on. Every number on it that covers a period comes from ClickHouse. The server's memory keeps only what is live: whether the station is sending now, the vessels it reported last, and which vessel is its own.

This replaces step 2 of [clickhouse.md](clickhouse.md#after-a-go), station series, and carries out the later phase of [coverage-map.md](coverage-map.md) that puts a station's own cells on its page. It also carries out item 3 of [historical-sources.md](historical-sources.md#columns-to-capture), the `statics` table, because the vessel types on the page need it.

## What the page shows

| Part | Window | Source |
| --- | --- | --- |
| Name, nearby place, own vessel | | SQLite station names |
| Status dot, on the "Last heard" line under the title | live | server memory |
| Messages per hour chart, headed by the uptime gauge and percent | 7 days | `station_hours` |
| Vessels, unique vessels, and vessels by type | rolling 24 hours | `station_vessels`, `statics` |
| Footprint on the map, and its size | 7 days | `station_coverage` |
| Vessels it reported last | live | server memory |
| Station ID | | the row |

The station list shows each station's status, uptime gauge, vessels, and unique vessels, and sorts by unique vessels, vessels, or uptime. The title wraps between words; only a raw station id, shown when a station has no other name, breaks anywhere.

## Where things stand

- `receptions` holds every position copy with its `station`, `source`, and `accepted`, which is true for the copy delivered first. It does not record whether a copy was the station's own ship (`!AIVDO`) or a stale copy of a report the vessel had already superseded, as AISHub's late copies of volunteer reports are. Static messages reach ClickHouse only from archive loads, in `vessel_statics`.
- `station_coverage` keeps, per day, station, resolution, and H3 cell, the distinct vessels heard there. A view fills it, a backfill appends whole days and records each in `station_coverage_backfilled`, and replay, an archive reload, and `rebuild-positions-1m` rebuild it per day ([replay_clickhouse.go](../server/replay_clickhouse.go)). The server loads the last 7 days each hour, merged across stations, and serves tiles, and serves one station's with `?station=`. `coverage`, the same without stations named, is written beside it for one release.
- Per station, the server keeps in memory a 7-day hourly event ring saved to the usage file, for the access tiers' 24-hour counts, and the MMSIs it sent as its own ship, for the own vessel. `vessels`, `vessels_24h`, `vessels_exclusive_24h`, `uptime_7d`, `positions`, `duplicates`, and `first_seen` come from the station series, the per-source counts in `/v1/stats` and MCP `get_coverage` too, and the nearby-place label from coverage cells.

## Station identity

A rollup's `station` is the id a station page is addressed by, as `receptions` holds it: a volunteer's receiver, and a feed's full receiver or path id, such as `kystverket/2573010` or `barentswatch/terra`. Ingest already writes a volunteer's receptions under the receiver's own id whatever stream its TAG block names ([pipeline.go](../server/pipeline.go)), and a token's subject may itself contain `/`, so `station:harbor/east` and `station:harbor/west` are two stations and nothing splits an id at a slash. Receptions written before ingest folded streams into the receiver carry the stream as a suffix; only those rows are folded, by the first-slash rule the server uses now, which can merge two stations whose subjects contain a slash in that period. The station names lookup and the station page's redirect for old tagged links keep an id the list holds whole. Where the network map counts sources, it folds a feed's ids into its source at read time, so AISHub stays one source and BarentsWatch's paths count once together, as `coverage` counts them now.

## What ClickHouse gains for live ingest

- **`receptions.stale`**, a `Bool` added by a numbered `ADD COLUMN` step, so rows from before it read as false: the copy is older than a report the vessel had already sent, which live ingest keeps from subscribers and from the station counts. The live writer, replay, and the archive loader set it.
- **`station_own`**: `hour`, `station`, `mmsi`, and `last_ts` as a max, one row per station, own vessel, and hour received, from every message a station sends as its own ship (`!AIVDO`), position or static. An `AggregatingMergeTree` ordered by `(station, hour, mmsi)`, kept 35 days. The live writer and replay write it; max merges the same in any order and any number of times. Own-ship messages that are static never reach `receptions`, so this is the only place they are kept.

Rollups built from rows written before these exist count stale copies as heard and have no own-ship evidence; this is noted where those days are shown.

## Rollups

Every rollup reads `receptions` with `chUsable` and, on a database still holding receptions in the first layout, the same exclusion of converted days the track reads apply, so a reception and its converted copy count once.

- **`station_coverage`**: `day`, `station`, `res`, `cell`, `source`, and `vessels` as a `uniqExact(mmsi)` state, at the resolutions the coverage map draws. An `AggregatingMergeTree` ordered by `(day, station, res, cell, source)`: the network map's hourly read takes every resolution of a span of days, and one station's cells are a range within each day. A projection by station is not used, because on ClickHouse 26.8 a lightweight delete leaves the projection serving the deleted rows. `source` lets the network map count a feed's receivers as one source without parsing the station. Kept 13 months as `coverage` is. A view fills it as receptions are written, and a backfill bins the days before the view, as `coverage` is filled now. Sets merge as unions, so the view and backfill overlapping, or a day backfilled twice, count each vessel once. Every deletion from `receptions` rebuilds the days it touched by deleting them and binning them again, as replay does for `coverage` now. Coverage counts every usable copy, the station's own ship and stale copies included: a stale copy can be the only report from a cell, and it is still a position the network heard. It replaces `coverage`: the network map merges `vessels` across stations per cell, and counts the sources with a row for the cell after folding feeds into their source.
- **`station_hours`**: `hour`, `station`, `source`, `receptions`, and `first` (copies with `accepted`). Ordered by `(station, hour)`, monthly partitions, kept for the life of the data. Uptime is the share of hours with a reception from the later of the window's start and the station's first hour ever, every hour after it counting whether or not the station sent in it, and the current hour counting once it has one. A station silent for most of the week reads as down for most of it. The earliest hour is when the station started, back through all history after the backfill. `first` over `receptions` is the share it delivered first.
- **`station_vessels`**: `hour`, `station`, `mmsi`, `receptions`, and `last_ts`, the count and latest time of its copies in the hour. Ordered by `(station, hour, mmsi)`, kept 35 days. A rolling window reads the rows with `last_ts` inside it, so its edges are exact to the reception. A vessel is unique to a station when no other station has a row for it inside the same window. Stale copies are left out when the hour is binned. A vessel the station has a `station_own` row for with `last_ts` inside the window is left out at query time, every row of it, not only the own-ship reports. The exclusion is applied when the window is read, because a window moving past an own-ship report needs no rebuild to count the vessel again: a boat hearing itself is not reception, and AISHub's late copy of a volunteer's report would otherwise take away its uniqueness.

`station_hours` and `station_vessels` hold counts, which would double if a reception were binned twice, so they are rebuilt rather than appended to. A view on `receptions` writes the hour of every reception inserted into `station_dirty`, whatever its age and whichever path wrote it: live ingest, a late copy hours old, an archive load, a reload, or replay. Deletions from `receptions` do not fire a view, so every path that deletes marks what it deletes in `station_dirty` and rebuilds those days of `station_coverage`, and of `coverage` until it is dropped, so a rollback never serves coverage the deletion removed: replay marks the rows it is about to delete, an archive reload and `rebuild-positions-1m`, which follows a purge, mark the whole day. An archive reload also deletes its source's `statics` rows for the file's day before it loads, because a corrected file can drop a state that merging would otherwise keep. Recording a converted day in `receptions_converted` changes which rows the exclusion keeps, so it adds that day's hours too. A job in the server runs every 5 minutes: it takes each hour whose newest marker is more than a minute old and newer than the marker its last rebuild recorded in `station_built`, bins the hour again from `receptions` into both tables as a new version, deletes the hour's older versions, and records the marker. A marker younger than a minute may belong to an insert still in flight, so it waits for the next run. An hour rebuilt twice ends up the same, an hour whose receptions were all deleted ends up empty, and an interrupted run leaves its hours unrecorded for the next. Markers expire after 14 days and the record after 15, so a server down or failing for less than two weeks catches up. A day that fails is left for the next run and holds up no other. Each rebuild writes its hours as a new version and then deletes their older ones, and reads take each hour's newest, so an hour is never read empty or twice. The backfill bins every day `receptions` held before the view, newest first, beside the job, recording each day.

Rows are position receptions. Static messages do not count toward uptime or messages per hour, which is true of what `receptions` holds and does not change a station that sends anything.

## Static data

**`statics`** keeps every distinct static state each source sent for each vessel each day: `mmsi`, `day`, `source`, and the state, which is name, call sign, IMO, ship type, the four antenna offsets, draught, destination, and ETA as sent, with `first_ts` and `last_ts` as min and max. It is an `AggregatingMergeTree` ordered by `(mmsi, day, source, state)`, kept for the life of the data. Keeping sources apart lets a purge delete one source's rows and leave the others' times intact.

- The live writer adds a row for every static message, and archive loads add a row for every static message in a file. Rows of one vessel, day, source, and state merge into one, so a vessel repeating its data every six minutes is one row a day per source with the times it was first and last heard.
- Min, max, and set merge the same in any order and any number of times, so archive files loading newest first, reloads, and retries all leave the same rows.
- A change is a new state: ordering a vessel's states across sources by `first_ts` gives its renames, destinations, and ETAs. A vessel that returns to an earlier state on a later day shows both days. Within one day a returning state is one row spanning both times.
- At the step that ships it, every vessel in the record gets one row with its current state, timed at the record's static time, or its last seen where the record has none, with the source `record`. Those rows follow the record: a purge leaves them, as it leaves the record, and they are the only rows with that source.
- A vessel's type is the latest non-zero `ship_type` by `last_ts`.

`vessel_statics` stays as the record import's input for now. Folding it into `statics` is a later change.

## Where memory's other uses go

- **Nearby-place labels** come from the station's `station_coverage` cells over 7 days, recomputed when the coverage map reloads. A station gets a label from its cells, not from 5 vessels' last positions.
- **`vessels`**, the last 30 minutes, and the **per-source counts** in `/v1/stats` and MCP `get_coverage` read `station_vessels` over the last 30 minutes, at most 5 minutes behind.
- **Own vessel** detection stays in memory. It is the station's identity, persisted in SQLite, not history. Its candidates, every MMSI the station sent as its own with the time of the last, are read back from the last hour of `station_own` at start, so the rule that two candidates within an hour mean no own vessel holds across a restart, as the station vessels file holds it now.
- **Access tiers** keep the in-memory rings, which need the current hour.

The per-station vessel maps, `exclusive`, the station vessels file, and its load and save at start and stop go. The deploy removes the file from the box. `first_seen`, `positions`, and `duplicates` stay in the API, which deployed clients and the OpenAPI schema require: `first_seen` is the station's first hour in `station_hours`, or the live row's own where it has none, as a station sending only static messages does, `positions` its receptions, and `duplicates` its receptions less its first copies.

## API

- **`GET /v1/stations`**: each row adds `uptime_7d`, and `vessels_24h` and `vessels_exclusive_24h` come from the rollups, from one query for every station cached for a minute. Live fields stay as they are.
- **`GET /v1/stations/{id}`**: the station adds `hours`, the 168 hourly reception counts oldest first, and `types`, the 24-hour vessels and unique vessels by ITU ship type or kind, from `station_vessels` joined with `statics`. Cached for a minute per station.
- **`GET /v1/coverage/tiles.json?station=` and `/v1/coverage/tiles/{z}/{x}/{y}?station=`**: the coverage tiles for one station's cells over 7 days, at every resolution the network map draws, with the same layer and properties. The TileJSON's tile URLs carry the parameter, and its `bounds` are the extent of the station's cells and its `fit` the extent of the middle 90% of them. The server loads a station's cells from `station_coverage` the first time they are asked for after each network load and keeps them until the next, and the tile cache keys on the station. The station id is a query value, so an id holding a slash is read whole.

## Privacy

The per-station vessel counts and types inherit the vessel opt-out the [policy](../docs/policy.md) describes, which is not built yet. When it is, its suppression list applies to these rollups as it does to every other history read.

## Map

The station page draws its tiles with the coverage map's own layer, fill, and color ramp, at a lower opacity than the coverage map, beneath every vessel layer. The map fits the TileJSON's `fit`, and the size the page states is its extent.

## Sizing

Measured on the box before step 2: rows a day in `coverage` per resolution, and the average number of stations with a reception per cell a day, which bounds `station_coverage` at that average times `coverage`. `station_hours` is stations times hours, a few hundred thousand rows a year. `station_vessels` is about the vessels heard each hour per station, a million rows a day, for 35 days. `statics` takes a row for every static message, tens of millions a day, which merge to about one per vessel per state per day, a few hundred thousand.

## Order of work

1. **Ingest evidence.** `receptions.stale` and `station_own`, written by the live writer and replay. Archive rows are not late copies of a live report, so they write `stale` as false.
2. **`station_coverage`.** The table, view, and backfill, replay, archive reloads, and `rebuild-positions-1m` rebuilding it and `coverage`, the coverage tiles reading it, and `?station=` on them. A week of tiles from both tables is compared before the switch; they differ only in `stations` for volunteer ids holding a slash after the stream-fold cutoff, which `coverage` folds and `station_coverage` keeps whole. `coverage` and its view keep being written for one release, so the previous server still works on rollback, and a later change drops them.
3. **Station series.** `station_hours`, `station_vessels`, `station_dirty` and its view, the rebuild job and backfill, own-vessel detection reading its candidates from `station_own` at start in place of the station vessels file, the list fields, labels from cells, `/v1/stats` and MCP reading the rollup, and the names lookup keeping listed ids whole. The in-memory vessel maps, `exclusive`, and the station vessels file go.
4. **`statics`.** The table, the live writer's rows, the seed from the record, and archive loads writing through it.
5. **Station page.** `hours` and `types`, and the web client: the uptime gauge and sort, the messages chart, the vessels donut, and the footprint cells.

Each step is its own pull request, reviewed before it is first pushed, with an in-place upgrade test against a database holding the previous schema.
