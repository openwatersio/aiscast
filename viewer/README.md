# viewer

Live map of what the hub is receiving. One static page, no build step; a client of the hub like any other.

```sh
cd viewer && python3 -m http.server 8089   # then open http://localhost:8089/?hub=localhost:8080
```

`?hub=` picks the hub (default `localhost:8080`). The map position is in the URL hash. Vessels fill from `GET /v1/vessels?bbox=` and update over `/v1/stream`; triangles point along heading (COG fallback), color is ship-type class, opacity fades with age, and a vessel disappears 30 minutes after its last report. Click a vessel for details and its last raw NMEA. Nothing is extrapolated: a vessel is drawn where it last reported.
