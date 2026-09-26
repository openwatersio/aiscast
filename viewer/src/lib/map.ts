import * as maplibregl from "maplibre-gl";
// MapLibre builds its worker URL as `new URL(`./${name}`, import.meta.url)` with the name
// chosen at runtime, which no bundler can follow, so the asset is never emitted and the
// worker 404s. Naming it here statically lets Vite bundle it (it imports a shared chunk, so
// copying the file alone is not enough) and hands back the hashed URL to point MapLibre at.
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import { CLASS_COLORS, CLASS_LABELS, shipClass } from "./ais";
import type { BBox, Stream } from "./stream";

maplibregl.setWorkerUrl(workerUrl);

// Same basemap as openwaters.io/ais.
const BASEMAP = "https://tiles.openfreemap.org/styles/fiord";

/** Anonymous subscriptions cap here, so a wider viewport shows coverage instead of vessels. */
const AREA_CAP = 100;

export interface MapController {
  map: maplibregl.Map;
  /** A blank basemap is otherwise indistinguishable from empty water. */
  status(): { state: "loading" | "ready" | "error"; detail: string };
  /** Re-measure the floating panels so the camera centres on the visible map, not the viewport. */
  refreshInsets(): void;
  /** Draws the vessel the current route is about, and its session track. */
  setFocus(mmsi: number | undefined): void;
  flyToVessel(mmsi: number, fallback?: [number, number]): void;
  fitBBox(bbox: BBox): void;
  onSelect(fn: (mmsi: number) => void): void;
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

export function createMap(container: HTMLElement, stream: Stream): MapController {
  const map = new maplibregl.Map({
    container,
    style: BASEMAP,
    center: [-70.9, 41.6],
    zoom: 8,
    hash: "map",
    attributionControl: false,
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
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

    const panels = [...document.querySelectorAll<HTMLElement>("[data-map-inset]")]
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
    map.setPadding(padding);
  }

  let focus: number | undefined;
  let attribution: maplibregl.AttributionControl | undefined;
  const selectHandlers: Array<(mmsi: number) => void> = [];
  let ready = false;

  const viewBBox = (): BBox => {
    const b = map.getBounds();
    const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
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

  function vesselFeatures() {
    const now = Date.now();
    const features: GeoJSON.Feature[] = [];
    for (const [mmsi, v] of stream.vessels) {
      if (v.lat == null || v.lon == null) continue;
      const hdg = v.heading ?? v.cog;
      const ageMin = (now - v.seen) / 60e3;
      features.push({
        type: "Feature",
        id: mmsi,
        geometry: { type: "Point", coordinates: [v.lon, v.lat] },
        properties: {
          mmsi,
          name: v.name ?? "",
          hdg: hdg ?? 0,
          hasHdg: hdg != null,
          cls: shipClass(v.kind, v.shipType),
          focused: mmsi === focus,
          // The selected vessel never fades. A translucent icon lets the halo's fill show
          // through it, which reads as the ring being drawn over the arrow.
          opacity: mmsi === focus ? 1 : ageMin < 3 ? 1 : ageMin < 10 ? 0.65 : 0.35,
        },
      });
    }
    return { type: "FeatureCollection", features } as GeoJSON.FeatureCollection;
  }

  function trackFeature(): GeoJSON.FeatureCollection {
    const v = focus ? stream.vessels.get(focus) : undefined;
    if (!v || v.track.length < 2) return { type: "FeatureCollection", features: [] };
    return {
      type: "FeatureCollection",
      features: [
        {
          type: "Feature",
          geometry: { type: "LineString", coordinates: v.track.map(([lon, lat]) => [lon, lat]) },
          properties: {},
        },
      ],
    };
  }

  function updateAttribution() {
    if (attribution) map.removeControl(attribution);
    const esc = (s: string) => s.replace(/[&<>"]/g, (c) => `&#${c.charCodeAt(0)};`);
    const custom = [...new Set(stream.credits.values())].map((s) =>
      esc(s).replace(/https?:\/\/[^\s)]+/g, (u) => `<a href="${u}" rel="noopener">${u}</a>`),
    );
    attribution = new maplibregl.AttributionControl({ compact: true, customAttribution: custom });
    map.addControl(attribution);
    // Compact mode still renders expanded on creation, which puts every source credit
    // across the bottom of the chart. Collapse to the `i`; one click still shows them all,
    // which is what the per-source licences require.
    container
      .querySelector(".maplibregl-ctrl-attrib")
      ?.classList.remove("maplibregl-compact-show");
  }

  function render() {
    if (!ready) return;
    (map.getSource("vessels") as maplibregl.GeoJSONSource | undefined)?.setData(vesselFeatures());
    (map.getSource("track") as maplibregl.GeoJSONSource | undefined)?.setData(trackFeature());
  }

  map.on("load", () => {
    map.addImage("ship", shipIcon(), { sdf: true });


    map.addSource("track", { type: "geojson", data: emptyFC() });
    map.addLayer({
      id: "track-line",
      type: "line",
      source: "track",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#38bdf8", "line-width": 2, "line-opacity": 0.8 },
    });

    map.addSource("vessels", { type: "geojson", data: emptyFC(), promoteId: "mmsi" });
    map.addLayer({
      id: "vessel-halo",
      type: "circle",
      source: "vessels",
      filter: ["get", "focused"],
      paint: {
        "circle-radius": 15,
        "circle-color": "#38bdf8",
        "circle-opacity": 0.14,
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
      const mmsi = Number(e.features?.[0]?.properties?.mmsi);
      const v = stream.vessels.get(mmsi);
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

    for (const layer of ["vessel-still", "vessel-moving"]) {
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
        const mmsi = e.features?.[0]?.properties?.mmsi;
        if (mmsi) for (const fn of selectHandlers) fn(Number(mmsi));
      });
    }

    ready = true;
    updateView();
    render();
    applyInsets();
    runPendingCamera();
  });

  function updateView() {
    const bbox = viewBBox();
    // Over the cap the server refuses the subscription, so ask for nothing and let the
    // coverage layer carry the view rather than showing an empty ocean.
    stream.setView(bboxArea(bbox) <= AREA_CAP ? [bbox] : []);
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
  stream.subscribe(() => {
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
      const hidden = container.clientWidth === 0 || container.clientHeight === 0;
      return { state: "loading" as const, detail: hidden ? "(window has no size)" : "" };
    },
    refreshInsets: applyInsets,
    setFocus(mmsi) {
      focus = mmsi;
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
      requestCamera(() => map.flyTo({ center, zoom: Math.max(map.getZoom(), 12), speed: 1.4 }));
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
