import { slackwaterHandoff } from "../lib/slackwater";
import { Tile } from "./ui/Section";

/**
 * Every vessel page offers Slackwater; a ferry's speaks to someone aboard it. The link
 * carries no position, so the copy offers the reader's coast rather than the vessel's.
 */
export function SlackwaterCard({ ferry }: { ferry: boolean }) {
  const context = ferry ? "ferry" : "vessel";
  const track = slackwaterHandoff(context);
  return (
    <aside className="mt-5" aria-label="Slackwater">
      <Tile>
        <h2 className="text-headline font-semibold text-fg">{ferry ? "Riding this ferry?" : "Heading out on the water?"}</h2>
        <p className="mt-1 text-subhead text-fg-secondary">Slackwater shows today's tides and currents for the coast around you.</p>
        <a
          href={`https://slackwater.xyz/?ref=ais-${context}`}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-1 inline-flex min-h-11 items-center text-subhead font-medium"
          onClick={track}
          onAuxClick={track}
        >Get Slackwater</a>
      </Tile>
    </aside>
  );
}
