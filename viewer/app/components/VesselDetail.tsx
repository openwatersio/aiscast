import { ArrowRight, Check, LocateFixed, MapPin, Route, Share } from "lucide-react";
import { useEffect, useRef, useState } from "react";
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
  vesselPath,
} from "../lib/ais";
import { publicApiBase, type VesselFeature } from "../lib/api";
import { useLive, useLiveVessel, type Live } from "../lib/live";
import { SITE } from "../lib/meta";
import { PageTitle } from "./Panel";
import { useShell } from "./Shell";
import { ActionButton, ActionRow } from "./ui/ActionButton";
import { Facts } from "./ui/Facts";
import { Section } from "./ui/Section";
import { StatGrid } from "./ui/StatGrid";

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
        <PageTitle className="text-large-title">MMSI {mmsi}</PageTitle>
        <p className="mt-2 text-body text-fg-secondary">
          {loading ? "Loading…" : "The network has never heard this vessel."}
        </p>
      </article>
    );
  }

  // AIS gives a destination and an ETA and nothing else about the voyage: no origin, no
  // departure time, no distance run. So this shows where the crew says they are going and
  // when, and does not draw a progress bar it has no way to position.
  const eta = parseEta(p?.eta);
  const etaText = eta
    ? eta.toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: "UTC" })
    : p?.eta;
  const voyage = parseDestination(p?.destination);

  // Navigational status is what the speed, course and heading mean: 0.0 knots reads very
  // differently under "At anchor" than under "Under way using engine".
  const navStatus = navStatusCode != null ? (NAV_STATUS[navStatusCode] ?? `Status ${navStatusCode}`) : undefined;

  const ids = [`MMSI ${mmsi}`, p?.imo ? `IMO ${p.imo}` : undefined, p?.callsign ? `Call sign ${p.callsign}` : undefined]
    .filter(Boolean)
    .join(" · ");

  const trackHere = track?.mmsi === mmsi ? track : undefined;
  const attribution = heard?.attribution;

  return (
    <article>
      {country && (
        <p className="text-subhead text-fg-muted">
          {flagEmoji(p!.flag)} {country}
        </p>
      )}
      <PageTitle className="truncate text-large-title">{name ?? `MMSI ${mmsi}`}</PageTitle>
      <p className="text-footnote text-fg-muted">
        {CLASS_LABELS[cls]}
        {type ? ` · type ${type}` : ""}
      </p>
      <p className="mt-0.5 text-footnote text-fg-muted">{ids}</p>

      <VesselActions live={live} mmsi={mmsi} name={name} hasTrack={Boolean(trackHere && trackHere.points > 1)} />

      {lat != null && lon != null && (
        <Section
          label="Last position"
          aside={
            seen ? (
              <time dateTime={new Date(seen).toISOString()} title={new Date(seen).toISOString()} suppressHydrationWarning>
                {relativeTime(new Date(seen))}
              </time>
            ) : undefined
          }
        >
          <div className="flex items-center gap-3">
            <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface text-accent">
              <MapPin className="size-5" aria-hidden />
            </span>
            <span className="min-w-0 font-mono text-footnote text-fg-secondary">{formatCoord(lat, lon)}</span>
          </div>
        </Section>
      )}

      {(voyage || p?.eta) && (
        <Section
          label="Destination"
          aside={etaText ? <span suppressHydrationWarning>ETA {etaText} UTC{eta && ` · ${relativeTime(eta)}`}</span> : undefined}
        >
          <div className="flex items-center gap-2" title={p?.destination}>
            {voyage?.from && (
              <>
                <span className="truncate text-body text-fg-secondary">
                  {voyage.from.flag} {voyage.from.code ?? voyage.from.raw}
                </span>
                <ArrowRight className="size-4 shrink-0 text-fg-muted" aria-label="to" />
              </>
            )}
            <span className="truncate text-headline font-semibold text-fg">
              {voyage?.to ? `${voyage.to.flag ?? ""} ${voyage.to.code ?? voyage.to.raw}`.trim() : "Not reported"}
            </span>
          </div>
          {voyage?.to?.country && <div className="text-body text-fg-secondary">{voyage.to.country}</div>}
        </Section>
      )}

      {trackHere && trackHere.points > 1 && (
        <Section
          label="Track"
          aside={
            <a href={`${publicApiBase()}/v1/vessels/${mmsi}/track?format=gpx`} download={`${mmsi}.gpx`}>
              Download GPX
            </a>
          }
        >
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-body text-fg">{trackHere.points.toLocaleString("en-US")} positions</span>
            <span className="text-footnote text-fg-muted">
              {trackHere.from ? `since ${relativeTime(new Date(trackHere.from))}` : ""}
            </span>
          </div>
        </Section>
      )}

      {/* Shown even when absent: a blank under "Speed" says the vessel is not reporting it,
          where an omitted row says nothing at all. */}
      <Section label={navStatus ?? "Motion"} bare>
        <StatGrid
          stats={[
            { label: "Speed", value: sog != null ? sog.toFixed(1) : undefined, unit: "\u00a0kn" },
            { label: "Course", value: cog != null ? Math.round(cog).toString() : undefined, unit: "°" },
            { label: "Heading", value: heading != null ? String(heading) : undefined, unit: "°" },
          ]}
        />
      </Section>

      <Facts
        className="mt-5"
        items={[
          ["Draught", p?.draught ? `${p.draught.toFixed(1)} m` : undefined],
          ["Size", p?.length ? `${p.length} × ${p.beam ?? "?"} m` : undefined],
          ["Station", station ? <Link to={`/stations/${station}`}>{station}</Link> : undefined],
        ]}
      />

      {/* The credit that came with this vessel's own last message, which only the stream carries. */}
      {attribution && (
        <p className="mt-5 text-footnote text-fg-muted">
          {heard?.license ? `${attribution} Licence: ${heard.license}.` : attribution}
        </p>
      )}
    </article>
  );
}

/** Follow, share, and show the track: what a reader does with a vessel once it is open. */
function VesselActions({ live, mmsi, name, hasTrack }: { live: Live | undefined; mmsi: number; name?: string; hasTrack: boolean }) {
  const [following, setFollowing] = useState(false);
  const [copied, setCopied] = useState(false);
  // The map owns the state: a drag or another vessel ends following without this button.
  useEffect(() => live?.ctl.onCameraFollow(setFollowing), [live]);

  async function share() {
    const url = `${SITE}${vesselPath(mmsi, name)}`;
    const title = name ?? `MMSI ${mmsi}`;
    if (navigator.share) {
      try {
        await navigator.share({ title, url });
        return;
      } catch (e) {
        // Dismissing the share sheet is the reader's choice. Anything else, such as a browser
        // that has the API but will not open a sheet, falls back to copying the link.
        if (e instanceof DOMException && e.name === "AbortError") return;
      }
    }
    await navigator.clipboard.writeText(url);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <ActionRow>
      <ActionButton icon={LocateFixed} label="Follow" pressed={following} onClick={() => live?.ctl.followCamera(!following)} />
      <ActionButton icon={copied ? Check : Share} label={copied ? "Copied" : "Share"} onClick={() => void share()} />
      <ActionButton icon={Route} label="Track" disabled={!hasTrack} onClick={() => live?.ctl.fitTrack()} />
    </ActionRow>
  );
}
