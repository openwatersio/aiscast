import { Antenna, ArrowRight, Check, ImagePlus, LocateFixed, MapPin, Route, Sailboat, Share } from "lucide-react";
import { Fragment, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import {
  CLASS_LABELS,
  flagEmoji,
  flagName,
  formatCoord,
  isVolunteer,
  NAV_STATUS,
  parseDestination,
  parseEta,
  relativeTime,
  shipClass,
  vesselDimensions,
  vesselPath,
} from "../lib/ais";
import { publicApiBase, type SourceRef, type VesselFeature, type VesselParticulars } from "../lib/api";
import { useLive, useLiveVessel, type Live } from "../lib/live";
import { CONTRIBUTE, CONTRIBUTE_PROMPT, DEVELOPERS, SIGNALK_PLUGIN } from "../lib/links";
import { SITE } from "../lib/meta";
import { shareLink } from "../lib/share";
import { cn } from "../lib/cn";
import { mediaKey, type Photo } from "../lib/media";
import { particularsFacts } from "../lib/particulars";
import { useMedia } from "../lib/useMedia";
import type { TrackRange } from "../lib/trackRange";
import { useStation } from "../lib/useStationTitle";
import { useTrack } from "../lib/useTrack";
import { FleetCard, type FleetCardData } from "./FleetCard";
import { PageTitle } from "./Panel";
import { ActionButton, ActionLink, ActionRow } from "./ui/ActionButton";
import { Facts } from "./ui/Facts";
import { Prompt } from "./ui/Prompt";
import { Section } from "./ui/Section";
import { ShipDiagram } from "./ui/ShipDiagram";
import { StatGrid } from "./ui/StatGrid";
import { uploadUrl, VesselPhotos } from "./VesselPhotos";
import { VesselTrack } from "./VesselTrack";

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
  const [trackRange, setTrackRange] = useState<TrackRange>({ span: 24, end: null });
  const { track, loading: trackLoading, failure: trackFailure } = useTrack(mmsi, trackRange);

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
  const stationRef = useStation(station);
  const source = fresher?.source ?? p?.source ?? heard?.source;
  const country = flagName(p?.flag);
  const imo = p?.imo ?? heard?.imo;
  const draught = p?.draught ? (
    <span className="flex items-baseline gap-2">
      <span className="text-caption text-fg-muted">Draught</span>
      <span className="text-subhead text-fg-secondary tabular-nums">{p.draught.toFixed(1)} m</span>
    </span>
  ) : undefined;
  const dimensions = vesselDimensions(
    p?.to_bow != null
      ? { toBow: p.to_bow, toStern: p.to_stern ?? 0, toPort: p.to_port ?? 0, toStarboard: p.to_starboard ?? 0 }
      : heard?.dimension && {
          toBow: heard.dimension.A,
          toStern: heard.dimension.B,
          toPort: heard.dimension.C,
          toStarboard: heard.dimension.D,
        },
    p?.length,
    p?.beam,
  );
  const media = useMedia(feature || heard ? mediaKey(imo, mmsi) : undefined);

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
        <VesselPhotos name={`MMSI ${mmsi}`} />
        <PageTitle className="text-large-title">MMSI {mmsi}</PageTitle>
        <p className="mt-2 text-body text-fg-secondary">
          {loading ? "Loading…" : "The network has never heard this vessel."}
        </p>
        {!loading && (
          <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mt-4">
            The network hears what its receivers hear. One where this vessel sails would pick it up.
          </Prompt>
        )}
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

  const attribution = heard?.attribution;

  return (
    <article>
      <VesselPhotos media={media} mmsi={mmsi} imo={imo} name={name ?? `MMSI ${mmsi}`} />
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

      <VesselActions live={live} mmsi={mmsi} imo={imo} name={name} hasTrack={(track?.coords.length ?? 0) > 0} />

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
            <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-accent-bg text-accent">
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

      <VesselTrack
        mmsi={mmsi}
        track={track}
        loading={trackLoading}
        failure={trackFailure}
        range={trackRange}
        firstSeen={p?.first_seen ? Date.parse(p.first_seen) : undefined}
        onRangeChange={setTrackRange}
      />

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

      {(dimensions || p?.draught) && (
        <Section label="Dimensions">
          {dimensions ? <ShipDiagram {...dimensions} type={type} aside={draught} /> : draught}
        </Section>
      )}

      <Facts
        className="mt-5"
        items={[
          ["Station", stationRef ? <Link to={`/stations/${stationRef.id}`}>{stationRef.title}</Link> : undefined],
        ]}
      />

      {p?.particulars && (
        <Particulars particulars={p.particulars} provenance={p.provenance} sources={p.sources} name={name} photos={media?.links.commonsCategory} />
      )}

      <ContributePrompt ownBoat={cls === "pleasure" || /ClassB/.test(p?.msg_type ?? "")} volunteer={isVolunteer(source)} />

      <Section label="Use this data">
        <p className="text-subhead text-fg-secondary">This vessel&rsquo;s latest record, as JSON:</p>
        <code className="mt-1.5 block rounded-md bg-surface-subtle px-2 py-1.5 font-mono text-footnote break-all text-fg">
          curl {publicApiBase()}/v1/vessels/{mmsi}
        </code>
        <p className="mt-2 text-subhead text-fg-secondary">
          Stream its positions as they are heard, or every vessel in an area.{" "}
          <a href={DEVELOPERS} className="font-medium whitespace-nowrap">
            Developers →
          </a>
        </p>
      </Section>

      <Fleets mmsi={mmsi} />

      {/* The credit that came with this vessel's own last message, which only the stream carries. */}
      {attribution && (
        <p className="mt-5 text-footnote text-fg-muted">
          {heard?.license ? `${attribution} Licence: ${heard.license}.` : attribution}
        </p>
      )}
    </article>
  );
}

/**
 * The vessel as registered, one section merged from the enrichment sources, and where to read
 * more about it. Absent for most vessels: Wikidata knows ships with an IMO, the Coast Guard
 * US-flag vessels it matches by call sign and name. The footer links each source's own page for
 * the vessel. For CC0 and public-domain sources the footer is provenance for the reader; for an
 * NLOD-licensed register it is also the credit the license asks for. `photos` is the Commons
 * category the photos above came from.
 */
function Particulars({
  particulars: m,
  provenance,
  sources,
  name,
  photos,
}: {
  particulars: VesselParticulars;
  provenance?: Record<string, string>;
  sources?: Record<string, SourceRef>;
  name?: string;
  photos?: string;
}) {
  const links: Array<[string, string | undefined]> = [
    ["Wikipedia", m.wikipedia],
    ...Object.values(sources ?? {}).map((s): [string, string | undefined] => [s.credit, s.url]),
    // The item's category, else the one the photos above came from.
    ["Wikimedia Commons", m.commons_category ?? photos],
  ];
  return (
    <Section label="Particulars">
      <Facts items={particularsFacts(m, name, provenance)} />
      <SourceLinks className="mt-3" links={links} />
    </Section>
  );
}

/** Where to read more, as one line of links; links without an address are left out. */
function SourceLinks({ links, className }: { links: Array<[string, string | undefined]>; className?: string }) {
  const linked = links.filter((l): l is [string, string] => !!l[1]);
  if (!linked.length) return null;
  return (
    <p className={cn("text-footnote text-fg-muted", className)}>
      {linked.map(([label, href], i) => (
        <Fragment key={label}>
          {i > 0 && " · "}
          <a href={href} target="_blank" rel="noopener">
            {label}
          </a>
        </Fragment>
      ))}
    </p>
  );
}

/** Follow, share, show the track, add a photo: what a reader does with a vessel once it is open. */
function VesselActions({
  live,
  mmsi,
  imo,
  name,
  hasTrack,
}: {
  live: Live | undefined;
  mmsi: number;
  imo?: number;
  name?: string;
  hasTrack: boolean;
}) {
  const [following, setFollowing] = useState(false);
  const [copied, setCopied] = useState(false);
  // The map owns the state: a drag or another vessel ends following without this button.
  useEffect(() => live?.ctl.onCameraFollow(setFollowing), [live]);

  async function share() {
    const title = name ?? `MMSI ${mmsi}`;
    const done = await shareLink({ title, text: `${title}, live on Open Waters AIS`, url: `${SITE}${vesselPath(mmsi, name)}` });
    if (done !== "copied") return;
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <ActionRow>
      <ActionButton icon={LocateFixed} label="Follow" pressed={following} onClick={() => live?.ctl.followCamera(!following)} />
      <ActionButton icon={copied ? Check : Share} label={copied ? "Copied" : "Share"} onClick={() => void share()} />
      <ActionButton icon={Route} label="Track" disabled={!hasTrack} onClick={() => live?.ctl.fitTrack()} />
      <ActionLink icon={ImagePlus} label="Add photo" href={uploadUrl(imo, mmsi)} target="_blank" rel="noopener" />
    </ActionRow>
  );
}

/**
 * One invitation to run a receiver, fitted to the vessel. Someone looking up a pleasure boat
 * or a class B set is often its owner, who has the antenna already. Otherwise it is shown when
 * a volunteer's receiver heard the vessel, which is the network working as it should.
 */
function ContributePrompt({ ownBoat, volunteer }: { ownBoat: boolean; volunteer: boolean }) {
  if (ownBoat) {
    return (
      <Prompt icon={Sailboat} href={SIGNALK_PLUGIN} action="Signal K plugin" className="mt-5">
        Is this your boat? Share what its AIS hears with the network, and see traffic beyond its range.
      </Prompt>
    );
  }
  if (!volunteer) return null;
  return (
    <Prompt icon={Antenna} href={CONTRIBUTE} action={CONTRIBUTE_PROMPT} className="mt-5">
      A volunteer&rsquo;s receiver heard this vessel.
    </Prompt>
  );
}

/** The fleets this vessel is in, as their cards, from routes/vessel-fleets.ts. Nothing until they come, or if none. */
function Fleets({ mmsi }: { mmsi: number }) {
  const [answer, setAnswer] = useState<{ cards: FleetCardData[]; photos: Record<string, Photo> }>();
  useEffect(() => {
    let current = true;
    setAnswer(undefined);
    void fetch(`/ais/vessels/fleets/${mmsi}`)
      .then((res) => (res.ok ? (res.json() as Promise<{ cards: FleetCardData[]; photos: Record<string, Photo> }>) : undefined))
      .catch(() => undefined)
      .then((a) => current && setAnswer(a));
    return () => {
      current = false;
    };
  }, [mmsi]);
  if (!answer?.cards.length) return null;
  return (
    <Section label={answer.cards.length === 1 ? "In a fleet" : "In fleets"} bare>
      <div className="space-y-3">
        {answer.cards.map((card) => (
          <FleetCard key={card.path} card={card} photos={answer.photos} featured />
        ))}
      </div>
    </Section>
  );
}
