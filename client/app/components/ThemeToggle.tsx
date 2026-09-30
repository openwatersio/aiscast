import { Moon, Sun, SunMoon } from "lucide-react";
import type { ThemeChoice } from "../lib/theme";
import { useShell } from "./Shell";
import { IconButton } from "./ui/IconButton";
import { MenuItem } from "./ui/Menu";

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

/** One button that steps through the theme choices, showing the current one, in the header. */
export function ThemeButton() {
  const { theme, label, next } = useThemeCycle();
  return <IconButton icon={ICON[theme]} label={label} onClick={next} className="max-md:hidden" />;
}

/** The same, as an icon in the header's menu, for a phone's header with no room for it. */
export function ThemeMenuItem({ className }: { className?: string }) {
  const { theme, label, next } = useThemeCycle();
  const Icon = ICON[theme];
  return (
    <MenuItem onClick={next} closeOnClick={false} label={label} className={className}>
      <Icon className="size-4 text-fg-secondary" aria-hidden />
    </MenuItem>
  );
}
