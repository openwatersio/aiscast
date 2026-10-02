# Coverage map design

How to visualize receiver-network coverage from received AIS messages. Research synthesized from a survey of real coverage maps (AIS, ADS-B, APRS, cellular), cartographic method, and MapLibre implementation options. Informs the coverage map in [specs/archive.md](../specs/archive.md) and the prototype in [analysis/anchorages/](../analysis/anchorages/).

## The core problem: traffic vs coverage

Message count per cell is a joint function of how many ships transmitted there and how well we heard them. A map of message counts is a traffic map wearing a coverage map's clothes. Shipping lanes dominate no matter the styling. The coverage map's real questions are:

1. Where can the network hear ships?
2. Where is coverage fragile (a single upstream source)?
3. Where would a new receiver help most?

Ecology solved the same observed-points-vs-effort problem with occupancy modeling: separate the state process (was a vessel there, transmitting?) from the observation process (did we hear it?).

## Metrics, from cheap to rigorous

- **Unique vessels per cell.** Removes the "one ship squawking every 2 seconds for an hour" distortion. Still conflates "no ships came" with "ships came, we missed them." Cheap: track MMSI sets during binning.
- **Presence/absence.** Binary: heard anything in the window. Answers question 1, throws away fragility.
- **Dwell-time reception rate.** For each vessel's time in a cell, compare observed message count to the count implied by its ITU-R M.1371 reporting interval (scheduled by class and speed). An afternoon of work from data we already have.
- **Reception probability (Hammond & Peters, [Journal of Navigation 2012](https://www.cambridge.org/core/journals/journal-of-navigation/article/estimating-ais-coverage-from-received-transmissions/C378EFBF3F17B85243382737861E5268)).** For consecutive reports from one vessel, the known reporting interval says how many transmissions should have occurred in between. Interpolate the path, assign missed transmissions fractionally to cells, estimate per-cell coverage with a Beta-binomial. The vessel is its own ground truth. Everything it needs (MMSI, time, speed, class) is in the archive.

An external traffic denominator (e.g. [EMODnet vessel density](https://www.emodnet-humanactivities.eu/documents/Vessel%20density%20maps_method_v1.5.pdf)) is the alternative, but it only covers EU waters and imports someone else's gaps.

## Redundancy counts every upstream source

Redundancy is the count of distinct sources hearing a cell: our stations, kystverket receivers, and each aggregator (aisstream, digitraffic, aishub) as one source each. A single aggregator is as fragile as a single station: if we lose the feed, we lose its coverage. Cells heard only via one aggregator are fringe cells, same as cells heard by one antenna.

## What existing projects do

Four paradigms in the wild:

1. **Per-station polar range outline.** Max observed range per bearing bucket over a rolling window, drawn as a stroked polygon around the station. AIS-catcher's webviewer, readsb/tar1090 (360 one-degree bins x 64 rolling intervals, outlier guard), and MarineTraffic's station dashboard all converged on this independently. The best per-station diagnostic (blocked bearings, antenna comparisons). Says nothing about reliability inside the lobe, and one ducting event distorts the shape.
2. **Raw dot scatter.** graphs1090, airplanes.live (up to 2M dots). High fidelity, unreadable at scale. The current prototype's failure mode.
3. **Fixed geographic grid.** aprs.fi (2 km cells, packet counts, 48 h window), CellMapper (per-tower polygons), AISHub's hex map. Generalizes across many stations; loses the directional diagnostic.
4. **Smoothed heatmap or contours.** nPerf (crowdsourced cellular) and modeled-propagation tools (CloudRF, Radio Mobile). Nobody in the observed-reception AIS/ADS-B world ships one: smoothing implies coverage where nothing was heard.

Lesson: geographic cells for the network-wide map, polar outlines for the per-station view, no KDE.

## Why hexagons, why not heatmap

**Heatmap (KDE) is rejected.** MapLibre's heatmap layer normalizes color per viewport, so the same density reads hot or cool depending on what else is on screen. Kernel smoothing blurs the coverage fringe, which is the recruiting signal. It cannot carry the station-count dimension at all.

**H3 hexagons are the right cell geometry.** Receiver footprints are roughly radial; hexes approximate circles with uniform neighbor distances. H3 cells are near-equal-area even at 71 N, where 0.05 degree lat/lon cells have shrunk ~3x in width. Multi-resolution is a solved pattern: bin fine, roll up to parents, switch resolution by zoom. Reference areas: res 4 = 1770 km2, res 5 = 253 km2, res 6 = 36 km2, res 7 = 5 km2. One caveat: an icosahedron pentagon vertex sits off the Norwegian coast ([h3-py #217](https://github.com/uber/h3-py/issues/217)); cosmetic for fill rendering, but never rely on H3 distance/adjacency math near it.

## Color and classification

Redundancy is a small integer, not a continuous field. A 4-class categorical map (0 / 1 fragile / 2 / 3+) is scannable and matches the operational question; a continuous ramp forces the reader to guess where "fragile" starts. Density needs a log-scaled sequential ramp (5 or fewer classes) and belongs on a separate toggle, not fused with redundancy in one bivariate encoding.

Honesty at the edges: scale opacity by evidence (a cell heard 3 times in 10 days fades, a cell heard 30,000 times is solid), and render no-traffic cells as nothing, never as "0% coverage." A minimum-observation threshold keeps a single ducting anomaly from reading as reliable coverage.

## Recommended design

- **Default view:** categorical redundancy hexmap (1 / 2 / 3+ sources), opacity scaled by evidence.
- **Density view:** unique vessels per cell (later: reception probability), log buckets, as a toggle.
- **Per-station view:** bearing-binned max-range polar outline, AIS-catcher style, when a station is selected.
- **Later:** an opportunity overlay (high traffic x low redundancy) as the literal feeder-recruiting answer, and reception-probability contours as a fade-out view.

## Implementation path

Prototype: bin with h3-py in coverage.py, emit cell ids and counts per resolution tier, render with h3-js and MapLibre fill layers switched by zoom. Production, per [specs/archive.md](../specs/archive.md): per-zoom H3 tiers to GeoJSON, tippecanoe to PMTiles (one archive, per-tier `-Z`/`-z` layers), served from R2 like the chart tiles. Client fetches tiles in view instead of parsing a 20 MB JSON. deck.gl is not warranted for pre-aggregated cells; a fill layer does it natively.

## Sources

- [Hammond & Peters, Estimating AIS Coverage from Received Transmissions](https://www.cambridge.org/core/journals/journal-of-navigation/article/estimating-ais-coverage-from-received-transmissions/C378EFBF3F17B85243382737861E5268)
- [AIS-catcher webviewer range plot](https://docs.aiscatcher.org/configuration/output/web-viewer/) and [range.js](https://github.com/jvde-github/AIS-catcher/blob/main/frontend/src/features/range.js)
- [readsb range outline](https://github.com/wiedehopf/readsb) / [tar1090 discussion](https://discussions.flightaware.com/t/comparing-tar1090-actual-range-plot-shapes/96965)
- [MarineTraffic station dashboard](https://support.marinetraffic.com/en/articles/9552968-ais-receiver-dashboard-statistics-and-map)
- [aprs.fi coverage grid](https://blog.aprs.fi/2008/09/digiigate-coverage-maps.html)
- [H3 resolution table](https://h3geo.org/docs/core-library/restable/), [why Kontur uses H3](https://www.kontur.io/blog/why-we-use-h3/)
- [tippecanoe](https://github.com/felt/tippecanoe), [PMTiles output](https://docs.protomaps.com/pmtiles/create)
- [MapLibre heatmap layer](https://maplibre.org/maplibre-gl-js/docs/examples/create-a-heatmap-layer/) and [KDE criticism](https://atlas.co/courses/gis-basics/heatmaps-and-kde/)
- [EMODnet vessel density method](https://www.emodnet-humanactivities.eu/documents/Vessel%20density%20maps_method_v1.5.pdf)
- [Uncertainty visualization](https://www.roger-beecham.com/vis-for-gds/class/08-class/)
