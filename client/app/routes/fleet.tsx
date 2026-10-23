import { useEffect, useRef, useState } from "react";
import { data, Link } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { RouteError, routeErrorMeta } from "../components/RouteError";
import { Credit } from "../components/ui/Credit";
import { PhotoCarousel } from "../components/ui/PhotoCarousel";
import { ChipRow, MenuChip } from "../components/ui/Chip";
import { Section } from "../components/ui/Section";
import { parseDestination, parseEta, shortAge, vesselActivity, vesselPath } from "../lib/ais";
import { browserAuth, getVessel, type VesselFeature } from "../lib/api";
import { cardRows } from "../lib/cardRows";
import { cn } from "../lib/cn";
import { FleetCard } from "../components/FleetCard";
import { coverKey, fleetCard, fleetPath, getFleet, parentPath, photoNames, type Fleet, type FleetSection, type FleetVessel } from "../lib/fleets.server";
import { fileTitle, mediaKey, type Photo } from "../lib/media";
import { namedPhotos } from "../lib/media.server";
import { useLive, useNow } from "../lib/live";
import { pageMeta, SITE } from "../lib/meta";
import { useMedia } from "../lib/useMedia";
import type { BBox } from "../lib/stream";
import type { Route } from "./+types/fleet";

export function loader({ params, request }: Route.LoaderArgs) {
  const fleet = getFleet((params["*"] ?? "").replace(/\/+$/, ""));
  if (!fleet) throw data("No such fleet", { status: 404 });
  const { children, ...rest } = fleet;
  return {
    fleet: rest,
    path: fleetPath(fleet.id),
    back: parentPath(fleet.id),
    cover: coverKey(fleet),
    cards: children.map((id) => fleetCard(getFleet(id)!)),
    // Streamed, not awaited: a slow Commons answer never holds up the page, and the photos fill in.
    photos: namedPhotos(photoNames(fleet), request.url),
  };
}

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

export const meta = ({ loaderData, error }: Route.MetaArgs) =>
  loaderData
    ? pageMeta({
      title: `${loaderData.fleet.title} | Open Waters AIS`,
      description: loaderData.fleet.id ? loaderData.fleet.summary : "Collections of notable vessels, from cruise ships to YouTube sailors, on the live Open Waters AIS map.",
      path: loaderData.path,
      // Drawn by the Worker (lib/shareCard.server.tsx).
      image: `${SITE}${loaderData.path}.png`,
    })
    : routeErrorMeta(error);

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  return <RouteError error={error} />;
}

export default function FleetPage({ loaderData }: Route.ComponentProps) {
  const { fleet, cards, back, cover } = loaderData;
  const photos = usePhotos(loaderData.photos);
  // Keyed by fleet, since one route serves them all: a fleet's answers and framing start afresh.
  return fleet.sections ? (
    <FleetView key={fleet.id} fleet={{ ...fleet, sections: fleet.sections }} back={back} cover={cover} photos={photos} />
  ) : (
    <GroupView fleet={fleet} cards={cards} back={back} photos={photos} />
  );
}

/** The named photos once they arrive, undefined until then; none if Commons did not answer. */
function usePhotos(promise: Promise<Record<string, Photo>>): Record<string, Photo> | undefined {
  const [photos, setPhotos] = useState<Record<string, Photo>>();
  useEffect(() => {
    let current = true;
    setPhotos(undefined);
    promise.then(
      (p) => current && setPhotos(p),
      () => current && setPhotos({}),
    );
    return () => {
      current = false;
    };
  }, [promise]);
  return photos;
}

const named = (names: string[] | undefined, photos: Record<string, Photo> | undefined) => (names ?? []).flatMap((n) => photos?.[fileTitle(n)] ?? []);

type Card = Route.ComponentProps["loaderData"]["cards"][number];

/** A group's fleets as cards, the first shown largest, as guides are shown in a maps app. */
function GroupView({ fleet, cards, back, photos }: { fleet: Omit<Fleet, "children">; cards: Card[]; back: string; photos?: Record<string, Photo> }) {
  return (
    <Panel back={back} title={fleet.title}>
      <PageTitle>{fleet.title}</PageTitle>
      <p className="mt-1 text-body text-fg-secondary">{fleet.summary}</p>
      {fleet.description && <p className="mt-2 text-footnote text-fg-muted">{fleet.description}</p>}
      {/* A wide card every third row, and no half row left with one card: lib/cardRows.ts. */}
      <div className="mt-4 space-y-3">
        {cardRows(cards).map((row) =>
          row.length === 1 ? (
            <FleetCard key={row[0]!.path} card={row[0]!} photos={photos} featured />
          ) : (
            <div key={row[0]!.path} className="grid grid-cols-2 gap-3">
              {row.map((c) => (
                <FleetCard key={c.path} card={c} photos={photos} />
              ))}
            </div>
          ),
        )}
      </div>
    </Panel>
  );
}

// Answers kept for a minute, so going back and forth between fleets does not ask again: the API
// allows an address 120 requests a minute, and the rest of the app shares them.
const answered = new Map<number, { at: number; feature: VesselFeature | null }>();

/** Each vessel's last report, once asked; null for a vessel the API has no answer for. */
function useReports(vessels: FleetVessel[]): Map<number, VesselFeature | null> {
  const fresh = (mmsi: number) => {
    const a = answered.get(mmsi);
    return a && Date.now() - a.at < 60_000 ? a : undefined;
  };
  const known = () => new Map(vessels.flatMap((v) => (v.mmsi && fresh(v.mmsi) ? [[v.mmsi, fresh(v.mmsi)!.feature] as const] : [])));
  const [reports, setReports] = useState(known);
  const key = vessels.map((v) => v.mmsi).join();
  useEffect(() => {
    let current = true;
    setReports(known());
    // ponytail: one request per vessel, fine for fleets of tens; batch by `mmsi=` if fleets grow to hundreds.
    for (const { mmsi } of vessels) {
      if (!mmsi || fresh(mmsi)) continue;
      void getVessel(browserAuth(), mmsi)
        // A 404 is an answer; an outage is not, so it is asked again next time.
        .then((f) => (answered.set(mmsi, { at: Date.now(), feature: f ?? null }), f ?? null))
        .catch(() => null)
        .then((f) => current && setReports((prev) => new Map(prev).set(mmsi, f)));
    }
    return () => {
      current = false;
    };
  }, [key]);
  return reports;
}

// Most recently heard first by default; "As listed" keeps the fleet's sections, such as a YouTube channel's boats.
type Sort = "recent" | "name" | "listed";
const SORTS: Array<{ value: Sort; label: string; chip: string }> = [
  { value: "recent", label: "Recent", chip: "Recent" },
  { value: "name", label: "Name", chip: "Name" },
  { value: "listed", label: "As listed", chip: "As listed" },
];

interface Entry {
  vessel: FleetVessel;
  section: FleetSection;
  report?: VesselFeature;
}

function FleetView({
  fleet,
  back,
  cover,
  photos,
}: {
  fleet: Omit<Fleet, "children"> & { sections: FleetSection[] };
  back: string;
  cover?: string;
  photos?: Record<string, Photo>;
}) {
  // The fleet's own photos head it; without any, or when they do not come, its cover vessel's do.
  const own = named(fleet.photos, photos);
  const media = useMedia(fleet.photos?.length && (!photos || own.length) ? undefined : cover);
  const hero = own.length ? own : (media?.photos ?? []);
  const live = useLive();
  const vessels = fleet.sections.flatMap((s) => s.vessels);
  const reports = useReports(vessels);
  const [hovered, setHovered] = useState<number>();
  const [sort, setSort] = useState<Sort>("recent");

  const seen = (e: Entry) => (e.report ? Date.parse(e.report.properties.seen) : 0);
  const entries = fleet.sections.flatMap((section) =>
    section.vessels.map((vessel): Entry => ({ vessel, section, report: (vessel.mmsi && reports.get(vessel.mmsi)) || undefined })),
  );

  // The map rings the vessels shown and frames the whole fleet, as it does search results.
  const ring = (es: Entry[]) =>
    es.flatMap(({ report: f }) =>
      f?.geometry
        ? [{ mmsi: f.properties.mmsi, lon: f.geometry.coordinates[0]!, lat: f.geometry.coordinates[1]!, name: f.properties.name, kind: f.properties.kind, shipType: f.properties.type, seen: Date.parse(f.properties.seen) }]
        : [],
    );
  useEffect(() => {
    live?.ctl.setResults(ring(entries));
    live?.ctl.highlightResult(hovered);
  });
  useEffect(() => () => live?.ctl.setResults([]), [live]);
  // Framed once every answer is in, so the camera does not chase each one.
  const settled = reports.size === vessels.filter((v) => v.mmsi).length;
  useEffect(() => {
    const placed = ring(entries);
    if (!live || !placed.length) return;
    const fit: BBox = [
      Math.min(...placed.map((p) => p.lat)),
      Math.min(...placed.map((p) => p.lon)),
      Math.max(...placed.map((p) => p.lat)),
      Math.max(...placed.map((p) => p.lon)),
    ];
    live.ctl.fitBBox(fit);
  }, [live, settled]);

  const row = (e: Entry, context?: string) => (
    <VesselCard
      key={`${e.section.title}-${e.vessel.name}-${e.vessel.mmsi}`}
      vessel={e.vessel}
      report={e.report}
      photos={named(e.vessel.photos, photos)}
      context={context}
      onHover={(over) => setHovered(over ? e.vessel.mmsi : undefined)}
    />
  );

  return (
    // Only photos head the fleet: an empty frame would push its vessels down for nothing.
    <Panel back={back} title={fleet.title} hero={hero.length > 0}>
      {hero.length ? (
        // Full bleed, as a guide opens in a maps app: the photos, then the title on a dark band.
        <div className="-mx-4 mb-4">
          <div className="relative aspect-[4/3] bg-surface-tile">
            <PhotoCarousel photos={hero} alt={fleet.title} />
          </div>
          <div className="px-4 pt-4 pb-5">
            <PageTitle className="text-large-title font-bold">{fleet.title}</PageTitle>
            <p className="mt-2 text-body text-fg-muted">{fleet.summary}</p>
            {fleet.description && <p className="mt-2 text-footnote text-fg-muted">{fleet.description}</p>}
            {fleet.source && <Source links={fleet.source} className="text-fg-muted [&_a]:text-fg-muted" />}
          </div>
        </div>
      ) : (
        <>
          <PageTitle>{fleet.title}</PageTitle>
          <p className="mt-1 text-body text-fg-secondary">{fleet.summary}</p>
          {fleet.description && <p className="mt-2 text-footnote text-fg-muted">{fleet.description}</p>}
          {fleet.source && <Source links={fleet.source} className="text-fg-muted" />}
        </>
      )}
      <ChipRow label="Sort vessels" className="-mx-3 mt-4">
        <MenuChip value={sort} options={SORTS} onChange={setSort} />
      </ChipRow>
      {sort === "listed"
        ? fleet.sections.map((s, i) => {
          const es = entries.filter((e) => e.section === s);
          return es.length ? (
            s.avatar || s.links ? (
              <section key={s.title ?? i} className="mt-6">
                <SectionHeader section={s} />
                <ul className="space-y-3">{es.map((e) => row(e))}</ul>
              </section>
            ) : (
              <Section key={s.title ?? i} label={s.title ?? plural(es.length, "vessel")} aside={s.subtitle} bare>
                <ul className="space-y-3">{es.map((e) => row(e))}</ul>
              </Section>
            )
          ) : null;
        })
        : entries.length > 0 && (
          <Section label={plural(entries.length, "vessel")} bare>
            <ul className="space-y-3">
              {[...entries]
                // Never heard sorts last, by name.
                .sort((a, b) => (sort === "recent" ? seen(b) - seen(a) : 0) || a.vessel.name.localeCompare(b.vessel.name))
                // Out of their sections, each row says which it is from.
                .map((e) => row(e, e.section.title))}
            </ul>
          </Section>
        )}
    </Panel>
  );
}

/** The vessel's own named photos, or once its card is in view, its Commons photos by IMO or MMSI. */
function useCardPhotos(vessel: FleetVessel, named: Photo[]) {
  const ref = useRef<HTMLLIElement>(null);
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || seen || named.length || !vessel.mmsi) return;
    // Only cards that have been on screen ask, as search rows do: a long fleet of cold lookups
    // would cost the Worker several upstream calls each.
    const io = new IntersectionObserver(([entry]) => entry?.isIntersecting && setSeen(true), { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, [seen, named.length, vessel.mmsi]);
  const media = useMedia(seen && vessel.mmsi ? mediaKey(vessel.imo, vessel.mmsi) : undefined);
  return { ref, photos: named.length ? named : (media?.photos ?? []) };
}

/**
 * A vessel as a card, as a place is in a maps app: what it is and when it was last heard, with
 * its first photo beside it when it has one, then its note and links across the card.
 * The name's link covers the card; the photos and links sit above it, each going where it says.
 */
function VesselCard({
  vessel,
  report,
  context,
  photos: named,
  onHover,
}: {
  vessel: FleetVessel;
  report?: VesselFeature;
  context?: string;
  photos: Photo[];
  onHover(over: boolean): void;
}) {
  const now = useNow();
  const { ref, photos } = useCardPhotos(vessel, named);
  const p = report?.properties;
  const age = p && shortAge(Math.max(0, Math.round((now - Date.parse(p.seen)) / 1000)));
  const doing = p && vesselActivity(p);
  // Where the crew says the vessel is going, as the vessel page reads it, and when.
  const voyage = parseDestination(p?.destination);
  // The country in words, with the crew's code beside it; a destination typed as words, as typed.
  const to = voyage?.to && (voyage.to.country ? `${voyage.to.code}, ${voyage.to.country}` : voyage.to.raw);
  const eta = to ? parseEta(p?.eta) : undefined;
  // An arrival well past is a voyage over, or a crew that never updated it.
  const etaText = eta && eta.getTime() > now - 12 * 3_600_000 ? eta.toLocaleString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" }) : undefined;
  return (
    <li
      ref={ref}
      onPointerEnter={() => onHover(true)}
      onPointerLeave={() => onHover(false)}
      className="relative rounded-2xl bg-surface p-4 shadow-sm transition-shadow has-[a[data-card]:hover]:shadow-md"
    >
      <div className="flex gap-3">
        <div className="min-w-0 flex-1">
          <h3 className="text-headline font-semibold text-fg">
            {vessel.mmsi ? (
              <Link data-card to={vesselPath(vessel.mmsi, p?.name, p?.kind)} className="text-fg no-underline after:absolute after:inset-0 after:rounded-2xl hover:text-fg">
                {vessel.name}
              </Link>
            ) : (
              vessel.name
            )}
          </h3>
          {/* One size under the name; what it is doing reads darker, everything else muted. */}
          <div className="mt-0.5 space-y-0.5 text-subhead text-fg-muted">
            {(context || vessel.subtitle) && <p>{[context, vessel.subtitle].filter(Boolean).join(" · ")}</p>}
            <p className="truncate">
              {!vessel.mmsi ? (
                "Last location unknown"
              ) : age ? (
                <>
                  {doing && <span className="text-fg-secondary">{doing} · </span>}
                  {age} ago
                </>
              ) : report === undefined ? (
                "\u00a0"
              ) : (
                "Last location unknown"
              )}
            </p>
            {to && (
              <p className="truncate" title={p?.destination}>
                {[`Bound for ${to}`, etaText && `ETA ${etaText}`].filter(Boolean).join(" · ")}
              </p>
            )}
          </div>
        </div>
        {photos[0] && <Thumb photo={photos[0]} className="size-24" />}
      </div>
      {/* Below the photo, the note and links take the card's full width. */}
      {vessel.note && <p className="mt-2 text-footnote text-fg-secondary">{vessel.note}</p>}
      {vessel.links && <Links links={vessel.links} className="relative z-10 mt-1 w-fit" />}
    </li>
  );
}

/** A photo, square and rounded, linking to its Commons page and its credit. */
function Thumb({ photo, className }: { photo: Photo; className: string }) {
  return (
    // Above the card's own link, with the credit beside the photo's link rather than in it, so a tap on
    // the © opens the credit. Anchored right, it opens leftward across the card.
    <span className={cn("relative z-10 block shrink-0", className)}>
      <a href={photo.page} target="_blank" rel="noopener" className="block size-full overflow-hidden rounded-xl bg-surface-tile">
        <img src={photo.thumb.replace("/960px-", "/330px-")} alt={photo.description ?? ""} loading="lazy" decoding="async" className="size-full object-cover" />
      </a>
      <Credit className="absolute right-1 bottom-1 max-w-[min(18rem,calc(100vw-4rem))]">
        {photo.artist} · {photo.license}
      </Credit>
    </span>
  );
}

/**
 * A section that is someone, such as a YouTube channel: their avatar, name and who they are, and
 * their links as buttons, as a guide's publisher is shown in a maps app.
 */
function SectionHeader({ section }: { section: FleetSection }) {
  return (
    <header className="mb-2 flex items-center gap-3 px-0.5">
      {section.avatar && (
        // YouTube's image hosts refuse a referrer from elsewhere now and then; without one they serve.
        <img src={section.avatar} alt="" referrerPolicy="no-referrer" className="size-10 shrink-0 rounded-full bg-surface-tile object-cover" />
      )}
      <div className="min-w-0 flex-1">
        <h2 className="truncate text-headline font-semibold text-fg">{section.title}</h2>
        {section.subtitle && <p className="truncate text-subhead text-fg-muted">{section.subtitle}</p>}
      </div>
      {Object.entries(section.links ?? {}).map(([label, url]) => (
        <a
          key={label}
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={`${section.title} on ${label}`}
          className="inline-flex h-8 shrink-0 items-center rounded-full bg-surface-subtle px-3 text-subhead font-medium text-fg no-underline hover:text-fg"
        >
          {label}
        </a>
      ))}
    </header>
  );
}

/** Where a fleet's members come from: "Source: Wikidata", each linked. */
function Source({ links, className }: { links: Record<string, string>; className?: string }) {
  return (
    <p className={`mt-2 text-footnote ${className ?? ""}`}>
      Source:{" "}
      {Object.entries(links).map(([label, url], i) => (
        <span key={label}>
          {i > 0 && ", "}
          <a href={url} target="_blank" rel="noopener noreferrer">
            {label}
          </a>
        </span>
      ))}
    </p>
  );
}

function Links({ links, className }: { links: Record<string, string>; className?: string }) {
  return (
    <p className={`flex flex-wrap gap-x-3 text-footnote ${className ?? ""}`}>
      {Object.entries(links).map(([label, url]) => (
        <a key={label} href={url} target="_blank" rel="noopener noreferrer">
          {label}
        </a>
      ))}
    </p>
  );
}
