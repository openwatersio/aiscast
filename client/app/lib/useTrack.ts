import { useEffect, useState } from "react";
import { trackGap } from "./ais";
import { browserAuth, getTrack } from "./api";
import { rangeBounds, type TrackRange } from "./trackRange";

export interface LoadedTrack {
  /** The range the server covered, which the chart spans even where the vessel went unheard. */
  from: number;
  to: number;
  /** The range ends now, so the stream's positions continue it. */
  live: boolean;
  /** The server covered less than was asked, because the token does not reach that far back. */
  limited: boolean;
  /** How long a silence between positions means the vessel went unheard, given how far they were thinned. */
  gap: number;
  coords: Array<[number, number]>;
  times: number[];
  sog: Array<number | null>;
}

/** Why there is no track: the range is beyond the token's reach, or the API did not answer. */
export type TrackFailure = "forbidden" | "unavailable";

// The server clamps a range to the token's reach from its own clock, which can differ from the
// browser's by this much without the range counting as cut short.
const CLOCK_SLACK_MS = 60e3;

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
      if (!t || t === "forbidden") {
        setTrack(undefined);
        setFailure(t ?? "unavailable");
        return;
      }
      const g = t.geometry;
      const coords = !g ? [] : g.type === "Point" ? [g.coordinates] : g.coordinates;
      const covered = Date.parse(t.properties.from);
      setFailure(undefined);
      setTrack({
        from: covered,
        to: Date.parse(t.properties.to),
        live: end == null,
        limited: covered - from > CLOCK_SLACK_MS,
        gap: trackGap((t.properties.interval ?? 0) * 1000),
        coords,
        times: t.properties.times.map((x) => Date.parse(x)),
        sog: t.properties.sog ?? coords.map(() => null),
      });
    });
    return () => {
      current = false;
    };
  }, [mmsi, span, end]);
  return { track, loading, failure };
}
