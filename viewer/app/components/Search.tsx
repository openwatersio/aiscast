import { Activity, KeyRound, RadioTower, type LucideIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { vesselPath } from "../lib/ais";
import { browserAuth, searchVessels, type VesselFeature } from "../lib/api";
import { useLive, useNow, useStreamFrame } from "../lib/live";
import { useShell } from "./Shell";
import { ClassDot, IconBadge, List, ListRow } from "./ui/List";
import { SearchField } from "./ui/SearchField";

const DESTINATIONS: Array<{ label: string; hint: string; icon: LucideIcon; to?: string; href?: string }> = [
  { to: "/stations", label: "Stations", hint: "Who is receiving, and where", icon: RadioTower },
  { to: "/network", label: "Network", hint: "Sources, rates and delay", icon: Activity },
  // The website's page, outside this app, so a plain link rather than a route.
  { href: "/ais/token", label: "Get a token", hint: "Feed your receiver, read the stream", icon: KeyRound },
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
          leading={<ClassDot kind={v.kind} type={v.shipType} />}
          title={v.name ?? v.mmsi}
          subtitle={`${v.sog != null ? `${v.sog.toFixed(1)} kn · ` : ""}MMSI ${v.mmsi}`}
          trailing={shortAge(Math.max(0, Math.round((now - v.seen) / 1000)))}
        />
      ))}
    </List>
  );
}
