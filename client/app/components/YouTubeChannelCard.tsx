import { Play } from "lucide-react";
import { VesselMapPreview } from "./VesselMapPreview";
import { vesselPath } from "../lib/ais";
import type { YouTubeChannel } from "../lib/explore";

export function YouTubeChannelCard({
  channel,
  preview = false,
}: {
  channel: YouTubeChannel;
  preview?: boolean;
}) {
  const [boat, ...previous] = channel.boats;
  const ChannelHeading = preview ? "h3" : "h2";
  const BoatHeading = preview ? "h4" : "h3";
  if (!boat) return null;
  return (
    <article id={channel.id} className="min-w-0 border-t border-line pt-5">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <ChannelHeading className="text-title">{channel.name}</ChannelHeading>
          <p className="mt-1 text-subhead text-fg-secondary">{channel.crew}</p>
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
          <BoatHeading className="text-headline font-semibold">
            {boat.name}
          </BoatHeading>
          <p className="mt-1 text-subhead text-fg-secondary">{boat.model}</p>
        </div>
        <span className="text-right text-footnote text-fg-muted">
          {boat.chapter}
        </span>
      </div>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-footnote">
        {boat.source && (
          <a href={boat.source} target="_blank" rel="noopener noreferrer">
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
      {!preview && previous.length > 0 && (
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
                <a href={`/ais${vesselPath(version.mmsi)}`}>{version.name}</a>
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
              <span className="block text-fg-secondary">{version.model}</span>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
