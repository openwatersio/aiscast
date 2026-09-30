import type { LucideIcon } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "../../lib/cn";

/** A row of the things to do with what a page shows, as under a place's name in a maps app. */
export function ActionRow({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 flex gap-2">{children}</div>;
}

/**
 * One action: an icon over a word, on a tile. `pressed` makes it a toggle, drawn filled while
 * it is on.
 */
export function ActionButton({
  icon: Icon,
  label,
  pressed,
  className,
  ...props
}: { icon: LucideIcon; label: string; pressed?: boolean } & Omit<ComponentProps<"button">, "children">) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      className={cn(
        "flex min-w-0 flex-1 flex-col items-center gap-1 rounded-lg bg-surface-tile px-2 py-2 text-caption font-medium text-accent transition-colors hover:bg-surface-subtle disabled:opacity-40 aria-pressed:bg-accent aria-pressed:text-surface",
        className,
      )}
      {...props}
    >
      <Icon className="size-5" aria-hidden />
      <span className="truncate">{label}</span>
    </button>
  );
}

/** An action that opens a page outside the app, drawn as an ActionButton. */
export function ActionLink({
  icon: Icon,
  label,
  className,
  ...props
}: { icon: LucideIcon; label: string } & Omit<ComponentProps<"a">, "children">) {
  return (
    <a
      className={cn(
        "flex min-w-0 flex-1 flex-col items-center gap-1 rounded-lg bg-surface-tile px-2 py-2 text-caption font-medium text-accent no-underline transition-colors hover:bg-surface-subtle",
        className,
      )}
      {...props}
    >
      <Icon className="size-5" aria-hidden />
      <span className="truncate">{label}</span>
    </a>
  );
}
