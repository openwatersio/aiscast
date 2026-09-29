import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  ViewTransition,
  type ReactNode,
} from "react";
import { useLocation, useMatches, useNavigate, useOutlet } from "react-router";
import { vesselPath } from "../lib/ais";
import { cn } from "../lib/cn";
import { createMap } from "../lib/map.client";
import { liveInstance, LiveContext, setLiveInstance, useLive, useNow, useStreamFrame, type Live } from "../lib/live";
import { Stream } from "../lib/stream";
import { resolveTheme, useTheme, type ThemeChoice } from "../lib/theme";
import { Sheet, type Detent } from "./Sheet";
import { ThemeToggle } from "./ThemeToggle";
import { TrackBar, type TrackSummary } from "./TrackBar";
import { PanelHeader } from "./ui/PanelHeader";

interface ShellState {
  query: string;
  setQuery(q: string): void;
  track: TrackSummary | undefined;
  theme: ThemeChoice;
  setTheme(choice: ThemeChoice): void;
  /** The sheet's height on a phone. On a wider screen the panel ignores it. */
  setDetent(detent: Detent): void;
}

const ShellContext = createContext<ShellState>({
  query: "",
  setQuery: () => undefined,
  track: undefined,
  theme: "dark",
  setTheme: () => undefined,
  setDetent: () => undefined,
});

export function useShell(): ShellState {
  return useContext(ShellContext);
}

// Survives opening a vessel, which remounts the search panel, and a reload of the tab. A map
// app that forgets what you searched for the moment you click a result is infuriating.
const SAVED_QUERY = "aiscast.query";

/**
 * How high the sheet sits when a route opens: low over the map with nothing chosen, halfway
 * for anything that has content to read while the map still shows where it is.
 */
function routeDetent(pathname: string): Detent {
  return pathname === "/map" ? "peek" : "half";
}

/**
 * The map is the page and one panel floats over it, holding a navigation stack: each route is
 * an entry. The map, the stream, and the vessel cache are built once and outlive every
 * navigation: the stream is capped at two connections per network address, and remounting
 * would spend that budget.
 */
export function Shell({ initialTheme }: { initialTheme: ThemeChoice }) {
  const container = useRef<HTMLDivElement>(null);
  const [live, setLive] = useState<Live | undefined>(liveInstance);
  const location = useLocation();
  const matches = useMatches();
  const navigate = useNavigate();
  const outlet = useOutlet();

  const vessel = matches.find((m) => m.id === "routes/vessel");
  const focusMmsi = (vessel?.loaderData as { mmsi?: number } | undefined)?.mmsi;

  // Set while rendering the new route rather than in an effect, so the sheet has moved in the
  // same commit and a route's camera move is aimed above where the sheet is going.
  const [detent, setDetent] = useState<Detent>(() => routeDetent(location.pathname));
  const [detentFor, setDetentFor] = useState(location.key);
  if (detentFor !== location.key) {
    setDetentFor(location.key);
    setDetent(routeDetent(location.pathname));
  }

  const [query, setQueryState] = useState("");
  useEffect(() => setQueryState(sessionStorage.getItem(SAVED_QUERY) ?? ""), []);
  const setQuery = useCallback((q: string) => {
    setQueryState(q);
    if (q) sessionStorage.setItem(SAVED_QUERY, q);
    else sessionStorage.removeItem(SAVED_QUERY);
  }, []);

  const [track, setTrack] = useState<TrackSummary>();
  const { choice, theme, setChoice } = useTheme(initialTheme);

  useEffect(() => {
    if (liveInstance() || !container.current) return;
    const stream = new Stream();
    // Resolved here rather than taken from render: during hydration a System choice still
    // reads as the server's dark, and the map should open in the device's scheme.
    const built = { stream, ctl: createMap(container.current, stream, resolveTheme(choice)) };
    setLiveInstance(built);
    setLive(built);
  }, []);

  // A vessel tapped on the map is pushed onto the stack, over whatever is showing.
  const onSelect = useRef<(mmsi: number, name?: string) => void>(undefined);
  onSelect.current = (mmsi, name) => {
    const known = live?.stream.vessels.get(mmsi)?.name;
    navigate(vesselPath(mmsi, known ?? name));
  };
  useEffect(() => {
    if (!live) return;
    live.ctl.onSelect((mmsi, name) => onSelect.current?.(mmsi, name));
    // Moving the map means looking at it, so the sheet gets out of the way.
    live.ctl.map.on("dragstart", () => setDetent("peek"));
  }, [live]);

  // A layout effect so it runs before any route's own effects: those ask for camera moves,
  // which have to be computed against where the panel will be, and a padding change after a
  // move has started cancels it.
  useLayoutEffect(() => {
    if (!live) return;
    live.ctl.refreshInsets();
  }, [live, location.key]);

  // Which way this navigation went, for the panel's transition: back when the history index
  // went down, which covers the browser's own back button as well as the panel's.
  const historyIndex = useRef<number | undefined>(undefined);
  useLayoutEffect(() => {
    const idx = (window.history.state as { idx?: number } | null)?.idx;
    const prev = historyIndex.current;
    document.documentElement.dataset.nav = prev != null && idx != null && idx < prev ? "back" : "forward";
    historyIndex.current = idx;
  }, [location.key]);

  useEffect(() => {
    if (live && !vessel) live.ctl.setFocus(undefined);
  }, [live, vessel]);

  useEffect(() => {
    live?.ctl.setTheme(theme);
  }, [live, theme]);

  const state = useMemo(
    () => ({ query, setQuery, track, theme: choice, setTheme: setChoice, setDetent }),
    [query, setQuery, track, choice, setChoice],
  );

  return (
    <LiveContext.Provider value={live}>
      <ShellContext.Provider value={state}>
        {/* Never display:none and never resized by a route: a zero-size map never fires
            `load`, and MapLibre measures its container once. */}
        <div id="map" ref={container} />

        {focusMmsi ? <TrackBar key={focusMmsi} mmsi={focusMmsi} onLoaded={setTrack} /> : null}

        <Sheet detent={detent} onDetentChange={setDetent}>
          {/* Keyed by route, so a navigation is this entry leaving and the next arriving. React
              runs it as a view transition because React Router navigates in transitions. */}
          <ViewTransition key={location.pathname} enter="panel-enter" exit="panel-exit" update="none">
            <div className="flex min-h-0 flex-1 flex-col">{outlet}</div>
          </ViewTransition>
          <footer className="flex items-center gap-2 border-t border-line py-1 pr-1.5 pl-3 text-xs text-fg-muted">
            <span className="min-w-0 flex-1">
              <StreamStatus /> · <a href="/ais/">Open Waters AIS</a> · not for navigation
            </span>
            <ThemeToggle />
          </footer>
        </Sheet>
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

/** One entry on the panel's stack. `back` names its parent, for after a direct visit. */
export function Panel({ back, header, children }: { back?: string; header?: ReactNode; children: ReactNode }) {
  return (
    <>
      <PanelHeader back={back} />
      {header}
      <div data-sheet-scroll className={cn("sidebar-scroll min-h-0 flex-1 pb-3", header ? "px-2" : "px-4")}>
        {children}
      </div>
    </>
  );
}
