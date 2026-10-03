import { Moon, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { useRouteLoaderData } from "react-router";
import type { loader } from "../root";
import { useTheme } from "../lib/theme";
import { Logo } from "./ui/Logo";
import { IconButton } from "./ui/IconButton";

export function DirectoryLayout({ children }: { children: ReactNode }) {
  const data = useRouteLoaderData<typeof loader>("root");
  const { theme, setChoice } = useTheme(data?.theme ?? "system");
  return (
    <div className="h-full overflow-y-auto bg-surface">
      <header className="border-b border-line px-4 py-4 md:px-8">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-3">
          <a
            href="/ais/"
            className="flex items-center gap-2 text-headline font-semibold text-fg hover:text-fg"
          >
            <Logo className="size-6" /> Open Waters AIS
          </a>
          <nav
            aria-label="AIS"
            className="ml-auto flex items-center gap-4 text-subhead"
          >
            <a href="/ais/explore/youtube">YouTube directory</a>
            <a href="/ais/vessels">Open AIS viewer</a>
            <IconButton
              icon={theme === "dark" ? Sun : Moon}
              label={`Switch to ${theme === "dark" ? "light" : "dark"} appearance`}
              onClick={() => setChoice(theme === "dark" ? "light" : "dark")}
            />
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-12">
        {children}
      </main>
      <footer className="mx-auto max-w-6xl border-t border-line px-4 py-6 text-footnote text-fg-muted md:px-8">
        Positions from <a href="/ais/">aiscast, the Open Waters AIS network</a>.
        Reports depend on receiver coverage. A position may be older than the
        vessel’s last heard time.
      </footer>
    </div>
  );
}
