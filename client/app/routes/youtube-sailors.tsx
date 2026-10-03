import { YouTubeChannelDirectory } from "../components/YouTubeChannelDirectory";
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
    <YouTubeChannelDirectory
      category="Sailors"
      description="Find sailing channels and their boats, past and present."
      channels={sailingChannels}
    />
  );
}
