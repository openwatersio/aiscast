import { YouTubeChannelDirectory } from "../components/YouTubeChannelDirectory";
import { pageMeta } from "../lib/meta";
import { CRUISERS_PATH } from "../lib/explore";
import { cruisingChannels } from "../lib/cruisers";

export const handle = { directory: true };
export const meta = () =>
  pageMeta({
    title: "YouTube cruisers & their boats | Open Waters AIS",
    description:
      "Explore cruising YouTube channels, their trawlers and motor yachts, and map previews that open the Open Waters AIS viewer.",
    path: CRUISERS_PATH,
  });

export default function YoutubeCruisers() {
  return (
    <YouTubeChannelDirectory
      category="Cruisers"
      description="Meet cruising channels and explore their trawlers, motor yachts, and boats past and present."
      channels={cruisingChannels}
    />
  );
}
