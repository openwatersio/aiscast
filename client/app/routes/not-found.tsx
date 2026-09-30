import { data, Link } from "react-router";
import { PageTitle, Panel } from "../components/Panel";
import { pageMeta } from "../lib/meta";

// Rendered inside the app rather than thrown to the root boundary, so the map stays up.
export function loader() {
  return data(null, { status: 404 });
}

export function clientLoader() {
  return null;
}

export const meta = () =>
  pageMeta({ title: "Not found | Open Waters AIS", description: "No page at this address.", path: "/vessels", noindex: true });

export default function NotFound() {
  return (
    <Panel back="/vessels" title="Not found">
      <PageTitle>Not found</PageTitle>
      <p className="mt-2 text-body text-fg-secondary">
        There is nothing at this address. Try the <Link to="/vessels">map</Link>, or search for a vessel by name or MMSI.
      </p>
    </Panel>
  );
}
