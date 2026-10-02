import { isRouteErrorResponse } from "react-router";
import { pageMeta } from "../lib/meta";
import { PageTitle, Panel } from "./Panel";

const isNotFound = (error: unknown) => isRouteErrorResponse(error) && error.status === 404;

/** The head of a page that could not load: not found, or unavailable. */
export function routeErrorMeta(error: unknown) {
  return isNotFound(error)
    ? pageMeta({ title: "Not found | Open Waters AIS", description: "There is nothing at this address.", path: "/vessels", noindex: true })
    : pageMeta({ title: "Unavailable | Open Waters AIS", description: "The AIS network did not answer.", path: "/vessels", noindex: true });
}

/** A thrown response's headers, such as a 503's Retry-After, on the document. */
export function routeErrorHeaders({ errorHeaders }: { errorHeaders?: Headers }): Headers {
  return errorHeaders ?? new Headers();
}

/**
 * A page that could not load, in the panel, so the map and the rest of the app stay put.
 * A 404 is an address with nothing at it; anything else, most often the API being
 * unavailable, is worth trying again.
 */
export function RouteError({ error }: { error: unknown }) {
  const notFound = isNotFound(error);
  return (
    <Panel back="/vessels" title={notFound ? "Not found" : "Unavailable"}>
      <PageTitle className="text-large-title">{notFound ? "Not found" : "Unavailable"}</PageTitle>
      <p className="mt-2 text-body text-fg-secondary">
        {notFound ? "There is nothing at this address." : "The AIS network did not answer. Try again in a minute."}
      </p>
    </Panel>
  );
}
