import { redirect } from "react-router";
import { SearchBox, SearchResults } from "../components/Search";
import { Panel } from "../components/Shell";
import { pageMeta } from "../lib/meta";

// `?station=<id>` was how the first viewer opened a station, and volunteers have shared
// those links. The station id is a permalink people are told to hand out.
function stationRedirect(request: Request) {
  const station = new URL(request.url).searchParams.get("station");
  if (station) throw redirect(`/stations/${station}`, 301);
  return null;
}

export function loader({ request }: { request: Request }) {
  return stationRedirect(request);
}

export function clientLoader({ request }: { request: Request }) {
  return stationRedirect(request);
}

export const meta = () =>
  pageMeta({
    title: "Live AIS vessel traffic | Open Waters AIS",
    description:
      "Live AIS vessel positions from open government feeds and volunteer receivers. Free, open, no account.",
    path: "/map",
  });

export default function Home() {
  return (
    <Panel list header={<SearchBox />}>
      <SearchResults />
    </Panel>
  );
}
