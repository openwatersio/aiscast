import { useEffect } from "react";
import { data } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { Facts } from "../components/ui/Facts";
import { ClassDot, List, ListRow } from "../components/ui/List";
import { Section, Tile } from "../components/ui/Section";
import { StatGrid } from "../components/ui/StatGrid";
import { formatAge, vesselPath } from "../lib/ais";
import { browserAuth, getStation, type ApiAuth } from "../lib/api";
import { serverEnv } from "../lib/context";
import { useLive } from "../lib/live";
import { pageMeta } from "../lib/meta";
import type { BBox } from "../lib/stream";
import type { Route } from "./+types/station";

async function load(auth: ApiAuth, id: string) {
  if (!id) throw data("Not found", { status: 404 });
  const found = await getStation(auth, id);
  return data({ id, found }, found ? undefined : { status: 404 });
}

export function loader({ params, context }: Route.LoaderArgs) {
  return load(context.get(serverEnv), params["*"]);
}

export function clientLoader({ params }: Route.ClientLoaderArgs) {
  return load(browserAuth(), params["*"]);
}

const n = (v: number) => v.toLocaleString("en-US");

export function meta({ loaderData }: Route.MetaArgs) {
  const id = loaderData?.id ?? "";
  const st = loaderData?.found?.station;
  return pageMeta({
    title: `${id} receiving station | Open Waters AIS`,
    description: st
      ? `AIS receiving station ${id}: ${n(st.events.last_24h)} messages in 24 hours, ${n(st.vessels)} vessels heard, last message ${formatAge(st.last_age_s)}.`
      : `AIS receiving station ${id}.`,
    path: `/stations/${id}`,
    noindex: !st,
  });
}

export default function Station({ loaderData }: Route.ComponentProps) {
  const { id, found } = loaderData;
  const st = found?.station;
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
    <Panel back="/stations" title={id}>
      <PageTitle className="break-all">{id}</PageTitle>

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
                { label: "Heard first elsewhere", value: n(st.duplicates) },
              ]}
            />
          </div>

          <Facts
            className="mt-4"
            items={[
              ["Messages 7 d", n(st.events.last_7d)],
              ["Coverage span", span],
              ["First heard", new Date(st.first_seen).toISOString().slice(0, 10)],
            ]}
          />

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
