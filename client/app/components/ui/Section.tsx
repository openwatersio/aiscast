import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A labelled group in a panel, in the inset-grouped style: a caption above, something short
 * that qualifies it on the right, and the content on a tile. Every section has this shape,
 * so a panel reads as one list of labelled things.
 */
export function Section({
  label,
  aside,
  children,
  className,
  bare = false,
}: {
  label: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Content that brings its own tiles, such as a StatGrid of tiles. */
  bare?: boolean;
}) {
  return (
    <section className={cn("mt-5", className)}>
      <div className="flex items-baseline justify-between gap-2 px-0.5">
        <h2 className="text-caption font-medium text-fg-muted uppercase">{label}</h2>
        {aside != null && <div className="text-footnote text-fg-muted tabular-nums first-letter:uppercase">{aside}</div>}
      </div>
      {bare ? <div className="mt-1.5">{children}</div> : <Tile className="mt-1.5">{children}</Tile>}
    </section>
  );
}

/** The surface a section's content sits on. */
export function Tile({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("rounded-lg bg-surface-tile p-3", className)}>{children}</div>;
}
