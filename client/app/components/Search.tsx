import { Activity, KeyRound, RadioTower, type LucideIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { flagEmoji, flagName, vesselPath } from "../lib/ais";
import { browserAuth, searchVessels, type VesselFeature } from "../lib/api";
import { useLive, useNow, useStreamFrame } from "../lib/live";
import { mediaKey, smallThumb } from "../lib/media";
import { SITE } from "../lib/meta";
import {
  filterParams,
  filterTest,
  firstDayOfWeek,
  hasFilters,
  HEARD,
  NO_FILTERS,
  SHIP_TYPES,
  type SearchFilters,
} from "../lib/searchFilters";
import { useMedia } from "../lib/useMedia";
import { cn } from "../lib/cn";
import { useShell } from "./Shell";
import { ThemeToggle } from "./ThemeToggle";
import { ChipRow, MenuChip } from "./ui/Chip";
import { ClassDot, IconBadge, List, ListRow } from "./ui/List";
import { SearchField } from "./ui/SearchField";

const DESTINATIONS: Array<{ label: string; hint: string; icon: LucideIcon; to?: string; href?: string }> = [
  { to: "/stations", label: "Stations", hint: "Who is receiving, and where", icon: RadioTower },
  { to: "/network", label: "Network", hint: "Sources, rates and delay", icon: Activity },
  // The website's page, outside this app, so a plain link rather than a route. Absolute, so a
  // dev server or a preview deploy, which serve only this app, still reach it.
  { href: `${SITE}/token`, label: "Get a token", hint: "Feed your receiver, read the stream", icon: KeyRound },
];

interface Row {
  mmsi: number;
  name?: string;
  sog?: number;
  kind?: string;
  shipType?: number;
  seen: number;
  flag?: string;
  imo?: number;
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
  };
}

function shortAge(seconds: number): string {
  return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.round(seconds / 60)}m` : `${Math.round(seconds / 3600)}h`;
}

// Per query and filters, for the life of the page: the list repaints every second for ages
// and speeds, and that must not ask the server again. Keyed on the Heard choice rather than
// the seconds it becomes, which change every minute.
const results = new Map<string, Row[]>();

/** The search box that heads the home panel, and the filters under it. */
export function SearchBox() {
  const { query, setQuery, setDetent } = useShell();
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
            onFocus={() => setDetent("full")}
          />
        </div>
        <ThemeToggle />
      </div>
      <SearchChips />
    </div>
  );
}

const TYPE_OPTIONS = [
  { value: "any", label: "Any type", chip: "Any type" },
  ...SHIP_TYPES.map((t) => ({ value: t.label, label: t.label, chip: t.label })),
];

/**
 * Filters for the search, as chips. They apply once there is a query to search, and wait,
 * selected, until then.
 */
function SearchChips() {
  const { filters, setFilters } = useShell();
  const set = (change: Partial<SearchFilters>) => setFilters({ ...filters, ...change });
  return (
    <ChipRow label="Search filters">
      <MenuChip value={filters.type} options={TYPE_OPTIONS} onChange={(type) => set({ type })} />
      <MenuChip value={filters.heard} options={HEARD} onChange={(heard) => set({ heard })} />
    </ChipRow>
  );
}

/** The destinations, or what the search turns up once there is a query. */
export function SearchResults() {
  const { query } = useShell();
  const q = query.trim();
  return q ? <Results q={q} /> : <Destinations />;
}

function Destinations() {
  return (
    <nav>
      <List>
        {DESTINATIONS.map((d) => (
          <ListRow key={d.label} to={d.to} href={d.href} leading={<IconBadge icon={d.icon} />} title={d.label} subtitle={d.hint} />
        ))}
      </List>
    </nav>
  );
}

function Results({ q }: { q: string }) {
  const live = useLive();
  useStreamFrame();
  const now = useNow();
  const { filters, setFilters } = useShell();
  const [, setAnswered] = useState(0);
  const key = `${q}|${JSON.stringify(filters)}`;

  // The server searches every vessel it has ever heard, not just what this tab is hearing,
  // so a berthed boat is findable.
  useEffect(() => {
    if (q.length < 2 || results.has(key)) return;
    let current = true;
    const t = setTimeout(() => {
      void searchVessels(browserAuth(), q, filterParams(filters, new Date(), firstDayOfWeek())).then((features) => {
        results.set(key, features.map(fromFeature));
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
  // as the server will filter it.
  const lower = q.toLowerCase();
  const passes = filterTest(filters, new Date(now), firstDayOfWeek());
  const rows: Row[] =
    hits ??
    [...(live?.stream.vessels.values() ?? [])]
      .filter((v) => (v.name?.toLowerCase().includes(lower) || String(v.mmsi).includes(q)) && passes(v))
      .sort((a, b) => b.seen - a.seen)
      .slice(0, 50);

  if (!rows.length) {
    if (hits && hasFilters(filters)) {
      return (
        <p className="px-2 py-3 text-body text-fg-muted">
          No matches with these filters.{" "}
          <button type="button" onClick={() => setFilters(NO_FILTERS)} className="text-accent hover:text-accent-hover">
            Clear filters
          </button>
        </p>
      );
    }
    return (
      <p className="px-2 py-3 text-body text-fg-muted">
        {hits ? "No vessel matches that name or MMSI." : q.length < 2 ? "Keep typing." : "Searching…"}
      </p>
    );
  }

  return (
    <List>
      {rows.map((v) => (
        <ListRow
          key={v.mmsi}
          to={vesselPath(v.mmsi, v.name)}
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
          subtitle={`${v.sog != null ? `${v.sog.toFixed(1)} kn · ` : ""}MMSI ${v.mmsi}`}
          trailing={shortAge(Math.max(0, Math.round((now - v.seen) / 1000)))}
        />
      ))}
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
