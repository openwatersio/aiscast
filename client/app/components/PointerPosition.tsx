import { useEffect, useState } from "react";
import { formatCoord, formatDecimalCoord } from "../lib/ais";
import { useLive } from "../lib/live";

/**
 * Where the mouse is on the map, in the map's bottom-left corner, so a position given as
 * numbers can be found by moving the pointer to it. Only with a mouse or trackpad on a wide
 * screen: a finger has no hover, and on a phone the sheet covers the bottom of the map.
 */
export function PointerPosition() {
  const live = useLive();
  const [at, setAt] = useState<{ lat: number; lng: number }>();

  useEffect(() => {
    if (!live) return;
    const { map } = live.ctl;
    // A world copy past the antimeridian reads its longitude beyond ±180; wrap() brings it back.
    const move = (e: { lngLat: { wrap(): { lat: number; lng: number } } }) => setAt(e.lngLat.wrap());
    const out = () => setAt(undefined);
    map.on("mousemove", move);
    map.on("mouseout", out);
    return () => {
      map.off("mousemove", move);
      map.off("mouseout", out);
    };
  }, [live]);

  if (!at) return null;
  return (
    <div
      aria-hidden
      className="pointer-dock pane pointer-events-none fixed z-10 hidden h-9 items-center gap-3 rounded-full px-3.5 font-mono text-footnote whitespace-nowrap text-fg tabular-nums md:pointer-fine:flex"
    >
      <span>{formatCoord(at.lat, at.lng)}</span>
      <span className="text-fg-secondary">{formatDecimalCoord(at.lat, at.lng)}</span>
    </div>
  );
}
