import { DirectoryLayout } from "../components/DirectoryLayout";
import { pageMeta } from "../lib/meta";
import { SAILORS_PATH } from "../lib/tracking";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "Vessel tracking directories | Open Waters AIS",
    description:
      "Browse collections of vessels and open their positions in the Open Waters AIS viewer.",
    path: "/tracking",
  });

export default function Tracking() {
  return (
    <DirectoryLayout>
      <h1 className="text-3xl font-semibold tracking-tight">Vessel tracking</h1>
      <p className="mt-3 text-body text-fg-secondary">
        Browse a collection, then open a vessel in the AIS viewer.
      </p>
      <a
        href={`/ais${SAILORS_PATH}`}
        className="mt-8 block max-w-xl rounded-xl border border-line p-6 hover:bg-accent-bg"
      >
        <h2 className="text-title">YouTube sailors</h2>
        <p className="mt-2 text-body text-fg-secondary">
          Nine sailing channels, their crews, and their boats past and present.
        </p>
      </a>
    </DirectoryLayout>
  );
}
