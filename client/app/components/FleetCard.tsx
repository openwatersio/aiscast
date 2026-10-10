import { Ship } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { cn } from "../lib/cn";
import { fileTitle, type Photo } from "../lib/media";
import { useMedia } from "../lib/useMedia";
import { Credit } from "./ui/Credit";

/** What a fleet's card shows, as the server's fleetCard() gives it. */
export interface FleetCardData {
  path: string;
  title: string;
  summary: string;
  /** The cover vessel's media key, for when the fleet names no photo or it does not come. */
  cover?: string;
  /** The Commons file the fleet names for its card. */
  photo?: string;
  /** Its channels' avatars, which cover a card that has no photo. */
  avatars?: string[];
  /** "30 vessels", "2 fleets". */
  count: string;
}

/**
 * A fleet's cover photo with its title over it, as a guide is shown in a maps app. `photos` are
 * the named photos once they arrive, undefined until then.
 */
export function FleetCard({ card, photos, featured = false }: { card: FleetCardData; photos?: Record<string, Photo>; featured?: boolean }) {
  const own = card.photo ? photos?.[fileTitle(card.photo)] : undefined;
  // A fleet that names a photo waits for it rather than flashing its cover vessel's first, and
  // falls back to that when the named one does not come.
  const media = useMedia(card.photo && (!photos || own) ? undefined : card.cover);
  const photo = own ?? media?.photos[0];
  // The credit sits beside the link, not in it, so a tap on it opens the credit rather than the fleet.
  return (
    <div className={cn("relative overflow-hidden rounded-xl bg-surface-tile", featured ? "aspect-[16/10]" : "aspect-square")}>
      <Link to={card.path} className="absolute inset-0 block text-white no-underline hover:text-white">
        {photo ? (
          <img src={photo.thumb} alt="" decoding="async" className="absolute inset-0 size-full object-cover" />
        ) : card.avatars?.length ? (
          // Who the fleet is, for one whose boats no one has photographed for Commons.
          // A row of five on a wide card, two by two on a square one, clear of the title below.
          <span className={cn("absolute inset-x-0 top-0 bottom-1/3 grid content-center justify-center gap-2 p-4", featured ? "grid-cols-5" : "grid-cols-2")} aria-hidden>
            {card.avatars.slice(0, featured ? 5 : 4).map((src) => (
              <img key={src} src={src} alt="" referrerPolicy="no-referrer" className={cn("rounded-full object-cover shadow-sm ring-2 ring-white/80", featured ? "size-14" : "size-12")} />
            ))}
          </span>
        ) : (
          <Ship className="absolute top-1/3 left-1/2 size-10 -translate-1/2 text-fg-muted" aria-hidden strokeWidth={1.5} />
        )}
        <span className="absolute inset-x-0 bottom-0 bg-linear-to-t from-black/75 to-transparent p-3 pt-12">
          <span className={cn("block leading-tight font-semibold", featured ? "text-title" : "text-headline")}>{card.title}</span>
          <span className="mt-0.5 block text-footnote text-white/80">{featured ? card.summary : card.count}</span>
        </span>
      </Link>
      {photo && (
        // The licences ask for a credit wherever the photo shows.
        <Credit className="absolute top-2 left-2 z-10 max-w-[calc(100%-1rem)]">
          {photo.artist} · {photo.license}
        </Credit>
      )}
    </div>
  );
}

/** The named photos once they arrive, undefined until then; none if Commons did not answer. */
export function usePhotos(promise: Promise<Record<string, Photo>>): Record<string, Photo> | undefined {
  const [photos, setPhotos] = useState<Record<string, Photo>>();
  useEffect(() => {
    let current = true;
    setPhotos(undefined);
    promise.then(
      (p) => current && setPhotos(p),
      () => current && setPhotos({}),
    );
    return () => {
      current = false;
    };
  }, [promise]);
  return photos;
}
