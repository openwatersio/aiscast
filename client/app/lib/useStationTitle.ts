import { useEffect, useState } from "react";
import { stationTitle, volunteerReceiver } from "./ais";
import { browserAuth, getStations } from "./api";

// One station list for the page, asked again at most every five minutes: every vessel names its
// station, and station names rarely change. A failed ask keeps the names it had and waits a minute,
// so an unavailable API is not asked again on every vessel.
const FRESH_MS = 5 * 60_000;
const RETRY_MS = 60_000;
let known: { until: number; titles: Map<string, string> } | undefined;
let asking: Promise<Map<string, string>> | undefined;

function titles(): Promise<Map<string, string>> {
  if (known && Date.now() < known.until) return Promise.resolve(known.titles);
  asking ??= getStations(browserAuth())
    .catch(() => undefined)
    .then((stations) => {
      known = stations
        ? { until: Date.now() + FRESH_MS, titles: new Map(stations.map((st) => [st.station, stationTitle(st)])) }
        : { until: Date.now() + RETRY_MS, titles: known?.titles ?? new Map() };
      return known.titles;
    })
    .finally(() => (asking = undefined));
  return asking;
}

/**
 * What to call a station: its name, or "Near" the place its traffic is, once the station list
 * arrives. The id until then, and for a station the list does not have. A volunteer's tagged id,
 * which vessel records still carry, is named as its receiver.
 */
export function useStationTitle(id: string | undefined): string | undefined {
  const key = id && (volunteerReceiver(id) ?? id);
  const [title, setTitle] = useState(() => (key ? (known?.titles.get(key) ?? key) : undefined));
  useEffect(() => {
    if (!key) return setTitle(undefined);
    setTitle(known?.titles.get(key) ?? key);
    let current = true;
    void titles().then((t) => {
      if (current) setTitle(t.get(key) ?? key);
    });
    return () => {
      current = false;
    };
  }, [key]);
  return title;
}
