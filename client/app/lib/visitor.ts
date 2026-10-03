/**
 * Roughly where the visitor is, as [longitude, latitude], from their network address. The
 * Worker writes it into each page's head as it leaves, after the edge cache, so the rendered
 * HTML is the same for everyone and can be shared, and the map reads it as it is built.
 */
const NAME = "aiscast-visitor";

export function visitorMeta([lon, lat]: [number, number]) {
  return `<meta name="${NAME}" content="${lon},${lat}">`;
}

/** The location the Worker wrote into this page, if it could place the visitor. */
export function readVisitor(doc: Pick<Document, "querySelector"> = document): [number, number] | undefined {
  const content = doc.querySelector(`meta[name="${NAME}"]`)?.getAttribute("content");
  const parts = content?.split(",") ?? [];
  if (parts.length !== 2 || parts.some((p) => !p)) return undefined;
  const [lon, lat] = parts.map(Number) as [number, number];
  return Number.isFinite(lon) && Number.isFinite(lat) ? [lon, lat] : undefined;
}
