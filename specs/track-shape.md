# Track shape

A vessel's track should follow the path it took. Today a track that names no `interval` is thinned to one position per round time step, so a vessel that sits still most of the day gets almost no points for the hour it moved, and the line cuts across land. This plan keeps the shape: the server reads the range at full detail, drops spikes, and simplifies the line to fit the limit. It also stops counting a moored vessel's GPS noise as movement in `positions_1m`, and raises the anonymous limit to 1,000 positions.

## What happens now

[server/track_api.go](../server/track_api.go) picks a default step with `defaultInterval`: the range divided by the limit, rounded up to a round step from 5 seconds to a day. The read keeps the first position in each step. Anonymous callers get 200 positions, personal tokens 1,000, feeder and above 5,000.

CERULEAN (368168720) on 2026-10-06, anonymous, as the web client asks:

| Range | Step | Positions | Positions over 1 kn |
| --- | --- | --- | --- |
| 24 hours | 10 minutes | 144 | 7 |
| 48 hours | 15 minutes | 192 | 14 |
| 7 days | 1 hour | 169 | 36 |

ClickHouse held 571 accepted positions for those 24 hours, 101 of them over 1 kn. The boat was tied up for about 23 of the 24 hours, and even steps spend the limit on that. At 7.8 kn a 10-minute step is 2.4 km. Each range picks different moments, so the drawn path changes with the range.

`positions_1m` ([server/clickhouse.go](../server/clickhouse.go)) keeps a row a minute while a vessel moves and a heartbeat every 30 minutes per place while it sits still. `anchor.still` in [server/tracks.go](../server/tracks.go) calls a report moving when it reports more than 0.5 kn, or when it is more than 50 m from where the vessel was last moving. CERULEAN reports 0.6 to 0.9 kn tied up, so 359 of its 458 rows on that day were one-minute rows from the dock. Across 2026-10-05, 0.69 M of 10.07 M one-minute rows came from vessel-hours that never left a 100 m box, 246,000 of them at 0.6 to 2 kn, from 14,165 vessels.

## Design

### Simplify by shape

When a request names no `interval`, the server:

1. Reads every position in the range. A range that starts in the last 48 hours reads the `positions` view, as now. Further back it reads `positions_1m` rows as they are, without grouping by a step. A range longer than 31 days groups `positions_1m` in ClickHouse to keep at most about 100,000 rows (a year is grouped to 6 minutes), the first position in each group, as the grouped reads do now.
2. Despikes the full-resolution positions with `despike`. Spikes are judged against real neighbors, not thinned ones.
3. Splits the positions into segments wherever the vessel went unheard (see [Breaks](#breaks)).
4. Simplifies each segment with Douglas–Peucker on synchronized Euclidean distance: a position's distance from where the line between its neighbors puts the vessel at that position's time. Distance alone would let a stop in the middle of a straight leg vanish; the time-synchronized distance keeps it, so the speed chart keeps the stop. The tolerance starts at 15 m, about GPS noise, so a track that fits the limit keeps every point that matters and draws the same path at 24 hours as at 48. Only when the kept positions exceed the limit does the tolerance grow, by binary search, until they fit. A segment always keeps its first and last position.

A vessel tied up for 23 hours keeps its arrival and its departure, and the hour underway keeps its corners. A ferry keeps each crossing's turns.

If a range has more segments than half the limit, the endpoints alone exceed it. The answer then keeps the newest `limit` positions and says `truncated`, as it does now.

An explicit `interval` keeps the current behavior: the first position per step, the newest `limit` when more match. `interval=0` keeps every position. Time series and GPX consumers that want even spacing ask for it.

### Breaks

A line is drawn only where there is evidence. The client splits a track at a silence longer than `trackGap(interval)`: 30 minutes plus two steps. A simplified track has no step, and collapsing a stay at the dock leaves hours between two kept positions while the vessel reported all along. So the server says where the vessel went unheard, from the full-resolution positions: a silence longer than 30 minutes in the `positions` view, or longer than 60 minutes in `positions_1m`, where a vessel sitting still has one heartbeat per 30-minute window and two can stand almost 60 minutes apart.

The answer carries `breaks`, the indexes of positions that start a new segment. A segment's first and last position are always kept, so a break falls between two kept positions. GPX writes one `trkseg` per segment.

### The answer

GeoJSON properties gain:

- `simplified`: true when the server simplified the track by shape.
- `tolerance_m`: the tolerance it used, 15 or more.
- `breaks`: segment starts, empty when the vessel was heard throughout.

`interval` is the resolution the positions were read at: 0 for the `positions` view, 60 for `positions_1m`, or the grouping step past 31 days. `truncated` keeps its meaning. [server/openapi.json](../server/openapi.json) documents the new fields. The geometry stays a `LineString` (or `Point`, or `null`), so existing clients keep working; they only lose the breaks they derived.

The MCP `get_vessel_track` tool gets the same default when `interval_minutes` is not given. Its schema and description change, so `mcpVersion` and `server.json` move together.

### Limits

Anonymous calls get 1,000 positions per track, as personal tokens do. Feeder and above stay at 5,000. The rate limit is unchanged. The cost of a track is the rows read, which does not depend on the limit, and the 31-day grouping bounds them. The MCP tool's own limits (50 by default, 200 at most) are unchanged.

`trackStep`, which rounds an anonymous or personal step past 48 hours to whole minutes, still applies to an explicit `interval`.

### The moving rule

A report is moving when the vessel is more than 50 m from its anchor, or when it has no anchor yet. Reported speed no longer counts. A vessel leaving at 3 kn passes 50 m in about 32 seconds, so its first one-minute row comes at most a minute later than now. A vessel drifting slowly still adds up past 50 m. The 0.3% of reports with no speed already go by distance.

`anchor.still` is the one rule for the live writer, `convert-receptions`, and the archive loads, so they change together. Days already written keep their verdicts in `receptions.moving` and their rows in `positions_1m`. Shape simplification drops dock noise within 15 m on its own, so old days draw well without a rebuild. Rewriting old verdicts is left until measurement shows a need.

On 2026-10-05 the rule would have kept up to 0.69 M fewer rows, about 6%. The measurement after the change is the rows per day in `positions_1m` against the 12.2 M on that day.

## Client

[client/app/lib/useTrack.ts](../client/app/lib/useTrack.ts) splits at `breaks` when the answer has them, and falls back to `trackGap(interval)` when it does not. The chart's speed line and the map's track use the same segments. The client ships before the server change, because a client without `breaks` support breaks the line at every collapsed stay.

## Order

1. Client: draw segments from `breaks`, with the fallback. Ships with or after #157.
2. Server: shape simplification, `breaks`, the new fields, GPX segments, the MCP default, the anonymous limit, and the moving rule. Tests: a synthetic dock-trip-dock track keeps the trip's corners and the dock's two ends; a stop mid-leg survives; a 2-hour silence makes a break and a 25-minute one does not; heartbeats 55 minutes apart in `positions_1m` do not; the tolerance grows only past the limit; a still vessel reporting 0.9 kn within 50 m is still. End to end against ClickHouse: the same, through the endpoint.
3. Measure on production: CERULEAN and a ferry at 24 hours, 48 hours, 7 days, and 30 days; request time for a year of a ferry; `positions_1m` rows per day.

## Open questions

- Whether the 15 m floor is right for fast vessels, whose one-minute rows are hundreds of meters apart. It only removes points the line already passes within 15 m of, so it cannot cut a corner wider than that.
- Whether old days need their moving verdicts rewritten, after the measurement above.
