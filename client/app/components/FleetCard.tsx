import { Ship } from "lucide-react";
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
  return (
    <Link
      to={card.path}
      className={cn(
        "relative block overflow-hidden rounded-xl bg-surface-tile text-white no-underline hover:text-white",
        featured ? "aspect-[16/10]" : "aspect-[3/4]",
      )}
    >
      {photo ? (
        <>
          <img src={photo.thumb} alt="" decoding="async" className="absolute inset-0 size-full object-cover" />
          {/* The licences ask for a credit wherever the photo shows; the carousels link it to the file. */}
          <Credit focusable={false} className="absolute top-2 left-2 max-w-[calc(100%-1rem)]">
            {photo.artist} · {photo.license}
          </Credit>
        </>
      ) : card.avatars?.length ? (
        // Who the fleet is, for one whose boats no one has photographed for Commons.
        // A row of five on a wide card, two by two on a tall one, clear of the title below.
        <span className={cn("absolute inset-x-0 top-0 bottom-1/3 grid content-center justify-center gap-2 p-4", featured ? "grid-cols-5" : "grid-cols-2")} aria-hidden>
          {card.avatars.slice(0, featured ? 5 : 4).map((src) => (
            <img key={src} src={src} alt="" referrerPolicy="no-referrer" className="size-14 rounded-full object-cover shadow-sm ring-2 ring-white/80" />
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
  );
}
