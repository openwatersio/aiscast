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

/** What the media route answers for one vessel. */
export interface VesselMedia {
  photos: Photo[];
  /** Wikidata particulars, a later phase. */
  particulars: null;
  links: { commonsCategory?: string };
}

export const NO_MEDIA: VesselMedia = { photos: [], particulars: null, links: {} };

/**
 * The key a vessel's media is looked up by: its IMO when it has a valid one, since Commons
 * files ships by hull number, and otherwise its MMSI, which a few Commons categories use.
 */
export function mediaKey(imo: number | undefined, mmsi: number): string {
  return isValidImo(imo) ? String(imo) : String(mmsi);
}
