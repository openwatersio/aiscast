import { data, Link } from "react-router";
import { Panel } from "../components/Shell";
import { pageMeta } from "../lib/meta";

// Rendered inside the app rather than thrown to the root boundary, so the map stays up.
export function loader() {
  return data(null, { status: 404 });
}

export function clientLoader() {
  return null;
}

export const meta = () =>
  pageMeta({ title: "Not found | Open Waters AIS", description: "No page at this address.", path: "/map", noindex: true });

export default function NotFound() {
  return (
    <Panel back="/map">
      <h1 className="text-xl font-semibold" style={{ color: "var(--text)" }}>
        Not found
      </h1>
      <p className="mt-2 text-sm" style={{ color: "var(--text-secondary)" }}>
        There is nothing at this address. Try the <Link to="/map">map</Link>, or search for a vessel by name or MMSI.
      </p>
    </Panel>
  );
}
