declare global {
  interface Window {
    plausible?: (name: string, options: { props: Record<string, string> }) => void;
  }
}

/** A Slackwater station page near a vessel, which the vessel page links (lib/slackwater.server.ts). */
export interface NearbyStation {
  kind: "tide" | "current";
  name: string;
  url: string;
}

/**
 * Where the Worker answers the stations near a position (routes/slackwater-nearby.ts), for a page
 * whose server render brought none, as one opened inside the app. Two decimals is about a kilometre: close
 * enough for stations 50 km out, and coarse enough that nearby vessels share one answer.
 */
export function nearbyPath(lat: number | undefined, lon: number | undefined): string | undefined {
  if (lat == null || lon == null || !Number.isFinite(lat) || !Number.isFinite(lon)) return undefined;
  return `/ais/vessels/slackwater/${lat.toFixed(2)},${lon.toFixed(2)}`;
}

const STATION_URL = /^https:\/\/slackwater\.xyz\/(tides|currents)\//;
const isNearby = (s: Partial<NearbyStation> | null): s is NearbyStation =>
  (s?.kind === "tide" || s?.kind === "current") && typeof s.name === "string" && typeof s.url === "string" && STATION_URL.test(s.url);

// Answers for the life of the page, so going back to a vessel does not ask again, and requests
// still on their way, so the loading view and the page after it share one. A failure isn't kept.
const asked = new Map<string, Promise<NearbyStation[] | undefined>>();

async function ask(path: string): Promise<NearbyStation[] | undefined> {
  try {
    const res = await fetch(path);
    if (!res.ok) return undefined;
    const listed = ((await res.json()) as { stations?: unknown }).stations;
    return Array.isArray(listed) ? listed.filter(isNearby) : [];
  } catch {
    return undefined;
  }
}

/** The stations the Worker names at `path`, or none when it can't say. Never throws. */
export async function askNearbyStations(path: string): Promise<NearbyStation[]> {
  let answer = asked.get(path);
  if (!answer) {
    answer = ask(path);
    asked.set(path, answer);
  }
  const stations = await answer;
  if (!stations) asked.delete(path);
  return stations ?? [];
}

/** A click handler that counts one Slackwater handoff from a ferry or other vessel page. */
export function slackwaterHandoff(context: "ferry" | "vessel") {
  return (event: { type: string; button: number }) => {
    if ((event.type === "click" && event.button === 0) || (event.type === "auxclick" && event.button === 1)) {
      window.plausible?.("slackwater_handoff", { props: { context } });
    }
  };
}
