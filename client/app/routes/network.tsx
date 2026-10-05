import { Antenna, CodeXml } from "lucide-react";
import { useEffect } from "react";
import { PageTitle, Panel } from "../components/Panel";
import { Facts } from "../components/ui/Facts";
import { Prompt } from "../components/ui/Prompt";
import { Section, Tile } from "../components/ui/Section";
import { StatGrid } from "../components/ui/StatGrid";
import { formatAge, isVolunteer } from "../lib/ais";
import { browserAuth, getCoverage, getStats, publicApiBase, type ApiAuth } from "../lib/api";
import { serverEnv } from "../lib/context";
import { CONTRIBUTE, CONTRIBUTE_PROMPT, DEVELOPERS } from "../lib/links";
import { useLive } from "../lib/live";
import { pageMeta } from "../lib/meta";
import type { Route } from "./+types/network";

async function load(auth: ApiAuth) {
  const [stats, coverage] = await Promise.all([getStats(auth), getCoverage(auth)]);
  return { stats, coverage };
}

export function loader({ context }: Route.LoaderArgs) {
  return load(context.get(serverEnv));
}

export function clientLoader() {
  return load(browserAuth());
}

export const meta = () =>
  pageMeta({
    title: "Network status | Open Waters AIS",
    description:
      "What the Open Waters AIS network is receiving right now and where it hears vessels: a coverage map, sources, message rates, delivery delay, vessels tracked, and stations feeding.",
    path: "/network",
  });

const n = (v: number) => v.toLocaleString("en-US");
// For a column that has to fit a panel: 42,961,771 reads as 43M.
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

const KINDS: Record<string, string> = { vessel: "Vessels", aton: "Aids to navigation", base: "Base stations", sar: "Search and rescue aircraft" };

export default function Network({ loaderData }: Route.ComponentProps) {
  const { stats, coverage } = loaderData;
  const live = useLive();

  // The map shows where the network hears vessels in place of the vessels while this page is open.
  useEffect(() => {
    if (!live || !coverage) return;
    live.ctl.setCoverage(coverage);
    return () => live.ctl.setCoverage(undefined);
  }, [live, coverage]);

  const sources = Object.entries(stats?.sources ?? {}).sort((a, b) => b[1].events.last_24h - a[1].events.last_24h);
  const volunteers = Object.entries(stats?.stations.by_source ?? {})
    .filter(([kind]) => isVolunteer(kind))
    .reduce((sum, [, count]) => sum + count, 0);
  const cell = "py-1.5 pl-3 text-right tabular-nums whitespace-nowrap";
  const head = "pb-1 pl-3 text-right font-medium whitespace-nowrap";
  const v = stats?.vessels;
  // [label, active, new]; a server without the vessel record sends no windows
  const windows: [string, number | undefined, number | undefined][] = v
    ? [
        ["30 min", v.active, undefined],
        ["24 h", v.last_24h, v.new?.last_24h],
        ["7 d", v.last_7d, v.new?.last_7d],
        ["30 d", v.last_30d, v.new?.last_30d],
        ["All", v.total, undefined],
      ]
    : [];
  const count = (key: string, c: number | undefined) =>
    c == null ? (
      <td key={key} className={`${cell} text-fg-muted`}>
        —
      </td>
    ) : (
      <td key={key} className={cell} title={n(c)}>
        {compact.format(c)}
      </td>
    );
  return (
    <Panel back="/vessels" title="Network">
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
                { label: "Vessels", value: n(stats.vessels.active) },
                { label: "Stations", value: `${stats.stations.active}/${stats.stations.total}` },
              ]}
            />
          </div>

          <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mt-3">
            {volunteers === 0
              ? "Volunteer receivers add coverage where the feeds do not reach."
              : `${volunteers === 1 ? "One station is" : `${n(volunteers)} stations are`} run by volunteers, sharing what their receivers hear.`}
          </Prompt>

          <Section label="Vessels">
            <table className="w-full text-subhead">
              <thead>
                <tr className="text-left text-caption text-fg-muted uppercase">
                  <th className="pb-1 font-medium" />
                  {windows.map(([label]) => (
                    <th key={label} className={head} title={label === "All" ? "Every vessel the network has heard" : undefined}>
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                <tr className="border-t border-line-subtle text-fg">
                  <td className="py-1.5" title="Vessels heard within each window">
                    Active
                  </td>
                  {windows.map(([label, active]) => count(label, active))}
                </tr>
                <tr className="border-t border-line-subtle text-fg">
                  <td className="py-1.5" title="Vessels the network had never heard before each window">
                    New
                  </td>
                  {windows.map(([label, , fresh]) => count(label, fresh))}
                </tr>
              </tbody>
            </table>
          </Section>

          <Section label="Sources">
            <table className="w-full text-subhead">
              <thead>
                <tr className="text-left text-caption text-fg-muted uppercase">
                  <th className="pb-1 font-medium">Source</th>
                  <th className={head} title="Messages in the last 24 hours, a rolling window rather than a total since start">
                    24 h
                  </th>
                  <th className={head} title="Vessels no other source kind heard in the last 30 minutes">
                    Unique
                  </th>
                  <th className={head} title="Median delay">
                    Delay
                  </th>
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

          <Section label="Tracked now">
            <Facts
              items={Object.entries(stats.vessels.by_kind)
                .sort((a, b) => b[1] - a[1])
                .map(([kind, count]): [string, string] => [KINDS[kind] ?? kind, n(count)])}
            />
          </Section>

          <Prompt icon={CodeXml} href={DEVELOPERS} action="Developers" className="mt-5">
            Everything on this page is at{" "}
            <a href={`${publicApiBase()}/v1/stats`} className="font-mono">
              /v1/stats
            </a>
            {coverage && (
              <>
                {" "}and the map at{" "}
                <a href={`${publicApiBase()}/v1/coverage/tiles.json`} className="font-mono">
                  /v1/coverage/tiles.json
                </a>
              </>
            )}
            , and every vessel in it is on the stream.
          </Prompt>
        </>
      )}
    </Panel>
  );
}
