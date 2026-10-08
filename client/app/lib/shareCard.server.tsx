import interBold from "@fontsource/inter/files/inter-latin-700-normal.woff?inline";
import interBoldExt from "@fontsource/inter/files/inter-latin-ext-700-normal.woff?inline";
import interRegular from "@fontsource/inter/files/inter-latin-400-normal.woff?inline";
import interRegularExt from "@fontsource/inter/files/inter-latin-ext-400-normal.woff?inline";
import shareMap from "../assets/share-map.jpg?inline";
import {
  CLASS_LABELS,
  flagName,
  isVolunteer,
  shipClass,
  stationCardPath,
  stationTitle,
  vesselCardPath,
  vesselDimensions,
  volunteerReceiver,
} from "./ais";
import { ApiUnavailable, getStations, getStats, getVessel, type ApiAuth, type Station, type Stats, type VesselProps } from "./api";
import { edgeCached, notGetOrHead } from "./edge.server";

/**
 * The image a shared link unfurls with, in the layout of the network's own card: a title, a line
 * under it, and up to three numbers. Pages without one share the default card, openwaters.io/og/ais.png.
 */
export interface ShareCardProps {
  title: string;
  subtitle?: string;
  /** What the numbers cover, over them: "Last 24 hours". */
  period?: string;
  stats: Array<{ value: string; label: string }>;
}

const COLORS = { panel: "#071421", label: "#60a5fa", title: "#ffffff", subtitle: "#cbd5e1", muted: "#94a3b8" };

/**
 * The title's and subtitle's heights at most: two lines each, so a third, which only the longest
 * names and places need, is cut off rather than run into the numbers. Not lineClamp, which with balanced wrapping cuts a
 * title that fits.
 */
const TITLE_MAX = Math.ceil(78 * 1.08 * 2);
const SUBTITLE_MAX = Math.ceil(34 * 1.3 * 2);

/** The text column's width: the card's, less its margins, so a title fits on one line. */
const TEXT = 1060;

/**
 * The panel over the map, a diagonal fade: solid at the top left, behind the title, and clear at
 * the bottom right, easing across almost the whole card so the map comes in gradually.
 */
const FADE = `linear-gradient(to top left, ${Array.from({ length: 21 }, (_, i) => {
  const t = i / 20;
  const alpha = t * t * (3 - 2 * t); // smoothstep, clear at the bottom right
  return `rgba(7, 20, 33, ${alpha.toFixed(3)}) ${(3 + t * 94).toFixed(2)}%`;
}).join(", ")})`;

/**
 * The card: the text over the map from openwaters.io/ais/, dimmed by a quarter, which shows through
 * the fade toward the bottom right. `assets/share-map.jpg` is that map, captured at the card's size without its controls.
 */
export function ShareCard({ title, subtitle, period, stats }: ShareCardProps) {
  return (
    <div style={{ display: "flex", width: "100%", height: "100%", background: COLORS.panel, fontFamily: "Inter" }}>
      <img src={shareMap} width={1200} height={630} style={{ position: "absolute", left: 0, top: 0, opacity: 0.75 }} />
      <div style={{ position: "absolute", left: 0, top: 0, width: 1200, height: 630, backgroundImage: FADE }} />
      <div style={{ display: "flex", flexDirection: "column", width: TEXT + 70, height: "100%", padding: "84px 0 60px 70px" }}>
        <div style={{ display: "flex", color: COLORS.label, fontSize: 26, fontWeight: 700, letterSpacing: 4 }}>OPEN WATERS AIS</div>
        <div style={{ display: "block", marginTop: 26, color: COLORS.title, fontSize: 78, fontWeight: 700, lineHeight: 1.08, maxHeight: TITLE_MAX, overflow: "hidden", letterSpacing: -1, wordBreak: "break-word", textWrap: "balance" }}>
          {title}
        </div>
        {subtitle && (
          <div style={{ display: "block", marginTop: 20, color: COLORS.subtitle, fontSize: 34, lineHeight: 1.3, maxHeight: SUBTITLE_MAX, overflow: "hidden", textWrap: "balance" }}>{subtitle}</div>
        )}
        <div style={{ display: "flex", flexDirection: "column", marginTop: "auto" }}>
          {period && (
            <div style={{ display: "flex", color: COLORS.muted, fontSize: 20, fontWeight: 700, letterSpacing: 3 }}>{period.toUpperCase()}</div>
          )}
          <div style={{ display: "flex", gap: 64, marginTop: 14 }}>
            {stats.map((s) => (
              <div key={s.label} style={{ display: "flex", flexDirection: "column" }}>
                <div style={{ display: "flex", color: COLORS.title, fontSize: 68, fontWeight: 700 }}>{s.value}</div>
                <div style={{ display: "flex", color: COLORS.muted, fontSize: 26 }}>{s.label}</div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** A woff file Vite has inlined as a data URL, back to its bytes. */
function bytes(dataUrl: string): ArrayBuffer {
  const bin = atob(dataUrl.slice(dataUrl.indexOf(",") + 1));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

// Latin and Latin Extended, so Nordic and other European names render; a name in another script
// shows its letters as boxes.
const FONTS = [
  { data: interRegular, weight: 400 },
  { data: interRegularExt, weight: 400 },
  { data: interBold, weight: 700 },
  { data: interBoldExt, weight: 700 },
] as const;

/**
 * The last render to start, settled by the request that started it when that render is done.
 * workers-og swaps in a fresh layout engine on every render and satori looks it up again at each
 * node, so a render that starts while another waits on an image can corrupt the other's layout.
 * Each render waits for the one before, for RENDER_WAIT_MS at most: a request canceled mid-render
 * never settles its turn, and the next card must not hang on it.
 */
let turn: Promise<void> = Promise.resolve();
export const RENDER_WAIT_MS = 5000;

/** workers-og, imported when an isolate first draws a card. An import settles without any request's I/O. */
let renderer: Promise<typeof import("workers-og")> | undefined;

/** The fonts' bytes, decoded when an isolate first draws a card rather than for every card. */
let fonts: Array<{ name: string; data: ArrayBuffer; weight: 400 | 700; style: "normal" }> | undefined;

/**
 * The card as a 1200×630 PNG. The renderer and its wasm load only when a card is asked for. workers-og
 * logs "init RESVG" and "Already initialized" on every render, which is harmless.
 *
 * workers-og answers 200 at once and renders into the body, so a render that fails would go out as a
 * 200 with a broken image, which crawlers keep. The card is read whole first, so a failure throws.
 */
export async function shareCard(props: ShareCardProps): Promise<Response> {
  const { ImageResponse } = await (renderer ??= import("workers-og"));
  const before = turn;
  let done!: () => void;
  turn = new Promise((resolve) => (done = resolve));
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([before, new Promise((resolve) => (timer = setTimeout(resolve, RENDER_WAIT_MS)))]);
  clearTimeout(timer);
  try {
    const rendering = new ImageResponse(<ShareCard {...props} />, {
      width: 1200,
      height: 630,
      fonts: (fonts ??= FONTS.map((f) => ({ name: "Inter", data: bytes(f.data), weight: f.weight, style: "normal" as const }))),
    });
    return new Response(await rendering.arrayBuffer(), { headers: { "content-type": "image/png" } });
  } finally {
    done();
  }
}

/**
 * `/ais/stations/<id>.png`, a station's card, answers with the id; any other path with undefined.
 * An id with a "." or ".." segment, which "%2F" can spell, is not a card: its cache key would resolve
 * to another station's, or out of the cards altogether. No station id has one.
 */
export function stationCardId(pathname: string): string | undefined {
  const m = /^\/ais\/stations\/(.+)\.png$/.exec(pathname);
  if (!m) return undefined;
  let id: string;
  try {
    id = decodeURIComponent(m[1]!);
  } catch {
    return undefined;
  }
  return id.split("/").some((s) => s === "." || s === "..") ? undefined : id;
}

const n = (v: number) => v.toLocaleString("en-US");

/** What a station's card says: its title, what it is and where, and its last 24 hours. */
export function stationCardProps(st: Station): ShareCardProps {
  const title = stationTitle(st);
  let subtitle = isVolunteer(st.source) ? "Volunteer receiver" : "Data feed";
  if (st.near && title !== `Near ${st.near}`) subtitle += ` near ${st.near}`;
  return {
    title,
    subtitle,
    period: "Last 24 hours",
    stats: [
      { value: n(st.vessels_24h ?? st.vessels), label: "vessels" },
      { value: n(st.vessels_exclusive_24h ?? 0), label: "unique vessels" },
    ],
  };
}

/**
 * The station list as a station card can trust it, or undefined while its counts are missing: for the
 * minutes after the server starts, and while it cannot read them from ClickHouse, every station's
 * 24-hour counts read 0. A card drawn then would show 0 vessels for the hour it is kept, and longer in
 * the copies link previews keep. No network that heard messages heard no vessels, so that list is
 * answered as an outage, which is not kept.
 */
function loaded(stations: Station[] | undefined): Station[] | undefined {
  if (!stations) return undefined;
  const heard = stations.some((s) => s.events.last_24h > 0);
  const counted = stations.some((s) => (s.vessels_24h ?? s.vessels) > 0);
  return heard && !counted ? undefined : stations;
}

/**
 * The station's card, a 404 for a station the API does not know, or a 503 when it cannot say. The
 * station comes from the list, a few KB, rather than its own answer, which carries every vessel it
 * last heard: 18 MB for AISHub.
 */
export async function stationCard(auth: ApiAuth, id: string): Promise<Response> {
  // A receiver's tagged path is the receiver, as its page redirects.
  const receiver = volunteerReceiver(id);
  if (receiver) return new Response(null, { status: 301, headers: { Location: `/ais${stationCardPath(receiver)}` } });
  const stations = loaded(await getStations(auth));
  if (!stations) return unavailable();
  const st = stations.find((s) => s.station === id);
  if (!st) return new Response("Not found", { status: 404 });
  return shareCard(stationCardProps(st));
}

/** The vessel a card path names, `/ais/vessels/<mmsi>.png`, or undefined for any other path. */
export function vesselCardMmsi(pathname: string): number | undefined {
  const m = /^\/ais\/vessels\/(\d{1,9})\.png$/.exec(pathname);
  const mmsi = m ? Number(m[1]) : 0;
  return mmsi > 0 ? mmsi : undefined;
}

const unavailable = () => new Response("The AIS API is unavailable", { status: 503, headers: { "retry-after": "60" } });

/** Facts that don't change by the minute, for the hour a card is kept: its size, year built, voyage draught, then its numbers. */
export function vesselCardProps(p: VesselProps): ShareCardProps {
  const size = vesselDimensions(
    p.to_bow != null ? { toBow: p.to_bow, toStern: p.to_stern ?? 0, toPort: p.to_port ?? 0, toStarboard: p.to_starboard ?? 0 } : undefined,
    p.length,
    p.beam,
  );
  const built = p.particulars?.year_built;
  const stats = [
    ...(size ? [{ value: `${n(size.length)} m`, label: "length" }, { value: `${n(size.beam)} m`, label: "beam" }] : []),
    ...(built ? [{ value: String(built), label: "built" }] : []),
    ...(p.draught ? [{ value: `${p.draught.toFixed(1)} m`, label: "draught" }] : []),
    { value: String(p.mmsi), label: "MMSI" },
    ...(p.imo ? [{ value: String(p.imo), label: "IMO" }] : []),
  ].slice(0, 3);
  // "Other" says nothing about a vessel, so its line is only the flag, or nothing.
  const cls = shipClass(p.kind, p.type);
  const subtitle = [cls !== "other" ? CLASS_LABELS[cls] : undefined, flagName(p.flag)].filter(Boolean).join(" · ");
  return { title: p.name ?? `MMSI ${p.mmsi}`, subtitle: subtitle || undefined, stats };
}

/** A vessel's card, a 404 for an MMSI the network has never heard, or a 503 when the API cannot say. */
export async function vesselCard(auth: ApiAuth, mmsi: number): Promise<Response> {
  let feature;
  try {
    feature = await getVessel(auth, mmsi);
  } catch (e) {
    if (e instanceof ApiUnavailable) return unavailable();
    throw e;
  }
  if (!feature) return new Response("Not found", { status: 404 });
  return shareCard(vesselCardProps(feature.properties));
}

const DAY_S = 86400;
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

/** The network's last 24 hours: vessels and messages from its stats, and the volunteer stations heard in them. */
export function networkCardProps(stats: Stats, stations: Station[]): ShareCardProps {
  return {
    title: "Network status",
    subtitle: "Live AIS from government feeds, aggregators, and volunteer receivers",
    period: "Last 24 hours",
    stats: [
      ...(stats.vessels.last_24h != null ? [{ value: n(stats.vessels.last_24h), label: "vessels" }] : []),
      { value: compact.format(stats.events.last_24h), label: "messages" },
      // Stations are the volunteer receivers, as on the station list's card; feeds are counted there.
      { value: n(stations.filter((s) => s.last_age_s < DAY_S && isVolunteer(s.source)).length), label: "stations" },
    ],
  };
}

export async function networkCard(auth: ApiAuth): Promise<Response> {
  // Its numbers come from the stats and the stations' last messages, not their vessel counts, so it
  // draws whether or not those are loaded.
  const [stats, stations] = await Promise.all([getStats(auth), getStations(auth)]);
  if (!stats || !stations) return unavailable();
  return shareCard(networkCardProps(stats, stations));
}

/**
 * The last 24 hours of the station list, with nothing counted twice: the feeds and the volunteer
 * stations heard, and the vessels that only one of them heard.
 */
export function stationsCardProps(stations: Station[]): ShareCardProps {
  const heard = stations.filter((s) => s.last_age_s < DAY_S);
  const volunteers = heard.filter((s) => isVolunteer(s.source)).length;
  return {
    title: "Receiving stations",
    subtitle: "Volunteer receivers and data feeds in the open AIS network",
    period: "Last 24 hours",
    stats: [
      { value: n(heard.length - volunteers), label: "feeds" },
      { value: n(volunteers), label: "stations" },
      { value: n(heard.reduce((sum, s) => sum + (s.vessels_exclusive_24h ?? 0), 0)), label: "vessels" },
    ],
  };
}

export async function stationsCard(auth: ApiAuth): Promise<Response> {
  const stations = loaded(await getStations(auth));
  if (!stations) return unavailable();
  return shareCard(stationsCardProps(stations));
}

/** A card's numbers cover 24 hours, so an hour old is fresh enough for a link preview. */
const CARD_TTLS = { 200: 3600, 301: 3600, 404: 300 };

/** The card a path names, by its canonical path within the app, and how to draw it. */
function cardFor(pathname: string, auth: ApiAuth): { path: string; make: () => Promise<Response> } | undefined {
  if (pathname === "/ais/network.png") return { path: "/network.png", make: () => networkCard(auth) };
  if (pathname === "/ais/stations.png") return { path: "/stations.png", make: () => stationsCard(auth) };
  const mmsi = vesselCardMmsi(pathname);
  if (mmsi != null) return { path: vesselCardPath(mmsi), make: () => vesselCard(auth, mmsi) };
  const id = stationCardId(pathname);
  if (id != null) return { path: stationCardPath(id), make: () => stationCard(auth, id) };
  return undefined;
}

/**
 * The answer to a card's path, or undefined for any other path. A card is kept at the edge by its
 * canonical path, so every spelling of one station's or vessel's path shares one card.
 */
export function serveCard(
  request: Request,
  url: URL,
  auth: ApiAuth,
  cache: Cache,
  waitUntil: (p: Promise<unknown>) => void,
): Promise<Response> | undefined {
  const card = cardFor(url.pathname, auth);
  if (!card) return undefined;
  const refused = notGetOrHead(request);
  if (refused) return Promise.resolve(refused);
  return edgeCached(cache, `${url.origin}/ais${card.path}`, CARD_TTLS, waitUntil, card.make);
}
