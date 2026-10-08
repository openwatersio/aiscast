import interBold from "@fontsource/inter/files/inter-latin-700-normal.woff?inline";
import interBoldExt from "@fontsource/inter/files/inter-latin-ext-700-normal.woff?inline";
import interRegular from "@fontsource/inter/files/inter-latin-400-normal.woff?inline";
import interRegularExt from "@fontsource/inter/files/inter-latin-ext-400-normal.woff?inline";
import shareMap from "../assets/share-map.jpg?inline";
import { isVolunteer, stationCardPath, stationTitle, volunteerReceiver } from "./ais";
import { getStations, type ApiAuth, type Station } from "./api";
import { edgeCached, notGetOrHead } from "./edge.server";

/**
 * The image a shared link unfurls with, in the layout of the network's own card: a title, a line
 * under it, and up to three numbers. Pages without one share the network's card.
 */
export interface ShareCardProps {
  title: string;
  subtitle?: string;
  /** What the numbers cover, over them: "Last 24 hours". */
  period?: string;
  stats: Array<{ value: string; label: string }>;
}

const COLORS = { panel: "#071421", label: "#60a5fa", title: "#ffffff", subtitle: "#cbd5e1", muted: "#94a3b8" };

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
        <div style={{ display: "block", marginTop: 26, color: COLORS.title, fontSize: 78, fontWeight: 700, lineHeight: 1.08, lineClamp: 2, letterSpacing: -1, wordBreak: "break-word" }}>
          {title}
        </div>
        {subtitle && (
          <div style={{ display: "block", marginTop: 20, color: COLORS.subtitle, fontSize: 34, lineHeight: 1.3, lineClamp: 2 }}>{subtitle}</div>
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
      fonts: FONTS.map((f) => ({ name: "Inter", data: bytes(f.data), weight: f.weight, style: "normal" as const })),
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
 * The station's card, a 404 for a station the API does not know, or a 503 when it cannot say. The
 * station comes from the list, a few KB, rather than its own answer, which carries every vessel it
 * last heard: 18 MB for AISHub.
 */
export async function stationCard(auth: ApiAuth, id: string): Promise<Response> {
  // A receiver's tagged path is the receiver, as its page redirects.
  const receiver = volunteerReceiver(id);
  if (receiver) return new Response(null, { status: 301, headers: { Location: `/ais${stationCardPath(receiver)}` } });
  const stations = await getStations(auth);
  if (!stations) return new Response("The AIS API is unavailable", { status: 503, headers: { "retry-after": "60" } });
  const st = stations.find((s) => s.station === id);
  if (!st) return new Response("Not found", { status: 404 });
  return shareCard(stationCardProps(st));
}

/** A card's numbers cover 24 hours, so an hour old is fresh enough for a link preview. */
const CARD_TTLS = { 200: 3600, 301: 3600, 404: 300 };

/**
 * The answer to `/ais/stations/<id>.png`, or undefined for any other path. A card is kept at the
 * edge by its station's id, so every spelling of one station's path shares one card.
 */
export function serveStationCard(
  request: Request,
  url: URL,
  auth: ApiAuth,
  cache: Cache,
  waitUntil: (p: Promise<unknown>) => void,
): Promise<Response> | undefined {
  const id = stationCardId(url.pathname);
  if (id == null) return undefined;
  const refused = notGetOrHead(request);
  if (refused) return Promise.resolve(refused);
  return edgeCached(cache, `${url.origin}/ais${stationCardPath(id)}`, CARD_TTLS, waitUntil, () => stationCard(auth, id));
}
