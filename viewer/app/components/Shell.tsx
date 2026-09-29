import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link, useLocation, useMatches, useNavigate, useOutlet } from "react-router";
import { vesselPath } from "../lib/ais";
import { createMap } from "../lib/map.client";
import { liveInstance, LiveContext, setLiveInstance, useLive, useNow, useStreamFrame, type Live } from "../lib/live";
import { Stream } from "../lib/stream";
import { CloseIcon } from "./icons";
import { TrackBar, type TrackSummary } from "./TrackBar";

/** Where a vessel was opened from, carried in history state rather than in its URL. */
export interface FromState {
  from?: string;
}

interface ShellState {
  /** True when this render is the second pane, beside the list it was opened from. */
  split: boolean;
  /** A list route hands over what it rendered, so a vessel opened from it can keep it on screen. */
  rememberList(path: string, node: ReactNode): void;
  query: string;
  setQuery(q: string): void;
  track: TrackSummary | undefined;
}

const ShellContext = createContext<ShellState>({
  split: false,
  rememberList: () => undefined,
  query: "",
  setQuery: () => undefined,
  track: undefined,
});

export function useShell(): ShellState {
  return useContext(ShellContext);
}

// Survives opening a vessel, which remounts the search panel, and a reload of the tab. A map
// app that forgets what you searched for the moment you click a result is infuriating.
const SAVED_QUERY = "aiscast.query";

/**
 * The map is the page and the panes float over it. The map, the stream, and the vessel cache
 * are built once and outlive every navigation: the stream is capped at two connections per
 * network address, and remounting would spend that budget.
 */
export function Shell() {
  const container = useRef<HTMLDivElement>(null);
  const [live, setLive] = useState<Live | undefined>(liveInstance);
  const location = useLocation();
  const matches = useMatches();
  const navigate = useNavigate();
  const outlet = useOutlet();

  const vessel = matches.find((m) => m.id === "routes/vessel");
  const focusMmsi = (vessel?.loaderData as { mmsi?: number } | undefined)?.mmsi;
  const from = (location.state as FromState | null)?.from;

  // What the last list route rendered. A ref, not state: it is read when the next route
  // renders, and storing it as state would re-render the list that is storing it.
  const list = useRef<{ path: string; node: ReactNode } | undefined>(undefined);
  const rememberList = useCallback((path: string, node: ReactNode) => {
    list.current = { path, node };
  }, []);
  const remembered = vessel && from && list.current?.path === from ? list.current : undefined;
  const split = Boolean(remembered);

  const [query, setQueryState] = useState("");
  useEffect(() => setQueryState(sessionStorage.getItem(SAVED_QUERY) ?? ""), []);
  const setQuery = useCallback((q: string) => {
    setQueryState(q);
    if (q) sessionStorage.setItem(SAVED_QUERY, q);
    else sessionStorage.removeItem(SAVED_QUERY);
  }, []);

  const [track, setTrack] = useState<TrackSummary>();

  useEffect(() => {
    if (liveInstance() || !container.current) return;
    const stream = new Stream();
    const built = { stream, ctl: createMap(container.current, stream) };
    setLiveInstance(built);
    setLive(built);
  }, []);

  // A click on the map opens the vessel beside whatever list is showing, as a click on the
  // list itself would.
  const listPath = vessel ? (split ? from : undefined) : location.pathname;
  const onSelect = useRef<(mmsi: number, name?: string) => void>(undefined);
  onSelect.current = (mmsi, name) => {
    const known = live?.stream.vessels.get(mmsi)?.name;
    navigate(vesselPath(mmsi, known ?? name), { state: { from: listPath } satisfies FromState });
  };
  useEffect(() => {
    live?.ctl.onSelect((mmsi, name) => onSelect.current?.(mmsi, name));
  }, [live]);

  // A layout effect so it runs before any route's own effects: those ask for camera moves,
  // which have to be computed against the panes this route shows, and a padding change after
  // a move has started cancels it.
  useLayoutEffect(() => {
    if (!live) return;
    live.ctl.refreshInsets();
  }, [live, location.key, split]);

  useEffect(() => {
    if (live && !vessel) live.ctl.setFocus(undefined);
  }, [live, vessel]);

  const state = useMemo(
    () => ({ split, rememberList, query, setQuery, track }),
    [split, rememberList, query, setQuery, track],
  );

  return (
    <LiveContext.Provider value={live}>
      <ShellContext.Provider value={state}>
        {/* Never display:none and never resized by a route: a zero-size map never fires
            `load`, and MapLibre measures its container once. */}
        <div id="map" ref={container} />

        {focusMmsi ? <TrackBar key={focusMmsi} mmsi={focusMmsi} onLoaded={setTrack} /> : null}

        <div className="pointer-events-none fixed inset-0 flex flex-col justify-end gap-2 p-2 md:flex-row md:justify-start md:gap-3 md:p-3">
          <aside
            data-map-inset
            className="pane pointer-events-auto order-2 flex max-h-[50vh] w-full flex-col overflow-hidden md:order-1 md:max-h-full md:w-[340px] lg:w-[380px]"
          >
            <div className="flex min-h-0 flex-1 flex-col">{remembered ? remembered.node : outlet}</div>
            <footer className="border-t px-3 py-2 text-xs" style={{ color: "var(--text-muted)" }}>
              <StreamStatus /> · <a href="/ais/">Open Waters AIS</a> · not for navigation
            </footer>
          </aside>

          {split && (
            <section
              data-map-inset
              className="pane sidebar-scroll pointer-events-auto relative order-1 max-h-[45vh] w-full md:order-2 md:max-h-full md:w-[380px] lg:w-[420px]"
            >
              <Link
                to={from!}
                className="absolute top-3 right-3 z-10 flex size-7 items-center justify-center rounded-full border no-underline backdrop-blur"
                style={{ backgroundColor: "var(--surface-subtle)", color: "var(--text-secondary)" }}
                aria-label="Close"
              >
                <CloseIcon className="size-4" />
              </Link>
              <div className="p-4 pr-12">{outlet}</div>
            </section>
          )}
        </div>
      </ShellContext.Provider>
    </LiveContext.Provider>
  );
}

function StreamStatus() {
  const live = useLive();
  useStreamFrame();
  // The map's state and the overview change without a stream frame, and the overview has none.
  useNow(1000);
  if (!live) return <span>connecting</span>;
  const { state, eventsPerSec, vessels } = live.stream;
  const map = live.ctl.status();
  const stream =
    live.ctl.mode() === "overview"
      ? "overview · zoom in for live"
      : state === "live"
        ? `${eventsPerSec}/s · ${vessels.size} tracked`
        : state === "capped"
          ? "zoom in for vessels"
          : state === "refused"
            ? "live stream in use in another tab"
            : state;
  return <span>{map.state === "ready" ? stream : `${stream} · map ${map.state} ${map.detail}`}</span>;
}

/**
 * A sidebar view. `back` pops the stack. A list view is also remembered, so a vessel opened
 * from it takes the second pane and leaves the list where it was.
 */
export function Panel({
  back,
  header,
  list = false,
  children,
}: {
  back?: string;
  /** Stays put above the scrolling content. */
  header?: ReactNode;
  list?: boolean;
  children: ReactNode;
}) {
  const { rememberList } = useShell();
  const { pathname } = useLocation();
  const node = (
    <>
      {back && (
        <div className="px-3 pt-3">
          <Link
            to={back}
            className="inline-flex size-8 items-center justify-center rounded-full no-underline hover:bg-[var(--surface-subtle)]"
            style={{ color: "var(--text-secondary)" }}
            aria-label="Back"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="size-5">
              <path d="M15 18l-6-6 6-6" />
            </svg>
          </Link>
        </div>
      )}
      {header}
      <div className={`sidebar-scroll min-h-0 flex-1 pb-3 ${header ? "px-2" : "px-4"}`}>{children}</div>
    </>
  );
  useEffect(() => {
    if (list) rememberList(pathname, node);
  });
  return node;
}
