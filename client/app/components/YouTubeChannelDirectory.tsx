import { DirectoryLayout } from "../components/DirectoryLayout";
import { YouTubeChannelCard } from "./YouTubeChannelCard";
import {
  CRUISERS_PATH,
  SAILORS_PATH,
  YOUTUBE_PATH,
  type YouTubeChannel,
} from "../lib/explore";

export function YouTubeChannelDirectory({
  category,
  description,
  channels,
}: {
  category: "Sailors" | "Cruisers";
  description: string;
  channels: YouTubeChannel[];
}) {
  return (
    <DirectoryLayout>
      <nav
        aria-label="Breadcrumb"
        className="mb-5 flex flex-wrap gap-2 text-subhead text-fg-muted"
      >
        <a href="/ais/explore">Explore</a>
        <span aria-hidden>/</span>
        <a href={`/ais${YOUTUBE_PATH}`}>YouTube</a>
        <span aria-hidden>/</span>
        <span aria-current="page">{category}</span>
      </nav>
      <h1 className="text-3xl font-semibold tracking-tight md:text-4xl">
        YouTube {category.toLowerCase()}
      </h1>
      <p className="mt-3 max-w-2xl text-body text-fg-secondary">
        {description} Open a map preview to explore in the AIS viewer.
      </p>
      <p className="mt-2 max-w-2xl text-subhead text-fg-muted">
        Boat versions are listed separately. A previous boat’s position follows
        that vessel, even after its creators move on.
      </p>
      <nav
        aria-label="YouTube collections"
        className="mt-6 flex gap-5 text-subhead"
      >
        {[
          ["Sailors", SAILORS_PATH],
          ["Cruisers", CRUISERS_PATH],
        ].map(([name, path]) =>
          category === name ? (
            <span
              key={name}
              aria-current="page"
              className="font-semibold text-fg"
            >
              {name}
            </span>
          ) : (
            <a key={name} href={`/ais${path}`}>
              {name}
            </a>
          ),
        )}
      </nav>
      <div className="mt-8 grid gap-x-8 gap-y-10 md:grid-cols-2">
        {channels.map((channel) => {
          return <YouTubeChannelCard key={channel.id} channel={channel} />;
        })}
      </div>
    </DirectoryLayout>
  );
}
