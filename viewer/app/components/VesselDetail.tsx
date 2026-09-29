import { useEffect, useRef } from "react";
import { Link } from "react-router";
import {
  CLASS_LABELS,
  flagEmoji,
  flagName,
  formatCoord,
  NAV_STATUS,
  parseDestination,
  parseEta,
  relativeTime,
  shipClass,
} from "../lib/ais";
import { publicApiBase, type VesselFeature } from "../lib/api";
import { useLive, useLiveVessel } from "../lib/live";
import { useShell } from "./Shell";

const LABEL = "text-[11px] font-medium tracking-wide uppercase";
const TILE = "tile-surface mt-1.5 rounded-lg p-3";

function SectionHead({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="mt-4 flex items-baseline justify-between gap-2">
      <h2 className={LABEL} style={{ color: "var(--text-muted)" }}>
        {title}
      </h2>
      {children}
    </div>
  );
}

/**
 * One vessel. `feature` is the record the API holds, which carries the particulars; the
 * stream's copy overrides the parts that move whenever it has heard something newer. While
 * the record is still loading, the stream's copy is all there is, and it is usually enough.
 */
export function VesselDetail({
  mmsi,
  feature,
  loading = false,
}: {
  mmsi: number;
  feature: VesselFeature | undefined;
  loading?: boolean;
}) {
  const live = useLive();
  const heard = useLiveVessel(mmsi);
  const { track } = useShell();

  const p = feature?.properties;
  const recordSeen = p ? Date.parse(p.seen) : 0;
  const fresher = heard && heard.lat != null && heard.seen > recordSeen ? heard : undefined;

  const name = p?.name ?? heard?.name;
  const kind = p?.kind ?? heard?.kind;
  const type = p?.type ?? heard?.shipType;
  const cls = shipClass(kind, type);
  const [recordLon, recordLat] = feature?.geometry?.coordinates ?? [];
  const lat = fresher?.lat ?? recordLat ?? heard?.lat;
  const lon = fresher?.lon ?? recordLon ?? heard?.lon;
  const seen = fresher?.seen ?? (recordSeen || heard?.seen);
  const sog = fresher ? fresher.sog : (p?.sog ?? heard?.sog);
  const cog = fresher ? fresher.cog : (p?.cog ?? heard?.cog);
  const heading = fresher ? fresher.heading : (p?.heading ?? heard?.heading);
  const navStatusCode = fresher?.navStatus ?? p?.nav_status ?? heard?.navStatus;
  const station = fresher?.station ?? p?.station ?? heard?.station;
  const country = flagName(p?.flag);

  // Draw, follow, and frame the vessel. The record's position seeds the stream, so the map
  // shows the vessel even when the stream is refused, which happens per address.
  const flown = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!live) return;
    if (feature?.geometry && p) {
      const [flon, flat] = feature.geometry.coordinates;
      live.stream.seed({
        mmsi,
        name: p.name,
        shipType: p.type,
        kind: p.kind,
        lat: flat,
        lon: flon,
        cog: p.cog,
        sog: p.sog,
        heading: p.heading,
        navStatus: p.nav_status,
        seen: Date.parse(p.seen),
        source: p.source,
        station: p.station,
      });
    }
    live.ctl.setFocus(mmsi);
    if (flown.current !== mmsi) {
      const at: [number, number] | undefined =
        lat != null && lon != null ? [lon, lat] : undefined;
      if (at) {
        flown.current = mmsi;
        live.ctl.flyToVessel(mmsi, at);
      }
    }
    // Keyed on whether a position is known, not on the position: the camera flies once, and
    // following a moving vessel is the map's job.
  }, [live, mmsi, feature, lat != null]);

  if (!feature && !heard) {
    return (
      <article>
        <h1 className="text-2xl font-semibold" style={{ color: "var(--text)" }}>
          MMSI {mmsi}
        </h1>
        <p className="mt-2 text-sm" style={{ color: "var(--text-secondary)" }}>
          {loading ? "Loading…" : "The network has never heard this vessel."}
        </p>
      </article>
    );
  }

  const size = p?.length ? `${p.length} × ${p.beam ?? "?"} m` : undefined;

  // AIS gives a destination and an ETA and nothing else about the voyage: no origin, no
  // departure time, no distance run. So this shows where the crew says they are going and
  // when, and does not draw a progress bar it has no way to position.
  const eta = parseEta(p?.eta);
  const etaText = eta
    ? eta.toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: "UTC" })
    : p?.eta;
  const voyage = parseDestination(p?.destination);

  // Navigational status is what the speed, course and heading below mean: 0.0 knots reads
  // very differently under "At anchor" than under "Under way using engine".
  const navStatus = navStatusCode != null ? (NAV_STATUS[navStatusCode] ?? `Status ${navStatusCode}`) : undefined;

  // Shown even when absent: a blank under "Speed" says the vessel is not reporting it, where
  // an omitted row says nothing at all. A degree sign sits tight against its number; a unit
  // word takes a non-breaking space.
  const motion = [
    { label: "Speed", value: sog != null ? sog.toFixed(1) : undefined, unit: " kn" },
    { label: "Course", value: cog != null ? Math.round(cog).toString() : undefined, unit: "°" },
    { label: "Heading", value: heading != null ? String(heading) : undefined, unit: "°" },
  ];

  const ids = [`MMSI ${mmsi}`, p?.imo ? `IMO ${p.imo}` : undefined, p?.callsign ? `Call sign ${p.callsign}` : undefined]
    .filter(Boolean)
    .join(" · ");

  const rows: Array<[string, string | undefined]> = [
    ["Draught", p?.draught ? `${p.draught.toFixed(1)} m` : undefined],
    ["Size", size],
  ];

  const trackHere = track?.mmsi === mmsi ? track : undefined;
  const attribution = heard?.attribution;

  return (
    <article>
      <div className="min-w-0">
        <p className="text-sm" style={{ color: "var(--text-muted)" }}>
          {country && (
            <span className="mr-1">
              {flagEmoji(p!.flag)} {country}
            </span>
          )}
        </p>
        <h1 className="truncate text-2xl font-semibold" style={{ color: "var(--text)" }}>
          {name ?? `MMSI ${mmsi}`}
        </h1>
        <p className="text-xs" style={{ color: "var(--text-muted)" }}>
          <span>{CLASS_LABELS[cls]}</span>
          {type ? <span> · type {type}</span> : null}
        </p>
        <p className="mt-0.5 text-xs" style={{ color: "var(--text-muted)" }}>
          {ids}
        </p>
      </div>

      {lat != null && lon != null && (
        <>
          <SectionHead title="Last position">
            {seen ? (
              <time
                className="text-[11px] first-letter:uppercase"
                style={{ color: "var(--text-muted)" }}
                dateTime={new Date(seen).toISOString()}
                title={new Date(seen).toISOString()}
                suppressHydrationWarning
              >
                {relativeTime(new Date(seen))}
              </time>
            ) : null}
          </SectionHead>
          <section className={`${TILE} flex items-center gap-3`}>
            <span
              className="flex size-9 shrink-0 items-center justify-center rounded-full"
              style={{ backgroundColor: "var(--surface)", color: "var(--accent)" }}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="size-5">
                <path d="M12 21s7-5.5 7-11a7 7 0 1 0-14 0c0 5.5 7 11 7 11Z" />
                <circle cx="12" cy="10" r="2.5" />
              </svg>
            </span>
            <div className="min-w-0 font-mono text-xs" style={{ color: "var(--text-secondary)" }}>
              {formatCoord(lat, lon)}
            </div>
          </section>
        </>
      )}

      {(voyage || p?.eta) && (
        <>
          <SectionHead title="Destination">
            {etaText && (
              <span className="text-[11px] tabular-nums" style={{ color: "var(--text-muted)" }} suppressHydrationWarning>
                ETA {etaText} UTC{eta && ` · ${relativeTime(eta)}`}
              </span>
            )}
          </SectionHead>
          <section className={TILE} title={p?.destination}>
            <div className="flex items-center gap-2">
              {voyage?.from && (
                <>
                  <span className="truncate text-sm" style={{ color: "var(--text-secondary)" }}>
                    {voyage.from.flag} {voyage.from.code ?? voyage.from.raw}
                  </span>
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    className="size-4 shrink-0"
                    style={{ color: "var(--text-muted)" }}
                    aria-label="to"
                  >
                    <path d="M5 12h14M13 6l6 6-6 6" />
                  </svg>
                </>
              )}
              <span className="truncate text-base font-semibold" style={{ color: "var(--text)" }}>
                {voyage?.to ? `${voyage.to.flag ?? ""} ${voyage.to.code ?? voyage.to.raw}`.trim() : "Not reported"}
              </span>
            </div>
            {voyage?.to?.country && (
              <div className="text-sm" style={{ color: "var(--text-secondary)" }}>
                {voyage.to.country}
              </div>
            )}
          </section>
        </>
      )}

      {trackHere && trackHere.points > 1 && (
        <>
          <SectionHead title="Track">
            <a className="text-[11px]" href={`${publicApiBase()}/v1/vessels/${mmsi}/track?format=gpx`} download={`${mmsi}.gpx`}>
              Download GPX
            </a>
          </SectionHead>
          <section className={`${TILE} flex items-baseline justify-between gap-2 text-sm`}>
            <span style={{ color: "var(--text)" }}>{trackHere.points.toLocaleString("en-US")} positions</span>
            <span className="text-xs" style={{ color: "var(--text-muted)" }}>
              {trackHere.from ? `since ${relativeTime(new Date(trackHere.from))}` : ""}
            </span>
          </section>
        </>
      )}

      <SectionHead title={navStatus ?? "Motion"} />
      <section className={`${TILE} grid grid-cols-3 gap-2`}>
        {motion.map((m) => (
          <div key={m.label} className="text-center">
            <div className="text-xl leading-tight font-semibold tabular-nums" style={{ color: "var(--text)" }}>
              {m.value ?? "–"}
              <span className="font-light" style={{ color: "var(--text-muted)" }}>
                {m.value ? m.unit : ""}
              </span>
            </div>
            <div className="text-[11px] tracking-wide uppercase" style={{ color: "var(--text-muted)" }}>
              {m.label}
            </div>
          </div>
        ))}
      </section>

      <dl className="mt-4">
        {rows
          .filter(([, v]) => v)
          .map(([k, v]) => (
            <div key={k} className="contents">
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        {station && (
          <>
            <dt>Station</dt>
            <dd className="truncate">
              <Link to={`/stations/${station}`}>{station}</Link>
            </dd>
          </>
        )}
      </dl>

      {/* The credit that came with this vessel's own last message, which only the stream carries. */}
      {attribution && (
        <p className="mt-4 text-xs" style={{ color: "var(--text-muted)" }}>
          {heard?.license ? `${attribution} Licence: ${heard.license}.` : attribution}
        </p>
      )}
    </article>
  );
}
