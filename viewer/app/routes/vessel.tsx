import { Suspense } from "react";
import { Await, data, redirect } from "react-router";
import { Panel, useShell } from "../components/Shell";
import { VesselDetail } from "../components/VesselDetail";
import { CLASS_LABELS, flagName, parseVesselParam, shipClass, vesselPath, vesselSlug } from "../lib/ais";
import { browserAuth, getVessel, type VesselFeature } from "../lib/api";
import { serverEnv } from "../lib/context";
import { liveInstance } from "../lib/live";
import { pageMeta } from "../lib/meta";
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
 * vessel and the facts are in the first response.
 */
export async function loader({ params, context }: Route.LoaderArgs) {
  const { mmsi, slug } = parse(params.param);
  const feature = await getVessel(context.get(serverEnv), mmsi);
  const name = feature?.properties.name;
  // The MMSI is canonical and the slug is cosmetic, so a stale or absent slug is corrected
  // with one permanent redirect rather than served as a second URL for the same vessel.
  if (feature && slug !== vesselSlug(name)) throw redirect(vesselPath(mmsi, name), 301);
  const record: VesselRecord = feature;
  return data({ mmsi, name, feature: record }, feature ? undefined : { status: 404 });
}

/**
 * Navigation inside the app. When the stream has heard the vessel, the pane opens at once
 * from that and the record fills in when the API answers, which is the difference between a
 * map app and a page load. When it has not, there is nothing to show yet, so this waits for
 * the record rather than opening an empty pane under a bare MMSI.
 */
export async function clientLoader({ params }: Route.ClientLoaderArgs) {
  const { mmsi } = parse(params.param);
  const heard = liveInstance()?.stream.vessels.get(mmsi);
  const record = getVessel(browserAuth(), mmsi);
  const feature: VesselRecord = heard?.name ? record : await record;
  return { mmsi, name: heard?.name ?? (feature instanceof Promise ? undefined : feature?.properties.name), feature };
}

export function meta({ loaderData }: Route.MetaArgs) {
  if (!loaderData) return pageMeta({ title: "Not found | Open Waters AIS", description: "No vessel at this address.", path: "/map", noindex: true });
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
  return pageMeta({
    title: `${label}${name ? ` (${mmsi})` : ""} live position | Open Waters AIS`,
    description,
    path: vesselPath(mmsi, name),
    noindex: !feature && !loading,
    jsonLd: feature
      ? {
          "@context": "https://schema.org",
          "@type": "Vehicle",
          name: label,
          identifier: String(mmsi),
          ...(country ? { countryOfOrigin: country } : {}),
        }
      : undefined,
  });
}

export default function Vessel({ loaderData }: Route.ComponentProps) {
  const { split } = useShell();
  const { mmsi, feature } = loaderData;
  const detail = (
    <Suspense fallback={<VesselDetail mmsi={mmsi} feature={undefined} loading />}>
      <Await resolve={feature}>{(f) => <VesselDetail mmsi={mmsi} feature={f} />}</Await>
    </Suspense>
  );
  // Beside the list it was opened from, or on its own in the sidebar for a direct visit.
  return split ? detail : <Panel back="/map">{detail}</Panel>;
}
