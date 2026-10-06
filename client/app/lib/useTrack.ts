import { useEffect, useState } from "react";
import { silences, trackGap } from "./ais";
import { browserAuth, getTrack } from "./api";
import { rangeBounds, type TrackRange } from "./trackRange";

export interface LoadedTrack {
  /** The range the server covered, which the chart spans even where the vessel went unheard. */
  from: number;
  to: number;
  /** The range ends now, so the stream's positions continue it. */
  live: boolean;
  /** The positions that start a stretch after the vessel went unheard. */
  breaks: ReadonlySet<number>;
  /** How long a silence after the last position means the vessel went unheard, given the step it was thinned to. */
  gap: number;
  coords: Array<[number, number]>;
  times: number[];
  sog: Array<number | null>;
}

/**
 * Where the vessel went unheard: the server's breaks, or, from a server that sends none, every
 * silence longer than the gap for the step it thinned to.
 */
export function trackBreaks(times: number[], breaks: number[] | undefined, interval: number | undefined): ReadonlySet<number> {
  if (breaks) return new Set(breaks);
  const unheard = silences(times, trackGap((interval ?? 0) * 1000));
  return new Set(times.map((_, i) => i).filter(unheard));
}

/** Why there is no track: the API did not answer. */
export type TrackFailure = "unavailable";

/**
 * A vessel's positions over `range`. The server spreads the tier's limit over the whole range
 * at a round interval. The last track stays while the next loads, so the chart does not jump.
 */
export function useTrack(
  mmsi: number,
  range: TrackRange,
): { track?: LoadedTrack; loading: boolean; failure?: TrackFailure } {
  const [track, setTrack] = useState<LoadedTrack>();
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState<TrackFailure>();
  const { span, end } = range;
  useEffect(() => {
    let current = true;
    setLoading(true);
    const { from, to } = rangeBounds({ span, end });
    void getTrack(browserAuth(), mmsi, { from, to }).then((t) => {
      if (!current) return;
      setLoading(false);
      if (!t) {
        setTrack(undefined);
        setFailure("unavailable");
        return;
      }
      const g = t.geometry;
      const coords = !g ? [] : g.type === "Point" ? [g.coordinates] : g.coordinates;
      const times = t.properties.times.map((x) => Date.parse(x));
      setFailure(undefined);
      setTrack({
        from: Date.parse(t.properties.from),
        to: Date.parse(t.properties.to),
        live: end == null,
        breaks: trackBreaks(times, t.properties.breaks, t.properties.interval),
        gap: trackGap((t.properties.interval ?? 0) * 1000),
        coords,
        times,
        sog: t.properties.sog ?? coords.map(() => null),
      });
    });
    return () => {
      current = false;
    };
  }, [mmsi, span, end]);
  return { track, loading, failure };
}
