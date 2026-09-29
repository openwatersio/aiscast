import "maplibre-gl/dist/maplibre-gl.css";
import { isRouteErrorResponse, Links, Meta, Scripts, ScrollRestoration, useRouteLoaderData } from "react-router";
import type { Route } from "./+types/root";
import "./app.css";
import { Shell } from "./components/Shell";
import { setPublicApi } from "./lib/api";
import { serverEnv } from "./lib/context";
import { themeFromCookie } from "./lib/theme";

export const links: Route.LinksFunction = () => [
  { rel: "preconnect", href: "https://tiles.openfreemap.org", crossOrigin: "anonymous" },
];

/**
 * The API the server rendered against is the one the browser should use. The theme choice
 * comes from its cookie so the first response is already in it.
 */
export function loader({ context, request }: Route.LoaderArgs) {
  return { api: context.get(serverEnv).api, theme: themeFromCookie(request.headers.get("cookie")) };
}

// Nothing here changes after the first load, and revalidating it would send every
// navigation back through the Worker.
export function shouldRevalidate() {
  return false;
}

export function Layout({ children }: { children: React.ReactNode }) {
  const data = useRouteLoaderData<typeof loader>("root");
  return (
    // The theme switcher writes data-theme directly, after hydration.
    <html lang="en" data-theme={data?.theme ?? "dark"} suppressHydrationWarning>
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <Meta />
        <Links />
      </head>
      <body className="h-full">
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App({ loaderData }: Route.ComponentProps) {
  setPublicApi(loaderData.api);
  return <Shell initialTheme={loaderData.theme} />;
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  const notFound = isRouteErrorResponse(error) && error.status === 404;
  return (
    <main className="p-6">
      <h1 className="text-xl font-semibold">{notFound ? "Not found" : "Something went wrong"}</h1>
      <p className="mt-2 text-sm" style={{ color: "var(--text-secondary)" }}>
        {notFound ? "There is nothing at this address." : "The page could not be shown."} Try the{" "}
        <a href="/ais/map">map</a>.
      </p>
      {import.meta.env.DEV && error instanceof Error && <pre className="mt-4 text-xs">{error.stack}</pre>}
    </main>
  );
}
