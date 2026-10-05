# Coverage map

Plan for [#30](https://github.com/openwatersio/aiscast/issues/30): one public map of where Open Waters AIS has vessel data for a period, from every source: feeds, stations, and historical archives alike. It answers "is my area on the feed?" before anyone has to ask. It also shows a prospective feeder the water their receiver would add.

The design research is [research/coverage-map-design.md](../research/coverage-map-design.md). A prototype tested it on about a week of the raw archive. It was a Python binner over every source's hourly files and a standalone MapLibre page, and it lives only in git history (`git show 0181f42:analysis/anchorages/coverage.py`). The coverage map is the one visualization that belongs in this repo. The other analyses from the same prototype belong outside it.

## What the prototype established

- **Count vessels, not messages.** A message count is a traffic map: the shipping lanes win however the map is styled. Unique vessels per cell removes the ship that reports every two seconds for an hour. It still cannot tell "no ships came" from "ships came and nobody heard them". Reception probability is the fix for that.
- **H3 hexagons, one resolution per zoom band.** Hexagons stay near equal-area at 71° N, where a 0.05° grid cell is a third the width it is at the equator. The prototype binned at resolution 7 and rolled up to 6, 5, 4, and 3 by zoom. At the lowest zooms, resolution 3 is the only one that reads. A 0.05° dot grid was tried first and dropped.
- **No heatmap.** MapLibre's heatmap normalizes per viewport, and smoothing paints coverage where nothing was heard. Smoothing also blurs the fringe, and the fringe is the recruiting signal.
- **Nothing where nothing was heard.** An empty cell renders as the basemap, never as "0 %". Opacity scales with evidence, so a cell heard a handful of times fades and a single ducting night does not read as coverage.
- **A source is a unit of failure.** Redundancy counts every station, and every aggregator as one source. Digitraffic, aisstream, and AISHub carry no receiver identity, so each reads as one source everywhere it reaches. For example, all of Finland reads as one source. That is true of the feed, not of their network.
- **Data hygiene.** Positions at exactly (0, 0) and the jitter around it are receivers with no GPS lock. AISHub's snapshots repeat every vessel, so sampling them undercounts density. One outlier position sets a station's bounding box to the Sahara, so a station's extent should come from cells, not a box.
- **Size.** A week of every source was 169 M positions in 352,000 resolution-7 cells, which made a 19 MB JSON file. The finest resolution had to load lazily for the viewport. Tiles are the answer at production scale.

## What we keep, and what changes

Kept: H3 cells, unique vessels as the measure, opacity by evidence, empty cells drawn as nothing, the zoom bands, and the rule that every source is a unit of failure.

Changed:

- **Receptions are the input, not the raw archive.** The server has already parsed and decoded every copy, marked implausible and bad-clock ones, and left out positions at null island. ClickHouse's `receptions` holds every copy from every source, so coverage needs no parsers of its own.
- **Aggregated per day as positions arrive, windowed at serving time.** ClickHouse keeps one more table, `coverage`, which a materialized view fills from `receptions` as they are written. The server sums a trailing window of its days. A new station appears the day after it starts feeding, and a station that stops fades out of the window on its own.
- **Vessels a day, not vessels in the window.** Distinct vessels do not add across days, so the measure is distinct vessels per day, averaged over the window. "About 40 vessels a day are heard here" also reads better than a window total.
- **Days heard is the evidence.** Of the days in the window, how many heard the cell at all. A cell heard on 1 of 7 days fades. A cell heard on all 7 is solid. This measure is easier to explain than a log of message counts, and it survives the AISHub sampling problem.
- **Vector tiles from the server, not PMTiles on R2.** The research proposed tippecanoe to PMTiles in a bucket. The window's aggregate is a few hundred thousand cells, small enough to hold in the server and tile on demand. That is what `/v1/vessels/tiles` already does. Coverage then lives in the `/v1` API next to everything else, and needs no public bucket, domain, or build step. A map needs only the TileJSON URL.
- **Presence and density first, redundancy second.** Redundancy needs every station that heard a position, and ClickHouse holds only the source whose copy was accepted until receptions join it. It also needs a decision about which sources count toward it, and a legend that explains fragility. The first map answers the question people actually ask: is there data for this water?
- **No per-source dropdown.** A station's own footprint belongs on its station page, drawn as a range outline, which is a later phase.

## Phase 1: where there is data

The smallest map that answers "is my area on the feed?", on the network page at `openwaters.io/ais/network`, beside the counts of what the network receives. Both answer what data the network has.

### ClickHouse: `coverage`

One row per (`day`, `res`, `cell`), beside `receptions` in the ClickHouse database the server writes:

| Column | Type | Meaning |
| --- | --- | --- |
| `day` | Date | The UTC day of the positions |
| `res` | UInt8 | H3 resolution, 3 through 6 |
| `cell` | UInt64 | H3 cell index at `res` |
| `vessels` | AggregateFunction(uniqExact, UInt32) | The distinct MMSIs heard in the cell that day |

- **Filled as receptions arrive.** A materialized view bins each batch written to `receptions`, skipping copies marked implausible or with a bad clock: each copy's cell at resolution 6, and the cells that contain it at 5, 4, and 3, so the levels nest exactly. The view sets `geotoh3_argument_order`, since ClickHouse releases have disagreed on whether `geoToH3` takes latitude or longitude first and a view keeps what it was created with.
- **Counted once.** Distinct sets merge as unions, so every copy of a transmission, and the same copies binned twice, count each vessel once. That makes a backfill over days the view already covers harmless.
- **Backfilled once.** The view sees only receptions written after it exists, and the receptions loaded from the lake before it are not among them. On first start the server bins the map's window from `receptions` and loads the map, then bins each older day back 13 months in the background, newest first and one day per query so the largest day stays within ClickHouse's memory cap, and records each in `coverage_backfilled`, an empty day too. A restart resumes with the days not yet recorded. Counting days back from today, rather than reading partitions, keeps the backfill independent of how `receptions` is partitioned.
- **Kept 13 months,** like the 15-minute rollup, so seasonal questions stay answerable.
- **Every position counts,** whatever its source: live feeds and stations, uncorroborated reports, and historical archives loaded into `receptions`. Coverage is where there is data for a period, not which source supplied it.
- **Bounded to what it keeps.** The view and the backfill bin only receptions within the 13 months coverage keeps, so a load of older history computes no cells its TTL would delete.

### Server: `/v1/coverage/tiles`

- **Loading.** Once ClickHouse connects and the backfill is done, and every hour after that, the server reads the 7 complete UTC days before today at resolutions 3 through 6. For each cell it keeps vessels a day (the window's sum of each day's distinct vessels, over the days with coverage) and days heard. Each cell's outline comes from `h3ToGeoBoundary` in the same query, so the server has no H3 code of its own. About 3 km edges at resolution 6 is finer than any receiver's range is known, and it keeps the server's copy small.
- **Tiles.** `GET /v1/coverage/tiles/{z}/{x}/{y}` answers a Mapbox Vector Tile with one polygon layer, `coverage`: a feature per cell with `vessels` (a day, one decimal) and `days` (1 to 7). The resolution follows the zoom band: resolution 3 below z5, 4 to z6, 5 to z8, 6 above. Tiles are built on demand from the in-memory cells and cached until the next load. The response carries `Cache-Control: public, max-age=3600`.
- **TileJSON.** `GET /v1/coverage/tiles.json` also carries `window`, with `from`, `to`, and `days`, which the key shows.
- **Limits.** The same tile rate limit as vessel tiles. The data is an aggregate with no vessel identity, so it needs no token and no area cap.
- **Without ClickHouse** the TileJSON answers 503, and the network page leaves the map showing vessels.

### Client: `/ais/network`

- The network page's loader fetches the coverage TileJSON beside `/v1/stats`. The home panel's browse list names coverage in the Network entry's hint.
- The map controller gets a coverage mode. The network page turns it on while it is open: the hexagon layer goes under the labels, and the vessels are hidden, since they would cover the hexagons they were counted in.
- Fill color is vessels a day on a four-step ramp of one blue: up to 1, up to 10, up to 100, and more than 100. A cell heard on some days but averaging under one vessel a day is in the first step; water nobody heard has no cell. Both themes start from the step that clears either basemap's water. The light theme darkens toward more vessels and the dark theme lightens. Opacity follows days heard. Hovering or tapping a cell says "About 12 vessels a day. Heard on all of the last 7 days".
- The key is a chip beside the status chip, the same height: "Vessels / day" and a segmented bar, each color labeled with the most it holds: 1, 10, 100, >100. Its hover text gives the window's dates. The page's developer prompt links `/v1/coverage/tiles.json` beside `/v1/stats`.

### Done when

- [x] ClickHouse keeps `coverage`, filled by a view and a backfill, with a test against a real ClickHouse server.
- [x] The server loads it, serves tiles, and has tests for the load, the zoom bands, polygon encoding, and a missing ClickHouse.
- [x] The page renders from a local server and passes the client's tests, typecheck, and style check.
- [x] [server/README.md](../server/README.md), [server/openapi.json](../server/openapi.json), and [client/README.md](../client/README.md) describe it.
- [ ] After the deploy, the backfill's log line, `coverage: binned N days of receptions`, and the first `coverage: N cells` line. ClickHouse's memory under the backfill's largest day stays inside its 4 GB cap.

## Later phases

Each is a separate issue once phase 1 ships.

1. **Redundancy.** Color by the number of distinct sources that heard a cell in the window: 1 (fragile), 2, and 3 or more. `receptions` holds every copy with its station, so a view can count stations per cell beside `coverage`. A historical archive counts as a source like any other, with its own name as its station, so a period the archives cover fills in with them and a recent week shows only what the live network heard. One decision remains: whether aggregators count fully or only toward a lower tier. A cell that only an aggregator hears is as fragile as one antenna, and one that can go away on the aggregator's terms. The same data answers "how much of this bounding box has two independent sources?" as a query, which makes a region's coverage target something to measure rather than judge.
2. **Where the network is deaf.** The inverse layer for recruiting. It shows cells with traffic that no station or open feed of ours hears, ranked by vessels a day. Aggregators see vessels the volunteer network cannot hear, so their rows supply the traffic that makes a gap measurable. It includes a ranked list of the largest gaps, which a monthly post can quote. It depends on redundancy's source classes.
3. **Reception probability.** Hammond and Peters: each vessel's reporting interval says how many reports it sent between two that were heard, and the missing ones are spread across the cells along its path. This replaces vessels a day as the fill, and fades out where coverage is uncertain. It needs per-vessel sequences, so it is a query over `positions`, not a sum of `coverage`.
4. **Coverage for a chosen period.** A period picker on the network page, such as last June, beside the trailing week. The server reads `coverage` for any range it keeps, and historical archives fill in the periods they cover. `coverage` keeps 13 months, and its view bins nothing older, so a period further back needs that bound raised. Days older than 13 months then come from a backfill over `receptions`, and the cost is the table's size, several hundred thousand rows a day.
5. **Per-station range outline.** On the station page: the farthest position heard in each bearing bucket over a rolling window, drawn as an outline around the station. This is what AIS-catcher, tar1090, and MarineTraffic converged on. It needs the station's location. AIS-catcher envelopes send it, and the server does not keep it yet. Other stations would need their operator to set it, which fits with station naming ([#51](https://github.com/openwatersio/aiscast/issues/51), [#53](https://github.com/openwatersio/aiscast/issues/53)).

## Open questions

- The window. Seven days tracks a station coming and going within a week. Thirty would be steadier where traffic is seasonal or sparse. Phase 1 uses 7 because it matches the other 7-day counts the API reports.
- Whether coverage belongs in MCP `get_coverage`, as "does the network hear this bbox, and how well". Probably yes once redundancy exists.
- The antimeridian. A cell that crosses 180° is drawn only on the side of its center, which leaves a sliver unfilled on the other side in the Bering Sea and the Pacific islands.
