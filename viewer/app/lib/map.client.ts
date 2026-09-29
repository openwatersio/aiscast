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
import type { Theme } from "./theme";

maplibregl.setWorkerUrl(workerUrl);

// Fiord is the basemap on openwaters.io/ais. Positron is its light counterpart from the same
// server, with the same fonts.
const BASEMAP: Record<Theme, string> = {
  dark: "https://tiles.openfreemap.org/styles/fiord",
  light: "https://tiles.openfreemap.org/styles/positron",
};

/** Colours drawn on the map, which cannot read CSS tokens. */
const PALETTE: Record<Theme, { track: string; outline: string; label: string; labelHalo: string; disc: string; discOpacity: number }> = {
  dark: { track: "#38bdf8", outline: "#0f172a", label: "#e2e8f0", labelHalo: "#0f172a", disc: "#0b1220", discOpacity: 0.55 },
  light: { track: "#0284c7", outline: "#ffffff", label: "#0f172a", labelHalo: "#ffffff", disc: "#ffffff", discOpacity: 0.8 },
};

/** The anonymous cap, until the stream's welcome frame says what this client may have. */
const DEFAULT_AREA_CAP = 100;

// Tile reloads. Within the area cap the stream reports every vessel in view, so the tiles
// only need reloading where the stream cannot speak for them: ground a pan reveals, a vessel
// the stream lets go of, and moving vessels that went silent before the stream saw them.
// Past the cap there is no stream, and the tiles are all there is.

/** The server rebuilds a tile at most every 10 s, so reloading sooner returns the same one. */
const TILE_FRESH_MS = 10_000;
/** How often the view reloads past the area cap, where nothing else updates it. */
const TILE_OVERVIEW_MS = 15_000;
/** How often a still view within the cap reloads, to clear vessels the server has dropped. */
const TILE_BACKSTOP_MS = 5 * 60_000;

/** Seconds since the last report at which a vessel fades, and fades further. */
const FADE_STEPS = [600, 3600] as const;

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
  /**
   * Keeps the open vessel centred as it moves. Dragging the map or opening another vessel
   * lets go; `onCameraFollow` hears every change, whichever side made it.
   */
  followCamera(on: boolean): void;
  onCameraFollow(fn: (on: boolean) => void): () => void;
  /** `overview` above the stream's area cap, where vessels come from tiles instead. */
  mode(): "live" | "overview";
  /** Swaps the basemap and the colours drawn over it. */
  setTheme(theme: Theme): void;
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
  initialTheme: Theme,
): MapController {
  let theme = initialTheme;
  const map = new maplibregl.Map({
    container,
    style: BASEMAP[theme],
    center: [-70.9, 41.6],
    zoom: 8,
    hash: "map",
    attributionControl: false,
    // North up and flat, always. A chart that turns under a stray two-finger twist is a chart
    // that has to be put back before it can be read.
    dragRotate: false,
    pitchWithRotate: false,
    touchPitch: false,
    // The vessel tiles say max-age=10, and by default MapLibre reloads every tile in view as
    // it expires. Within the area cap the stream already reports what changes, so the tiles
    // reload on the rules in refreshTiles() instead. The basemap does not change.
    refreshExpiredTiles: false,
  });
  map.touchZoomRotate.disableRotation();
  map.keyboard.disableRotation();
  // A link made before rotation was off can still carry a bearing and a pitch in its hash, so
  // they are put back here. Not with maxPitch: 0, which makes such a hash invalid and loses the
  // link's position along with its tilt. And only after load: moving the camera any earlier
  // rewrites the hash before the link's own position has been read from it.
  map.once("load", () => {
    if (map.getBearing() !== 0 || map.getPitch() !== 0) map.jumpTo({ bearing: 0, pitch: 0 });
  });

  // Browser tests query what is drawn through this. Production builds leave it out unless
  // built for the tests.
  if (import.meta.env.DEV || import.meta.env.VITE_E2E) (window as { aiscastMap?: maplibregl.Map }).aiscastMap = map;
  // On a wide screen the controls sit bottom right, clear of the theme chip and the credits
  // at the top; on a phone the sheet covers the bottom, so they go top right. Decided once:
  // MapLibre places a control when it is added.
  const controls = window.matchMedia("(min-width: 768px)").matches ? "bottom-right" : "top-right";
  // Zoom buttons only with a mouse or trackpad; a touch screen pinches, as in a maps app.
  if (window.matchMedia("(pointer: fine)").matches) {
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), controls);
  }
  // MapLibre's own control: permission prompt, the accuracy circle, and the follow state
  // are all handled. Nothing here needs to know where the user is.
  map.addControl(
    new maplibregl.GeolocateControl({
      positionOptions: { enableHighAccuracy: true },
      trackUserLocation: true,
      showUserLocation: true,
    }),
    controls,
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

    // A panel that animates between resting places says where it will rest, so a camera
    // move made while it is still travelling is aimed at where the map will be visible.
    const panels = [
      ...document.querySelectorAll<HTMLElement>("[data-map-inset]"),
    ]
      .map((el) => {
        const r = el.getBoundingClientRect();
        const restTop = Number(el.dataset.restTop);
        return Number.isFinite(restTop) && el.dataset.restTop ? new DOMRect(r.x, restTop, r.width, r.height) : r;
      })
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

  // One style for both sources, so a vessel looks the same whether its tile or the stream is
  // drawing it. Both carry kind, type, hdg and age_s; shipClass() is written out here because
  // the tiles carry no class.
  const type: any = ["to-number", ["get", "type"], 0];
  const classExpr: any = [
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
  const colorExpr: any = ["match", classExpr, ...Object.entries(CLASS_COLORS).flat(), CLASS_COLORS.other];
  // The tiles keep moored and anchored vessels for a week, so an hour-old berth is still where
  // the boat is. The selected vessel never fades: a translucent icon lets the halo's fill show
  // through, which reads as the ring being drawn over the arrow.
  const fadeExpr: any = [
    "case",
    ["boolean", ["get", "focused"], false],
    1,
    ["step", ["get", "age_s"], 1, FADE_STEPS[0], 0.65, FADE_STEPS[1], 0.4],
  ];
  // A tile's copy of a vessel the stream is drawing is hidden, not removed: tiles cannot be
  // edited, but feature state can be set on them by id, and it survives a tile reload.
  const tileFadeExpr: any = ["case", ["boolean", ["feature-state", "live"], false], 0, fadeExpr];
  const iconSize: any = ["interpolate", ["linear"], ["zoom"], 2, 0.3, 6, 0.55, 9, 0.7];
  const dotRadius: any = ["interpolate", ["linear"], ["zoom"], 2, 1.5, 6, 3, 9, 4];

  /** The vessel layers for one source: dots for the stationary, arrows for the moving, names. */
  function vesselLayers(
    prefix: string,
    source: string,
    sourceLayer: string | undefined,
    opacity: any,
  ): maplibregl.LayerSpecification[] {
    const from = sourceLayer ? { source, "source-layer": sourceLayer } : { source };
    return [
      {
        id: `${prefix}-still`,
        type: "circle",
        ...from,
        filter: ["!", ["has", "hdg"]],
        paint: {
          "circle-radius": dotRadius,
          "circle-color": colorExpr,
          "circle-opacity": opacity,
          "circle-stroke-width": ["step", ["zoom"], 0, 6, 1],
          "circle-stroke-color": PALETTE[theme].outline,
          "circle-stroke-opacity": opacity,
        },
      },
      {
        id: `${prefix}-moving`,
        type: "symbol",
        ...from,
        filter: ["has", "hdg"],
        layout: {
          "icon-image": "ship",
          "icon-size": iconSize,
          "icon-rotate": ["get", "hdg"],
          "icon-rotation-alignment": "map",
          "icon-allow-overlap": true,
          "icon-ignore-placement": true,
        },
        paint: {
          "icon-color": colorExpr,
          "icon-opacity": opacity,
          "icon-halo-color": PALETTE[theme].outline,
          "icon-halo-width": 1,
        },
      },
      {
        id: `${prefix}-label`,
        type: "symbol",
        ...from,
        minzoom: 11,
        layout: {
          "text-field": ["coalesce", ["get", "name"], ""],
          // The basemap's glyph server has only the fonts its style uses. MapLibre's default
          // stack is not among them, so without this every label 404s.
          "text-font": ["Noto Sans Regular"],
          "text-size": 11,
          "text-offset": [0, 1.3],
          "text-anchor": "top",
          "text-optional": true,
        },
        paint: {
          "text-color": PALETTE[theme].label,
          "text-halo-color": PALETTE[theme].labelHalo,
          "text-halo-width": 1.5,
          "text-opacity": opacity,
        },
      },
    ] as maplibregl.LayerSpecification[];
  }

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
      const ageS = at ? 0 : (now - v.seen) / 1000;
      const properties: Record<string, unknown> = {
        mmsi,
        kind: v.kind,
        // The fade step rather than the age, so a feature changes only when it crosses one
        // and the diff sent to the map stays small.
        age_s: ageS < FADE_STEPS[0] ? 0 : ageS < FADE_STEPS[1] ? FADE_STEPS[0] : FADE_STEPS[1],
        focused: mmsi === focus,
      };
      if (v.name) properties.name = v.name;
      if (v.shipType) properties.type = v.shipType;
      if (hdg != null) properties.hdg = hdg;
      features.push({
        type: "Feature",
        id: mmsi,
        geometry: { type: "Point", coordinates: [lon!, lat!] },
        properties,
      });
    }
    return features;
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
    const custom = ["Not for navigation", ...new Set(stream.credits.values())].map((s) =>
      esc(s).replace(
        /https?:\/\/[^\s)]+/g,
        (u) => `<a href="${u}" rel="noopener">${u}</a>`,
      ),
    );
    attribution = new maplibregl.AttributionControl({
      compact: true,
      customAttribution: custom,
    });
    // Top right on every screen: at the bottom a phone's sheet would cover it, and the sources'
    // licences need it reachable.
    map.addControl(attribution, "top-right");
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

  // What the overlay holds, by MMSI: the key it was last sent with, and where it was drawn.
  const drawn = new Map<number, { key: string; at: [number, number] }>();

  /** Sends the map only the vessels that changed since the last frame. */
  function renderVessels() {
    adoptFromTiles();
    const add: GeoJSON.Feature[] = [];
    const current = new Set<number>();
    for (const f of vesselFeatures()) {
      const id = f.id as number;
      current.add(id);
      const key = JSON.stringify([f.geometry, f.properties]);
      if (drawn.get(id)?.key === key) continue;
      drawn.set(id, { key, at: (f.geometry as GeoJSON.Point).coordinates as [number, number] });
      add.push(f);
    }
    const remove = [...drawn.keys()].filter((id) => !current.has(id));
    // A vessel let go of while still in view shows its tile copy again, which is only as
    // current as the tile. Out of view, it went because the viewport moved, and the tiles
    // reload when a move starts.
    const bounds = map.getBounds();
    if (remove.some((id) => bounds.contains(drawn.get(id)!.at))) refreshTiles();
    for (const id of remove) drawn.delete(id);
    if (add.length || remove.length) {
      void (map.getSource("vessels") as maplibregl.GeoJSONSource | undefined)?.updateData({ add, remove });
    }
    hideTileCopies(current);
  }

  const hiddenInTiles = new Set<number>();

  function hideTileCopies(live: Set<number>) {
    if (!hasTiles) return;
    for (const id of live) {
      if (hiddenInTiles.has(id)) continue;
      map.setFeatureState({ source: "tiles", sourceLayer: "vessels", id }, { live: true });
      hiddenInTiles.add(id);
    }
    for (const id of hiddenInTiles) {
      if (live.has(id)) continue;
      map.removeFeatureState({ source: "tiles", sourceLayer: "vessels", id }, "live");
      hiddenInTiles.delete(id);
    }
  }

  // MMSIs already looked up in the tiles, so one missing from them is not asked for every
  // frame. Cleared when the tiles reload.
  const askedTiles = new Set<number>();

  /** Gives the stream the particulars the tiles already hold for vessels it has only positions for. */
  function adoptFromTiles() {
    if (!hasTiles) return;
    const missing: number[] = [];
    for (const [mmsi, v] of stream.vessels) {
      if ((v.name && v.shipType) || askedTiles.has(mmsi)) continue;
      askedTiles.add(mmsi);
      missing.push(mmsi);
    }
    if (!missing.length) return;
    const found = map.querySourceFeatures("tiles", {
      sourceLayer: "vessels",
      filter: ["in", ["get", "mmsi"], ["literal", missing]],
    });
    for (const f of found) {
      const p = f.properties;
      stream.adopt(Number(p.mmsi), {
        name: typeof p.name === "string" ? p.name : undefined,
        kind: p.kind,
        shipType: typeof p.type === "number" ? p.type : undefined,
      });
    }
  }

  function render() {
    if (!ready) return;
    renderVessels();
    renderTrack();
    followFocus();
  }

  // Camera follow, for the open vessel. The camera eases to each new position rather than
  // jumping, so a moving vessel slides across the chart instead of the chart lurching.
  let cameraFollows = false;
  let followedAt = "";
  const followListeners = new Set<(on: boolean) => void>();
  function setCameraFollow(on: boolean) {
    if (on === cameraFollows) return;
    cameraFollows = on;
    followedAt = "";
    for (const fn of followListeners) fn(on);
    if (on) followFocus();
  }
  function followFocus() {
    if (!cameraFollows || focus == null) return;
    const v = stream.vessels.get(focus);
    if (v?.lat == null || v.lon == null) return;
    const at = `${v.lon},${v.lat}`;
    if (at === followedAt) return;
    followedAt = at;
    map.easeTo({ center: [v.lon, v.lat], duration: 1000 });
  }
  // Only a person drags; a camera move made here does not start a drag.
  map.on("dragstart", () => setCameraFollow(false));

  // Hovercard. setDOMContent rather than setHTML: a vessel name is operator-typed text
  // arriving off the air, so it never goes near an HTML parser.
  const hover = new maplibregl.Popup({
    closeButton: false,
    closeOnClick: false,
    offset: 12,
    className: "vessel-hovercard",
  });

  // The first feature under the pointer that is showing. A tile's copy of a vessel the stream
  // is drawing is invisible but still answers queries, at wherever the tile last had it.
  const visible = (e: maplibregl.MapLayerMouseEvent) => e.features?.find((f) => !f.state?.live);

  function showHover(e: maplibregl.MapLayerMouseEvent) {
    const f = visible(e);
    if (!f) return;
    map.getCanvas().style.cursor = "pointer";
    const mmsi = Number(f.properties?.mmsi);
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

  for (const layer of ["vessel-still", "vessel-moving", "tile-still", "tile-moving"]) {
    map.on("mousemove", layer, showHover);
    map.on("mouseleave", layer, () => {
      map.getCanvas().style.cursor = "";
      hover.remove();
    });
    map.on("click", layer, (e: maplibregl.MapLayerMouseEvent) => {
      const props = visible(e)?.properties;
      const mmsi = Number(props?.mmsi);
      const name = typeof props?.name === "string" && props.name ? props.name : undefined;
      if (mmsi) for (const fn of selectHandlers) fn(mmsi, name);
    });
  }

  // Sources, layers, and images belong to the style, so swapping the basemap for a theme
  // removes them. This puts them back, on the first style and on every swap.
  map.on("style.load", () => {
    const c = PALETTE[theme];
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
        "line-color": c.track,
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
        "line-color": c.track,
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
        "circle-color": c.track,
        "circle-stroke-width": 2,
        "circle-stroke-color": c.outline,
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
      // A disc of the theme's own background, not a tint. Class colours run from green
      // through orange to violet, and every one of them stands out more against that than
      // against a wash of the accent. The rim carries the selection; the disc the symbol.
      paint: {
        "circle-radius": 15,
        "circle-color": c.disc,
        "circle-opacity": c.discOpacity,
        "circle-stroke-width": 2,
        "circle-stroke-color": c.track,
      },
    });
    for (const layer of vesselLayers("vessel", "vessels", undefined, fadeExpr)) map.addLayer(layer);

    // What the map held went with the old style, so everything is sent again.
    drawn.clear();
    hiddenInTiles.clear();
    historyKey = "";
    trackKey = "";
    hasTiles = false;
    if (tileJSON) addTileLayers();

    ready = true;
    updateView();
    render();
    applyInsets();
    runPendingCamera();
  });

  // Every vessel's last known position comes from the tile endpoint, at every zoom: a snapshot
  // the server rebuilds every 10 s, which no tier's area cap applies to. Within the cap the
  // stream draws over it live. Without tiles (a server that does not serve them) the stream
  // draws alone and the wide view stays empty.
  let tileJSON: { tiles: string[]; minzoom?: number; maxzoom?: number; attribution?: string } | undefined;
  // True while the current style has the tile source.
  let hasTiles = false;
  // Past the stream's area cap, where the stream follows only the open vessel.
  let overview = false;
  let lastRefresh = 0;

  void (async () => {
    try {
      const res = await fetch(`${publicApiBase()}/v1/vessels/tiles.json`);
      if (!res.ok) return;
      tileJSON = await res.json();
    } catch {
      return;
    }
    lastRefresh = Date.now();
    setInterval(() => {
      if (Date.now() - lastRefresh >= (overview ? TILE_OVERVIEW_MS : TILE_BACKSTOP_MS)) refreshTiles();
    }, 5_000);
    // Tiles a pan brings back from MapLibre's cache are as old as when they left the view.
    map.on("movestart", refreshTiles);
    // Before the style has loaded, its style.load adds them.
    if (ready) {
      addTileLayers();
      render();
    }
  })();

  function addTileLayers() {
    if (!tileJSON) return;
    // One credit linking to the per-source list, which is how the tiles are licensed to be
    // credited. The attribution control adds it beside the stream's per-source lines.
    map.addSource("tiles", {
      type: "vector",
      tiles: tileJSON.tiles,
      minzoom: tileJSON.minzoom ?? 0,
      maxzoom: tileJSON.maxzoom ?? 14,
      attribution: tileJSON.attribution,
    });
    // Beneath the stream's layers, so the stream's labels are placed first and the open
    // vessel's halo draws over everything.
    for (const layer of vesselLayers("tile", "tiles", "vessels", tileFadeExpr)) map.addLayer(layer, "vessel-halo");
    hasTiles = true;
  }

  function refreshTiles() {
    // A hidden tab would fetch tiles nobody sees. The next visible tick catches up.
    if (!hasTiles || document.hidden || Date.now() - lastRefresh < TILE_FRESH_MS) return;
    lastRefresh = Date.now();
    askedTiles.clear();
    map.refreshTiles("tiles");
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
    if (overview !== !fits) {
      overview = !fits;
      render();
    }
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
    mode: () => (overview && tileJSON ? "overview" : "live"),
    setTheme(next) {
      if (next === theme) return;
      theme = next;
      // Nothing is drawn until the new style's layers are back.
      ready = false;
      map.setStyle(BASEMAP[theme], { diff: false });
    },
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
        setCameraFollow(false);
        // Followed MMSIs count against a limit of 10, so the last vessel opened lets go.
        if (focus) stream.unfollow(focus);
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
    followCamera: setCameraFollow,
    onCameraFollow(fn) {
      followListeners.add(fn);
      return () => followListeners.delete(fn);
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
