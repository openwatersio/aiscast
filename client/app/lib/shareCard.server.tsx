import interBold from "@fontsource/inter/files/inter-latin-700-normal.woff?inline";
import interBoldExt from "@fontsource/inter/files/inter-latin-ext-700-normal.woff?inline";
import interRegular from "@fontsource/inter/files/inter-latin-400-normal.woff?inline";
import interRegularExt from "@fontsource/inter/files/inter-latin-ext-400-normal.woff?inline";
import shareMap from "../assets/share-map.jpg?inline";
import { isVolunteer, stationTitle, volunteerReceiver } from "./ais";
import { ApiUnavailable, getStation, type ApiAuth, type Station } from "./api";

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
        <div style={{ display: "block", marginTop: 26, color: COLORS.title, fontSize: 78, fontWeight: 700, lineHeight: 1.08, lineClamp: 2, letterSpacing: -1 }}>
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
 * The card as a 1200×630 PNG. The renderer and its wasm load only when a card is asked for. workers-og
 * initializes its wasm on every render and logs "Already initialized" past the first, which is harmless.
 *
 * workers-og answers 200 at once and renders into the body, so a render that fails would go out as a
 * 200 with a broken image, which crawlers keep. The card is read whole first, so a failure throws.
 */
export async function shareCard(props: ShareCardProps): Promise<Response> {
  const { ImageResponse } = await import("workers-og");
  const rendering = new ImageResponse(<ShareCard {...props} />, {
    width: 1200,
    height: 630,
    fonts: FONTS.map((f) => ({ name: "Inter", data: bytes(f.data), weight: f.weight, style: "normal" as const })),
  });
  return new Response(await rendering.arrayBuffer(), { headers: { "content-type": "image/png" } });
}

/** `/ais/stations/<id>.png`, a station's card, answers with the id; any other path with undefined. */
export function stationCardId(pathname: string): string | undefined {
  const m = /^\/ais\/stations\/(.+)\.png$/.exec(pathname);
  if (!m) return undefined;
  try {
    return decodeURIComponent(m[1]!);
  } catch {
    return undefined;
  }
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

/** The station's card, a 404 for a station the API does not know, or a 503 when it cannot say. */
export async function stationCard(auth: ApiAuth, id: string): Promise<Response> {
  // A receiver's tagged path is the receiver, as its page redirects.
  const receiver = volunteerReceiver(id);
  if (receiver) return new Response(null, { status: 301, headers: { Location: `/ais/stations/${receiver}.png` } });
  let found;
  try {
    found = await getStation(auth, id);
  } catch (e) {
    if (e instanceof ApiUnavailable) return new Response("The AIS API is unavailable", { status: 503, headers: { "retry-after": "60" } });
    throw e;
  }
  if (!found) return new Response("Not found", { status: 404 });
  return shareCard(stationCardProps(found.station));
}
