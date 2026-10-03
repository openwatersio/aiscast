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
            count: sailingChannels.length,
            description:
              "Sailing adventures, boat builds, and life under sail.",
          },
          {
            name: "Cruisers",
            path: CRUISERS_PATH,
            count: cruisingChannels.length,
            description: "Trawlers, motor yachts, and life on the water.",
          },
        ].map((collection) => (
          <a
            key={collection.path}
            href={`/ais${collection.path}`}
            className="block rounded-xl border border-line p-6 hover:bg-accent-bg"
          >
            <h2 className="text-title">{collection.name}</h2>
            <p className="mt-2 text-body text-fg-secondary">
              {collection.description}
            </p>
            <p className="mt-3 text-subhead text-fg-muted">
              {collection.count} channels · Boats past and present
            </p>
          </a>
        ))}
      </div>
    </DirectoryLayout>
  );
}
