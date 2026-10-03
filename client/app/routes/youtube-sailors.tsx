import { Play } from "lucide-react";
import { DirectoryLayout } from "../components/DirectoryLayout";
import { VesselMapPreview } from "../components/VesselMapPreview";
import { vesselPath } from "../lib/ais";
import { pageMeta } from "../lib/meta";
import { sailingChannels, SAILORS_PATH } from "../lib/explore";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "YouTube sailors & their boats | Open Waters AIS",
    description:
      "A directory of sailing YouTube channels, their boats past and present, and map previews that open the Open Waters AIS viewer.",
    path: SAILORS_PATH,
  });

export default function YoutubeSailors() {
  return (
    <DirectoryLayout>
      <nav
        aria-label="Breadcrumb"
        className="mb-5 flex flex-wrap gap-2 text-subhead text-fg-muted"
      >
        <a href="/ais/explore">Explore</a>
        <span aria-hidden>/</span>
        <a href="/ais/explore/youtube">YouTube</a>
        <span aria-hidden>/</span>
        <span aria-current="page">Sailors</span>
      </nav>
      <h1 className="text-3xl font-semibold tracking-tight md:text-4xl">
        YouTube sailors
      </h1>
      <p className="mt-3 max-w-2xl text-body text-fg-secondary">
        Find sailing channels and their boats, past and present. Open a map preview
        to explore in the AIS viewer.
      </p>
      <p className="mt-2 max-w-2xl text-subhead text-fg-muted">
        Boat versions are listed separately. A previous boat’s position follows
        that vessel, even after its creators move on.
      </p>
      <div className="mt-8 grid gap-x-8 gap-y-10 md:grid-cols-2">
        {sailingChannels.map((channel) => {
          const [boat, ...previous] = channel.boats;
          if (!boat) return null;
          return (
            <article
              key={channel.id}
              id={channel.id}
              className="min-w-0 border-t border-line pt-5"
            >
              <div className="mb-4 flex items-start justify-between gap-4">
                <div>
                  <h2 className="text-title">{channel.name}</h2>
                  <p className="mt-1 text-subhead text-fg-secondary">
                    {channel.crew}
                  </p>
                </div>
                <a
                  href={channel.youtube}
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label={`${channel.name} on YouTube`}
                  className="flex shrink-0 items-center gap-1.5 rounded-full border border-line px-3 py-2 text-subhead hover:bg-accent-bg"
                >
                  <Play className="size-4" aria-hidden />
                  <span>YouTube</span>
                </a>
              </div>
              <VesselMapPreview boat={boat} />
              <div className="mt-4 flex items-start justify-between gap-3">
                <div>
                  <h3 className="text-headline font-semibold">{boat.name}</h3>
                  <p className="mt-1 text-subhead text-fg-secondary">
                    {boat.model}
                  </p>
                </div>
                <span className="text-right text-footnote text-fg-muted">
                  {boat.chapter}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-footnote">
                {boat.source && (
                  <a
                    href={boat.source}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    Boat details
                  </a>
                )}
                {boat.mmsi && (
                  <a
                    href={boat.identitySource}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    MMSI {boat.mmsi}
                  </a>
                )}
              </div>
              {previous.length > 0 && (
                <ul
                  aria-label={`${channel.name} boat versions`}
                  className="mt-4 space-y-3 border-t border-line-subtle pt-3"
                >
                  {previous.map((version, i) => (
                    <li key={`${version.name}-${i}`} className="text-subhead">
                      <span className="mr-2 text-footnote text-fg-muted">
                        {version.chapter}
                      </span>
                      {version.mmsi ? (
                        <a href={`/ais${vesselPath(version.mmsi)}`}>
                          {version.name}
                        </a>
                      ) : version.source ? (
                        <a
                          href={version.source}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          {version.name}
                        </a>
                      ) : (
                        <span>{version.name}</span>
                      )}
                      <span className="block text-fg-secondary">
                        {version.model}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </article>
          );
        })}
      </div>
    </DirectoryLayout>
  );
}
