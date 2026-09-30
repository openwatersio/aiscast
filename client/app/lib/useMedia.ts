import { useEffect, useState } from "react";
import { NO_MEDIA, type VesselMedia } from "./media";

// Answers for the life of the page, so going back to a vessel does not ask again.
const answered = new Map<string, VesselMedia>();

/**
 * A vessel's photos, from the Worker's media route (routes/vessel-media.ts), by the key
 * mediaKey() gives. Undefined until the answer arrives; a failed lookup answers as no photos.
 */
export function useMedia(key: string | undefined): VesselMedia | undefined {
  const [media, setMedia] = useState<VesselMedia | undefined>(() => (key ? answered.get(key) : undefined));
  useEffect(() => {
    if (!key) return setMedia(undefined);
    const known = answered.get(key);
    if (known) return setMedia(known);
    setMedia(undefined);
    let current = true;
    void fetch(`/ais/vessels/media/${key}`)
      .then((res) => (res.ok ? (res.json() as Promise<VesselMedia>) : NO_MEDIA))
      .catch(() => NO_MEDIA)
      .then((answer) => {
        answered.set(key, answer);
        if (current) setMedia(answer);
      });
    return () => {
      current = false;
    };
  }, [key]);
  return media;
}
