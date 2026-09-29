import { Link } from "react-router";
import { Panel } from "../components/Shell";
import { formatAge } from "../lib/ais";
import { browserAuth, getStations, type ApiAuth } from "../lib/api";
import { serverEnv } from "../lib/context";
import { pageMeta } from "../lib/meta";
import type { Route } from "./+types/stations";

async function load(auth: ApiAuth) {
  const stations = await getStations(auth);
  return { stations: stations?.sort((a, b) => b.events.last_24h - a.events.last_24h) };
}

export function loader({ context }: Route.LoaderArgs) {
  return load(context.get(serverEnv));
}

export function clientLoader() {
  return load(browserAuth());
}

export const meta = () =>
  pageMeta({
    title: "Receiving stations | Open Waters AIS",
    description:
      "Every receiver feeding the Open Waters AIS network: government feeds and volunteer stations, with message counts, vessels heard, and coverage.",
    path: "/stations",
  });

const n = (v: number) => v.toLocaleString("en-US");

export default function Stations({ loaderData }: Route.ComponentProps) {
  const { stations } = loaderData;
  const active = stations?.filter((s) => s.last_age_s < 300).length ?? 0;
  return (
    <Panel back="/map" list>
      <h1 className="text-xl font-semibold" style={{ color: "var(--text)" }}>
        Stations
      </h1>
      <p className="mt-1 text-sm" style={{ color: "var(--text-secondary)" }}>
        {stations
          ? `${n(stations.length)} heard, ${n(active)} active in the last five minutes.`
          : "Station list unavailable."}{" "}
        Open one to see where its traffic is now.
      </p>
      {stations && (
        <ul className="mt-4 space-y-1">
          {stations.map((s) => (
            <li key={s.station}>
              <Link
                to={`/stations/${s.station}`}
                className="flex items-baseline gap-2 rounded-md px-2 py-1.5 text-sm no-underline hover:bg-[var(--surface-subtle)]"
                style={{ color: "var(--text)" }}
              >
                <span className="min-w-0 flex-1 truncate font-medium">{s.station}</span>
                <span className="shrink-0 tabular-nums" style={{ color: "var(--text-secondary)" }}>
                  {n(s.events.last_24h)}
                </span>
                <span className="w-16 shrink-0 text-right tabular-nums" style={{ color: "var(--text-muted)" }}>
                  {formatAge(s.last_age_s)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}
