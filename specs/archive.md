# The data archive

aiscast's durable output is an archive anyone can build on. Two layers: the raw per-source log the server already writes, and a derived layer of deduplicated, decoded Parquet built nightly from it ([#31](https://github.com/openwatersio/aiscast/issues/31)). The derived layer is the product. Analytics and visualizations drove its design, but only two consumers ship as part of this project: the coverage map and the history and track APIs ([#32](https://github.com/openwatersio/aiscast/issues/32)). Anchorage maps, route networks, draft studies, and whatever else people invent are external consumers reading the same files.

```
R2 raw archive (per source, hourly, source-native)
        │  nightly derive job: decode + dedupe
        ▼
R2 derived archive: Parquet, partitioned by day
        messages    one row per transmission, deduplicated
        receptions  one row per copy heard, with source and station
        vessels     latest-wins static data per MMSI
        │                                    │
        ▼                                    ▼
  coverage map (this project)      external consumers (DuckDB, pandas, anything that reads Parquet over HTTP)
```

## Raw layer

Exactly what the server writes today, unchanged: `<license>/<source>/YYYY/MM/DD/HH.gz`, append-only, one line per reception: `RFC3339 receive time \t station \t source-native payload`. Formats are per source: tag-blocked NMEA (kystverket, feeders), digitraffic multi-line JSON, aisstream JSON lines, aishub snapshot dumps. See [architecture.md](../docs/architecture.md) for the reception record and why it is kept lossless.

The raw layer is the source of truth and the derived layer is a pure function of it. Backfilling a new historical source means converting to the line format, writing objects under the dates it covers, and rerunning the derive job for that range. A better decoder or a fixed parser reruns the same way. No derived data may accumulate state outside this property.

## Derived layer

Three Parquet table families in R2 Data Catalog (Iceberg), partitioned by day, per [#31](https://github.com/openwatersio/aiscast/issues/31). Iceberg is what makes the consumer contract mechanical rather than promised: schema evolution enforces additive-only changes, atomic partition swap makes backfills and reprocessing safe, and the catalog is what R2 SQL and the [#32](https://github.com/openwatersio/aiscast/issues/32) Worker query. Both query paths (R2 SQL, DuckDB reading the catalog directly) get verified before anything is built on them. Day partitioning alone: sources are heavily overlapping copies of the same broadcast, so source is a fact about a reception, not a boundary in the data.

**messages** is one row per transmission. Duplication across sources is the norm, not the exception: the same broadcast arrives via kystverket, aisstream, and aishub, and a consumer counting positions must not count it three times. Identity follows the server's hot-path rule (canonical payload content within a 10 s window of canonical time). Fields are stored at wire precision: lat/lon as 1/600000 degree integers, SOG in 0.1 kn units, so the same transmission is byte-identical whether it arrived as raw NMEA or as a source's pre-decoded JSON. Columns: message id (payload hash), mmsi, canonical time, message type, position and kinematics at wire precision, decoded fields per type.

**receptions** is one row per copy heard: message id, source, station, receive time. This table is where the duplication lives on purpose. Coverage reads it, station health reads it, and receive-time deltas between stations for one message are the raw material for multilateration. Source and license tag are columns.

**vessels** is latest-wins static data per MMSI: name, callsign, type, dimensions, draught, with the time of last update.

Known limits, accepted: a stationary vessel can transmit byte-identical positions minutes apart, so time is part of the identity key; aishub snapshots arrive up to a minute late, so its receptions occasionally key to a neighboring message, which coverage tolerates.

The derive job also owns data hygiene, so every consumer inherits it instead of rediscovering it: net-buoy fleets in unallocated MID gaps (578-599) are flagged, null-island positions (no GPS lock, jitter around 0,0) are dropped, and out-of-range coordinates are dropped. Filters that are analysis choices rather than data defects (minimum stop duration, track-gap limits) stay in consumers.

## The contract with consumers

The archive is only a product if outsiders can rely on it.

- **Access.** Public R2 bucket. R2 egress is free, which is what makes a public bulk archive affordable at all.
- **Stability.** Schema changes are additive. A column's meaning and units never change; new columns and tables may appear. Breaking changes mean a new table family name, with the old one kept until consumers move.
- **Completeness.** A day partition is written once, after the day closes, by the nightly job. Consumers can treat the presence of a partition as "this day is done." Backfills and reprocessing replace whole day partitions atomically. The gap between now and the last closed day is served by aiscast's in-memory state through the [#32](https://github.com/openwatersio/aiscast/issues/32) APIs, not by the archive.
- **Attribution.** Consumers publishing derived work must credit kystverket (NLOD-2.0) and digitraffic (CC-BY-4.0); those licenses require it. All sources are cleared for use and redistribution, including AISHub (confirmed in writing 2026-08-22) and aisstream.
- **Privacy.** The archive necessarily contains per-MMSI data; that is what AIS is. First-party public products ship aggregates only. Consumer documentation states the norm: a stop event is where an identifiable small boat sleeps, publish grids and counts, not tracks of named pleasure craft.

## First-party consumers

**Coverage map.** Ships with this project because it is about the network itself: message density, redundancy (stations hearing each cell, where orange singles are one failure from a dead zone), and per-station footprints. It reads receptions, bins to a 0.05 degree grid, and bakes PMTiles served from R2 like the chart tiles. It doubles as the feeder-recruiting map: the fringe cells show where a new station adds the most.

**History and track APIs** ([#32](https://github.com/openwatersio/aiscast/issues/32)). `GET /v1/vessels/{mmsi}`, per-vessel tracks, and bbox history, served by a Worker over R2 SQL with caching, stitching aiscast's in-memory hot window over the archived days. These are the parameterized queries the baked-tile approach cannot serve, and they are why the derived layer lives in a catalog rather than bare Parquet paths.

## Prototypes

`analysis/anchorages/` holds working prototypes that validated the parsers and this design: stop-event detection with swing-radius classification (a boat at anchor swings 25 m or more, a docked boat sits within GPS noise), voyage slicing between stops, and the coverage grid, each with a MapLibre viewer. They are also the reference examples of what external consumers can build from the derived layer.

## Open questions

- Whether aishub's once-per-minute snapshot sampling needs a weighting hint in receptions so density comparisons across sources are honest.
- Dedup determinism: identical input has produced message counts differing by ~0.02% between runs, likely tie-ordering in the 10 s window grouping. Pin down before leaning on "id stable across reruns."
- The normalized-archive question: every new source currently needs a parser in both the server (Go) and the lake (Python). Archiving the server's already-normalized event stream would collapse the lake to one parser, at the cost of double-writing ingest. Parked; revisit when the next source lands.

## Implementation

The derive job is [lake/derive.py](../lake/README.md); its README carries the full schema, source handling table, and hygiene rules. The catalog is R2 Data Catalog on bucket `ais-lake`, and both query paths work against it: DuckDB attaches the catalog directly, and R2 SQL queries it server-side (the API token needs the R2 Data Catalog permission, plus R2 SQL Read for the latter). Messages keep every position report; downsampling is a consumer choice, not the archive's. Day reruns replace the day's rows, so backfills and parser fixes are reruns. Nightly runs belong on the box, which keeps the raw hour files on local disk: [server/deploy/derive.service](../server/deploy/derive.service) and derive.timer are the units, not yet installed.
