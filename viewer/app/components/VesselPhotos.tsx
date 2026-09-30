import { ImagePlus } from "lucide-react";
import { isValidImo } from "../lib/ais";
import { mediaKey } from "../lib/media";
import { useMedia } from "../lib/useMedia";
import { PhotoCarousel } from "./ui/PhotoCarousel";

/**
 * The top of a vessel's page: its photographs from Wikimedia Commons, full width. The frame
 * is there whether or not the vessel has any, so the page does not jump when they arrive, and
 * without any it asks for one. `mmsi` is undefined while the vessel itself is loading.
 */
export function VesselPhotos({ mmsi, imo, name }: { mmsi?: number; imo?: number; name: string }) {
  const media = useMedia(mmsi == null ? undefined : mediaKey(imo, mmsi));
  return (
    // Bleeds past the panel's padding to its edges.
    <div className="relative -mx-4 mb-3 aspect-video bg-surface-tile">
      {media?.photos.length ? (
        <PhotoCarousel key={mmsi} photos={media.photos} alt={name} />
      ) : media && mmsi != null ? (
        <a
          href={uploadUrl(imo, mmsi)}
          target="_blank"
          rel="noopener"
          className="flex h-full flex-col items-center justify-center gap-1.5 text-footnote text-fg-muted no-underline hover:text-fg"
        >
          <ImagePlus className="size-8" aria-hidden strokeWidth={1.5} />
          Share a photo
        </a>
      ) : null}
    </div>
  );
}

/**
 * Commons' upload form, filing the photo in the category the lookup reads, so it shows here
 * once uploaded. Commons files ships by IMO; the MMSI category is for vessels without one.
 */
function uploadUrl(imo: number | undefined, mmsi: number): string {
  const category = isValidImo(imo) ? `IMO ${imo}` : `MMSI ${mmsi}`;
  return `https://commons.wikimedia.org/wiki/Special:UploadWizard?categories=${encodeURIComponent(category)}`;
}
