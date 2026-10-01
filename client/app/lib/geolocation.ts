import { useSyncExternalStore } from "react";

// The reader's position, asked for only when they choose Near me, and kept for the life of the
// page so the next search does not ask again.
let position: [number, number] | undefined;
const listeners = new Set<() => void>();

/**
 * Asks the browser where the reader is, as [lat, lon]. The browser prompts for permission the
 * first time. Rejects with a sentence for the reader when it cannot say.
 */
export function locate(): Promise<[number, number]> {
  return new Promise((resolve, reject) => {
    if (!("geolocation" in navigator)) {
      reject(new Error("This browser cannot share your location."));
      return;
    }
    // The browser's own timeout starts only once permission is given, and a prompt left
    // unanswered, or a browser that never asks, would leave the reader waiting for good.
    const giveUp = setTimeout(() => reject(new Error("Your location is not available right now.")), 30_000);
    navigator.geolocation.getCurrentPosition(
      (p) => {
        clearTimeout(giveUp);
        position = [p.coords.latitude, p.coords.longitude];
        for (const fn of listeners) fn();
        resolve(position);
      },
      (e) => {
        clearTimeout(giveUp);
        reject(
          new Error(
            e.code === e.PERMISSION_DENIED
              ? "Location access is off for this site."
              : "Your location is not available right now.",
          ),
        );
      },
      // A boat moves slowly enough that a fix from the last minute will do.
      { maximumAge: 60_000, timeout: 15_000 },
    );
  });
}

/** The reader's position once `locate` has found it. */
export function useMyPosition(): [number, number] | undefined {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => position,
    () => undefined,
  );
}
