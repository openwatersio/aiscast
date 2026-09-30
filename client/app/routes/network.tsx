import { PageTitle, Panel } from "../components/Panel";
import { Facts } from "../components/ui/Facts";
import { Section, Tile } from "../components/ui/Section";
import { StatGrid } from "../components/ui/StatGrid";
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
// For a column that has to fit a panel: 42,961,771 reads as 43M.
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

const KINDS: Record<string, string> = { vessel: "Vessels", aton: "Aids to navigation", base: "Base stations", sar: "Search and rescue aircraft" };

export default function Network({ loaderData }: Route.ComponentProps) {
  const { stats } = loaderData;
  const sources = Object.entries(stats?.sources ?? {}).sort((a, b) => b[1].events.last_24h - a[1].events.last_24h);
  const cell = "py-1.5 pl-3 text-right tabular-nums whitespace-nowrap";
  const head = "pb-1 pl-3 text-right font-medium whitespace-nowrap";
  return (
    <Panel back="/map" title="Network">
      <PageTitle>Network</PageTitle>

      {!stats ? (
        <Tile className="mt-4 text-body text-fg-secondary">The status endpoint is not reachable right now.</Tile>
      ) : (
        <>
          <div className="mt-4">
            <StatGrid
              tiles
              stats={[
                { label: "Messages/s", value: Math.round(stats.events.per_second).toString() },
                { label: "Vessels", value: n(stats.vessels.total) },
                { label: "Stations", value: `${stats.stations.active}/${stats.stations.total}` },
              ]}
            />
          </div>

          <Section label="Sources">
            <table className="w-full text-subhead">
              <thead>
                <tr className="text-left text-caption text-fg-muted uppercase">
                  <th className="pb-1 font-medium">Source</th>
                  <th className={head}>24 h</th>
                  <th className={head}>Only here</th>
                  <th className={head}>Delay</th>
                  <th className={head}>Last</th>
                </tr>
              </thead>
              <tbody>
                {sources.map(([name, s]) => (
                  <tr key={name} className="border-t border-line-subtle text-fg">
                    <td className="py-1.5">{name}</td>
                    <td className={cell} title={n(s.events.last_24h)}>
                      {compact.format(s.events.last_24h)}
                    </td>
                    <td className={`${cell} text-fg-secondary`}>{n(s.vessels_exclusive)}</td>
                    <td className={`${cell} text-fg-secondary`}>{s.delay ? `${s.delay.p50.toFixed(1)}s` : "—"}</td>
                    <td className={`${cell} text-fg-muted`}>{formatAge(s.last_age_s)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
          <p className="mt-2 px-0.5 text-footnote text-fg-muted">
            "Only here" counts vessels no other source kind heard in the last 30 minutes, and delay is the median.
            Counts are rolling windows, not totals since start.
          </p>

          <Section label="Tracked now">
            <Facts
              items={Object.entries(stats.vessels.by_kind)
                .sort((a, b) => b[1] - a[1])
                .map(([kind, count]): [string, string] => [KINDS[kind] ?? kind, n(count)])}
            />
          </Section>
        </>
      )}
    </Panel>
  );
}
