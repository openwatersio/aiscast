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
 * Which listed station an id names, and what to call it. An id the list has stands as it is. One it
 * lacks may be a volunteer's tagged id, which vessel records still carry, so it falls back to the
 * receiver when the list has that. A token subject can itself contain "/", so the exact id is tried
 * first. Before the list arrives, and for an id it has neither way, the id names itself.
 */
export function resolveStation(id: string, titles: Map<string, string> | undefined): { id: string; title: string } {
  const listed = titles?.get(id);
  if (listed != null) return { id, title: listed };
  const receiver = volunteerReceiver(id);
  const viaReceiver = receiver != null ? titles?.get(receiver) : undefined;
  if (receiver != null && viaReceiver != null) return { id: receiver, title: viaReceiver };
  return { id, title: id };
}

/** resolveStation for a station id, once the station list arrives. */
export function useStation(id: string | undefined): { id: string; title: string } | undefined {
  const [station, setStation] = useState(() => (id ? resolveStation(id, known?.titles) : undefined));
  useEffect(() => {
    if (!id) return setStation(undefined);
    setStation(resolveStation(id, known?.titles));
    let current = true;
    void titles().then((t) => {
      if (current) setStation(resolveStation(id, t));
    });
    return () => {
      current = false;
    };
  }, [id]);
  return station;
}
