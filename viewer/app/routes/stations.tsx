import { Panel } from "../components/Shell";
import { List, ListRow } from "../components/ui/List";
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
    <Panel back="/map">
      <h1 className="text-title text-fg">Stations</h1>
      <p className="mt-1 text-body text-fg-secondary">
        {stations ? `${n(stations.length)} heard, ${n(active)} active in the last five minutes.` : "Station list unavailable."}{" "}
        Open one to see where its traffic is now.
      </p>
      {stations && (
        <List className="mt-4">
          {stations.map((s) => (
            <ListRow
              key={s.station}
              to={`/stations/${s.station}`}
              title={s.station}
              subtitle={`${s.source.split(":")[0]} · ${n(s.events.last_24h)} messages today · ${n(s.vessels)} vessels`}
              trailing={formatAge(s.last_age_s)}
            />
          ))}
        </List>
      )}
    </Panel>
  );
}
