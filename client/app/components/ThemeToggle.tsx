import { Moon, Sun, SunMoon } from "lucide-react";
import type { ThemeChoice } from "../lib/theme";
import { useShell } from "./Shell";
import { IconButton } from "./ui/IconButton";

const NEXT: Record<ThemeChoice, ThemeChoice> = { dark: "light", light: "system", system: "dark" };
const ICON = { dark: Moon, light: Sun, system: SunMoon };
const NAME = { dark: "Dark", light: "Light", system: "System" };

function useThemeCycle() {
  const { theme, setTheme } = useShell();
  return {
    theme,
    label: `Appearance: ${NAME[theme]}. Switch to ${NAME[NEXT[theme]]}.`,
    next: () => setTheme(NEXT[theme]),
  };
}

/** One button that steps through the theme choices, showing the current one. Phones only. */
export function ThemeToggle() {
  const { theme, label, next } = useThemeCycle();
  return <IconButton icon={ICON[theme]} label={label} onClick={next} className="md:hidden" />;
}

/** The same, as a round button over the map's top-right corner. Wide screens only. */
export function ThemeChip() {
  const { theme, label, next } = useThemeCycle();
  const Icon = ICON[theme];
  return (
    <button
      type="button"
      onClick={next}
      aria-label={label}
      title={label}
      className="pane pointer-events-auto fixed top-3 right-3 z-10 hidden size-9 items-center justify-center rounded-full text-fg transition-colors hover:bg-surface-subtle md:flex"
    >
      <Icon className="size-4" aria-hidden />
    </button>
  );
}
