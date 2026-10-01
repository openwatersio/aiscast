import { ChevronDown } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "../../lib/cn";
import { Menu, MenuRadioGroup, MenuRadioItem } from "./Menu";

/**
 * A row of chips that scrolls sideways when it outgrows the panel, fading where it clips.
 * Touches here scroll the row rather than drag the sheet.
 */
export function ChipRow({ children, className, label }: { children: ReactNode; className?: string; label: string }) {
  return (
    <div
      role="group"
      aria-label={label}
      data-sheet-ignore
      className={cn(
        "no-scrollbar flex snap-x scroll-px-3 gap-2 overflow-x-auto overscroll-x-contain px-3",
        "[mask-image:linear-gradient(to_right,transparent,black_0.75rem,black_calc(100%-0.75rem),transparent)]",
        className,
      )}
    >
      {children}
    </div>
  );
}

function chipClass(selected: boolean): string {
  return cn(
    "inline-flex shrink-0 snap-start items-center gap-1 rounded-full border px-3 py-1.5 text-subhead whitespace-nowrap transition-colors",
    selected ? "border-fg/25 bg-fg/15 text-fg" : "border-line text-fg-secondary hover:bg-surface-subtle hover:text-fg",
  );
}

/**
 * A chip that opens a menu of choices rather than toggling, and wears the chosen one's label.
 * It is tinted unless the choice is the first, which is the one that filters nothing.
 */
export function MenuChip<T>({
  value,
  options,
  onChange,
}: {
  value: T;
  /** `label` is the menu's; `chip`, the chip's while that choice holds. */
  options: Array<{ value: T; label: string; chip: string }>;
  onChange(value: T): void;
}) {
  const current = options.find((o) => o.value === value) ?? options[0]!;
  const selected = current !== options[0];
  return (
    <Menu trigger={<ChipButton selected={selected}>{current.chip}</ChipButton>}>
      <MenuRadioGroup value={value} onChange={onChange}>
        {options.map((o) => (
          <MenuRadioItem key={String(o.value)} value={o.value}>
            {o.label}
          </MenuRadioItem>
        ))}
      </MenuRadioGroup>
    </Menu>
  );
}

// The menu's trigger, which Base UI hands its props and ref to.
export function ChipButton({ selected, children, className, ...props }: { selected: boolean } & ComponentProps<"button">) {
  return (
    <button type="button" className={cn(chipClass(selected), className)} {...props}>
      {children}
      <ChevronDown className="size-3.5" aria-hidden />
    </button>
  );
}
