import { isValidImo } from "./ais";

/** A photograph of a vessel on Wikimedia Commons, with what its licence asks us to show. */
export interface Photo {
  /** A standard-width (960px) thumbnail, exactly as the API returned it. */
  thumb: string;
  width: number;
  height: number;
  /** The file's page on Commons: its provenance, and where the credit links. */
  page: string;
  artist: string;
  license: string;
  licenseUrl?: string;
  description?: string;
}

/** What the media route answers for one vessel: its photos, and the Commons category they came from. */
export interface VesselMedia {
  photos: Photo[];
  links: { commonsCategory?: string };
}

export const NO_MEDIA: VesselMedia = { photos: [], links: {} };

/**
 * The key a vessel's media is looked up by: its IMO when it has a valid one, since Commons
 * files ships by hull number, and otherwise its MMSI, which a few Commons categories use.
 * Undefined when neither is worth asking about: only a ship's MMSI starts with a 2 to 7.
 */
export function mediaKey(imo: number | undefined, mmsi: number): string | undefined {
  if (isValidImo(imo)) return String(imo);
  return mmsi >= 200_000_000 && mmsi < 800_000_000 ? String(mmsi) : undefined;
}

/**
 * A photo's thumbnail at 120px, for a list row. The media route gives the 960px one; both
 * are standard Wikimedia widths, which render freely, so the width in its path is swapped.
 */
export function smallThumb(photo: Photo): string {
  return photo.thumb.replace("/960px-", "/120px-");
}

/** A Commons file's title as Commons lists it, `File:` and the name with spaces, which keys namedPhotos. */
export const fileTitle = (name: string) => `File:${name.replace(/^file:/i, "").replace(/_/g, " ")}`;
