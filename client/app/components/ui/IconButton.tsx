import type { LucideIcon } from "lucide-react";
import type { ComponentProps } from "react";
import { Link } from "react-router";
import { cn } from "../../lib/cn";

/** A round, icon-only control. The target is 32px, or 28px when `small`. */
export function iconButtonClass(small = false, className?: string): string {
  return cn(
    "inline-flex shrink-0 items-center justify-center rounded-full text-fg-secondary no-underline transition-colors hover:bg-surface-subtle hover:text-fg disabled:pointer-events-none disabled:opacity-40",
    small ? "size-7" : "size-8",
    className,
  );
}

/** Icon-only buttons still need a name; `label` is both the accessible name and the tooltip. */
export function IconButton({
  icon: Icon,
  label,
  small,
  className,
  ...props
}: { icon: LucideIcon; label: string; small?: boolean } & Omit<ComponentProps<"button">, "children">) {
  return (
    <button type="button" aria-label={label} title={label} className={iconButtonClass(small, className)} {...props}>
      <Icon className={small ? "size-4" : "size-5"} aria-hidden />
    </button>
  );
}

export function IconLink({
  icon: Icon,
  label,
  small,
  className,
  ...props
}: { icon: LucideIcon; label: string; small?: boolean } & Omit<ComponentProps<typeof Link>, "children">) {
  return (
    <Link aria-label={label} title={label} className={iconButtonClass(small, className)} {...props}>
      <Icon className={small ? "size-4" : "size-5"} aria-hidden />
    </Link>
  );
}
