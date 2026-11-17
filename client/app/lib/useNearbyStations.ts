import { useEffect, useState } from "react";
import { askNearbyStations, nearbyPath, type NearbyStation } from "./slackwater";

/**
 * The Slackwater stations to link from a vessel's card. A server render brings them (`given`); a
 * page opened inside the app brings none, so they are asked of the Worker by the vessel's position
 * (routes/slackwater-nearby.ts). Undefined until the first answer; while a moving vessel's next
 * answer is on its way, the last one stands.
 */
export function useNearbyStations(given: NearbyStation[] | undefined, lat: number | undefined, lon: number | undefined) {
  const path = given ? undefined : nearbyPath(lat, lon);
  const [asked, setAsked] = useState<NearbyStation[]>();
  useEffect(() => {
    if (!path) return;
    let current = true;
    void askNearbyStations(path).then((stations) => {
      if (current) setAsked(stations);
    });
    return () => {
      current = false;
    };
  }, [path]);
  // With no position there is nothing near to name, whatever was asked before.
  return given ?? (path ? asked : undefined);
}
