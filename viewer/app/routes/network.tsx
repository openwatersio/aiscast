import { Panel } from "../components/Shell";
import { formatAge } from "../lib/ais";
import { browserAuth, getStats } from "../lib/api";
import { serverEnv } from "../lib/context";
import { pageMeta } from "../lib/meta";
import type { Route } from "./+types/network";

export async function loader({ context }: Route.LoaderArgs) {
  return { stats: await getStats(context.get(serverEnv)) };
}

export async function clientLoader() {
  return { stats: await getStats(browserAuth()) };
}

export const meta = () =>
  pageMeta({
    title: "Network status | Open Waters AIS",
    description:
      "What the Open Waters AIS network is receiving right now: sources, message rates, delivery delay, vessels tracked, and stations feeding.",
    path: "/network",
  });

const n = (v: number) => v.toLocaleString("en-US");

export default function Network({ loaderData }: Route.ComponentProps) {
  const { stats } = loaderData;
  const sources = Object.entries(stats?.sources ?? {}).sort((a, b) => b[1].events.last_24h - a[1].events.last_24h);
  return (
    <Panel back="/map" list>
      <h1 className="text-xl font-semibold" style={{ color: "var(--text)" }}>
        Network
      </h1>

      {!stats ? (
        <p className="card mt-4 text-sm" style={{ color: "var(--text-secondary)" }}>
          The status endpoint is not reachable right now.
        </p>
      ) : (
        <>
          <div className="mt-4 grid grid-cols-3 gap-2">
            {[
              ["Messages/s", Math.round(stats.events.per_second).toString()],
              ["Vessels", n(stats.vessels.total)],
              ["Stations", `${stats.stations.active}/${stats.stations.total}`],
            ].map(([label, value]) => (
              <div key={label} className="card text-center">
                <div className="text-xl font-semibold tabular-nums" style={{ color: "var(--text)" }}>
                  {value}
                </div>
                <div className="text-xs" style={{ color: "var(--text-muted)" }}>
                  {label}
                </div>
              </div>
            ))}
          </div>

          <h2 className="mt-6 text-sm font-semibold" style={{ color: "var(--text)" }}>
            Sources
          </h2>
          <table className="mt-2 w-full text-sm">
            <thead>
              <tr style={{ color: "var(--text-muted)" }} className="text-left text-xs">
                <th className="pb-1 font-medium">Source</th>
                <th className="pb-1 text-right font-medium">24 h</th>
                <th className="pb-1 text-right font-medium">Only here</th>
                <th className="pb-1 text-right font-medium">Delay p50</th>
                <th className="pb-1 text-right font-medium">Last</th>
              </tr>
            </thead>
            <tbody>
              {sources.map(([name, s]) => (
                <tr key={name} className="border-t">
                  <td className="py-1.5">{name}</td>
                  <td className="py-1.5 text-right tabular-nums">{n(s.events.last_24h)}</td>
                  <td className="py-1.5 text-right tabular-nums" style={{ color: "var(--text-secondary)" }}>
                    {n(s.vessels_exclusive)}
                  </td>
                  <td className="py-1.5 text-right tabular-nums" style={{ color: "var(--text-secondary)" }}>
                    {s.delay ? `${s.delay.p50.toFixed(1)}s` : "—"}
                  </td>
                  <td className="py-1.5 text-right tabular-nums" style={{ color: "var(--text-muted)" }}>
                    {formatAge(s.last_age_s)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>

          <p className="mt-4 text-xs" style={{ color: "var(--text-muted)" }}>
            "Only here" counts vessels no other source kind heard in the last 30 minutes. Counts are rolling
            windows, not totals since start.
          </p>
        </>
      )}
    </Panel>
  );
}
