import { createContext, useCallback, useContext, useEffect, useState, useSyncExternalStore } from "react";
import type { MapController } from "./map.client";
import type { Stream, Vessel } from "./stream";

/** The map and the stream, built once in the browser and kept for the life of the page. */
export interface Live {
  stream: Stream;
  ctl: MapController;
}

export const LiveContext = createContext<Live | undefined>(undefined);

// Also held outside React, for client loaders, which run before any component renders.
let instance: Live | undefined;

export function liveInstance(): Live | undefined {
  return instance;
}

export function setLiveInstance(live: Live) {
  instance = live;
}

/** Undefined during the server render and until the map has been built. */
export function useLive(): Live | undefined {
  return useContext(LiveContext);
}

/**
 * Re-renders the caller on each stream frame, which the stream batches to at most one a
 * second. The stream mutates vessels in place, so its version is what tells frames apart.
 */
export function useStreamFrame(): number {
  const live = useLive();
  const subscribe = useCallback(
    (onChange: () => void) => (live ? live.stream.subscribe(onChange) : () => undefined),
    [live],
  );
  return useSyncExternalStore(
    subscribe,
    () => live?.stream.version ?? 0,
    () => 0,
  );
}

/** This vessel as the stream last heard it, if it has. */
export function useLiveVessel(mmsi: number): Vessel | undefined {
  const live = useLive();
  useStreamFrame();
  return live?.stream.vessels.get(mmsi);
}

/** The current time, ticking, for ages that have to keep counting between stream frames. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}
