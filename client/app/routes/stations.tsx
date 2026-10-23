import { useEffect, useState } from "react";
import { PageTitle, Panel } from "../components/Panel";
import { Activity, Plus } from "lucide-react";
import { ChipRow, ClearChip, MenuChip } from "../components/ui/Chip";
import { IconBadge, List, ListRow, StationTitle, StatusDot } from "../components/ui/List";
import { Section } from "../components/ui/Section";
import { isVolunteer, stationStatus, stationTitles, uptimePercent } from "../lib/ais";
import { browserAuth, getCoverage, getStations, type ApiAuth, type Station } from "../lib/api";
import { serverEnv } from "../lib/context";
import { useLive } from "../lib/live";
import { CONTRIBUTE, CONTRIBUTE_PROMPT } from "../lib/links";
import { pageMeta, SITE } from "../lib/meta";
import type { Route } from "./+types/stations";

async function load(auth: ApiAuth) {
  const [stations, coverage] = await Promise.all([getStations(auth), getCoverage(auth)]);
  return { stations, coverage };
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
      "Every volunteer receiver feeding the Open Waters AIS network, with the vessels each hears, how many no other station hears, and which are live now.",
    path: "/stations",
    // Drawn by the Worker (lib/shareCard.server.tsx).
    image: `${SITE}/stations.png`,
  });

const n = (v: number) => v.toLocaleString("en-US");

// Every figure is over the same 24 hours, so unique vessels can never outnumber the vessels heard.
// A server without vessels_24h is still deployed for a while; its 30-minute count stands in.
const vessels = (s: Station) => s.vessels_24h ?? s.vessels;
const unique = (s: Station) => s.vessels_exclusive_24h ?? 0;

const uptime = (s: Station) => uptimePercent(s.uptime_7d);

type Sort = "unique" | "vessels" | "uptime";
const SORTS: Array<{ value: Sort; label: string; chip: string; of(s: Station): number }> = [
  { value: "unique", label: "Most unique vessels", chip: "Unique vessels", of: unique },
  { value: "vessels", label: "Most vessels in 24 hours", chip: "Vessels", of: vessels },
  { value: "uptime", label: "Highest uptime over 7 days", chip: "Uptime", of: (s) => uptime(s) ?? -1 },
];

/** How many of a section's stations are live, of all of them: "29/35". */
function LiveCount({ stations }: { stations: Station[] }) {
  const live = stations.filter((s) => stationStatus(s.last_age_s) === "live").length;
  return <span title={`${n(live)} of ${n(stations.length)} live now`}>{`${n(live)}/${n(stations.length)}`}</span>;
}

export default function Stations({ loaderData }: Route.ComponentProps) {
  const [sortBy, setSortBy] = useState<Sort>("unique");
  // The stations that heard the coverage cell last clicked on the map, which the list narrows to.
  const [area, setArea] = useState<Set<string>>();
  const live = useLive();
  const { coverage } = loaderData;

  useEffect(() => {
    if (!live) return;
    const off = live.ctl.onCoverageSelect((ids) => setArea(ids && new Set(ids)));
    return () => {
      off();
      live.ctl.clearCoverageSelect();
    };
  }, [live]);
  const clearArea = () => {
    setArea(undefined);
    live?.ctl.clearCoverageSelect();
  };

  // The map shows how many stations hear each cell in place of the vessels while this page is open.
  useEffect(() => {
    if (!live || !coverage) return;
    live.ctl.setCoverage(coverage, "stations");
    return () => live.ctl.setCoverage(undefined);
  }, [live, coverage]);

  const sort = SORTS.find((o) => o.value === sortBy)!;
  // Many stations tie on uptime at 100%; among them, the ones that add the most lead.
  const stations = loaderData.stations && [...loaderData.stations].sort((a, b) => sort.of(b) - sort.of(a) || unique(b) - unique(a));
  const titles = stationTitles(stations ?? []);
  const volunteers = stations?.filter((s) => isVolunteer(s.source) && (!area || area.has(s.station))) ?? [];
  const feeds = stations?.filter((s) => !isVolunteer(s.source)) ?? [];

  const row = (s: Station) => {
    const status = stationStatus(s.last_age_s);
    const subtitle = [
      // A named station's title says nothing of where it is.
      s.name ? s.near : undefined,
      vessels(s) === 1 ? "1 vessel" : `${n(vessels(s))} vessels`,
      `${n(unique(s))} unique`,
    ].filter(Boolean);
    return (
      <ListRow
        key={s.station}
        to={`/stations/${s.station}`}
        leading={
          // The width of an IconBadge, so rows line up with any that have one.
          <span className="flex w-9 shrink-0 justify-center">
            <StatusDot status={status} uptime={uptime(s)} />
          </span>
        }
        title={<StationTitle name={titles.get(s.station)!} />}
        subtitle={
          <>
            {subtitle.join(" · ")}
            {/* The dot and its gauge are hidden from screen readers, so they hear them here. */}
            <span className="sr-only">
              {status !== "live" && `, ${status}`}
              {uptime(s) != null && `, ${uptime(s)}% uptime`}
            </span>
          </>
        }
      />
    );
  };

  return (
    <Panel
      back="/vessels"
      title="Stations"
      actions={
        <a
          href={CONTRIBUTE}
          title={CONTRIBUTE_PROMPT}
          className="inline-flex h-7 shrink-0 items-center gap-1 rounded-full px-2.5 text-subhead font-medium text-accent no-underline transition-colors hover:bg-surface-subtle hover:text-accent-hover"
        >
          <Plus className="size-4" aria-hidden />
          Add Station
        </a>
      }
    >
      <PageTitle>Stations</PageTitle>
      {!stations && <p className="mt-1 text-body text-fg-secondary">Station list unavailable.</p>}
      {stations && (
        <ChipRow label="Sort and filter stations" className="-mx-3 mt-4">
          <MenuChip value={sortBy} options={SORTS} onChange={setSortBy} />
          {area && <ClearChip onClear={clearArea}>Selected area</ClearChip>}
        </ChipRow>
      )}
      {stations && (
        <Section label="Volunteer receivers" aside={<LiveCount stations={volunteers} />} bare>
          <List>{volunteers.map(row)}</List>
          {area && !volunteers.length && (
            <p className="mt-1 text-body text-fg-secondary">No listed volunteer receiver heard this area.</p>
          )}
        </Section>
      )}
      {/* Feeds are a handful of upstreams, not receivers anyone runs, so they live with the network's sources. */}
      <List className="mt-5">
        <ListRow
          to="/network"
          leading={<IconBadge icon={Activity} />}
          title="Data feeds"
          subtitle={feeds.length ? `${n(feeds.length)} government feeds and partner networks` : "Government feeds and partner networks"}
        />
      </List>
    </Panel>
  );
}
