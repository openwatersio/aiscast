import { Antenna, Check, Copy, Share } from "lucide-react";
import { useEffect, useState } from "react";
import { data, Link, redirect } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { RouteError, routeErrorHeaders, routeErrorMeta } from "../components/RouteError";
import { Facts } from "../components/ui/Facts";
import { IconButton } from "../components/ui/IconButton";
import { Prompt } from "../components/ui/Prompt";
import { ClassDot, List, ListRow, StationTitle, StatusDot } from "../components/ui/List";
import { Section, Tile } from "../components/ui/Section";
import { StatGrid } from "../components/ui/StatGrid";
import {
  CLASS_LABELS,
  flagEmoji,
  flagName,
  formatAge,
  isVolunteer,
  shipClass,
  shortAge,
  stationCardPath,
  stationDescription,
  stationStatus,
  stationName,
  stationTitle,
  vesselPath,
  volunteerReceiver,
} from "../lib/ais";
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
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

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

export default function Station({ loaderData }: Route.ComponentProps) {
  const { id, found } = loaderData;
  const st = found?.station;
  const title = st ? stationTitle(st) : id;
  const vessels = found?.vessels.features ?? [];
  const live = useLive();

  // The station's own bbox is every position it has heard since the server last started, which for
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

  const extent = st?.bbox ? formatExtent(st.bbox) : undefined;
  // Vessels newest first, so what the station hears now leads and the order holds from one load to
  // the next. Base stations, aids to navigation, and fishing gear follow: they transmit every few
  // seconds, and would otherwise always lead.
  const fixed = (f: (typeof vessels)[number]) => (["base", "aton", "gear"].includes(f.properties.kind) ? 1 : 0);
  const heard = [...vessels].sort((a, b) => fixed(a) - fixed(b) || Date.parse(b.properties.seen) - Date.parse(a.properties.seen));
  const now = Date.now();
  const own = st?.mmsi != null && st.name_from === "vessel" && st.name ? st.name : undefined;

  return (
    <Panel back="/stations" title={title} actions={<ShareStation id={id} title={title} />}>
      <PageTitle className="break-all">
        <StationTitle name={st ? stationName(st) : { title: id }} suffixClassName="text-headline" />
      </PageTitle>

      {!st ? (
        <Tile className="mt-4 text-body text-fg-secondary">No station with this id has been heard in the last 30 days.</Tile>
      ) : (
        <>
          <p className="mt-1 flex items-center gap-1.5 text-body text-fg-secondary">
            <StatusDot status={stationStatus(st.last_age_s)} inline />
            {isVolunteer(st.source) ? "Volunteer receiver" : "Data feed"}
            {own ? ` aboard ${own}` : ""} · last message {formatAge(st.last_age_s)}
          </p>

          <div className="mt-4">
            <StatGrid
              tiles
              stats={[
                { label: "Vessels 24 h", value: n(st.vessels_24h ?? st.vessels) },
                { label: "Unique 24 h", value: n(st.vessels_exclusive_24h ?? 0) },
                { label: "Messages 24 h", value: compact.format(st.events.last_24h) },
              ]}
            />
          </div>

          <Facts
            className="mt-4"
            items={[
              ["Vessels now", n(st.vessels)],
              ["Messages 7 d", n(st.events.last_7d)],
              ["Others sent first", st.duplicates ? `${n(st.duplicates)} messages` : undefined],
              ["Extent heard", extent],
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

          <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mt-5">
            {isVolunteer(st.source)
              ? "Someone runs this receiver and shares what it hears. You can run one too."
              : "Volunteer receivers fill in where feeds like this one do not reach."}
          </Prompt>

          <Section label="Last reported here" aside={vessels.length === 1 ? "1 vessel" : `${n(vessels.length)} vessels`} bare>
            <List>
              {heard.slice(0, MAX_VESSELS).map((f) => {
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
            {vessels.length > MAX_VESSELS && (
              <p className="mt-2 px-2 text-footnote text-fg-muted">
                The {n(MAX_VESSELS)} most recent of {n(vessels.length)}. The rest are on the map.
              </p>
            )}
          </Section>
        </>
      )}
    </Panel>
  );
}

const MAX_VESSELS = 200;

/** A bbox's height and width on the ground, in nautical miles: "5.4 × 13 nm". */
function formatExtent([minLat, minLon, maxLat, maxLon]: [number, number, number, number]): string {
  const height = (maxLat - minLat) * 60;
  const width = (maxLon - minLon) * 60 * Math.cos((((minLat + maxLat) / 2) * Math.PI) / 180);
  const f = (nm: number) => (nm < 10 ? nm.toFixed(1) : n(Math.round(nm)));
  return `${f(height)} × ${f(width)} nm`;
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
      <IconButton icon={copied ? Check : Copy} label={copied ? "Copied" : "Copy station ID"} small onClick={() => void copy()} />
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
