import { useCallback, useState, useSyncExternalStore } from "react";

export type ThemeChoice = "dark" | "light" | "system";
export type Theme = "dark" | "light";

export const THEME_CHOICES: ThemeChoice[] = ["dark", "light", "system"];

// A cookie rather than localStorage: the Worker reads it and renders the choice into the
// first response, so the page never paints in the wrong scheme and then flips.
const COOKIE = "aiscast-theme";

/** The stored choice from a Cookie header. Dark when there is none. */
export function themeFromCookie(header: string | null): ThemeChoice {
  const m = header?.match(new RegExp(`(?:^|;\\s*)${COOKIE}=([a-z]+)`));
  const value = m?.[1];
  return THEME_CHOICES.includes(value as ThemeChoice) ? (value as ThemeChoice) : "dark";
}

const LIGHT_QUERY = "(prefers-color-scheme: light)";

function subscribeScheme(onChange: () => void) {
  const mq = window.matchMedia(LIGHT_QUERY);
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

/** What a choice comes to on this device. */
export function resolveTheme(choice: ThemeChoice): Theme {
  if (choice !== "system") return choice;
  return typeof window !== "undefined" && window.matchMedia(LIGHT_QUERY).matches ? "light" : "dark";
}

/**
 * The visitor's theme choice and what it resolves to. The server render only knows the
 * cookie, so System renders dark there and resolves on the device.
 */
export function useTheme(initial: ThemeChoice) {
  const [choice, setChoiceState] = useState(initial);
  const deviceLight = useSyncExternalStore(
    subscribeScheme,
    () => window.matchMedia(LIGHT_QUERY).matches,
    () => false,
  );
  const theme: Theme = choice === "system" ? (deviceLight ? "light" : "dark") : choice;

  const setChoice = useCallback((next: ThemeChoice) => {
    setChoiceState(next);
    document.documentElement.dataset.theme = next;
    document.cookie = `${COOKIE}=${next}; Path=/ais; Max-Age=31536000; SameSite=Lax`;
  }, []);

  return { choice, theme, setChoice };
}
