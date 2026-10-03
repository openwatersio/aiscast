import "maplibre-gl/dist/maplibre-gl.css";
import { useEffect } from "react";
import { isRouteErrorResponse, Links, Meta, Outlet, Scripts, ScrollRestoration, useMatches, useRouteLoaderData } from "react-router";
import type { Route } from "./+types/root";
import "./app.css";
import { Shell } from "./components/Shell";
import { setPublicApi } from "./lib/api";
import { serverEnv } from "./lib/context";
import { installErrorReporting, reportError } from "./lib/report";
import { themeFromCookie } from "./lib/theme";

// Here rather than in an effect, so an error during hydration is heard.
installErrorReporting();

export const links: Route.LinksFunction = () => [
  { rel: "preconnect", href: "https://tiles.openfreemap.org", crossOrigin: "anonymous" },
  // The logo's rings, as the header draws them. From public/ais/assets, unhashed, because the
  // manifest names the icons by path.
  { rel: "icon", type: "image/svg+xml", href: "/ais/assets/icon.svg" },
  { rel: "apple-touch-icon", href: "/ais/assets/icon-180.png" },
  { rel: "manifest", href: "/ais/assets/manifest.webmanifest" },
];

/**
 * The API the server rendered against is the one the browser should use. The theme choice
 * comes from its cookie so the first response is already in it. Nothing here is about the
 * visitor beyond that, so a page can be kept at the edge (workers/app.ts).
 */
export function loader({ context, request }: Route.LoaderArgs) {
  return {
    api: context.get(serverEnv).api,
    theme: themeFromCookie(request.headers.get("cookie")),
  };
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
    <html lang="en" data-theme={data?.theme ?? "system"} suppressHydrationWarning>
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="apple-mobile-web-app-title" content="Open Waters AIS" />
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
  const directory = useMatches().some((match) => (match.handle as { directory?: boolean } | undefined)?.directory);
  setPublicApi(loaderData.api);
  if (directory) return <Outlet />;
  return <Shell initialTheme={loaderData.theme} />;
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  const notFound = isRouteErrorResponse(error) && error.status === 404;
  useEffect(() => {
    if (!isRouteErrorResponse(error)) reportError("boundary", error);
  }, [error]);
  return (
    <main className="p-6">
      <h1 className="text-title text-fg">{notFound ? "Not found" : "Something went wrong"}</h1>
      <p className="mt-2 text-body text-fg-secondary">
        {notFound ? "There is nothing at this address." : "The page could not be shown."} Try the{" "}
        <a href="/ais/vessels">map</a>.
      </p>
      {import.meta.env.DEV && error instanceof Error && <pre className="mt-4 text-xs">{error.stack}</pre>}
    </main>
  );
}
