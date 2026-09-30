import { useEffect, useState } from "react";
import { browserAuth, getTrack } from "./api";

/** The server keeps this many hours of positions per vessel and clamps anything longer. */
export const TRACK_WINDOW_HOURS = 48;

// Positions a request may return: 200 anonymous, 1,000 with a personal token. A little under,
// so the interval below always fits.
const BUDGET = { anonymous: 190, token: 950 };

export interface LoadedTrack {
  /** The range asked for, which the chart spans even where the vessel went unheard. */
  from: number;
  to: number;
  coords: Array<[number, number]>;
  times: number[];
  sog: Array<number | null>;
}

/**
 * A vessel's positions over the last `hours`. The server keeps the first position in each
 * interval, and the interval is sized so the whole range fits the tier's limit: without it
 * the limit would cut the range short from the start, and a day's track would begin a few
 * hours ago.
 */
export function useTrack(mmsi: number, hours: number): { track?: LoadedTrack; loading: boolean } {
  const [track, setTrack] = useState<LoadedTrack>();
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let current = true;
    setLoading(true);
    const auth = browserAuth();
    const to = Date.now();
    const from = to - hours * 3600e3;
    const budget = auth.token ? BUDGET.token : BUDGET.anonymous;
    const intervalSeconds = Math.ceil((hours * 3600) / budget);
    void getTrack(auth, mmsi, { from, to, intervalSeconds }).then((t) => {
      if (!current) return;
      setLoading(false);
      // A failed fetch leaves whatever track was showing.
      if (!t) return;
      const coords = t.geometry?.coordinates ?? [];
      setTrack({
        from,
        to,
        coords,
        times: t.properties.times.map((x) => Date.parse(x)),
        sog: t.properties.sog ?? coords.map(() => null),
      });
    });
    return () => {
      current = false;
    };
  }, [mmsi, hours]);
  return { track, loading };
}
