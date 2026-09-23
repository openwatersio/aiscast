import { navigate } from "astro:transitions/client";
import { vesselPath } from "./ais";
import { createMap, type MapController } from "./map";
import { Stream, type BBox } from "./stream";

// The map, the WebSocket and the vessel cache outlive navigation. Astro moves the persisted
// #map element into each new document, but this module's bindings are re-evaluated, so the
// live objects hang off window and every entry point reuses them.
declare global {
  interface Window {
    aiscast?: { stream: Stream; ctl: MapController };
  }
}

function boot(): { stream: Stream; ctl: MapController } | undefined {
  if (window.aiscast) return window.aiscast;
  const container = document.getElementById("map");
  if (!container) return undefined;

  const stream = new Stream();
  const ctl = createMap(container, stream);
  ctl.onSelect((mmsi) => {
    const v = stream.vessels.get(mmsi);
    // Through the router, not location.assign: a document load would discard everything
    // this function just built.
    navigate(vesselPath(mmsi, v?.name));
  });
  // The status line is repainted on every stream tick, and this subscription has to be
  // registered exactly once for the life of the page, not once per navigation.
  stream.subscribe(paintStatus);
  window.aiscast = { stream, ctl };
  return window.aiscast;
}

function applyRoute() {
  const app = boot();
  if (!app) return;
  const { dataset } = document.body;

  // `from` tells the server which pane to render into. It is not part of the vessel's
  // identity, so it comes straight back out of the address bar: a link copied from here is
  // the canonical one, and reloading it gives the direct-link layout, which is correct.
  const url = new URL(location.href);
  if (url.searchParams.has("from")) {
    url.searchParams.delete("from");
    history.replaceState(history.state, "", url.pathname + url.search + url.hash);
  }

  // Insets first: the panel for this route is already in the DOM, and every camera move
  // below has to know how much of the map it covers.
  app.ctl.refreshInsets();

  const mmsi = Number(dataset.focusMmsi);
  app.ctl.setFocus(Number.isInteger(mmsi) && mmsi > 0 ? mmsi : undefined);
  if (Number.isInteger(mmsi) && mmsi > 0) {
    const at = (dataset.focusAt ?? "").split(",").map(Number);
    const fallback: [number, number] | undefined =
      at.length === 2 && at.every(Number.isFinite) ? [at[0]!, at[1]!] : undefined;
    app.ctl.flyToVessel(mmsi, fallback);
  }

  const fit = dataset.fitBbox;
  if (fit) {
    const parts = fit.split(",").map(Number);
    if (parts.length === 4 && parts.every(Number.isFinite)) app.ctl.fitBBox(parts as BBox);
  }


  // MapLibre sizes to its container, which changed width if the sidebar layout did.
  requestAnimationFrame(() => app.ctl.map.resize());
}


function paintStatus() {
  const el = document.querySelector<HTMLElement>("[data-stream-status]");
  const app = window.aiscast;
  if (!el || !app) return;
  const { state, eventsPerSec, vessels } = app.stream;
  const stream =
    state === "capped"
      ? "zoom in for vessels"
      : state === "live"
        ? `${eventsPerSec}/s · ${vessels.size} tracked`
        : state;
  const map = app.ctl.status();
  el.textContent = map.state === "ready" ? stream : `${stream} · map ${map.state} ${map.detail}`;
}

applyRoute();
paintStatus();

// after-swap, not page-load: the new document is in place and the persisted map element has
// already been moved into it, so reading body data attributes here sees the new route.
document.addEventListener("astro:after-swap", () => {
  applyRoute();
  paintStatus();
});
