import { ArrowLeftRight, Check, Copy, Share } from "lucide-react";
import { useEffect, useState, type PointerEvent } from "react";
import { data, Link, redirect } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { RouteError, routeErrorHeaders, routeErrorMeta } from "../components/RouteError";
import { Facts } from "../components/ui/Facts";
import { IconButton } from "../components/ui/IconButton";
import { Prompt, ReceiverLink } from "../components/ui/Prompt";
import { ClassDot, List, ListRow, StationTitle, StatusDot } from "../components/ui/List";
import { Section, Tile } from "../components/ui/Section";
import {
  CLASS_COLORS,
  CLASS_LABELS,
  flagEmoji,
  flagName,
  vesselMix,
  fitBox,
  creditLine,
  type MixSlice,
  type ShipClass,
  type StationStatus,
  formatAge,
  isVolunteer,
  shipClass,
  shortAge,
  stationCardPath,
  stationDescription,
  stationStatus,
  stationName,
  stationTitle,
  uptimePercent,
  vesselPath,
  volunteerReceiver,
} from "../lib/ais";
import { browserAuth, getStation, getStationCoverage, orUnavailable, type ApiAuth } from "../lib/api";
import { cn } from "../lib/cn";
import { serverEnv } from "../lib/context";
import { useLive } from "../lib/live";
import { AISSTREAM_STATUS, AISSTREAM_VS, TOKEN } from "../lib/links";
import { pageMeta, SITE } from "../lib/meta";
import { shareLink } from "../lib/share";
import type { BBox } from "../lib/stream";
import type { Route } from "./+types/station";

async function load(auth: ApiAuth, id: string) {
  if (!id) throw data("Not found", { status: 404 });
  const receiver = volunteerReceiver(id);
  if (receiver) throw redirect(`/stations/${receiver}`, 301);
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
      ? stationDescription(title, st)
      : `AIS receiving station ${id}.`,
    path: st ? `/stations/${id}` : undefined,
    noindex: !st,
    // Drawn by the Worker (lib/shareCard.server.tsx).
    ...(st ? { image: `${SITE}${stationCardPath(id)}` } : {}),
  });
}

// Keyed by the station, so moving from one station's page to another's starts fresh: no footprint, list,
// or pinned class carries over.
export default function Station({ loaderData }: Route.ComponentProps) {
  return <StationPage key={loaderData.id} loaderData={loaderData} />;
}

function StationPage({ loaderData }: Pick<Route.ComponentProps, "loaderData">) {
  const { id, found } = loaderData;
  const st = found?.station;
  const title = st ? stationTitle(st) : id;
  const vessels = found?.vessels.features ?? [];
  const live = useLive();

  // The station's coverage cells over 7 days, fetched once the page is drawn so a slow load never holds
  // the page: undefined while loading, null when it has none.
  const [footprint, setFootprint] = useState<Awaited<ReturnType<typeof getStationCoverage>> | null>();
  useEffect(() => {
    if (!found) return;
    let gone = false;
    getStationCoverage(browserAuth(), id)
      .catch(() => undefined)
      // Without a fit, the answer is not one station's cells, as from a server that ignores `station`.
      .then((tiles) => !gone && setFootprint(tiles?.fit ? tiles : null));
    return () => {
      gone = true;
    };
  }, [id, Boolean(found)]);
  useEffect(() => {
    if (!live || !footprint) return;
    live.ctl.setFootprint(footprint);
    return () => live.ctl.setFootprint(undefined);
  }, [live, footprint]);
  const fitted: BBox | undefined = fitBox(footprint?.fit);

  // The station's own bbox is every position it has heard since the server last started, which for
  // a satellite or an aggregate is most of the planet. What a reader wants on opening a station is
  // where it hears: at once the vessels it is currently reporting, or the recorded extent when it has
  // none, so the map opens on the station; then its footprint once that comes.
  useEffect(() => {
    if (!live || !found) return;
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
  useEffect(() => {
    if (live && fitted) live.ctl.fitBBox(fitted);
  }, [live, fitted?.join()]);

  // The size the footprint's frame takes in, which leaves out a few stray far-off cells, over its window.
  const extent = fitted && footprint && `About ${formatExtent(fitted)} over ${footprint.window.days === 1 ? "1 day" : `${footprint.window.days} days`}`;
  const credit = creditLine(found?.vessels.attribution, found?.attribution);
  // Vessels newest first, so what the station hears now leads and the order holds from one load to
  // the next. Base stations, aids to navigation, and fishing gear follow: they transmit every few
  // seconds, and would otherwise always lead.
  const fixed = (f: (typeof vessels)[number]) => (["base", "aton", "gear"].includes(f.properties.kind) ? 1 : 0);
  const heard = [...vessels].sort((a, b) => fixed(a) - fixed(b) || Date.parse(b.properties.seen) - Date.parse(a.properties.seen));
  const now = Date.now();
  const own = st?.mmsi != null && st.name_from === "vessel" && st.name ? st.name : undefined;
  const [showAll, setShowAll] = useState(false);

  // People searching for aisstream.io land here, most of them developers using its API or checking
  // whether it is down; both have a page on the website. Keyed on the address rather than the station
  // record, so the links stay up after 30 days of silence, when the server stops listing the station.
  const aisstream = id === "aisstream" && (
    <Prompt icon={ArrowLeftRight} href={AISSTREAM_VS} action="Compare the APIs" className="mt-5">
      An aisstream.io client works with Open Waters AIS once you change the hostname and use an{" "}
      <a href={TOKEN}>Open Waters AIS token</a> as its APIKey. Whether aisstream.io is delivering right now is on its <a href={AISSTREAM_STATUS}>status page</a>.
    </Prompt>
  );

  return (
    <Panel back="/stations" title={title} actions={<ShareStation id={id} title={title} />}>
      <PageTitle className="break-all">
        <StationTitle name={st ? stationName(st) : { title: id }} suffixClassName="text-headline" />
      </PageTitle>

      {!st ? (
        <>
          <Tile className="mt-4 text-body text-fg-secondary">No station with this id has been heard recently.</Tile>
          {aisstream}
        </>
      ) : (
        <>
          <p className="mt-1 flex items-center gap-1.5 text-body text-fg-secondary">
            <StatusDot status={stationStatus(st.last_age_s)} className="size-4" />
            Last heard {formatAge(st.last_age_s)}
          </p>

          <Vessels
            mix={vesselMix(st.types)}
            total={st.vessels_24h ?? st.vessels}
            only={st.vessels_exclusive_24h ?? 0}
          />

          {st.hours && st.hours_from && (
            <Activity hours={st.hours} from={Date.parse(st.hours_from)} uptime={st.uptime_7d} status={stationStatus(st.last_age_s)} />
          )}

          {aisstream}

          <Section label="Last reported here" aside={vessels.length === 1 ? "1 vessel" : `${n(vessels.length)} vessels`} bare>
            <List>
              {heard.slice(0, showAll ? MAX_VESSELS : FIRST_VESSELS).map((f) => {
                const p = f.properties;
                const cls = shipClass(p.kind, p.type);
                const seen = Math.max(0, Math.round((now - Date.parse(p.seen)) / 1000));
                return (
                  <ListRow
                    key={p.mmsi}
                    to={vesselPath(p.mmsi, p.name, p.kind)}
                    leading={<ClassDot kind={p.kind} type={p.type} />}
                    title={
                      <>
                        {p.name ?? `MMSI ${p.mmsi}`}
                        {p.flag && flagEmoji(p.flag) && (
                          <span role="img" aria-label={flagName(p.flag) ?? p.flag} className="ml-1.5">
                            {flagEmoji(p.flag)}
                          </span>
                        )}
                      </>
                    }
                    subtitle={[p.name ? `MMSI ${p.mmsi}` : undefined, cls !== "other" ? CLASS_LABELS[cls] : undefined]
                      .filter(Boolean)
                      .join(" · ")}
                    trailing={
                      // The server and the browser each take their own now, so the age may differ by a second.
                      <time dateTime={p.seen} title={p.seen} suppressHydrationWarning>
                        {shortAge(seen)}
                      </time>
                    }
                  />
                );
              })}
            </List>
            {!showAll && vessels.length > FIRST_VESSELS && (
              <button type="button" onClick={() => setShowAll(true)} className="mt-1 px-2 py-2 text-subhead text-accent hover:text-accent-hover">
                {vessels.length > MAX_VESSELS ? `Show the ${n(MAX_VESSELS)} most recent` : `Show all ${n(vessels.length)}`}
              </button>
            )}
            {showAll && vessels.length > MAX_VESSELS && (
              <p className="mt-2 px-2 text-footnote text-fg-muted">
                The {n(MAX_VESSELS)} most recent of {n(vessels.length)}. The rest are on the map.
              </p>
            )}
          </Section>

          {/* The credits for the vessels listed and for the ship types counted, which the policy asks every page to show. */}
          {credit && <p className="mt-3 px-2 text-caption text-fg-muted">Data: {credit}</p>}

          <ReceiverLink className="mt-5" />

          <Section label="About this station">
            <Facts
              className="text-subhead"
              items={[
                ["Kind", isVolunteer(st.source) ? "Volunteer receiver" : "Data feed"],
                ["Heard across", extent],
                [
                  "Own vessel",
                  st.mmsi != null ? (
                    <Link to={vesselPath(st.mmsi, own)} className="text-accent hover:text-accent-hover">
                      {own ?? `MMSI ${st.mmsi}`}
                    </Link>
                  ) : undefined,
                ],
                ["Station ID", <CopyId id={st.station} />],
              ]}
            />
          </Section>
        </>
      )}
    </Panel>
  );
}

const MAX_VESSELS = 200;
const FIRST_VESSELS = 10;

/** A bbox's height and width on the ground, in nautical miles: "5.4 × 13 nm". */
function formatExtent([minLat, minLon, maxLat, maxLon]: [number, number, number, number]): string {
  const height = (maxLat - minLat) * 60;
  const width = (maxLon - minLon) * 60 * Math.cos((((minLat + maxLat) / 2) * Math.PI) / 180);
  const f = (nm: number) => (nm < 10 ? nm.toFixed(1) : n(Math.round(nm)));
  return `${f(height)} × ${f(width)} nm`;
}

/**
 * The vessels a station heard in the last day: a donut of them by class, in the colors the map draws
 * them in, beside a legend of the counts. Pointing at a slice or row previews its class, and clicking,
 * tapping, or pressing a row pins it: the middle names it, and the caption says how many of that class are
 * unique, heard by no other station. Each row also says so to a screen reader.
 */
function Vessels({ mix, total, only }: { mix: MixSlice[]; total: number; only: number }) {
  // A mouse previews a class while over it; a click, tap, or key pins one until pressed again.
  const [hovered, setHovered] = useState<ShipClass>();
  const [pinned, setPinned] = useState<ShipClass>();
  const at = hovered ?? pinned;
  const focus = mix.find((m) => m.cls === at);
  const R = 48; // in a 120-unit box, the ring 14 wide
  const C = 2 * Math.PI * R;
  const GAP = mix.length > 1 ? 1.5 : 0; // the surface between slices, along the ring
  const sum = mix.reduce((a, m) => a + m.vessels, 0);
  // The types are read apart from the list's counts, so where there are types their own sums lead, and the
  // middle and the caption agree with the slices around them.
  if (sum) {
    total = sum;
    only = mix.reduce((a, m) => a + m.only, 0);
  }
  const hover = (cls: ShipClass | undefined) => (e: PointerEvent) => e.pointerType === "mouse" && setHovered(cls);
  let start = 0;
  return (
    <Section label="Vessels" aside="Last 24 hours">
      <div className="flex items-center gap-4">
        <div className="relative size-28 shrink-0">
          <svg viewBox="0 0 120 120" className="size-full -rotate-90" aria-hidden>
            {!sum && <circle cx={60} cy={60} r={R} fill="none" strokeWidth={14} className="stroke-line" />}
            {sum > 0 &&
              mix.map((m) => {
                const len = (m.vessels / sum) * C;
                const slice = (
                  <circle
                    key={m.cls}
                    cx={60}
                    cy={60}
                    r={R}
                    fill="none"
                    strokeWidth={at === m.cls ? 16 : 14}
                    stroke={CLASS_COLORS[m.cls]}
                    strokeDasharray={`${Math.max(0, len - GAP)} ${C}`}
                    strokeDashoffset={-start}
                    opacity={at && at !== m.cls ? 0.35 : 1}
                    onPointerEnter={hover(m.cls)}
                    onPointerLeave={hover(undefined)}
                  />
                );
                start += len;
                return slice;
              })}
          </svg>
          <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center text-center">
            <span className="text-title font-semibold text-fg tabular-nums">{n(focus?.vessels ?? total)}</span>
            <span className="text-caption text-fg-muted">{focus ? CLASS_LABELS[focus.cls] : "vessels"}</span>
          </div>
        </div>
        <ul className="min-w-0 flex-1 space-y-0.5 text-subhead">
          {mix.map((m) => (
            <li key={m.cls}>
              <button
                type="button"
                aria-pressed={pinned === m.cls}
                className={cn("flex w-full items-center gap-2 rounded px-1 text-left", at === m.cls && "bg-surface-subtle")}
                onPointerEnter={hover(m.cls)}
                onPointerLeave={hover(undefined)}
                onClick={() => setPinned(pinned === m.cls ? undefined : m.cls)}
              >
                <ClassDot cls={m.cls} />
                <span className="min-w-0 flex-1 truncate text-fg-secondary">{CLASS_LABELS[m.cls]}</span>
                <span className="text-fg tabular-nums">{n(m.vessels)}</span>
                <span className="sr-only">, {n(m.only)} unique</span>
              </button>
            </li>
          ))}
        </ul>
      </div>
      <p className="mt-3 text-footnote text-fg-muted" aria-live="polite">
        {focus ? `${n(focus.only)} of ${n(focus.vessels)} ${CLASS_LABELS[focus.cls].toLowerCase()} unique` : `${n(only)} unique`}
      </p>
    </Section>
  );
}

/**
 * Messages in each hour of the last 7 days, as one bar per hour, headed by the station's uptime. Pointing
 * at a bar, tapping it, or stepping with the arrow keys names its hour and count in place of that. Hours
 * are UTC, which the server counts in, from the hour `from` names, so a cached page or one left open labels
 * each count by the hour it was counted in.
 */
function Activity({ hours, from, uptime, status }: { hours: number[]; from: number; uptime?: number | null; status: StationStatus }) {
  const [at, setAt] = useState<number>();
  const percent = uptimePercent(uptime);
  const max = Math.max(1, ...hours);
  const HEIGHT = 48;
  const BAR = 2;
  // The hour each bar starts, in hours since the epoch.
  const first = Math.floor(from / 3_600_000);
  const hourOf = (i: number) => new Date((first + i) * 3_600_000);
  const day = (i: number) => hourOf(i).toLocaleDateString("en-US", { weekday: "short", timeZone: "UTC" });
  const label = (i: number) => `${day(i)} ${hourOf(i).getUTCHours().toString().padStart(2, "0")}:00 UTC · ${n(hours[i]!)}`;
  // Unknown uptime, while the server has not counted the station's hours, reads as nothing rather than none.
  const sent = percent != null ? `${percent}% uptime` : "";
  const total = hours.reduce((a, h) => a + h, 0);
  const pick = (e: PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    setAt(Math.min(hours.length - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * hours.length))));
  };
  return (
    <Section
      label="Messages"
      aside={
        <span className="flex items-center gap-1.5" aria-live="polite">
          {at == null ? (
            <>
              {percent != null && <StatusDot status={status} uptime={percent} className="size-4" />}
              {sent}
            </>
          ) : (
            label(at)
          )}
        </span>
      }
    >
      <svg
        viewBox={`0 0 ${hours.length * BAR} ${HEIGHT + 12}`}
        className="block h-auto w-full touch-pan-y rounded focus-visible:outline-2 focus-visible:outline-accent"
        role="img"
        tabIndex={0}
        aria-label={`Messages per hour over the last 7 days: ${n(total)} in all.${sent ? ` ${sent}.` : ""} Arrow keys step through the hours.`}
        onPointerMove={(e) => e.pointerType === "mouse" && pick(e)}
        onPointerDown={pick}
        onPointerLeave={(e) => e.pointerType === "mouse" && setAt(undefined)}
        onBlur={() => setAt(undefined)}
        onKeyDown={(e) => {
          const step = { ArrowLeft: -1, ArrowRight: 1, Home: -hours.length, End: hours.length }[e.key];
          if (step == null) return;
          e.preventDefault();
          // The first arrow picks the latest hour; Home and End go to their end at once.
          const from = at ?? (e.key === "Home" ? 0 : hours.length - 1);
          setAt(Math.min(hours.length - 1, Math.max(0, at == null ? from : from + step)));
        }}
      >
        {at != null && <rect x={at * BAR - 1} y={0} width={BAR + 2} height={HEIGHT} className="fill-surface-subtle" />}
        {hours.map((h, i) =>
          h > 0 ? (
            <rect
              key={i}
              x={i * BAR}
              y={HEIGHT - Math.max(1, (h / max) * HEIGHT)}
              width={BAR - 0.5}
              height={Math.max(1, (h / max) * HEIGHT)}
              className={i === at ? "fill-accent-hover" : "fill-accent"}
            />
          ) : null,
        )}
        <line x1={0} x2={hours.length * BAR} y1={HEIGHT + 0.5} y2={HEIGHT + 0.5} className="stroke-line" strokeWidth={1} />
        {hours.map((_, i) =>
          hourOf(i).getUTCHours() === 0 ? (
            <text key={i} x={i * BAR + 1} y={HEIGHT + 10} className="fill-fg-muted" fontSize={8}>
              {day(i)}
            </text>
          ) : null,
        )}
      </svg>
    </Section>
  );
}

/** A station's full id, which only its operator needs, cut short with a button to copy it whole. */
function CopyId({ id }: { id: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    await navigator.clipboard.writeText(id);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <span className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 truncate font-mono text-footnote" title={id}>
        {id}
      </span>
      {/* Pulled into the line, so the row is as tall as its text and lines up with its label. */}
      <IconButton icon={copied ? Check : Copy} label={copied ? "Copied" : "Copy station ID"} small className="-my-1.5" onClick={() => void copy()} />
    </span>
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
