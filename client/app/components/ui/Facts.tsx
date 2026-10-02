import { Fragment, type ReactNode } from "react";
import { cn } from "../../lib/cn";

/** Label and value pairs. A pair whose value is absent is left out. */
export function Facts({ items, className }: { items: Array<[string, ReactNode | undefined]>; className?: string }) {
  return (
    <dl className={cn("grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-body", className)}>
      {items
        .filter(([, value]) => value != null && value !== "")
        .map(([label, value]) => (
          <Fragment key={label}>
            <dt className="font-medium text-fg-secondary">{label}</dt>
            <dd className="m-0 min-w-0 truncate text-fg tabular-nums">{value}</dd>
          </Fragment>
        ))}
    </dl>
  );
}
