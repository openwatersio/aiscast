import { Antenna, Check, Share } from "lucide-react";
import { useEffect, useState } from "react";
import { data, Link } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { RouteError, routeErrorHeaders, routeErrorMeta } from "../components/RouteError";
import { Facts } from "../components/ui/Facts";
import { IconButton } from "../components/ui/IconButton";
import { Prompt } from "../components/ui/Prompt";
import { ClassDot, List, ListRow } from "../components/ui/List";
import { Section, Tile } from "../components/ui/Section";
import { StatGrid } from "../components/ui/StatGrid";
import { formatAge, isVolunteer, stationTitle, vesselPath } from "../lib/ais";
import { browserAuth, getStation, orUnavailable, type ApiAuth } from "../lib/api";
import { serverEnv } from "../lib/context";
import { useLive } from "../lib/live";
import { CONTRIBUTE, CONTRIBUTE_PROMPT } from "../lib/links";
import { pageMeta, SITE } from "../lib/meta";
import { shareLink } from "../lib/share";
import type { BBox } from "../lib/stream";
import type { Route } from "./+types/station";

async function load(auth: ApiAuth, id: string) {
  if (!id) throw data("Not found", { status: 404 });
  const found = await orUnavailable(getStation(auth, id));
  return data({ id, found }, found ? undefined : { status: 404 });
}

export function loader({ params, context }: Route.LoaderArgs) {
  return load(context.get(serverEnv), params["*"]);
}

export function clientLoader({ params }: Route.ClientLoaderArgs) {
  return load(browserAuth(), params["*"]);
}

const n = (v: number) => v.toLocaleString("en-US");

export const headers = routeErrorHeaders;

export function meta({ loaderData, error }: Route.MetaArgs) {
  if (!loaderData) return routeErrorMeta(error);
  const id = loaderData.id;
  const st = loaderData?.found?.station;
  const title = st ? stationTitle(st) : id;
  return pageMeta({
    title: `${title} receiving station | Open Waters AIS`,
    description: st
      ? `AIS receiving station ${title}: ${n(st.events.last_24h)} messages in 24 hours, ${n(st.vessels)} vessels heard, last message ${formatAge(st.last_age_s)}.`
      : `AIS receiving station ${id}.`,
    path: `/stations/${id}`,
    noindex: !st,
  });
}

export default function Station({ loaderData }: Route.ComponentProps) {
  const { id, found } = loaderData;
  const st = found?.station;
  const title = st ? stationTitle(st) : id;
  const vessels = found?.vessels.features ?? [];
  const live = useLive();

  // The station's own bbox is every position it has heard since it was first seen, which for
  // a satellite or an aggregate is most of the planet. What a reader wants on opening a
  // station is where its traffic is right now, so fit the vessels it is currently reporting
  // and fall back to the recorded extent only when it has none.
  useEffect(() => {
    if (!live) return;
    const positions = vessels.flatMap((f) => (f.geometry ? [f.geometry.coordinates] : []));
    const fit: BBox | undefined = positions.length
      ? [
          Math.min(...positions.map((c) => c[1])),
          Math.min(...positions.map((c) => c[0])),
          Math.max(...positions.map((c) => c[1])),
          Math.max(...positions.map((c) => c[0])),
        ]
      : st?.bbox;
    if (fit) live.ctl.fitBBox(fit);
  }, [live, found]);

  const span = st?.bbox
    ? `${(st.bbox[2] - st.bbox[0]).toFixed(2)}° × ${(st.bbox[3] - st.bbox[1]).toFixed(2)}°`
    : undefined;

  return (
    <Panel back="/stations" title={title} actions={<ShareStation id={id} title={title} />}>
      <PageTitle className="break-all">{title}</PageTitle>

      {!st ? (
        <Tile className="mt-4 text-body text-fg-secondary">No station with this id has been heard since the server started.</Tile>
      ) : (
        <>
          <p className="mt-1 text-body text-fg-secondary">
            Source {st.source} · last message {formatAge(st.last_age_s)}
          </p>

          <div className="mt-4">
            <StatGrid
              tiles
              stats={[
                { label: "Messages 24 h", value: n(st.events.last_24h) },
                { label: "Vessels", value: n(st.vessels) },
                { label: "Unique vessels", value: n(st.vessels_exclusive_24h ?? 0) },
              ]}
            />
          </div>

          <Facts
            className="mt-4"
            items={[
              ["Vessels 24 h", st.vessels_24h != null ? n(st.vessels_24h) : undefined],
              ["Heard first elsewhere", n(st.duplicates)],
              ["Messages 7 d", n(st.events.last_7d)],
              ["Coverage span", span],
              ["First heard", new Date(st.first_seen).toISOString().slice(0, 10)],
              [
                "Own vessel",
                st.mmsi != null ? (
                  <Link to={vesselPath(st.mmsi, st.name_from === "vessel" ? st.name : undefined)} className="text-accent hover:text-accent-hover">
                    {st.name_from === "vessel" && st.name ? st.name : `MMSI ${st.mmsi}`}
                  </Link>
                ) : undefined,
              ],
            ]}
          />

          <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mt-5">
            {isVolunteer(st.source)
              ? "Someone runs this receiver and shares what it hears. You can run one too."
              : "Volunteer receivers fill in where feeds like this one do not reach."}
          </Prompt>

          <Section label={`Latest to hear these ${n(vessels.length)} vessels`} bare>
            <List>
              {vessels.slice(0, 200).map((f) => (
                <ListRow
                  key={f.properties.mmsi}
                  to={vesselPath(f.properties.mmsi, f.properties.name)}
                  leading={<ClassDot kind={f.properties.kind} type={f.properties.type} />}
                  title={f.properties.name ?? f.properties.mmsi}
                  subtitle={`MMSI ${f.properties.mmsi}`}
                />
              ))}
            </List>
          </Section>
        </>
      )}
    </Panel>
  );
}

/** The station's link, for its operator to pass around. */
function ShareStation({ id, title }: { id: string; title: string }) {
  const [copied, setCopied] = useState(false);
  async function share() {
    const done = await shareLink({ title: `${title} receiving station`, text: "A receiver in the open AIS network", url: `${SITE}/stations/${id}` });
    if (done !== "copied") return;
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return <IconButton icon={copied ? Check : Share} label={copied ? "Link copied" : "Share"} small onClick={() => void share()} />;
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  return <RouteError error={error} />;
}
