import { Moon, Sun, SunMoon } from "lucide-react";
import type { ThemeChoice } from "../lib/theme";
import { useShell } from "./Shell";

const NEXT: Record<ThemeChoice, ThemeChoice> = { dark: "light", light: "system", system: "dark" };
const ICON = { dark: Moon, light: Sun, system: SunMoon };
const NAME = { dark: "Dark", light: "Light", system: "System" };

/** One button that steps through the theme choices, showing the current one. */
export function ThemeToggle() {
  const { theme, setTheme } = useShell();
  const Icon = ICON[theme];
  const label = `Appearance: ${NAME[theme]}. Switch to ${NAME[NEXT[theme]]}.`;
  return (
    <button
      type="button"
      onClick={() => setTheme(NEXT[theme])}
      aria-label={label}
      title={label}
      className="flex size-7 shrink-0 items-center justify-center rounded-full text-fg-muted transition-colors hover:bg-surface-subtle hover:text-fg"
    >
      <Icon className="size-4" aria-hidden />
    </button>
  );
}
