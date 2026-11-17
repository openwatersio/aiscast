import { slackwaterHandoff, type NearbyStation } from "../lib/slackwater";
import { Tile } from "./ui/Section";

const outbound = { target: "_blank", rel: "noopener noreferrer" } as const;

/**
 * Every vessel page offers Slackwater; a ferry's speaks to someone aboard it. The app link
 * carries no position, so its copy offers the reader's coast rather than the vessel's. The
 * station pages near the vessel's last position, from the server render or asked of the Worker by
 * a page opened inside the app (useNearbyStations), are linked by name
 * at their own addresses, so a crawler that reaches the vessel reaches them too. A name and a link
 * copy none of a station's data, so its attribution notice is left to the station page
 * (docs/policy.md, the Slackwater row).
 */
export function SlackwaterCard({ ferry, stations = [] }: { ferry: boolean; stations?: NearbyStation[] }) {
  const context = ferry ? "ferry" : "vessel";
  const track = slackwaterHandoff(context);
  return (
    <aside className="mt-5" aria-label="Slackwater">
      <Tile>
        <h2 className="text-headline font-semibold text-fg">{ferry ? "Riding this ferry?" : "Heading out on the water?"}</h2>
        <p className="mt-1 text-subhead text-fg-secondary">Slackwater shows today's tides and currents for the coast around you.</p>
        {stations.length > 0 && (
          <>
            <p className="mt-2 text-subhead text-fg-secondary">Near its last position:</p>
            <ul>
              {stations.map((s) => (
                <li key={s.url}>
                  {/* 32 px rows read as a list; at 44 px each line sat in its own band of space. */}
                  <a href={s.url} {...outbound} className="flex min-h-8 w-fit items-center text-subhead" onClick={track} onAuxClick={track}>
                    {/* Kind first: a NOAA current's name can end in its bearing, "Ediz Hook Light, 1.0 nm N of". */}
                    {s.kind === "tide" ? "Tides" : "Currents"}: {s.name}
                  </a>
                </li>
              ))}
            </ul>
          </>
        )}
        <a
          href={`https://slackwater.xyz/?ref=ais-${context}`}
          {...outbound}
          className="mt-1 inline-flex min-h-11 items-center text-subhead font-medium"
          onClick={track}
          onAuxClick={track}
        >Get Slackwater</a>
      </Tile>
    </aside>
  );
}
