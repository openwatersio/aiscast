import { Link, redirect } from "react-router";
import { SearchBox, SearchResults } from "../components/Search";
import { FleetCard, usePhotos } from "../components/FleetCard";
import { Panel } from "../components/Panel";
import { Section } from "../components/ui/Section";
import { coverPhoto, fleetCard, getFleet } from "../lib/fleets.server";
import { namedPhotos } from "../lib/media.server";
import { pageMeta } from "../lib/meta";
import type { Route } from "./+types/home";

// `?station=<id>` was how the first viewer opened a station, and volunteers have shared
// those links. The station id is a permalink people are told to hand out.
function stationRedirect(request: Request) {
  const station = new URL(request.url).searchParams.get("station");
  if (station) throw redirect(`/stations/${station}`, 301);
}

export function loader({ request }: Route.LoaderArgs) {
  stationRedirect(request);
  // Two square cards, then a wide one. A fleet that is renamed drops out rather than failing the map.
  const fleets = ["cruise-ships", "superyachts", "tall-ships"].flatMap((id) => getFleet(id) ?? []);
  return {
    fleets: fleets.map(fleetCard),
    photos: namedPhotos(fleets.flatMap((f) => coverPhoto(f) ?? []), request.url),
  };
}

type HomeData = Awaited<ReturnType<typeof loader>>;
let asked: Promise<HomeData> | undefined;

// The fleets are fixed for a build, so the Worker is asked once a session and returning to the
// map costs nothing after that. Without an answer, such as offline or from an older build, the
// map opens without them and the next visit asks again.
export function clientLoader({ request, serverLoader }: Route.ClientLoaderArgs): Promise<HomeData> {
  stationRedirect(request);
  asked ??= serverLoader()
    .then((d) => (d?.fleets ? d : Promise.reject(new Error("No fleets"))))
    .catch(() => {
      asked = undefined;
      return { fleets: [], photos: Promise.resolve({}) };
    });
  return asked;
}

export const meta = () =>
  pageMeta({
    title: "Live AIS vessel traffic | Open Waters AIS",
    description:
      "Live AIS vessel positions from open government feeds and volunteer receivers. Free, open, no account.",
    path: "/vessels",
  });

export default function Home({ loaderData }: Route.ComponentProps) {
  const photos = usePhotos(loaderData.photos);
  const [a, b, wide] = loaderData.fleets;
  return (
    <Panel header={<SearchBox />}>
      <SearchResults>
        {a && (
          <Section label="Fleets" aside={<Link to="/fleets">Browse all</Link>} bare className="mt-6">
            <div className="space-y-3">
              <div className="grid grid-cols-2 gap-3">
                {[a, b].map((c) => c && <FleetCard key={c.path} card={c} photos={photos} />)}
              </div>
              {wide && <FleetCard card={wide} photos={photos} featured />}
            </div>
          </Section>
        )}
      </SearchResults>
    </Panel>
  );
}
