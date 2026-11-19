import { createQueryClient, SlackwaterProvider, TideConditions } from "@slackwater/react";
import { Component, useEffect, useState, type ReactNode } from "react";
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

/** Past this with no curve, a request that never answers counts as one that failed. */
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

/** The widget, with its own queries, so a failure in them hides it and the links below stand alone. */
function TideHeadline({ id }: { id: string }) {
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
    const stop = client.getQueryCache().subscribe((event) => {
      if (event.type !== "updated") return;
      if (widgetFailed(event.query.state)) return setFailed(true);
      const answer = answered(event.query.state);
      if (answer) arrived.add(answer);
      if (arrived.size === 2) clearTimeout(wait);
    });
    return () => {
      clearTimeout(wait);
      stop();
    };
  }, [client]);
  if (failed) return null;
  return (
    // Not a link: the widget reads out its tide and credit, which a link's name would hide.
    // Its station leads the list below, as text a crawler or a page without scripts can read.
    // The widget's credit is drawn for its own dark canvas; under it, ours is the page's ink.
    <div className="mt-2 [&_figcaption]:text-fg-muted">
      <WidgetBoundary>
        <SlackwaterProvider baseUrl={TIDES_API} queryClient={client}>
          <TideConditions id={id} />
        </SlackwaterProvider>
      </WidgetBoundary>
    </div>
  );
}

/**
 * The tides near a vessel's last position: the nearest tide station the tides API predicts, drawn
 * by Slackwater's widget, then the station pages within reach, that one first, linked by name at
 * their own addresses so a crawler that reaches the vessel reaches them too. The widget prints its
 * station's credit under the curve; a name and a link copy none of a station's data, so the
 * others' notices are left to their pages (docs/policy.md, the Slackwater row). The stations come from the server
 * render, or are asked of the Worker by a page opened inside the app (useNearbyStations). The app
 * link carries no position, so it stays a quiet line under the stations.
 */
export function SlackwaterCard({ ferry, stations = [] }: { ferry: boolean; stations?: NearbyStation[] }) {
  const context = ferry ? "ferry" : "vessel";
  const track = slackwaterHandoff(context);
  const headline = stations[0]?.id;
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
        {headline && <TideHeadline key={headline} id={headline} />}
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
      </Tile>
    </aside>
  );
}
