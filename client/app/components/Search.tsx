import { Antenna } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import {
  centerBoxes,
  CLASS_LABELS,
  distanceNM,
  flagEmoji,
  flagName,
  formatDistance,
  shipClass,
  shortAge,
  vesselPath,
} from "../lib/ais";
import { browserAuth, searchVessels, vesselsInArea, type VesselFeature } from "../lib/api";
import { useLive, useMapView, useNow, useStreamFrame } from "../lib/live";
import { mediaKey, smallThumb } from "../lib/media";
import { CONTRIBUTE, CONTRIBUTE_PROMPT } from "../lib/links";
import { BROWSE } from "../lib/nav";
import {
  areaParams,
  filterParams,
  filterTest,
  firstDayOfWeek,
  hasFilters,
  HEARD,
  NO_FILTERS,
  SHIP_TYPES,
  sortOf,
  WHERE,
  type SearchFilters,
  type SearchView,
  type Where,
} from "../lib/searchFilters";
import { locate, useMyPosition } from "../lib/geolocation";
import { useMedia } from "../lib/useMedia";
import { cn } from "../lib/cn";
import { useLoading, useShell } from "./Shell";
import { ChipRow, MenuChip } from "./ui/Chip";
import { ClassDot, IconBadge, List, ListRow } from "./ui/List";
import { Prompt } from "./ui/Prompt";
import { SearchField } from "./ui/SearchField";

interface Row {
  mmsi: number;
  name?: string;
  sog?: number;
  kind?: string;
  shipType?: number;
  seen: number;
  flag?: string;
  imo?: number;
  lat?: number;
  lon?: number;
  length?: number;
  /** The town or region nearest the vessel, from the server. */
  near?: string;
}

function fromFeature(f: VesselFeature): Row {
  const p = f.properties;
  return {
    mmsi: p.mmsi,
    name: p.name,
    sog: p.sog,
    kind: p.kind,
    shipType: p.type,
    seen: Date.parse(p.seen),
    flag: p.flag,
    imo: p.imo,
    lon: f.geometry?.coordinates[0],
    lat: f.geometry?.coordinates[1],
    length: p.length,
    near: p.near,
  };
}

/** What tells one result from another of the same name: where it is, what it is, and whether it is moving. */
function rowSubtitle(v: Row, byMMSI: boolean): string | undefined {
  const cls = shipClass(v.kind, v.shipType);
  const what = [cls !== "other" ? CLASS_LABELS[cls] : undefined, v.length ? `${v.length} m` : undefined].filter(Boolean).join(", ");
  const parts = [
    // A vessel without a name is already titled by its MMSI.
    byMMSI && v.name ? `MMSI ${v.mmsi}` : undefined,
    v.near,
    what || undefined,
    // A moored boat reads 0.0 kn, which says nothing a still dot does not.
    v.sog != null && v.sog >= 0.5 ? `${v.sog.toFixed(1)} kn` : undefined,
  ].filter(Boolean);
  if (parts.length) return parts.join(" · ");
  return v.name ? `MMSI ${v.mmsi}` : undefined;
}

// Per query, filters, and view, the most recent of them: the list repaints every second for
// ages and speeds, and that must not ask the server again. Keyed on the Heard choice rather
// than the seconds it becomes, which change every minute.
const results = new Map<string, Row[]>();
// The newest answer per query and filters, whatever the view, shown while a pan's answer loads
// so the list reorders rather than falling back to what the stream holds.
const latest = new Map<string, Row[]>();

// Each pan is a new view and so a new answer, and an answer for the map can hold thousands of
// vessels, so only the most recent are kept. A Map iterates in insertion order, oldest first.
const CACHED_ANSWERS = 20;

function remember(cache: Map<string, Row[]>, key: string, rows: Row[]) {
  cache.delete(key);
  cache.set(key, rows);
  for (const old of cache.keys()) {
    if (cache.size <= CACHED_ANSWERS) break;
    cache.delete(old);
  }
}

/**
 * The search box that heads the home panel, and the filters under it while searching. Search
 * starts when the field takes focus and ends at its clear button or Escape, not when it loses
 * focus, which a tap on a chip or a scroll of the list does.
 */
export function SearchBox() {
  const { query, setQuery, searching, startSearch, endSearch, setDetent } = useShell();
  const navigate = useNavigate();
  return (
    <div className="pb-2">
      <div className="flex items-center gap-1 p-3 pb-2">
        <div className="min-w-0 flex-1">
          <SearchField
            placeholder="Search vessels by name or MMSI"
            value={query}
            onChange={setQuery}
            onSubmit={(q) => {
              if (/^\d{7,9}$/.test(q)) navigate(`/vessels/${q}`);
            }}
            // Searching needs room for results, as in a maps app.
            onFocus={() => {
              startSearch();
              setDetent("full");
            }}
            onClear={endSearch}
            clearable={searching}
          />
        </div>
      </div>
      {searching && <SearchChips />}
    </div>
  );
}

const TYPE_OPTIONS = [
  { value: "any", label: "Any type", chip: "Any type" },
  ...SHIP_TYPES.map((t) => ({ value: t.label, label: t.label, chip: t.label })),
];

/** Filters for the search, as chips. */
function SearchChips() {
  const { query, filters, setFilters } = useShell();
  const live = useLive();
  const [locating, setLocating] = useState(false);
  const [note, setNote] = useState<string>();
  const set = (change: Partial<SearchFilters>) => {
    setNote(undefined);
    setFilters({ ...filters, ...change });
  };
  const typed = Boolean(query.trim());
  // Read when the position arrives, so a chip changed while it was being found is kept.
  const current = useRef({ filters, typed });
  current.current = { filters, typed };
  // With nothing typed the list is the map's, so the choice is which part of the map.
  const options = typed ? WHERE : WHERE.filter((o) => o.value !== "anywhere");
  const where = !typed && filters.where === "anywhere" ? "view" : filters.where;

  const chooseWhere = (next: Where) => {
    if (next !== "me") return set({ where: next });
    // Near me waits for a position, so the list never orders from nowhere.
    setLocating(true);
    setNote(undefined);
    locate()
      .then((at) => {
        setFilters({ ...current.current.filters, where: "me" });
        if (!current.current.typed) live?.ctl.flyToPoint(at);
      })
      .catch((e: Error) => setNote(`${e.message} Results are ordered from the middle of the map.`))
      .finally(() => setLocating(false));
  };

  return (
    <>
      <ChipRow label="Search filters">
        <MenuChip value={where} options={options} onChange={chooseWhere} />
        <MenuChip value={filters.type} options={TYPE_OPTIONS} onChange={(type) => set({ type })} />
        <MenuChip value={filters.heard} options={HEARD} onChange={(heard) => set({ heard })} />
      </ChipRow>
      {(locating || note) && (
        <p role="status" className="px-3 pt-2 text-footnote text-fg-muted">
          {locating ? "Finding your location…" : note}
        </p>
      )}
    </>
  );
}

/** What the search turns up, the vessels on the map while searching with nothing typed, or the destinations. */
export function SearchResults({ children }: { children?: ReactNode }) {
  const { query, searching } = useShell();
  const q = query.trim();
  return q ? <Results q={q} /> : searching ? <InView /> : <Destinations>{children}</Destinations>;
}

/** What there is to browse besides vessels, and below it whatever the page adds, such as fleets. */
function Destinations({ children }: { children?: ReactNode }) {
  return (
    <>
      <nav aria-label="Browse">
        <List>
          {BROWSE.map((d) => (
            <ListRow key={d.label} to={d.to} leading={<IconBadge icon={d.icon} />} title={d.label} subtitle={d.hint} />
          ))}
        </List>
      </nav>
      {children}
    </>
  );
}

function Results({ q }: { q: string }) {
  const now = useNow();
  const mapView = useMapView();
  const { filters } = useShell();
  const [, setAnswered] = useState(0);
  const me = useMyPosition();
  const view: SearchView = {
    origin: filters.where === "me" ? me : mapView?.center,
    boxes: mapView?.boxes,
  };
  // Only what the search asks for: panning the map changes nothing for a search sorted by
  // recency and not kept to the view.
  const viewKey = [sortOf(filters) === "nearest" ? view.origin : "", filters.where === "view" ? view.boxes : ""];
  const baseKey = `${q}|${JSON.stringify(filters)}`;
  const key = `${baseKey}|${JSON.stringify(viewKey)}`;
  // Past the area cap the server refuses the box, so the reader is asked to zoom in instead.
  const tooWide = filters.where === "view" && mapView != null && !mapView.fits;

  // The server searches every vessel it has ever heard, not just what this tab is hearing,
  // so a berthed boat is findable.
  useEffect(() => {
    if (q.length < 2 || tooWide || results.has(key)) return;
    let current = true;
    const t = setTimeout(() => {
      void searchVessels(browserAuth(), q, filterParams(filters, new Date(), firstDayOfWeek(), view)).then((features) => {
        const rows = features.map(fromFeature);
        remember(results, key, rows);
        remember(latest, baseKey, rows);
        if (current) setAnswered((n) => n + 1);
      });
    }, 150);
    return () => {
      current = false;
      clearTimeout(t);
    };
    // tooWide as well: the stream's welcome can raise the area cap after a view was refused.
  }, [key, tooWide]);

  const hits = results.get(key);
  const passes = filterTest(filters, new Date(now), firstDayOfWeek(), view);
  const center = view.origin;
  // Until the server answers, the list keeps what it last showed, so it changes once per answer
  // rather than with every key pressed: this query's answer for another view, or the last
  // answer to any query.
  const shown = useRef<Row[]>(undefined);
  const rows: Row[] | undefined = tooWide
    ? []
    : (hits ?? latest.get(baseKey)?.filter(passes).sort(rowOrder(filters, center)) ?? shown.current);
  if (hits) shown.current = hits;
  useLoading(q.length >= 2 && !tooWide && !hits);

  if (tooWide) {
    return <p className="px-2 py-3 text-body text-fg-muted">Zoom in to search what&rsquo;s on the map.</p>;
  }

  if (q.length < 2) return <p className="px-2 py-3 text-body text-fg-muted">Keep typing.</p>;
  if (!rows) return null;
  if (!rows.length) {
    if (!hits) return null;
    if (hasFilters(filters)) return <NoMatches />;
    return (
      <>
        <p className="px-2 py-3 text-body text-fg-muted">No vessel matches that name or MMSI.</p>
        {hits && (
          <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mx-2">
            The network only knows vessels its receivers have heard.
          </Prompt>
        )}
      </>
    );
  }

  return <ResultList rows={rows} center={center} byMMSI={/^\d+$/.test(q)} />;
}

// Of the vessels in view, the most the list shows. A busy harbour holds hundreds.
const IN_VIEW_LIMIT = 50;

/**
 * The vessels on the map that pass the filters, while searching with nothing typed. The server
 * holds those heard before this tab was open, and those the stream has heard since are brought
 * up to date.
 */
function InView() {
  const live = useLive();
  useStreamFrame();
  const now = useNow();
  const mapView = useMapView();
  const { filters } = useShell();
  const [, setAnswered] = useState(0);
  // The view whose request failed, and how many times the reader has asked again.
  const [failed, setFailed] = useState<string>();
  const [attempt, setAttempt] = useState(0);
  const me = useMyPosition();
  // Where is the list's own order, so changing it asks the server nothing.
  const baseKey = `view|${JSON.stringify({ ...filters, where: "" })}`;
  // A view wider than this client may ask for lists the middle of it: the largest area the cap
  // allows around the centre, which is where the nearest are anyway. Only an MMSI-only token
  // has no area at all.
  const partial = mapView != null && !mapView.fits;
  const boxes = !mapView ? undefined : partial ? (mapView.cap > 0 ? centerBoxes(mapView.center, mapView.cap) : undefined) : mapView.boxes;
  const key = `${baseKey}|${JSON.stringify(boxes)}`;

  useEffect(() => {
    if (!boxes || results.has(key)) return;
    let current = true;
    const t = setTimeout(() => {
      void vesselsInArea(browserAuth(), areaParams(filters, new Date(), firstDayOfWeek(), boxes)).then((features) => {
        if (!features) {
          if (current) setFailed(key);
          return;
        }
        const rows = features.map(fromFeature);
        remember(results, key, rows);
        remember(latest, baseKey, rows);
        if (current) setAnswered((n) => n + 1);
      });
    }, 150);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [key, attempt]);
  useLoading(boxes != null && !results.has(key) && failed !== key);

  if (!mapView) return null;
  if (!boxes) {
    return <p className="px-2 py-3 text-body text-fg-muted">Zoom in to list the vessels on the map.</p>;
  }

  const hits = results.get(key);
  // Without an answer, what the stream holds, which is only what is live on the map.
  const unanswered = !hits && failed === key;
  const inView = filterTest({ ...filters, where: "view" }, new Date(now), firstDayOfWeek(), mapView);
  const inBoxes = filterTest({ ...NO_FILTERS, where: "view" }, new Date(now), firstDayOfWeek(), { boxes });
  const passes = (v: Row) => inView(v) && inBoxes(v);
  const origin = filters.where === "me" && me ? me : mapView.center;
  // Ranked on the server's answer, and only then brought up to date from the stream: ranked on
  // live data, the list would reorder every time a vessel reported.
  const all = [...(hits ?? latest.get(baseKey) ?? (unanswered ? [...(live?.stream.vessels.values() ?? [])] : []))]
    .sort(rowOrder(filters, origin))
    .map((r) => {
      const v = live?.stream.vessels.get(r.mmsi);
      return v && v.seen > r.seen && v.lat != null && v.lon != null
        ? { ...r, lat: v.lat, lon: v.lon, sog: v.sog, seen: v.seen }
        : r;
    })
    .filter(passes);

  const unavailable = unanswered && (
    <p className="px-2 py-3 text-body text-fg-muted">
      The server did not answer, so this is only what is live on the map.{" "}
      <button
        type="button"
        onClick={() => {
          setFailed(undefined);
          setAttempt((n) => n + 1);
        }}
        className="text-accent hover:text-accent-hover"
      >
        Try again
      </button>
    </p>
  );

  if (!all.length) {
    if (unavailable) return unavailable;
    // The list fills in when the answer comes; the map already shows what is there.
    if (!hits) return null;
    // With nothing typed the list is always the view's, so that is no filter to clear.
    if (hasFilters({ ...filters, where: "anywhere" })) return <NoMatches onMap />;
    // Zoomed out over open water or inland, the map itself shows where the vessels are.
    if (partial) return null;
    return <p className="px-2 py-3 text-body text-fg-muted">No vessels on the map. Zoom out or move the map to see more.</p>;
  }
  return (
    <>
      {unavailable}
      <ResultList
        rows={all.slice(0, IN_VIEW_LIMIT)}
        center={origin}
        byMMSI={false}
        ringAll={hasFilters({ ...filters, where: "anywhere" })}
      />
      {all.length > IN_VIEW_LIMIT ? (
        <p className="px-2 py-3 text-footnote text-fg-muted">
          {sortOf(filters) === "nearest" ? "The" : "The most recent"} {IN_VIEW_LIMIT} of {all.length.toLocaleString("en-US")}
          {sortOf(filters) === "recent" ? "" : filters.where === "me" ? " nearest you" : " nearest the middle of the map"}.
          Zoom in for the rest.
        </p>
      ) : (
        partial && (
          <p className="px-2 py-3 text-footnote text-fg-muted">Only vessels near the middle of the map. Zoom in to list the rest.</p>
        )
      )}
    </>
  );
}

/** How results are ordered: by distance from the middle of the map, then the most recently heard. */
function rowOrder(filters: SearchFilters, center: [number, number] | undefined): (a: Row, b: Row) => number {
  const nearest = sortOf(filters) === "nearest" && center != null;
  return (a, b) => (nearest ? (distanceTo(center, a) ?? Infinity) - (distanceTo(center, b) ?? Infinity) : 0) || b.seen - a.seen;
}

function distanceTo(center: [number, number] | undefined, v: Row): number | undefined {
  return center && v.lat != null && v.lon != null ? distanceNM(center, [v.lat, v.lon]) : undefined;
}

function NoMatches({ onMap }: { onMap?: boolean }) {
  const { filters, setFilters } = useShell();
  return (
    <p className="px-2 py-3 text-body text-fg-muted">
      {onMap ? "No vessels on the map match these filters." : "No matches with these filters."}{" "}
      <button
        type="button"
        // Near me is an order, not a filter, so it stays.
        onClick={() => setFilters({ ...NO_FILTERS, where: filters.where === "me" ? "me" : "anywhere" })}
        className="text-accent hover:text-accent-hover"
      >
        Clear filters
      </button>
    </p>
  );
}

/**
 * Result rows, each ringed on the map for as long as the list shows, and the one under the
 * pointer ringed brighter. A list of everything on the map rings only that one: ringing every
 * vessel marks nothing out.
 */
function ResultList({
  rows,
  center,
  byMMSI,
  ringAll = true,
}: {
  rows: Row[];
  center?: [number, number];
  byMMSI: boolean;
  ringAll?: boolean;
}) {
  const live = useLive();
  const now = useNow();
  const [hovered, setHovered] = useState<number>();
  useEffect(() => {
    const ringed = ringAll ? rows : rows.filter((r) => r.mmsi === hovered);
    live?.ctl.setResults(ringed.filter((r): r is Row & { lat: number; lon: number } => r.lat != null && r.lon != null));
    live?.ctl.highlightResult(hovered);
  });
  useEffect(() => () => live?.ctl.setResults([]), [live]);
  return (
    <List>
      {rows.map((v) => {
        const nm = distanceTo(center, v);
        return (
          <ListRow
            key={v.mmsi}
            to={vesselPath(v.mmsi, v.name)}
            onHover={(over) => setHovered(over ? v.mmsi : undefined)}
            leading={<VesselThumb row={v} />}
            title={
              <>
                {v.name ?? v.mmsi}
                {v.flag && flagEmoji(v.flag) && (
                  <span role="img" aria-label={flagName(v.flag) ?? v.flag} className="ml-1.5">
                    {flagEmoji(v.flag)}
                  </span>
                )}
              </>
            }
            subtitle={rowSubtitle(v, byMMSI)}
            trailing={
              <span className="flex flex-col items-end">
                {nm != null && <span>{formatDistance(nm)}</span>}
                <span>{shortAge(Math.max(0, Math.round((now - v.seen) / 1000)))}</span>
              </span>
            }
          />
        );
      })}
    </List>
  );
}

/**
 * A result's mark: its class dot, and its first photo once that arrives. Only rows that have
 * been on screen ask for photos, since a long list of cold lookups would cost the Worker
 * several upstream calls each.
 */
function VesselThumb({ row }: { row: Row }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [seen, setSeen] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [broken, setBroken] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || seen) return;
    const io = new IntersectionObserver(([entry]) => entry?.isIntersecting && setSeen(true));
    io.observe(el);
    return () => io.disconnect();
  }, [seen]);
  const photo = useMedia(seen ? mediaKey(row.imo, row.mmsi) : undefined)?.photos[0];
  return (
    <span ref={ref} className="relative flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md bg-surface-tile">
      <ClassDot kind={row.kind} type={row.shipType} />
      {photo && !broken && (
        <img
          src={smallThumb(photo)}
          alt=""
          decoding="async"
          onLoad={() => setLoaded(true)}
          onError={() => setBroken(true)}
          className={cn("absolute inset-0 size-full object-cover transition-opacity duration-300", loaded ? "opacity-100" : "opacity-0")}
        />
      )}
    </span>
  );
}
