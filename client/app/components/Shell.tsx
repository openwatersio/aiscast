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
} from "react";
import { useLocation, useMatches, useNavigate, useNavigation, useNavigationType, useOutlet } from "react-router";
import { parseVesselParam, vesselPath } from "../lib/ais";
import type { CoverageTiles } from "../lib/api";
import { createMap } from "../lib/map.client";
import { liveInstance, LiveContext, setLiveInstance, type Live } from "../lib/live";
import { NO_FILTERS, type SearchFilters } from "../lib/searchFilters";
import { Stream } from "../lib/stream";
import { resolveTheme, useTheme, type ThemeChoice } from "../lib/theme";
import { readVisitor } from "../lib/visitor";
import { CoverageKey } from "./CoverageKey";
import type { CoverageMeasure } from "../lib/coverage";
import { Sheet, type Detent } from "./Sheet";
import { Header } from "./Header";
import { StatusChip } from "./StatusChip";
import { stackStateFor, type StackState } from "./ui/PanelHeader";

interface ShellState {
  query: string;
  setQuery(q: string): void;
  /** The search's filter chips. */
  filters: SearchFilters;
  setFilters(filters: SearchFilters): void;
  /**
   * Whether the panel is searching: from the field taking focus until `endSearch`. With
   * nothing typed, it lists the vessels on the map.
   */
  searching: boolean;
  startSearch(): void;
  /** Leaves search, clearing the query and the filters. */
  endSearch(): void;
  /** Shows the panel's loading bar until the returned function is called. */
  startLoading(): () => void;
  theme: ThemeChoice;
  setTheme(choice: ThemeChoice): void;
  /** The sheet's height on a phone. On a wider screen the panel ignores it. */
  setDetent(detent: Detent): void;
}

const ShellContext = createContext<ShellState>({
  query: "",
  setQuery: () => undefined,
  filters: NO_FILTERS,
  setFilters: () => undefined,
  searching: false,
  startSearch: () => undefined,
  endSearch: () => undefined,
  startLoading: () => () => undefined,
  theme: "system",
  setTheme: () => undefined,
  setDetent: () => undefined,
});

export function useShell(): ShellState {
  return useContext(ShellContext);
}

// How long a search may take before the loading bar says it is still going. Searching as you
// type answers in well under this, and a bar on every key pressed is only noise.
const SLOW_MS = 1000;

/** Shows the panel's loading bar once `active` has lasted longer than a search usually takes. */
export function useLoading(active: boolean) {
  const { startLoading } = useShell();
  useEffect(() => {
    if (!active) return;
    let stop: (() => void) | undefined;
    const t = setTimeout(() => (stop = startLoading()), SLOW_MS);
    return () => {
      clearTimeout(t);
      stop?.();
    };
  }, [active, startLoading]);
}

// Survives opening a vessel, which remounts the search panel, and a reload of the tab. A map
// app that forgets what you searched for the moment you click a result is infuriating.
const SAVED_QUERY = "aiscast.query";

/**
 * How high the sheet sits when a route opens: low over the map with nothing chosen, halfway
 * for anything that has content to read while the map still shows where it is.
 */
function routeDetent(pathname: string): Detent {
  return pathname === "/vessels" ? "peek" : "half";
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

  const navigation = useNavigation();
  const vessel = matches.find((m) => m.id === "routes/vessel");
  // The network and stations pages draw coverage in place of the vessels, and its key goes over
  // the map: vessels a day on one, the stations that heard each cell on the other.
  const coverageRoute = matches.find((m) => m.id === "routes/network" || m.id === "routes/stations");
  const coverage = (coverageRoute?.loaderData as { coverage?: CoverageTiles } | undefined)?.coverage;
  const coverageMeasure: CoverageMeasure = coverageRoute?.id === "routes/stations" ? "stations" : "vessels";

  // Set while rendering the new route rather than in an effect, so the sheet has moved in the
  // same commit and a route's camera move is aimed above where the sheet is going.
  const [detent, setDetent] = useState<Detent>(() => routeDetent(location.pathname));
  const [detentFor, setDetentFor] = useState(location.key);
  if (detentFor !== location.key) {
    setDetentFor(location.key);
    setDetent(routeDetent(location.pathname));
  }

  const [query, setQueryState] = useState("");
  const [filters, setFilters] = useState(NO_FILTERS);
  // Kept here rather than in the panel, so opening a vessel and coming back finds the same list.
  const [searching, setSearching] = useState(false);
  const setQuery = useCallback((q: string) => {
    setQueryState(q);
    // Text can arrive without the field taking focus, restored from the last visit or autofilled.
    if (q.trim()) setSearching(true);
    if (q) sessionStorage.setItem(SAVED_QUERY, q);
    else sessionStorage.removeItem(SAVED_QUERY);
  }, []);
  useEffect(() => setQuery(sessionStorage.getItem(SAVED_QUERY) ?? ""), [setQuery]);
  const startSearch = useCallback(() => setSearching(true), []);
  // A count, so two lists loading at once keep the bar until both are done.
  const [loading, setLoading] = useState(0);
  const startLoading = useCallback(() => {
    setLoading((n) => n + 1);
    let done = false;
    return () => {
      if (done) return;
      done = true;
      setLoading((n) => n - 1);
    };
  }, []);
  const endSearch = useCallback(() => {
    setQuery("");
    setFilters(NO_FILTERS);
    setSearching(false);
  }, [setQuery]);

  const { choice, theme, setChoice } = useTheme(initialTheme);

  useEffect(() => {
    if (liveInstance() || !container.current) return;
    const stream = new Stream();
    // Resolved here rather than taken from render: during hydration a System choice still
    // reads as the server's dark, and the map should open in the device's scheme. The visitor's
    // location is already in the head, so the first frame is drawn there.
    const built = { stream, ctl: createMap(container.current, stream, resolveTheme(choice), readVisitor()) };
    setLiveInstance(built);
    setLive(built);
  }, []);

  // A vessel tapped on the map is pushed onto the stack, over whatever is showing. Over another
  // vessel it records how far back the page beneath them is, so Back skips the vessels.
  const onSelect = useRef<(mmsi: number, name?: string) => void>(undefined);
  // The state a tap asked for, until its page lands. The vessel's loader redirects an address
  // whose slug is not the record's name, and a redirect lands without the state, so it is put
  // back then.
  const tapped = useRef<{ mmsi: number; state: StackState }>(undefined);
  onSelect.current = (mmsi, name) => {
    const known = live?.stream.vessels.get(mmsi)?.name;
    const state = stackStateFor(location, Boolean(vessel));
    tapped.current = state && { mmsi, state };
    navigate(vesselPath(mmsi, known ?? name), { state });
  };
  const navigationType = useNavigationType();
  useEffect(() => {
    const tap = tapped.current;
    tapped.current = undefined;
    if (!tap || navigationType !== "PUSH" || !vessel) return;
    if (parseVesselParam(vessel.params.param ?? "")?.mmsi !== tap.mmsi) return;
    if ((location.state as StackState | null)?.backSteps !== undefined) return;
    // The page is already loaded, so this only puts the state back. The map keeps its camera in
    // the hash, outside the router's sight.
    navigate(
      { pathname: location.pathname, search: location.search, hash: window.location.hash },
      { replace: true, state: tap.state, defaultShouldRevalidate: false },
    );
  }, [location.key]);
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
    () => ({
      query,
      setQuery,
      filters,
      setFilters,
      searching,
      startSearch,
      endSearch,
      startLoading,
      theme: choice,
      setTheme: setChoice,
      setDetent,
    }),
    [query, setQuery, filters, searching, startSearch, endSearch, startLoading, choice, setChoice],
  );

  return (
    <LiveContext.Provider value={live}>
      <ShellContext.Provider value={state}>
        {/* Never display:none and never resized by a route: a zero-size map never fires
            `load`, and MapLibre measures its container once. */}
        <div id="map" ref={container} />

        <Header />
        <StatusChip />
        {coverage && <CoverageKey coverage={coverage} measure={coverageMeasure} />}

        <Sheet detent={detent} onDetentChange={setDetent}>
          {/* While the next entry loads, or a search. Most answer before its delay runs out,
              so it shows only for the slow ones, such as a large station's vessel list. */}
          <div
            aria-hidden
            data-active={navigation.state !== "idle" || loading > 0 || undefined}
            className="pending-bar pointer-events-none absolute inset-x-0 top-0 h-0.5 overflow-hidden opacity-0 transition-opacity data-active:opacity-100 data-active:delay-150"
          />
          {/* Keyed by route, so a navigation is this entry leaving and the next arriving. React
              runs it as a view transition because React Router navigates in transitions. */}
          <ViewTransition key={location.pathname} enter="panel-enter" exit="panel-exit" update="none">
            <div className="flex min-h-0 flex-1 flex-col">{outlet}</div>
          </ViewTransition>
        </Sheet>
      </ShellContext.Provider>
    </LiveContext.Provider>
  );
}
