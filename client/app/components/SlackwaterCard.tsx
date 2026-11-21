import { createQueryClient, SlackwaterProvider, TideConditions } from "@slackwater/react";
import { Component, useCallback, useEffect, useState, type ReactNode } from "react";
import { slackwaterHandoff, TIDES_API, type NearbyStation } from "../lib/slackwater";
import { Tile } from "./ui/Section";

const outbound = { target: "_blank", rel: "noopener noreferrer" } as const;

/**
 * Whether one of the widget's queries leaves it with nothing to show here: a request that failed,
 * whose message the widget would print; a station the API says it can't predict, whose reason it
 * would print; or predictions without the credit their licence asks for beside them.
 */
export function widgetFailed(state: { status: string; data?: unknown }, now = Date.now()): boolean {
  // The widget prints any of its queries' errors in place of the curve, a refetch's included.
  if (state.status === "error") return true;
  if (state.status !== "success") return false;
  const data = state.data as { predictions?: unknown; timeline?: unknown; station?: { attribution?: unknown } } | undefined;
  if (data?.predictions === false) return true;
  if (!Array.isArray(data?.timeline)) return false;
  const credit = data.station?.attribution;
  if (!(typeof credit === "string" && credit.trim() !== "")) return true;
  // A curve that doesn't reach the present has no reading, and the widget shows only its note.
  const times = (data.timeline as Array<{ time?: unknown }>).map((t) => new Date(t.time as string | Date).getTime());
  return times.length < 2 || !(Math.min(...times) <= now && now <= Math.max(...times));
}

/** Past this without both the curve and its highs and lows, the widget counts as failed. */
const WIDGET_WAIT_MS = 15_000;

/** Which of the answers the widget waits for, its curve and its highs and lows, a query holds. */
function answered(state: { status: string; data?: unknown }): "timeline" | "extremes" | undefined {
  if (state.status !== "success") return undefined;
  const data = state.data as { timeline?: unknown; extremes?: unknown } | undefined;
  if (Array.isArray(data?.timeline)) return "timeline";
  if (Array.isArray(data?.extremes)) return "extremes";
  return undefined;
}

/**
 * In the browser, a render error inside the widget leaves the card's links, not the vessel route's
 * error page. React runs no error boundary in a server render, where the widget draws only its
 * placeholder.
 */
class WidgetBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? null : this.props.children;
  }
}

/** The station's credit in an answer of the curve the widget draws, which is the credit it owes. */
export function creditOf(state: { status: string; data?: unknown }): string | undefined {
  const data = state.data as { timeline?: unknown; station?: { attribution?: unknown } } | undefined;
  const credit = data?.station?.attribution;
  return state.status === "success" && Array.isArray(data?.timeline) && typeof credit === "string" && credit.trim() ? credit : undefined;
}

/**
 * The widget, with its own queries, so a failure in them hides it and the links below stand alone.
 * It hands up the credit its curve came with, for the card to keep under its links rather than the
 * paragraph the widget would print under the curve; a failed widget hands up none.
 */
function TideHeadline({ id, href, track, onCredit }: {
  id: string;
  href: string;
  track: (event: { type: string; button: number }) => void;
  onCredit: (credit: string | undefined) => void;
}) {
  const [client] = useState(() => {
    const created = createQueryClient();
    // One retry: past that a reader has waited for a curve long enough to stop expecting one.
    created.setDefaultOptions({ ...created.getDefaultOptions(), queries: { ...created.getDefaultOptions().queries, retry: 1 } });
    return created;
  });
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    // The widget's fetches have no timeout of their own, and a hung one would spin forever.
    const wait = setTimeout(() => setFailed(true), WIDGET_WAIT_MS);
    // The widget draws once both its curve and its highs and lows have come, so the wait ends then.
    const arrived = new Set<string>();
    let credit: string | undefined;
    const stop = client.getQueryCache().subscribe((event) => {
      if (event.type !== "updated") return;
      if (widgetFailed(event.query.state)) return setFailed(true);
      credit = creditOf(event.query.state) ?? credit;
      const answer = answered(event.query.state);
      if (answer) arrived.add(answer);
      if (arrived.size === 2) {
        clearTimeout(wait);
        // Handed up once the curve is drawn, so the credit never shows beside a loading box.
        onCredit(credit);
      }
    });
    return () => {
      clearTimeout(wait);
      stop();
    };
  }, [client, onCredit]);
  useEffect(() => {
    if (failed) onCredit(undefined);
  }, [failed, onCredit]);
  if (failed) return null;
  return (
    // A tap or click anywhere on the widget opens its station's page, through a link laid over it.
    // That link is for pointers only: the widget names its figure from the caption hidden here, so
    // a link around it would be read as the credit paragraph, and an aria-label would hide the
    // reading. Keyboards and screen readers reach the same page by the station's own link, which
    // leads the list below as text a crawler or a page without scripts can read too. Its credit
    // runs to a paragraph, so the card keeps it folded under the links, not under the curve; the
    // widget has no way yet to leave its caption out (openwatersio/slackwater#558).
    <div className="relative mt-2 [&_figcaption]:hidden">
      <WidgetBoundary>
        <SlackwaterProvider baseUrl={TIDES_API} queryClient={client}>
          <TideConditions id={id} />
        </SlackwaterProvider>
      </WidgetBoundary>
      <a
        href={href}
        {...outbound}
        aria-hidden="true"
        tabIndex={-1}
        className="absolute inset-0"
        onClick={track}
        onAuxClick={track}
      />
    </div>
  );
}

/**
 * The tides near a vessel's last position: the nearest tide station the tides API predicts, drawn
 * by Slackwater's widget, then the station pages within reach, that one first, linked by name at
 * their own addresses so a crawler that reaches the vessel reaches them too. The drawn station's
 * credit closes the card in a folded "Data sources", as `@slackwater/react` folds its own lists'
 * credits; a name and a link copy none of a station's data, so the others' notices are left to
 * their pages (docs/policy.md, the Slackwater row). The stations come from the server
 * render, or are asked of the Worker by a page opened inside the app (useNearbyStations). The app
 * link carries no position, so it stays a quiet line under the stations.
 */
export function SlackwaterCard({ ferry, stations = [] }: { ferry: boolean; stations?: NearbyStation[] }) {
  const context = ferry ? "ferry" : "vessel";
  const track = slackwaterHandoff(context);
  const headline = stations[0]?.id;
  // Held with the station it credits, so a moving vessel's next headline never shows the last one's.
  const [credited, setCredited] = useState<{ id: string; credit: string }>();
  const onCredit = useCallback(
    (credit: string | undefined) => setCredited(credit && headline ? { id: headline, credit } : undefined),
    [headline],
  );
  const credit = credited && credited.id === headline ? credited.credit : undefined;
  const app = (
    <a
      href={`https://slackwater.xyz/?ref=ais-${context}`}
      {...outbound}
      className="inline-flex min-h-11 items-center"
      onClick={track}
      onAuxClick={track}
    >Get Slackwater</a>
  );
  // Open water, or a page whose stations haven't come: no tides to show, only the app, in one line.
  if (!stations.length) {
    return (
      <aside className="mt-5" aria-label="Slackwater">
        <p className="text-footnote text-fg-muted">Tides and currents for your coast: {app}</p>
      </aside>
    );
  }
  return (
    <aside className="mt-5" aria-label="Slackwater">
      <Tile>
        <h2 className="text-headline font-semibold text-fg">{stations.some((s) => s.kind === "tide") ? "Tides nearby" : "Currents nearby"}</h2>
        {/* Keyed by station, so a moving vessel's next headline starts with a clean slate. */}
        {headline && <TideHeadline key={headline} id={headline} href={stations[0]!.url} track={track} onCredit={onCredit} />}
        <ul className="mt-2">
          {stations.map((s) => (
            <li key={s.url}>
              {/* 32 px rows read as a list; at 44 px each line sat in its own band of space. */}
              <a href={s.url} {...outbound} className="flex min-h-8 w-fit items-center text-subhead" onClick={track} onAuxClick={track}>
                {/* Kind first: a NOAA current's name can end in its bearing, "Ediz Hook Light, 1.0 nm N of". */}
                {s.kind === "tide" ? "Tides" : "Currents"}: {s.name}
              </a>
            </li>
          ))}
        </ul>
        <p className="text-footnote text-fg-muted">{app}</p>
        {credit && (
          <details className="text-footnote text-fg-muted wrap-anywhere">
            {/* A block, not flex, so the browser keeps the marker that says it opens. */}
            <summary className="cursor-pointer py-2">Data sources</summary>
            <p className="pb-1">{credit}</p>
          </details>
        )}
      </Tile>
    </aside>
  );
}
