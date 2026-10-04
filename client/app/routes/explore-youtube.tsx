import { YouTubeChannelCard } from "../components/YouTubeChannelCard";
import { DirectoryLayout } from "../components/DirectoryLayout";
import { pageMeta } from "../lib/meta";
import {
  YOUTUBE_PATH,
  SAILORS_PATH,
  CRUISERS_PATH,
  sailingChannels,
} from "../lib/explore";
import { cruisingChannels } from "../lib/cruisers";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "YouTube channels | Open Waters AIS",
    description:
      "Browse sailing and cruising YouTube channels, discover their boats, and explore them in the AIS viewer.",
    path: YOUTUBE_PATH,
  });

export default function YoutubeDirectory() {
  return (
    <DirectoryLayout>
      <nav
        aria-label="Breadcrumb"
        className="mb-5 flex gap-2 text-subhead text-fg-muted"
      >
        <a href="/ais/explore">Explore</a>
        <span aria-hidden>/</span>
        <span aria-current="page">YouTube</span>
      </nav>
      <h1 className="text-3xl font-semibold tracking-tight md:text-4xl">
        YouTube channels
      </h1>
      <p className="mt-3 max-w-2xl text-body text-fg-secondary">
        Find a channel, discover its boats, and open a map preview to explore in
        the AIS viewer.
      </p>
      <div className="mt-8 grid gap-6 md:grid-cols-2">
        {[
          {
            name: "Sailors",
            path: SAILORS_PATH,
            channels: sailingChannels,
            description:
              "Sailing adventures, boat builds, and life under sail.",
          },
          {
            name: "Cruisers",
            path: CRUISERS_PATH,
            channels: cruisingChannels,
            description: "Trawlers, motor yachts, and life on the water.",
          },
        ].map((collection) => (
          <section
            key={collection.path}
            className="min-w-0"
            aria-label={collection.name}
          >
            <h2 className="text-title">{collection.name}</h2>
            <p className="mt-2 text-body text-fg-secondary">
              {collection.description}
            </p>
            <p className="mt-2 text-subhead text-fg-muted">
              {collection.channels.length} channels · Boats past and present
            </p>
            <a
              href={`/ais${collection.path}`}
              className="my-5 inline-flex items-center rounded-full border border-line px-4 py-3 text-subhead font-semibold hover:bg-accent-bg"
            >
              Browse all {collection.name.toLowerCase()}{" "}
              <span className="ml-2" aria-hidden>
                →
              </span>
            </a>
            {collection.channels[0] && (
              <YouTubeChannelCard channel={collection.channels[0]} preview />
            )}
          </section>
        ))}
      </div>
    </DirectoryLayout>
  );
}
