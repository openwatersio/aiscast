import * as maplibregl from "maplibre-gl";
// MapLibre builds its worker URL as `new URL(`./${name}`, import.meta.url)` with the name
// chosen at runtime, which no bundler can follow, so the asset is never emitted and the
// worker 404s. Naming it here statically lets Vite bundle it (it imports a shared chunk, so
// copying the file alone is not enough) and hands back the hashed URL to point MapLibre at.
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import {
  bearing,
  CLASS_COLORS,
  CLASS_LABELS,
  interpolateAt,
  mergeTrack,
  shipClass,
  splitTrack,
} from "./ais";
import { publicApiBase } from "./api";
import type { BBox, Stream } from "./stream";

maplibregl.setWorkerUrl(workerUrl);

// Same basemap as openwaters.io/ais.
const BASEMAP = "https://tiles.openfreemap.org/styles/fiord";

/** The anonymous cap, until the stream's welcome frame says what this client may have. */
const DEFAULT_AREA_CAP = 100;

/** The server rebuilds a tile at most every 10 s, so reloading faster returns the same one. */
const TILE_REFRESH_MS = 15_000;

export interface MapController {
  map: maplibregl.Map;
  /** A blank basemap is otherwise indistinguishable from empty water. */
  status(): { state: "loading" | "ready" | "error"; detail: string };
  /** Re-measure the floating panels so the camera centres on the visible map, not the viewport. */
  refreshInsets(): void;
  /** Draws the vessel the current route is about, and its track. */
  setFocus(mmsi: number | undefined): void;
  /**
   * The positions the server holds for the focused vessel. `endedAt` is the time of its last
   * point, so live positions already covered by it are not drawn a second time.
   */
  setTrack(
    coords: Array<[number, number]>,
    endedAt?: number,
    times?: number[],
  ): void;
  /**
   * Reveal the track as far as this moment and mark the vessel's position there. Null
   * returns the whole track and resumes following the live positions.
   */
  scrubTo(at: number | null): void;
  /** Bring the whole track into the uncovered map, for starting playback. */
  fitTrack(): void;
  flyToVessel(mmsi: number, fallback?: [number, number]): void;
  fitBBox(bbox: BBox): void;
  /** A vessel clicked on the map, with the name its feature carries for the URL slug. */
  onSelect(fn: (mmsi: number, name?: string) => void): void;
  /** `overview` above the stream's area cap, where vessels come from tiles instead. */
  mode(): "live" | "overview";
}

function shipIcon(): ImageData {
  const s = 24;
  const c = document.createElement("canvas");
  c.width = c.height = s;
  const g = c.getContext("2d")!;
  g.beginPath();
  g.moveTo(s / 2, 1);
  g.lineTo(s - 3, s - 2);
  g.lineTo(s / 2, s * 0.7);
  g.lineTo(3, s - 2);
  g.closePath();
  g.fillStyle = "#000";
  g.fill();
  return g.getImageData(0, 0, s, s);
}

function bboxArea(b: BBox): number {
  return Math.abs(b[2] - b[0]) * Math.abs(b[3] - b[1]);
}

export function createMap(
  container: HTMLElement,
  stream: Stream,
): MapController {
  const map = new maplibregl.Map({
    container,
    style: BASEMAP,
    center: [-70.9, 41.6],
    zoom: 8,
    hash: "map",
    attributionControl: false,
  });
  map.addControl(
    new maplibregl.NavigationControl({ showCompass: false }),
    "top-right",
  );
  // MapLibre's own control: permission prompt, the accuracy circle, and the follow state
  // are all handled. Nothing here needs to know where the user is.
  map.addControl(
    new maplibregl.GeolocateControl({
      positionOptions: { enableHighAccuracy: true },
      trackUserLocation: true,
      showUserLocation: true,
    }),
    "top-right",
  );

  // A basemap that fails to load is otherwise a silent blank rectangle: MapLibre reports
  // style, tile and worker failures here and nowhere else.
  let mapError = "";
  map.on("error", (e) => {
    mapError = e.error?.message ?? "basemap failed to load";
    console.error("[map]", e.error ?? e);
  });

  // A map built while its container has no size stays broken after the container gains one:
  // MapLibre measures once and never re-measures on its own. That happens whenever the page
  // loads hidden, which covers a background tab, a collapsed panel, and a display:none
  // parent. Watching the box and resizing is the only thing that recovers it.
  new ResizeObserver(() => {
    if (container.clientWidth > 0 && container.clientHeight > 0) {
      map.resize();
      applyInsets();
      runPendingCamera();
    }
  }).observe(container);

  // A camera move needs a map that is loaded, sized, and already padded for the panels.
  // Route changes ask for one before all three are true, and issuing it early is worse than
  // useless: the move is computed against the wrong viewport and then cancelled by the
  // setPadding that follows. So the intent is held and replayed once the map can honour it.
  let pendingCamera: (() => void) | undefined;
  function requestCamera(move: () => void) {
    pendingCamera = move;
    runPendingCamera();
  }
  function runPendingCamera() {
    if (!pendingCamera || !ready) return;
    if (!container.clientWidth || !container.clientHeight) return;
    const move = pendingCamera;
    pendingCamera = undefined;
    applyInsets();
    move();
  }

  // The panels float over the map, so the viewport centre is not the centre of the map the
  // reader can see. Camera padding moves every flyTo, fitBounds and jumpTo into the
  // uncovered area, which is the difference between "centred" and "hidden behind a panel".
  const GAP = 16;
  function applyInsets() {
    const w = container.clientWidth;
    const h = container.clientHeight;
    if (!w || !h) return;

    const panels = [
      ...document.querySelectorAll<HTMLElement>("[data-map-inset]"),
    ]
      .map((el) => el.getBoundingClientRect())
      .filter((r) => r.width > 0 && r.height > 0);

    const padding = { top: 0, right: 0, bottom: 0, left: 0 };
    if (panels.length) {
      // Wide layout docks the panels down the left edge; narrow stacks them at the bottom.
      if (window.matchMedia("(min-width: 768px)").matches) {
        padding.left = Math.max(...panels.map((r) => r.right)) + GAP;
      } else {
        padding.bottom = h - Math.min(...panels.map((r) => r.top)) + GAP;
      }
    }
    // MapLibre cannot resolve a centre when padding leaves no room for one.
    padding.left = Math.min(padding.left, w * 0.75);
    padding.bottom = Math.min(padding.bottom, h * 0.75);
    // Setting padding is a camera jump, which cancels a flight in progress, so only a change
    // is applied.
    const current = map.getPadding();
    if (current.left !== padding.left || current.bottom !== padding.bottom || current.top || current.right) {
      map.setPadding(padding);
    }
    // Anything docked over the map centres in what the panes leave, not in the viewport. This
    // is the pane's own edge, without the camera's gap, which would shift it by that much.
    const edge =
      panels.length && window.matchMedia("(min-width: 768px)").matches
        ? Math.max(...panels.map((r) => r.right))
        : 0;
    document.documentElement.style.setProperty(
      "--map-left",
      `${Math.round(edge)}px`,
    );
  }

  let focus: number | undefined;
  let attribution: maplibregl.AttributionControl | undefined;
  const selectHandlers: Array<(mmsi: number, name?: string) => void> = [];
  let ready = false;

  const viewBBox = (): BBox => {
    const b = map.getBounds();
    const clamp = (v: number, lo: number, hi: number) =>
      Math.max(lo, Math.min(hi, v));
    return [
      clamp(b.getSouth(), -90, 90),
      clamp(b.getWest(), -180, 180),
      clamp(b.getNorth(), -90, 90),
      clamp(b.getEast(), -180, 180),
    ];
  };

  const colorExpr: any = [
    "match",
    ["get", "cls"],
    ...Object.entries(CLASS_COLORS).flat(),
    CLASS_COLORS.other,
  ];

  // shipClass() as an expression, for tile features, which carry kind and type but no class.
  const type: any = ["to-number", ["get", "type"], 0];
  const tileClassExpr: any = [
    "case",
    ["in", ["get", "kind"], ["literal", ["aton", "base", "sar"]]],
    ["get", "kind"],
    ["==", type, 30],
    "fishing",
    ["in", type, ["literal", [36, 37]]],
    "pleasure",
    ["all", [">=", type, 50], ["<=", type, 59]],
    "special",
    ["all", [">=", type, 60], ["<=", type, 69]],
    "passenger",
    ["all", [">=", type, 70], ["<=", type, 79]],
    "cargo",
    ["all", [">=", type, 80], ["<=", type, 89]],
    "tanker",
    "other",
  ];
  const tileColorExpr: any = [
    "match",
    tileClassExpr,
    ...Object.entries(CLASS_COLORS).flat(),
    CLASS_COLORS.other,
  ];
  // Tiles keep moored and anchored vessels for a week, so the fade runs longer than the
  // stream's: an hour-old berth is still where the boat is.
  const tileOpacityExpr: any = ["step", ["get", "age_s"], 1, 600, 0.65, 3600, 0.4];

  function vesselFeatures() {
    const now = Date.now();
    const features: GeoJSON.Feature[] = [];
    for (const [mmsi, v] of stream.vessels) {
      if (v.lat == null || v.lon == null) continue;
      // In the overview the tiles draw everyone. The stream still follows the open vessel by
      // MMSI, so it alone is drawn from here, live and haloed.
      if (overview && mmsi !== focus) continue;
      const at = mmsi === focus ? scrubPoint() : undefined;
      // While scrubbing, the icon is the vessel at the moment being replayed, pointed along
      // the track it was following. Its present position becomes the dot on the line.
      const lon = at ? at.point[0] : v.lon;
      const lat = at ? at.point[1] : v.lat;
      const ahead = at ? history[Math.min(at.index + 1, history.length - 1)] : undefined;
      const hdg =
        at && ahead ? bearing(history[at.index] ?? at.point, ahead) : (v.heading ?? v.cog);
      const ageMin = at ? 0 : (now - v.seen) / 60e3;
      features.push({
        type: "Feature",
        id: mmsi,
        geometry: { type: "Point", coordinates: [lon!, lat!] },
        properties: {
          mmsi,
          name: v.name ?? "",
          hdg: hdg ?? 0,
          hasHdg: hdg != null,
          cls: shipClass(v.kind, v.shipType),
          focused: mmsi === focus,
          // The selected vessel never fades. A translucent icon lets the halo's fill show
          // through it, which reads as the ring being drawn over the arrow.
          opacity:
            mmsi === focus ? 1 : ageMin < 3 ? 1 : ageMin < 10 ? 0.65 : 0.35,
        },
      });
    }
    return { type: "FeatureCollection", features } as GeoJSON.FeatureCollection;
  }

  // The history the server holds, set by the route. The session's own positions extend it so
  // the line reaches the vessel's current mark between fetches.
  let history: Array<[number, number]> = [];
  let historyTimes: number[] = [];
  let historyEnd = 0;

  /** A line per stretch the vessel was actually heard, so gaps are not drawn as passages. */
  function lines(
    coords: Array<[number, number]>,
    times: number[],
  ): GeoJSON.FeatureCollection {
    const segments = splitTrack(coords, times);
    return {
      type: "FeatureCollection",
      features: segments.map((seg) => ({
        type: "Feature",
        geometry: { type: "LineString", coordinates: seg },
        properties: {},
      })),
    };
  }

  // Set while the reader is scrubbing or replaying, as the moment being shown: the point of
  // replay is the past, so the live tail is left off.
  let scrubAt: number | null = null;

  function scrubPoint() {
    return scrubAt === null
      ? undefined
      : interpolateAt(history, historyTimes, scrubAt);
  }

  function trackFeature(): GeoJSON.FeatureCollection {
    const v = focus ? stream.vessels.get(focus) : undefined;
    const at = scrubPoint();
    if (at) {
      // Extend the solid line to the interpolated point between ordinary reports, which is
      // what makes playback glide. Never into a gap: there the vessel's course is unknown,
      // and only the dashed line underneath may cross it.
      const coords = history.slice(0, at.index + 1);
      const times = historyTimes.slice(0, at.index + 1);
      if (!at.inGap && coords.length) {
        coords.push(at.point);
        times.push(scrubAt!);
      }
      return lines(coords, times);
    }
    const live = (v?.track ?? []).filter(([, , t]) => t > historyEnd);
    return lines(mergeTrack(history, historyEnd, v?.track ?? []), [
      ...historyTimes,
      ...live.map(([, , t]) => t),
    ]);
  }

  function updateAttribution() {
    if (attribution) map.removeControl(attribution);
    const esc = (s: string) =>
      s.replace(/[&<>"]/g, (c) => `&#${c.charCodeAt(0)};`);
    const custom = [...new Set(stream.credits.values())].map((s) =>
      esc(s).replace(
        /https?:\/\/[^\s)]+/g,
        (u) => `<a href="${u}" rel="noopener">${u}</a>`,
      ),
    );
    attribution = new maplibregl.AttributionControl({
      compact: true,
      customAttribution: custom,
    });
    map.addControl(attribution);
    // Compact mode still renders expanded on creation, which puts every source credit
    // across the bottom of the chart. Collapse to the `i`; one click still shows them all,
    // which is what the per-source licences require.
    container
      .querySelector(".maplibregl-ctrl-attrib")
      ?.classList.remove("maplibregl-compact-show");
  }

  let trackKey = "";
  let historyKey = "";

  function renderTrack() {
    if (!ready) return;
    // The dashed line changes only when a new track arrives, never while scrubbing.
    if (historyKey !== `${history.length}|${historyEnd}`) {
      historyKey = `${history.length}|${historyEnd}`;
      // Unsplit on purpose. The solid line above is split, so wherever the vessel went
      // unheard only this shows through, which is the whole point: a dashed stretch says a
      // course was never reported rather than leaving a blank the eye reads as an end.
      (
        map.getSource("track-all") as maplibregl.GeoJSONSource | undefined
      )?.setData(
        history.length > 1
          ? {
              type: "FeatureCollection",
              features: [
                {
                  type: "Feature",
                  geometry: { type: "LineString", coordinates: history },
                  properties: {},
                },
              ],
            }
          : emptyFC(),
      );
    }

    const v = focus ? stream.vessels.get(focus) : undefined;
    const key = `${scrubAt}|${history.length}|${historyEnd}|${v?.track.length ?? 0}`;
    if (key === trackKey) return;
    trackKey = key;

    (map.getSource("track") as maplibregl.GeoJSONSource | undefined)?.setData(
      trackFeature(),
    );
    // Swapped while scrubbing: the dot is where the vessel is now, the icon is where it was.
    const scrubbing = scrubAt !== null;
    const live = focus ? stream.vessels.get(focus) : undefined;
    const head: [number, number] | undefined =
      scrubbing && live?.lon != null && live.lat != null ? [live.lon, live.lat] : undefined;
    (
      map.getSource("track-head") as maplibregl.GeoJSONSource | undefined
    )?.setData(
      head
        ? {
            type: "FeatureCollection",
            features: [
              {
                type: "Feature",
                geometry: { type: "Point", coordinates: head },
                properties: {},
              },
            ],
          }
        : emptyFC(),
    );
  }

  function render() {
    if (!ready) return;
    (map.getSource("vessels") as maplibregl.GeoJSONSource | undefined)?.setData(
      vesselFeatures(),
    );
    renderTrack();
  }

  map.on("load", () => {
    map.addImage("ship", shipIcon(), { sdf: true });

    map.addSource("track", { type: "geojson", data: emptyFC() });
    map.addSource("track-all", { type: "geojson", data: emptyFC() });
    map.addSource("track-head", { type: "geojson", data: emptyFC() });
    // The whole track, dashed, drawn once and never re-cut. A dash pattern starts at the
    // line's first coordinate, so a geometry that gains a new start on every scrub step
    // makes the dashes crawl. The solid line above covers whatever has been reached.
    map.addLayer({
      id: "track-all",
      type: "line",
      source: "track-all",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": "#38bdf8",
        "line-width": 2,
        "line-opacity": 0.3,
        "line-dasharray": [0.5, 3],
      },
    });

    map.addLayer({
      id: "track-line",
      type: "line",
      source: "track",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": "#38bdf8",
        "line-width": 2.5,
        "line-opacity": 0.85,
      },
    });

    map.addLayer({
      id: "track-head",
      type: "circle",
      source: "track-head",
      paint: {
        "circle-radius": 6,
        "circle-color": "#38bdf8",
        "circle-stroke-width": 2,
        "circle-stroke-color": "#0f172a",
      },
    });

    map.addSource("vessels", {
      type: "geojson",
      data: emptyFC(),
      promoteId: "mmsi",
    });
    map.addLayer({
      id: "vessel-halo",
      type: "circle",
      source: "vessels",
      filter: ["get", "focused"],
      // A dark disc, not a tint. Class colours run from green through orange to violet, and
      // every one of them has more contrast against near-black than against a pale wash of
      // the accent. The rim carries the selection; the disc carries the symbol.
      paint: {
        "circle-radius": 15,
        "circle-color": "#0b1220",
        "circle-opacity": 0.55,
        "circle-stroke-width": 2,
        "circle-stroke-color": "#38bdf8",
      },
    });
    map.addLayer({
      id: "vessel-still",
      type: "circle",
      source: "vessels",
      filter: ["!", ["get", "hasHdg"]],
      paint: {
        "circle-radius": 4,
        "circle-color": colorExpr,
        "circle-opacity": ["get", "opacity"],
        "circle-stroke-width": 1,
        "circle-stroke-color": "#0f172a",
      },
    });
    map.addLayer({
      id: "vessel-moving",
      type: "symbol",
      source: "vessels",
      filter: ["get", "hasHdg"],
      layout: {
        "icon-image": "ship",
        "icon-size": 0.7,
        "icon-rotate": ["get", "hdg"],
        "icon-rotation-alignment": "map",
        "icon-allow-overlap": true,
        "icon-ignore-placement": true,
      },
      paint: {
        "icon-color": colorExpr,
        "icon-opacity": ["get", "opacity"],
        "icon-halo-color": "#0f172a",
        "icon-halo-width": 1,
      },
    });
    map.addLayer({
      id: "vessel-label",
      type: "symbol",
      source: "vessels",
      minzoom: 11,
      layout: {
        "text-field": ["get", "name"],
        "text-size": 11,
        "text-offset": [0, 1.3],
        "text-anchor": "top",
        "text-optional": true,
      },
      paint: {
        "text-color": "#e2e8f0",
        "text-halo-color": "#0f172a",
        "text-halo-width": 1.5,
        "text-opacity": ["get", "opacity"],
      },
    });

    // Hovercard. setDOMContent rather than setHTML: a vessel name is operator-typed text
    // arriving off the air, so it never goes near an HTML parser.
    const hover = new maplibregl.Popup({
      closeButton: false,
      closeOnClick: false,
      offset: 12,
      className: "vessel-hovercard",
    });

    function showHover(e: maplibregl.MapLayerMouseEvent) {
      const f = e.features?.[0];
      const mmsi = Number(f?.properties?.mmsi);
      const live = stream.vessels.get(mmsi);
      // A tile feature carries its own summary; a stream feature is looked up for the latest.
      const p = f?.properties ?? {};
      const v = live
        ? live
        : f && f.geometry.type === "Point"
          ? {
              name: p.name as string | undefined,
              kind: p.kind as string | undefined,
              shipType: p.type as number | undefined,
              sog: p.sog as number | undefined,
              cog: p.cog as number | undefined,
              seen: Date.now() - Number(p.age_s ?? 0) * 1000,
              lon: f.geometry.coordinates[0],
              lat: f.geometry.coordinates[1],
            }
          : undefined;
      if (!v) return;

      const el = document.createElement("div");
      const title = el.appendChild(document.createElement("strong"));
      title.textContent = v.name ?? String(mmsi);

      const meta = el.appendChild(document.createElement("div"));
      meta.className = "meta";
      meta.textContent = [
        CLASS_LABELS[shipClass(v.kind, v.shipType)],
        v.sog != null ? `${v.sog.toFixed(1)} kn` : undefined,
        v.cog != null ? `${Math.round(v.cog)}°` : undefined,
      ]
        .filter(Boolean)
        .join(" · ");

      const age = el.appendChild(document.createElement("div"));
      age.className = "meta";
      const secs = Math.max(0, Math.round((Date.now() - v.seen) / 1000));
      age.textContent = `${v.name ? `MMSI ${mmsi} · ` : ""}${secs < 60 ? `${secs}s` : `${Math.round(secs / 60)} min`} ago`;

      hover.setLngLat([v.lon!, v.lat!]).setDOMContent(el).addTo(map);
    }

    for (const layer of ["vessel-still", "vessel-moving", "overview-still", "overview-moving"]) {
      map.on("mouseenter", layer, (e: maplibregl.MapLayerMouseEvent) => {
        map.getCanvas().style.cursor = "pointer";
        showHover(e);
      });
      map.on("mousemove", layer, showHover);
      map.on("mouseleave", layer, () => {
        map.getCanvas().style.cursor = "";
        hover.remove();
      });
      map.on("click", layer, (e: maplibregl.MapLayerMouseEvent) => {
        const props = e.features?.[0]?.properties;
        const mmsi = Number(props?.mmsi);
        const name = typeof props?.name === "string" && props.name ? props.name : undefined;
        if (mmsi) for (const fn of selectHandlers) fn(mmsi, name);
      });
    }

    ready = true;
    updateView();
    render();
    applyInsets();
    runPendingCamera();
    void addOverview();
  });

  // Above the stream's area cap, vessels come from the tile endpoint: a snapshot of every
  // vessel's last known position, rebuilt by the server every 10 s, which no tier's cap
  // applies to. Without it (a server that does not serve tiles) the wide view stays empty.
  let hasTiles = false;
  let overview = false;

  async function addOverview() {
    let tj: { tiles: string[]; minzoom?: number; maxzoom?: number; attribution?: string };
    try {
      const res = await fetch(`${publicApiBase()}/v1/vessels/tiles.json`);
      if (!res.ok) return;
      tj = await res.json();
    } catch {
      return;
    }
    // One credit linking to the per-source list, which is how the tiles are licensed to be
    // credited. The attribution control adds it beside the stream's per-source lines.
    map.addSource("overview", {
      type: "vector",
      tiles: tj.tiles,
      minzoom: tj.minzoom ?? 0,
      maxzoom: tj.maxzoom ?? 14,
      attribution: tj.attribution,
    });
    const hidden = { visibility: "none" as const };
    // Beneath the stream's layers, so the open vessel's halo and icon draw over its tile twin.
    const before = "vessel-halo";
    map.addLayer(
      {
        id: "overview-still",
        type: "circle",
        source: "overview",
        "source-layer": "vessels",
        filter: ["!", ["has", "hdg"]],
        layout: hidden,
        paint: {
          "circle-radius": ["interpolate", ["linear"], ["zoom"], 2, 1.5, 6, 3, 9, 4],
          "circle-color": tileColorExpr,
          "circle-opacity": tileOpacityExpr,
          "circle-stroke-width": ["step", ["zoom"], 0, 6, 1],
          "circle-stroke-color": "#0f172a",
        },
      },
      before,
    );
    map.addLayer(
      {
        id: "overview-moving",
        type: "symbol",
        source: "overview",
        "source-layer": "vessels",
        filter: ["has", "hdg"],
        layout: {
          ...hidden,
          "icon-image": "ship",
          "icon-size": ["interpolate", ["linear"], ["zoom"], 2, 0.3, 6, 0.55, 9, 0.7],
          "icon-rotate": ["get", "hdg"],
          "icon-rotation-alignment": "map",
          "icon-allow-overlap": true,
          "icon-ignore-placement": true,
        },
        paint: {
          "icon-color": tileColorExpr,
          "icon-opacity": tileOpacityExpr,
          "icon-halo-color": "#0f172a",
          "icon-halo-width": 1,
        },
      },
      before,
    );
    hasTiles = true;
    setInterval(() => {
      if (overview) map.refreshTiles("overview");
    }, TILE_REFRESH_MS);
    updateView();
  }

  function setOverview(on: boolean) {
    if (on === overview) return;
    overview = on;
    if (hasTiles) {
      for (const id of ["overview-still", "overview-moving"]) {
        map.setLayoutProperty(id, "visibility", on ? "visible" : "none");
      }
    }
    render();
  }

  /** Square degrees this client may subscribe to: 0 is unlimited, below 0 is MMSI-only. */
  function areaCap(): number {
    const area = stream.limits?.area;
    return typeof area === "number" ? area : DEFAULT_AREA_CAP;
  }

  function updateView() {
    const bbox = viewBBox();
    const cap = areaCap();
    // Over the cap the server refuses the subscription, so ask for nothing and let the tiles
    // carry the view rather than showing an empty ocean.
    const fits = cap === 0 || (cap > 0 && bboxArea(bbox) <= cap);
    stream.setView(fits ? [bbox] : []);
    if (ready) setOverview(!fits && hasTiles);
  }

  let moveTimer: ReturnType<typeof setTimeout>;
  map.on("moveend", () => {
    clearTimeout(moveTimer);
    moveTimer = setTimeout(updateView, 300);
  });

  // Bounds are known as soon as the map is constructed, so the subscription does not wait
  // on the style or on WebGL. A browser that cannot render the map still fills the vessel
  // list, and a slow tile server delays pixels rather than data.
  updateView();

  let creditCount = 0;
  let cap = areaCap();
  stream.subscribe(() => {
    // The welcome frame arrives after the first subscription, and a token raises the cap.
    if (areaCap() !== cap) {
      cap = areaCap();
      updateView();
    }
    render();
    if (stream.credits.size !== creditCount) {
      creditCount = stream.credits.size;
      updateAttribution();
    }
  });
  updateAttribution();

  return {
    map,
    status() {
      if (mapError) return { state: "error" as const, detail: mapError };
      if (ready) return { state: "ready" as const, detail: "" };
      const hidden =
        container.clientWidth === 0 || container.clientHeight === 0;
      return {
        state: "loading" as const,
        detail: hidden ? "(window has no size)" : "",
      };
    },
    refreshInsets: applyInsets,
    mode: () => (overview ? "overview" : "live"),
    setTrack(coords, endedAt, times) {
      history = coords;
      historyTimes = times ?? coords.map((_, i) => i);
      historyEnd = endedAt ?? 0;

      // The track and the vessel's position come from different endpoints, so the track can
      // end past where the icon sits: the recorded positions are current while the cached
      // one waits on the stream. Left alone the line runs on past its own vessel. The cache
      // keeps whichever is newer, so this is the same merge any late report gets.
      const last = coords[coords.length - 1];
      if (focus && last && endedAt) {
        stream.seed({ mmsi: focus, seen: endedAt, lon: last[0], lat: last[1] });
      }
      render();
    },
    fitTrack() {
      if (history.length < 2) return;
      const lons = history.map((c) => c[0]);
      const lats = history.map((c) => c[1]);
      requestCamera(() =>
        fit([
          Math.min(...lats),
          Math.min(...lons),
          Math.max(...lats),
          Math.max(...lons),
        ]),
      );
    },
    scrubTo(at) {
      scrubAt = at;
      render();
    },
    setFocus(mmsi) {
      if (mmsi !== focus) {
        history = [];
        historyTimes = [];
        historyEnd = 0;
        scrubAt = null;
      }
      focus = mmsi;
      stream.trackOnly(mmsi);
      if (mmsi) stream.follow(mmsi);
      render();
    },
    flyToVessel(mmsi, fallback) {
      // The server already knew where this vessel was when it rendered the page. Waiting for
      // the stream to say so again leaves the map parked somewhere else on a cold load, and
      // forever if the stream is refused (it is capped per address).
      const v = stream.vessels.get(mmsi);
      const center: [number, number] | undefined =
        v?.lat != null && v.lon != null ? [v.lon, v.lat] : fallback;
      if (!center) return;
      requestCamera(() =>
        map.flyTo({ center, zoom: Math.max(map.getZoom(), 12), speed: 1.4 }),
      );
    },
    fitBBox(bbox) {
      // fitBounds' own padding replaces the camera padding rather than adding to it, so the
      // panel insets have to be folded in here or the fitted box lands under a panel.
      requestCamera(() => fit(bbox));
    },
    onSelect(fn) {
      selectHandlers.push(fn);
    },
  };

  function fit(bbox: BBox) {
    const p = map.getPadding();
    map.fitBounds(
      [
        [bbox[1], bbox[0]],
        [bbox[3], bbox[2]],
      ],
      {
        padding: {
          top: (p.top ?? 0) + 40,
          right: (p.right ?? 0) + 40,
          bottom: (p.bottom ?? 0) + 40,
          left: (p.left ?? 0) + 40,
        },
        maxZoom: 11,
      },
    );
  }
}

function emptyFC(): GeoJSON.FeatureCollection {
  return { type: "FeatureCollection", features: [] };
}
