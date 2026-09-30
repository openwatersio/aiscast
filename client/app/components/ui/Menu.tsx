import { Menu as BaseMenu } from "@base-ui/react/menu";
import type { ReactElement, ReactNode } from "react";

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
          <BaseMenu.Popup className="pane min-w-40 p-1.5 outline-none">{children}</BaseMenu.Popup>
        </BaseMenu.Positioner>
      </BaseMenu.Portal>
    </BaseMenu.Root>
  );
}

export function MenuItem({ onClick, children }: { onClick(): void; children: ReactNode }) {
  return (
    <BaseMenu.Item className={ITEM} onClick={onClick}>
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

/** A line of explanation under a menu's items. */
export function MenuNote({ children }: { children: ReactNode }) {
  return <p className="mt-1 border-t border-line px-2 pt-1.5 text-caption text-fg-muted">{children}</p>;
}
