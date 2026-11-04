import { trackSlackwaterHandoff } from "../lib/slackwater";
import { Tile } from "./ui/Section";

export function SlackwaterCard() {
  return (
    <aside className="mt-5" aria-label="Slackwater">
      <Tile>
        <h2 className="text-headline font-semibold text-fg">Riding this ferry?</h2>
        <p className="mt-1 text-subhead text-fg-secondary">Slackwater shows today's tides and currents for the coast around you.</p>
        <a
          href="https://slackwater.xyz/?ref=ais-ferry"
          target="_blank"
          rel="noopener noreferrer"
          className="mt-1 inline-flex min-h-11 items-center text-subhead font-medium"
          onClick={trackSlackwaterHandoff}
          onAuxClick={trackSlackwaterHandoff}
        >Get Slackwater</a>
      </Tile>
    </aside>
  );
}
