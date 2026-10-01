import { Antenna } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
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
  AREAS,
  areaParams,
  filterParams,
  filterTest,
  firstDayOfWeek,
  hasFilters,
  HEARD,
  NO_FILTERS,
  SHIP_TYPES,
  SORTS,
  type SearchFilters,
  type SearchView,
} from "../lib/searchFilters";
import { useMedia } from "../lib/useMedia";
import { cn } from "../lib/cn";
import { useShell } from "./Shell";
import { ChipButton, ChipRow, MenuChip } from "./ui/Chip";
import { Menu, MenuRadioGroup, MenuRadioItem, MenuSeparator } from "./ui/Menu";
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

// Per query, filters, and view, for the life of the page: the list repaints every second for
// ages and speeds, and that must not ask the server again. Keyed on the Heard choice rather
// than the seconds it becomes, which change every minute.
const results = new Map<string, Row[]>();
// The newest answer per query and filters, whatever the view, shown while a pan's answer loads
// so the list reorders rather than falling back to what the stream holds.
const latest = new Map<string, Row[]>();

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
  const set = (change: Partial<SearchFilters>) => setFilters({ ...filters, ...change });
  return (
    <ChipRow label="Search filters">
      <SortChip filters={filters} typed={Boolean(query.trim())} set={set} />
      <MenuChip value={filters.type} options={TYPE_OPTIONS} onChange={(type) => set({ type })} />
      <MenuChip value={filters.heard} options={HEARD} onChange={(heard) => set({ heard })} />
    </ChipRow>
  );
}

/**
 * The order, and for a typed search where it looks. With nothing typed the list is already the
 * map's, so there is no where to choose.
 */
function SortChip({
  filters,
  typed,
  set,
}: {
  filters: SearchFilters;
  typed: boolean;
  set(change: Partial<SearchFilters>): void;
}) {
  const inView = typed && filters.inView;
  const sort = SORTS.find((o) => o.value === filters.sort) ?? SORTS[0]!;
  return (
    <Menu
      trigger={
        <ChipButton selected={sort !== SORTS[0] || inView}>
          {sort.chip}
          {inView && " in this area"}
        </ChipButton>
      }
    >
      <MenuRadioGroup value={filters.sort} onChange={(s) => set({ sort: s })}>
        {SORTS.map((o) => (
          <MenuRadioItem key={o.value} value={o.value}>
            {o.label}
          </MenuRadioItem>
        ))}
      </MenuRadioGroup>
      {typed && (
        <>
          <MenuSeparator />
          <MenuRadioGroup value={filters.inView} onChange={(v) => set({ inView: v })}>
            {AREAS.map((o) => (
              <MenuRadioItem key={o.label} value={o.value}>
                {o.label}
              </MenuRadioItem>
            ))}
          </MenuRadioGroup>
        </>
      )}
    </Menu>
  );
}

/** What the search turns up, the vessels on the map while searching with nothing typed, or the destinations. */
export function SearchResults() {
  const { query, searching } = useShell();
  const q = query.trim();
  return q ? <Results q={q} /> : searching ? <InView /> : <Destinations />;
}

/** What there is to browse besides vessels. */
function Destinations() {
  return (
    <nav aria-label="Browse">
      <List>
        {BROWSE.map((d) => (
          <ListRow key={d.label} to={d.to} leading={<IconBadge icon={d.icon} />} title={d.label} subtitle={d.hint} />
        ))}
      </List>
    </nav>
  );
}

function Results({ q }: { q: string }) {
  const live = useLive();
  useStreamFrame();
  const now = useNow();
  const mapView = useMapView();
  const { filters } = useShell();
  const [, setAnswered] = useState(0);
  // Only what the search asks for: panning the map changes nothing for a search sorted by
  // recency and not kept to the view.
  const view: SearchView | undefined = mapView && {
    center: mapView.center,
    boxes: mapView.boxes,
  };
  const viewKey = [filters.sort === "nearest" ? mapView?.center : "", filters.inView ? mapView?.boxes : ""];
  const baseKey = `${q}|${JSON.stringify(filters)}`;
  const key = `${baseKey}|${JSON.stringify(viewKey)}`;
  // Past the area cap the server refuses the box, so the reader is asked to zoom in instead.
  const tooWide = filters.inView && mapView != null && !mapView.fits;

  // The server searches every vessel it has ever heard, not just what this tab is hearing,
  // so a berthed boat is findable.
  useEffect(() => {
    if (q.length < 2 || tooWide || results.has(key)) return;
    let current = true;
    const t = setTimeout(() => {
      void searchVessels(browserAuth(), q, filterParams(filters, new Date(), firstDayOfWeek(), view)).then((features) => {
        const rows = features.map(fromFeature);
        results.set(key, rows);
        latest.set(baseKey, rows);
        if (current) setAnswered((n) => n + 1);
      });
    }, 150);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [key]);

  const hits = results.get(key);
  // Before the server answers, show what this tab already holds rather than nothing, filtered
  // and ordered as the server will.
  const lower = q.toLowerCase();
  const passes = filterTest(filters, new Date(now), firstDayOfWeek(), view);
  const center = view?.center;
  const order = rowOrder(filters, center);
  const rows: Row[] = tooWide
    ? []
    : (hits ??
      latest.get(baseKey)?.filter(passes).sort(order) ??
      [...(live?.stream.vessels.values() ?? [])]
        .filter((v) => (v.name?.toLowerCase().includes(lower) || String(v.mmsi).includes(q)) && passes(v))
        .sort(order)
        .slice(0, 50));

  if (tooWide) {
    return <p className="px-2 py-3 text-body text-fg-muted">Zoom in to search what&rsquo;s on the map.</p>;
  }

  if (!rows.length) {
    if (hits && hasFilters(filters)) return <NoMatches />;
    return (
      <>
        <p className="px-2 py-3 text-body text-fg-muted">
          {hits ? "No vessel matches that name or MMSI." : q.length < 2 ? "Keep typing." : "Searching…"}
        </p>
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
  // The order is the list's own, so changing it asks the server nothing.
  const baseKey = `view|${JSON.stringify({ ...filters, sort: "", inView: true })}`;
  const key = `${baseKey}|${JSON.stringify(mapView?.boxes)}`;
  const tooWide = mapView != null && !mapView.fits;

  useEffect(() => {
    if (!mapView || tooWide || results.has(key)) return;
    let current = true;
    const t = setTimeout(() => {
      void vesselsInArea(browserAuth(), areaParams(filters, new Date(), firstDayOfWeek(), mapView)).then((features) => {
        if (!features) return;
        const rows = features.map(fromFeature);
        results.set(key, rows);
        latest.set(baseKey, rows);
        if (current) setAnswered((n) => n + 1);
      });
    }, 150);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [key]);

  if (!mapView) return null;
  if (tooWide) {
    return <p className="px-2 py-3 text-body text-fg-muted">Zoom in to list the vessels on the map.</p>;
  }

  const hits = results.get(key);
  const passes = filterTest({ ...filters, inView: true }, new Date(now), firstDayOfWeek(), mapView);
  // Ranked on the server's answer, and only then brought up to date from the stream: ranked on
  // live data, the list would reorder every time a vessel reported.
  const all = [...(hits ?? latest.get(baseKey) ?? [])]
    .sort(rowOrder(filters, mapView.center))
    .map((r) => {
      const v = live?.stream.vessels.get(r.mmsi);
      return v && v.seen > r.seen && v.lat != null && v.lon != null
        ? { ...r, lat: v.lat, lon: v.lon, sog: v.sog, seen: v.seen }
        : r;
    })
    .filter(passes);

  if (!all.length) {
    if (!hits) return <p className="px-2 py-3 text-body text-fg-muted">Looking…</p>;
    // In view has no chip with nothing typed, so it is no filter to clear.
    return hasFilters({ ...filters, inView: false }) ? (
      <NoMatches onMap />
    ) : (
      <p className="px-2 py-3 text-body text-fg-muted">No vessels on the map. Zoom out or move the map to see more.</p>
    );
  }
  return (
    <>
      <ResultList rows={all.slice(0, IN_VIEW_LIMIT)} center={mapView.center} byMMSI={false} />
      {all.length > IN_VIEW_LIMIT && (
        <p className="px-2 py-3 text-footnote text-fg-muted">
          {filters.sort === "nearest" ? "The" : "The most recent"} {IN_VIEW_LIMIT} of {all.length.toLocaleString("en-US")}
          {filters.sort === "nearest" ? " nearest the middle of the map" : ""}. Zoom in for the rest.
        </p>
      )}
    </>
  );
}

/** How results are ordered: by distance from the middle of the map, then the most recently heard. */
function rowOrder(filters: SearchFilters, center: [number, number] | undefined): (a: Row, b: Row) => number {
  const nearest = filters.sort === "nearest" && center != null;
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
        onClick={() => setFilters({ ...NO_FILTERS, sort: filters.sort })}
        className="text-accent hover:text-accent-hover"
      >
        Clear filters
      </button>
    </p>
  );
}

/** Result rows, with each result ringed on the map for as long as the list shows. */
function ResultList({ rows, center, byMMSI }: { rows: Row[]; center?: [number, number]; byMMSI: boolean }) {
  const live = useLive();
  const now = useNow();
  useEffect(() => {
    live?.ctl.setResults(rows.filter((r): r is Row & { lat: number; lon: number } => r.lat != null && r.lon != null));
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
            onHover={(over) => live?.ctl.highlightResult(over ? v.mmsi : undefined)}
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
