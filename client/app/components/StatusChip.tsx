import { cn } from "../lib/cn";
import { useLive, useNow, useStreamFrame } from "../lib/live";

/**
 * What the map is showing, in a word, over its top-left corner. A dot says whether it is
 * live: the stream is drawing this area as it hears it, or the tiles are standing in.
 */
export function StatusChip() {
  const live = useLive();
  useStreamFrame();
  // The map's state and the overview change without a stream frame.
  useNow(1000);

  let tone: "positive" | "caution" | "idle" = "idle";
  let text = "Connecting";
  let detail: string | undefined;
  if (live) {
    const map = live.ctl.status();
    const { state, eventsPerSec } = live.stream;
    if (map.state === "error") {
      tone = "caution";
      text = "Map unavailable";
      detail = map.detail;
    } else if (live.ctl.mode() === "overview") {
      text = "Overview";
      detail = "Zoom in for live positions. This view refreshes every 15 seconds.";
    } else if (state === "live") {
      tone = "positive";
      text = "Live";
      detail = `${eventsPerSec} messages a second in view`;
    } else if (state === "refused") {
      tone = "caution";
      text = "Live in another tab";
      detail = "This network's live streams are in use elsewhere. Positions refresh from the tiles instead.";
    } else if (state === "capped") {
      text = "Zoom in for live";
    } else if (state === "reconnecting") {
      tone = "caution";
      text = "Reconnecting";
    }
  }

  return (
    <div
      role="status"
      title={detail}
      className="status-dock pane pointer-events-auto fixed z-10 flex h-9 items-center gap-2 rounded-full px-3.5 text-footnote font-medium text-fg"
    >
      <span
        aria-hidden
        className={cn(
          "size-2.5 rounded-full",
          tone === "positive" ? "bg-positive" : tone === "caution" ? "bg-caution" : "bg-fg-muted",
        )}
      />
      {text}
    </div>
  );
}
