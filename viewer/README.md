# viewer

Live map of what aiscast is receiving: https://openwatersio.github.io/aiscast/ (GitHub Pages, deployed by `.github/workflows/pages.yml` on every push to `main` that touches `viewer/`). One static page, no build step. It is a client of aiscast like any other.

```sh
cd viewer && python3 -m http.server 8089   # then open http://localhost:8089/?server=localhost:8080
```

GitHub Pages serves the page at the project site, pointed at `ais.openwaters.io`. `?server=` picks another server. `?station=<id>` highlights one station's vessels, fits the map to what it hears, and shows its stats from `/v1/stations/{id}`. [token.html](token.html) is the self-serve token page. It defaults to `localhost:8080` when the page itself is served from localhost, and to `ais.openwaters.io` otherwise. The map position lives in the URL hash.

Vessels fill from `GET /v1/vessels?bbox=` and update over `/v1/stream`. Triangles point along heading (COG fallback), color is ship-type class, and opacity fades with age. A vessel disappears 30 minutes after its last report. Click a vessel for details and its last raw NMEA. The viewer extrapolates nothing: it draws a vessel where that vessel last reported.
