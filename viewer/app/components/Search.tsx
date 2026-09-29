import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";
import { CLASS_COLORS, shipClass, vesselPath } from "../lib/ais";
import { browserAuth, searchVessels, type VesselFeature } from "../lib/api";
import { useLive, useNow, useStreamFrame } from "../lib/live";
import { CloseIcon, SearchIcon } from "./icons";
import { useShell } from "./Shell";

const DESTINATIONS = [
  {
    to: "/stations",
    label: "Stations",
    hint: "Who is receiving, and where",
    icon: "M12 2a3 3 0 0 1 3 3c0 1.3-.8 2.4-2 2.8V22h-2V7.8A3 3 0 0 1 12 2Zm-6.4.9 1.4 1.4a8 8 0 0 0 0 11.3l-1.4 1.4a10 10 0 0 1 0-14.1Zm12.8 0a10 10 0 0 1 0 14.1l-1.4-1.4a8 8 0 0 0 0-11.3l1.4-1.4ZM8.4 5.7 9.8 7a4 4 0 0 0 0 5.6l-1.4 1.4a6 6 0 0 1 0-8.4Zm7.2 0a6 6 0 0 1 0 8.4L14.2 12a4 4 0 0 0 0-5.6l1.4-1.4Z",
  },
  {
    to: "/network",
    label: "Network",
    hint: "Sources, rates and delay",
    icon: "M3 13h3.5l2.5 6 4-14 2.5 8H21",
    stroke: true,
  },
  {
    // The website's page, outside this app, so a plain link rather than a route.
    href: "/ais/token",
    label: "Get a token",
    hint: "Feed your receiver, read the stream",
    icon: "M14 7a5 5 0 1 1-4.6 6.9L8 15H5.5l-1 1H3v-2.5l5.1-5.1A5 5 0 0 1 14 7Zm2 2.5a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3Z",
  },
];

interface Row {
  mmsi: number;
  name?: string;
  sog?: number;
  kind?: string;
  shipType?: number;
  seen: number;
}

function fromFeature(f: VesselFeature): Row {
  const p = f.properties;
  return { mmsi: p.mmsi, name: p.name, sog: p.sog, kind: p.kind, shipType: p.type, seen: Date.parse(p.seen) };
}

function shortAge(seconds: number): string {
  return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.round(seconds / 60)}m` : `${Math.round(seconds / 3600)}h`;
}

// Per query, for the life of the page: the list repaints every second for ages and speeds,
// and that must not ask the server again.
const results = new Map<string, Row[]>();

/** The search box that heads the home panel. */
export function SearchBox() {
  const { query, setQuery, setDetent } = useShell();
  const navigate = useNavigate();
  return (
    <div className="p-3">
      <form
        className="relative"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          const q = query.trim();
          if (/^\d{7,9}$/.test(q)) navigate(`/vessels/${q}`);
        }}
      >
        <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-[var(--text-muted)]" />
        <label className="sr-only" htmlFor="q">
          Search vessels by name or MMSI
        </label>
        <input
          id="q"
          name="q"
          type="search"
          autoComplete="off"
          placeholder="Search vessels by name or MMSI"
          // 16px on phones: iOS Safari zooms the page into any field set smaller when it takes focus.
          className="w-full rounded-full border py-2.5 pr-9 pl-9 text-base outline-none focus:border-[var(--accent)] md:text-sm"
          style={{ backgroundColor: "var(--surface-subtle)", color: "var(--text)" }}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          // Searching needs room for results, as in a maps app.
          onFocus={() => setDetent("full")}
        />
        {query && (
          <button
            type="button"
            className="absolute top-1/2 right-2 flex size-6 -translate-y-1/2 items-center justify-center rounded-full"
            style={{ color: "var(--text-muted)" }}
            aria-label="Clear search"
            onClick={() => setQuery("")}
          >
            <CloseIcon className="size-4" />
          </button>
        )}
      </form>
    </div>
  );
}

/** The destinations, or what the search turns up once there is a query. */
export function SearchResults() {
  const { query } = useShell();
  const q = query.trim();
  return q ? <Results q={q} /> : <Destinations />;
}

function Destinations() {
  const itemClass = "flex items-center gap-3 rounded-lg px-2 py-2.5 no-underline hover:bg-[var(--surface-subtle)]";
  return (
    <nav>
      <ul>
        {DESTINATIONS.map((d) => {
          const body = (
            <>
              <span
                className="flex size-9 shrink-0 items-center justify-center rounded-full"
                style={{ backgroundColor: "var(--accent-bg)", color: "var(--accent)" }}
              >
                <svg
                  viewBox="0 0 24 24"
                  className="size-5"
                  fill={d.stroke ? "none" : "currentColor"}
                  stroke={d.stroke ? "currentColor" : "none"}
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  aria-hidden="true"
                >
                  <path d={d.icon} />
                </svg>
              </span>
              <span className="min-w-0">
                <span className="block text-sm font-medium">{d.label}</span>
                <span className="block text-xs" style={{ color: "var(--text-muted)" }}>
                  {d.hint}
                </span>
              </span>
            </>
          );
          return (
            <li key={d.label}>
              {d.to ? (
                <Link to={d.to} className={itemClass} style={{ color: "var(--text)" }}>
                  {body}
                </Link>
              ) : (
                <a href={d.href} className={itemClass} style={{ color: "var(--text)" }}>
                  {body}
                </a>
              )}
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

function Results({ q }: { q: string }) {
  const live = useLive();
  useStreamFrame();
  const now = useNow();
  const [, setAnswered] = useState(0);

  // The server searches every vessel it has ever heard, not just what this tab is hearing,
  // so a berthed boat is findable.
  useEffect(() => {
    if (q.length < 2 || results.has(q)) return;
    let current = true;
    const t = setTimeout(() => {
      void searchVessels(browserAuth(), q).then((features) => {
        results.set(q, features.map(fromFeature));
        if (current) setAnswered((n) => n + 1);
      });
    }, 150);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [q]);

  const hits = results.get(q);
  // Before the server answers, show what this tab already holds rather than nothing.
  const lower = q.toLowerCase();
  const rows: Row[] =
    hits ??
    [...(live?.stream.vessels.values() ?? [])]
      .filter((v) => v.name?.toLowerCase().includes(lower) || String(v.mmsi).includes(q))
      .sort((a, b) => b.seen - a.seen)
      .slice(0, 50);

  if (!rows.length) {
    return (
      <p className="px-2 py-3 text-sm" style={{ color: "var(--text-muted)" }}>
        {hits ? "No vessel matches that name or MMSI." : q.length < 2 ? "Keep typing." : "Searching…"}
      </p>
    );
  }

  return (
    <ul className="space-y-0.5">
      {rows.map((v) => (
        <li key={v.mmsi}>
          <Link
            to={vesselPath(v.mmsi, v.name)}
            className="flex items-center gap-2.5 rounded-lg px-2 py-2 text-sm no-underline hover:bg-[var(--surface-subtle)]"
            style={{ color: "var(--text)" }}
          >
            <span className="size-2.5 shrink-0 rounded-full" style={{ background: CLASS_COLORS[shipClass(v.kind, v.shipType)] }} />
            <span className="min-w-0 flex-1">
              <span className="block truncate">{v.name ?? v.mmsi}</span>
              <span className="block text-xs" style={{ color: "var(--text-muted)" }}>
                {v.sog != null ? `${v.sog.toFixed(1)} kn · ` : ""}MMSI {v.mmsi}
              </span>
            </span>
            <span className="shrink-0 text-xs tabular-nums" style={{ color: "var(--text-muted)" }}>
              {shortAge(Math.max(0, Math.round((now - v.seen) / 1000)))}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}
