import { useEffect, useRef, useState } from "react";
import { useRouteLoaderData } from "react-router";
import type { Map as PreviewMap } from "maplibre-gl";
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import type { loader } from "../root";
import { vesselPath } from "../lib/ais";
import { browserAuth, getVessel, type VesselFeature } from "../lib/api";
import { resolveTheme } from "../lib/theme";
import type { BoatVersion } from "../lib/explore";
import { previewPosition } from "../lib/explore-preview";

export function VesselMapPreview({ boat }: { boat: BoatVersion }) {
  const container = useRef<HTMLDivElement>(null);
  const root = useRouteLoaderData<typeof loader>("root");
  const [feature, setFeature] = useState<VesselFeature>();
  const [status, setStatus] = useState(
    boat.mmsi ? "Loading last report…" : "AIS identity not yet confirmed",
  );
  const [mapFailed, setMapFailed] = useState(false);
  const href = boat.mmsi
    ? `/ais${vesselPath(boat.mmsi, feature?.properties.name)}`
    : "/ais/vessels";

  useEffect(() => {
    const element = container.current;
    if (!element) return;
    let disposed = false;
    let map: PreviewMap | undefined;
    let themeObserver: MutationObserver | undefined;
    let position: [number, number] | undefined;
    let started = false;
    const observer = new IntersectionObserver(
      async ([entry]) => {
        if (!entry?.isIntersecting || started) return;
        started = true;
        observer.disconnect();
        const record = boat.mmsi
          ? getVessel(browserAuth(), boat.mmsi)
          : Promise.resolve(undefined);
        try {
          const found = await record;
          if (disposed) return;
          setFeature(found);
          position = previewPosition(boat, found);
          if (boat.mmsi)
            setStatus(
              position
                ? "Last known position"
                : "No position in the AIS network",
            );
        } catch {
          if (disposed) return;
          setStatus("AIS reports unavailable. Try the viewer.");
        }
        try {
          const maps = await import("maplibre-gl");
          if (disposed) return;
          maps.setWorkerUrl(workerUrl);
          const style = () => {
            const choice = document.documentElement.dataset.theme;
            const theme = resolveTheme(
              choice === "dark" || choice === "light" ? choice : "system",
            );
            return `https://tiles.openfreemap.org/styles/${theme === "dark" ? "fiord" : "positron"}`;
          };
          map = new maps.Map({
            container: element,
            style: style(),
            center: position ?? [0, 20],
            zoom: position ? 9 : 0.5,
            interactive: false,
            attributionControl: false,
          });
          map.on("error", () => {
            if (!disposed) setMapFailed(true);
          });
          map.on("style.load", () => {
            if (!position || !map) return;
            map.addSource("vessel", {
              type: "geojson",
              data: {
                type: "Feature",
                geometry: { type: "Point", coordinates: position },
                properties: {},
              },
            });
            map.addLayer({
              id: "vessel",
              type: "circle",
              source: "vessel",
              paint: {
                "circle-radius": 7,
                "circle-color": "#38bdf8",
                "circle-stroke-color": "#ffffff",
                "circle-stroke-width": 2,
              },
            });
            setMapFailed(false);
          });
          themeObserver = new MutationObserver(() => map?.setStyle(style()));
          themeObserver.observe(document.documentElement, {
            attributes: true,
            attributeFilter: ["data-theme"],
          });
          map.getCanvas().setAttribute("tabindex", "-1");
        } catch {
          if (!disposed) setMapFailed(true);
        }
      },
      { rootMargin: "200px" },
    );
    observer.observe(element);
    return () => {
      disposed = true;
      observer.disconnect();
      themeObserver?.disconnect();
      map?.remove();
    };
  }, [boat, root?.theme]);

  return (
    <div>
      <a
        href={href}
        aria-label={`Open ${boat.mmsi ? boat.name : "the map"} in the AIS viewer`}
        className="group relative block overflow-hidden rounded-lg bg-surface-subtle focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-accent"
      >
        <div
          ref={container}
          aria-hidden
          className="pointer-events-none aspect-16/9 w-full"
        />
        {(!previewPosition(boat, feature) || mapFailed) && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-surface/60 p-6 text-center text-subhead text-fg-secondary">
            {mapFailed ? "Map preview unavailable" : status}
          </div>
        )}
        <span className="absolute right-3 bottom-3 rounded-full bg-surface px-3 py-1.5 text-footnote font-semibold text-accent group-hover:bg-accent-bg">
          Open AIS viewer
        </span>
      </a>
      <p role="status" className="mt-2 text-footnote text-fg-secondary">
        {status}
        {feature && previewPosition(boat, feature) && (
          <>
            {" "}
            · Last heard{" "}
            <time dateTime={feature.properties.seen}>
              {new Date(feature.properties.seen).toUTCString()}
            </time>
          </>
        )}
      </p>
      {feature?.attribution && (
        <p className="mt-1 text-footnote text-fg-muted">
          {Object.values(feature.attribution).join(" · ")}
        </p>
      )}
      <p className="mt-1 text-[10px] text-fg-muted">
        <a href="https://openfreemap.org/">OpenFreeMap</a> ·{" "}
        <a href="https://openmaptiles.org/">OpenMapTiles</a> ·{" "}
        <a href="https://www.openstreetmap.org/copyright">
          © OpenStreetMap contributors
        </a>
      </p>
    </div>
  );
}
