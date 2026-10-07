import { Menu as BaseMenu } from "@base-ui/react/menu";
import type { ReactElement, ReactNode } from "react";
import { cn } from "../../lib/cn";

const ITEM =
  "flex w-full cursor-default items-center justify-between gap-3 rounded-md px-2 py-1.5 text-left text-body text-fg outline-none select-none data-highlighted:bg-surface-subtle data-disabled:opacity-50";

/**
 * A menu opened from `trigger`. Base UI does the behaviour: keyboard navigation, focus
 * return, closing on an outside press, and keeping the popup on screen.
 */
export function Menu({
  trigger,
  side = "bottom",
  align = "start",
  children,
}: {
  trigger: ReactElement;
  side?: "top" | "bottom";
  align?: "start" | "end";
  children: ReactNode;
}) {
  return (
    <BaseMenu.Root>
      <BaseMenu.Trigger render={trigger} />
      <BaseMenu.Portal>
        <BaseMenu.Positioner side={side} align={align} sideOffset={8} className="z-50">
          {/* Base UI measures the room left on screen, so a long menu on a short phone scrolls. */}
          <BaseMenu.Popup className="pane max-h-available min-w-40 overflow-y-auto p-1.5 outline-none">{children}</BaseMenu.Popup>
        </BaseMenu.Positioner>
      </BaseMenu.Portal>
    </BaseMenu.Root>
  );
}

export function MenuItem({
  onClick,
  closeOnClick = true,
  label,
  className,
  children,
}: {
  onClick(): void;
  /** Off for an item that answers in place, such as one that says it copied a link. */
  closeOnClick?: boolean;
  /** The accessible name, for an item that is only an icon. */
  label?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <BaseMenu.Item
      className={cn(ITEM, className)}
      onClick={onClick}
      closeOnClick={closeOnClick}
      aria-label={label}
      title={label}
      label={label}
    >
      {children}
    </BaseMenu.Item>
  );
}

/** One choice of several. The chosen one is drawn in the accent colour. */
export function MenuRadioGroup<T>({ value, onChange, children }: { value: T; onChange(value: T): void; children: ReactNode }) {
  return (
    <BaseMenu.RadioGroup value={value} onValueChange={(v: T) => onChange(v)}>
      {children}
    </BaseMenu.RadioGroup>
  );
}

export function MenuRadioItem({ value, children, hint }: { value: unknown; children: ReactNode; hint?: ReactNode }) {
  return (
    <BaseMenu.RadioItem value={value} closeOnClick className={`${ITEM} data-checked:text-accent`}>
      <span>{children}</span>
      {hint != null && <span className="text-caption text-fg-muted">{hint}</span>}
    </BaseMenu.RadioItem>
  );
}

/** A link to a page outside the app, such as the website's. */
export function MenuLinkItem({
  href,
  label,
  className,
  children,
}: {
  href: string;
  /** The accessible name, for a link that is only an icon. */
  label?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <BaseMenu.LinkItem
      href={href}
      closeOnClick
      aria-label={label}
      title={label}
      label={label}
      className={cn(ITEM, "no-underline", className)}
    >
      {children}
    </BaseMenu.LinkItem>
  );
}

/** A line of explanation under a menu's items. */
export function MenuNote({ children }: { children: ReactNode }) {
  return <p className="mt-1 border-t border-line px-2 pt-1.5 text-caption text-fg-muted">{children}</p>;
}
