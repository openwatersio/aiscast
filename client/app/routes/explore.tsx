import { DirectoryLayout } from "../components/DirectoryLayout";
import { pageMeta } from "../lib/meta";
import { YOUTUBE_PATH } from "../lib/explore";
import { TECH_YACHTS_PATH } from "../lib/tech-yachts";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "Explore vessels | Open Waters AIS",
    description:
      "Browse collections of vessels and open their positions in the Open Waters AIS viewer.",
    path: "/explore",
  });

export default function Explore() {
  return (
    <DirectoryLayout>
      <h1 className="text-3xl font-semibold tracking-tight">Explore vessels</h1>
      <p className="mt-3 text-body text-fg-secondary">
        Browse a collection, then open a vessel in the AIS viewer.
      </p>
      <div className="mt-8 grid gap-6 md:grid-cols-2">
        {[
          {
            path: YOUTUBE_PATH,
            title: "YouTube channels",
            description:
              "Sailors and cruisers, their crews, and their boats past and present.",
          },
          {
            path: TECH_YACHTS_PATH,
            title: "Tech billionaires & their yachts",
            description:
              "Sailing and motor yachts connected to technology founders and leaders, past and present.",
          },
        ].map((collection) => (
          <a
            key={collection.path}
            href={`/ais${collection.path}`}
            className="block rounded-xl border border-line p-6 hover:bg-accent-bg"
          >
            <h2 className="text-title">{collection.title}</h2>
            <p className="mt-2 text-body text-fg-secondary">
              {collection.description}
            </p>
          </a>
        ))}
      </div>
    </DirectoryLayout>
  );
}
