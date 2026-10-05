import { DirectoryLayout } from "../components/DirectoryLayout";
import { VesselPhotos } from "../components/VesselPhotos";
import { useMedia } from "../lib/useMedia";
import { VesselMapPreview } from "../components/VesselMapPreview";
import { pageMeta } from "../lib/meta";
import {
  TECH_YACHTS_PATH,
  techYachts,
  type TechYacht,
} from "../lib/tech-yachts";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "Tech billionaires & their yachts | Open Waters AIS",
    description:
      "Explore sailing and motor yachts connected to technology founders and leaders, past and present.",
    path: TECH_YACHTS_PATH,
  });

export default function TechYachts() {
  return (
    <DirectoryLayout>
      <nav
        aria-label="Breadcrumb"
        className="mb-5 flex gap-2 text-subhead text-fg-muted"
      >
        <a href="/ais/explore">Explore</a>
        <span aria-hidden>/</span>
        <span aria-current="page">Tech yachts</span>
      </nav>
      <h1 className="text-3xl font-semibold tracking-tight md:text-4xl">
        Tech billionaires &amp; their yachts
      </h1>
      <p className="mt-3 max-w-2xl text-body text-fg-secondary">
        From racing sailboats to expedition yachts: boats owned or commissioned
        by technology founders and leaders, past and present.
      </p>
      <p className="mt-2 max-w-2xl text-subhead text-fg-muted">
        Ownership reflects public reporting, with former connections labeled.
        Sources checked October 5, 2026.
      </p>
      <nav aria-label="Yacht sections" className="mt-6 flex gap-5 text-subhead">
        {techYachts.map((section) => (
          <a key={section.id} href={`#${section.id}`}>
            {section.title} · {section.boats.length}
          </a>
        ))}
      </nav>
      {techYachts.map((section) => (
        <section
          key={section.id}
          id={section.id}
          aria-labelledby={`${section.id}-title`}
          className="mt-10 scroll-mt-6"
        >
          <h2 id={`${section.id}-title`} className="text-2xl font-semibold">
            {section.title}
          </h2>
          <div className="mt-5 grid gap-x-8 gap-y-10 md:grid-cols-2">
            {section.boats.map((boat) => (
              <YachtCard key={boat.id} boat={boat} />
            ))}
          </div>
        </section>
      ))}
    </DirectoryLayout>
  );
}

function YachtCard({ boat }: { boat: TechYacht }) {
  const media = useMedia(boat.imo ? String(boat.imo) : undefined);
  return (
    <article id={boat.id} className="min-w-0 border-t border-line pt-5">
      <h3 className="text-title">{boat.name}</h3>
      <p className="mt-1 text-subhead text-fg-secondary">{boat.model}</p>
      <p className="mt-3 text-body text-fg-secondary">{boat.description}</p>
      <a
        className="mt-3 inline-block text-footnote"
        href={boat.source}
        target="_blank"
        rel="noopener noreferrer"
      >
        Yacht details
      </a>
      <VesselPhotos
        media={media}
        mmsi={boat.mmsi}
        imo={boat.imo}
        name={boat.name}
        className="relative mt-4 aspect-video overflow-hidden rounded-lg bg-surface-tile"
      />
      <p className="mt-4 text-headline font-semibold">{boat.person}</p>
      <p className="mt-1 text-subhead text-fg-secondary">
        {boat.connection} · {boat.chapter}
      </p>
      <a
        className="mt-2 inline-block text-footnote"
        href={boat.ownershipSource}
        target="_blank"
        rel="noopener noreferrer"
      >
        Ownership history
      </a>
      <div className="mt-4">
        <VesselMapPreview boat={boat} />
      </div>
      {boat.mmsi && (
        <a
          className="mt-3 inline-block text-footnote"
          href={boat.identitySource}
          target="_blank"
          rel="noopener noreferrer"
        >
          MMSI {boat.mmsi}
        </a>
      )}
    </article>
  );
}
