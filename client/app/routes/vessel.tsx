import { Suspense, type ReactNode } from "react";
import { Await, data, redirect } from "react-router";
import { X } from "lucide-react";
import { Panel } from "../components/Panel";
import { RouteError, routeErrorHeaders, routeErrorMeta } from "../components/RouteError";
import { IconLink } from "../components/ui/IconButton";
import { VesselDetail } from "../components/VesselDetail";
import { CLASS_LABELS, flagName, parseVesselParam, pathSlug, shipClass, vesselCardPath, vesselPath } from "../lib/ais";
import { browserAuth, getVessel, orUnavailable, type VesselFeature } from "../lib/api";
import { serverEnv } from "../lib/context";
import { liveInstance } from "../lib/live";
import { mediaKey } from "../lib/media";
import { firstPhoto } from "../lib/media.server";
import { pageMeta, SITE } from "../lib/meta";
import type { Route } from "./+types/vessel";

/** Awaited for a server render, possibly still in flight for navigation inside the app. */
type VesselRecord = VesselFeature | Promise<VesselFeature | undefined> | undefined;

function parse(param: string) {
  const parsed = parseVesselParam(param);
  if (!parsed) throw data("Not found", { status: 404 });
  return parsed;
}

/**
 * A direct visit, a crawler, or an unfurl bot. The record is awaited so the head names the
 * vessel and the facts are in the first response, and so is its photo if one comes quickly,
 * so a shared link unfurls with the ship.
 */
export async function loader({ params, context, request }: Route.LoaderArgs) {
  const { mmsi, slug } = parse(params.param);
  const feature = await orUnavailable(getVessel(context.get(serverEnv), mmsi));
  const name = feature?.properties.name;
  const kind = feature?.properties.kind;
  // The MMSI is canonical and the slug is cosmetic, so a stale or absent slug is corrected
  // with one permanent redirect rather than served as a second URL for the same vessel.
  if (feature && slug !== pathSlug(name, kind)) throw redirect(vesselPath(mmsi, name, kind), 301);
  const record: VesselRecord = feature;
  // Gear has no photo: its MMSI is one a maker or owner chose, often a ship's, whose photo a link would unfurl with.
  const key = feature && kind !== "gear" && mediaKey(feature.properties.imo, mmsi);
  const photo = key ? await firstPhoto(key, request.url) : undefined;
  return data({ mmsi, name, feature: record, photo }, feature ? undefined : { status: 404 });
}

/**
 * Navigation inside the app. When the stream has heard the vessel, the pane opens at once
 * from that and the record fills in when the API answers, which is the difference between a
 * map app and a page load. When it has not, there is nothing to show yet, so this waits for
 * the record rather than opening an empty pane under a bare MMSI. Either way the address is
 * corrected to the canonical slug before the pane renders, as a document request's is: from
 * the stream's name on the fast path, from the record's otherwise.
 */
export async function clientLoader({ params }: Route.ClientLoaderArgs) {
  const { mmsi, slug } = parse(params.param);
  const heard = liveInstance()?.stream.vessels.get(mmsi);
  const record = getVessel(browserAuth(), mmsi);
  if (heard?.name) {
    if (slug !== pathSlug(heard.name, heard.kind)) throw redirect(vesselPath(mmsi, heard.name, heard.kind));
    // The pane is already open from the stream's copy, which stands if the record fails.
    const feature: VesselRecord = record.catch(() => undefined);
    return { mmsi, name: heard.name, kind: heard.kind, feature };
  }
  const feature = await orUnavailable(record);
  const name = feature?.properties.name;
  const kind = feature?.properties.kind;
  if (feature && slug !== pathSlug(name, kind)) throw redirect(vesselPath(mmsi, name, kind));
  return { mmsi, name, feature: feature as VesselRecord };
}

export const headers = routeErrorHeaders;

export function meta({ loaderData, error }: Route.MetaArgs) {
  if (!loaderData) return routeErrorMeta(error);
  const { mmsi } = loaderData;
  const feature = loaderData.feature instanceof Promise ? undefined : loaderData.feature;
  const props = feature?.properties;
  const name = props?.name ?? loaderData.name;
  const label = name ?? `MMSI ${mmsi}`;
  const country = flagName(props?.flag);
  const loading = loaderData.feature instanceof Promise;
  const description = props
    ? `Live AIS position for ${label}${country ? `, ${country}` : ""}. ${CLASS_LABELS[shipClass(props.kind, props.type)]}. ` +
      `Last heard ${new Date(props.seen).toUTCString()}.`
    : `AIS vessel ${label}.`;
  const kind = props?.kind ?? ("kind" in loaderData ? loaderData.kind : undefined);
  const path = vesselPath(mmsi, name, kind);
  const photo = "photo" in loaderData ? loaderData.photo : undefined;
  return pageMeta({
    title: `${label}${name ? ` (${mmsi})` : ""} live position | Open Waters AIS`,
    description,
    // An MMSI the network has never heard has no page of its own to point at.
    path: feature || loading ? path : undefined,
    // A vessel that has never sent its name is a page about a bare MMSI, and the sitemap
    // leaves it out for the same reason. So is gear: a fishing-net buoy, named by its serial.
    // Their links are still worth following.
    noindex: (!loading && !name) || kind === "gear",
    // A photo when Wikimedia has one, else the vessel's card, drawn by the Worker (lib/shareCard.server.tsx).
    image: photo ?? (feature || loading ? `${SITE}${vesselCardPath(mmsi)}` : undefined),
    // schema.org has no ship, and Vehicle covers transport over water. The flag is not
    // countryOfOrigin, which is where a thing was made, so it is left out.
    // Fishing gear is not a vehicle, and its page is not indexed.
    jsonLd: feature && name && kind !== "gear"
      ? {
          "@context": "https://schema.org",
          "@type": "Vehicle",
          name: label,
          url: `${SITE}${path}`,
          identifier: [
            { "@type": "PropertyValue", propertyID: "MMSI", value: String(mmsi) },
            ...(props?.imo ? [{ "@type": "PropertyValue", propertyID: "IMO", value: String(props.imo) }] : []),
          ],
          ...(photo ? { image: photo } : {}),
        }
      : undefined,
  });
}

export default function Vessel({ loaderData }: Route.ComponentProps) {
  const { mmsi, feature, name } = loaderData;
  const heardKind = "kind" in loaderData ? loaderData.kind : undefined;
  // Back pops to whatever pushed this vessel; close always returns to the map, as a place
  // card's does in a maps app. A vessel's page opens with its photos under the bar; gear has
  // none, so its bar stands on the page as any other panel's does, and the title starts below it.
  // The record's kind decides once it is in, the stream's until then.
  const panel = (kind: string | undefined, detail: ReactNode) => (
    <Panel back="/vessels" title={name ?? `MMSI ${mmsi}`} hero={kind !== "gear"} actions={<IconLink icon={X} label="Close" to="/vessels" small />}>
      {detail}
    </Panel>
  );
  return (
    <Suspense fallback={panel(heardKind, <VesselDetail mmsi={mmsi} feature={undefined} loading />)}>
      <Await resolve={feature}>{(f) => panel(f?.properties.kind ?? heardKind, <VesselDetail mmsi={mmsi} feature={f} />)}</Await>
    </Suspense>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  return <RouteError error={error} />;
}
