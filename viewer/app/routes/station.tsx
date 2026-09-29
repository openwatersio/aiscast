import { useEffect } from "react";
import { data, Link } from "react-router";
import { Panel } from "../components/Shell";
import { CLASS_COLORS, formatAge, shipClass, vesselPath } from "../lib/ais";
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
    <Panel back="/stations">
      <h1 className="text-xl font-semibold break-all" style={{ color: "var(--text)" }}>
        {id}
      </h1>

      {!st ? (
        <p className="card mt-4 text-sm" style={{ color: "var(--text-secondary)" }}>
          No station with this id has been heard since the server started.
        </p>
      ) : (
        <>
          <p className="mt-1 text-sm" style={{ color: "var(--text-secondary)" }}>
            Source {st.source} · last message {formatAge(st.last_age_s)}
          </p>

          <dl className="mt-4">
            <dt>Messages 24 h</dt>
            <dd>{n(st.events.last_24h)}</dd>
            <dt>Messages 7 d</dt>
            <dd>{n(st.events.last_7d)}</dd>
            <dt>Vessels (30 min)</dt>
            <dd>{n(st.vessels)}</dd>
            <dt>Heard elsewhere first</dt>
            <dd>{n(st.duplicates)}</dd>
            {span && (
              <>
                <dt>Coverage span</dt>
                <dd>{span}</dd>
              </>
            )}
            <dt>First heard</dt>
            <dd>{new Date(st.first_seen).toISOString().slice(0, 10)}</dd>
          </dl>

          <h2 className="mt-6 text-sm font-semibold" style={{ color: "var(--text)" }}>
            Latest to hear these {n(vessels.length)} vessels
          </h2>
          <ul className="mt-2 space-y-1">
            {vessels.slice(0, 200).map((f) => (
              <li key={f.properties.mmsi}>
                <Link
                  to={vesselPath(f.properties.mmsi, f.properties.name)}
                  className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm no-underline hover:bg-[var(--surface-subtle)]"
                  style={{ color: "var(--text)" }}
                >
                  <span
                    className="size-2 shrink-0 rounded-full"
                    style={{ background: CLASS_COLORS[shipClass(f.properties.kind, f.properties.type)] }}
                  />
                  <span className="min-w-0 flex-1 truncate">{f.properties.name ?? f.properties.mmsi}</span>
                </Link>
              </li>
            ))}
          </ul>
        </>
      )}
    </Panel>
  );
}
