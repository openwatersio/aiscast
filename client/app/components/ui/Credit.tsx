import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A photo's credit as a © in a circle, which opens to the whole credit on hover, or on a tap
 * where there is no hover, since it takes focus. The credit is in the page throughout, so a
 * screen reader reads it, and the licences' attribution is there wherever the photo is.
 * `focusable` is off inside a link, where a second focus stop would sit inside the first.
 */
export function Credit({ children, focusable = true, className }: { children: ReactNode; focusable?: boolean; className?: string }) {
  return (
    <span
      tabIndex={focusable ? 0 : undefined}
      className={cn(
        "group/credit pointer-events-auto inline-flex max-w-full min-w-0 items-center rounded-full bg-white/25 text-caption text-black/50 backdrop-blur-sm outline-none",
        className,
      )}
    >
      {/* The credit's own ©, which stays in sight. */}
      <span className="flex size-6 shrink-0 items-center justify-center">©</span>
      <span
        className={cn(
          "max-w-0 min-w-0 truncate opacity-0 transition-[max-width,opacity,padding] duration-200",
          "group-hover/credit:max-w-80 group-hover/credit:pr-2.5 group-hover/credit:opacity-100",
          "group-focus-within/credit:max-w-80 group-focus-within/credit:pr-2.5 group-focus-within/credit:opacity-100",
          "[&_a]:text-white [&_a]:no-underline [&_a:hover]:underline",
        )}
      >
        {" "}
        {children}
      </span>
    </span>
  );
}
