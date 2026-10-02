import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { CLASS_COLORS, shipClass } from "../../lib/ais";
import { cn } from "../../lib/cn";

export function List({ children, className }: { children: ReactNode; className?: string }) {
  return <ul className={cn("space-y-0.5", className)}>{children}</ul>;
}

/**
 * One row of a list: an optional mark on the left, a title and a second line, and something
 * short on the right. A row with `to` is a link within the app, one with `href` leaves it.
 */
export function ListRow({
  to,
  href,
  leading,
  title,
  subtitle,
  trailing,
  onHover,
}: {
  to?: string;
  href?: string;
  leading?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  trailing?: ReactNode;
  /** The pointer entering the row, and leaving it. */
  onHover?(over: boolean): void;
}) {
  const body = (
    <>
      {leading}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-headline text-fg">{title}</span>
        {subtitle != null && <span className="block truncate text-subhead text-fg-muted">{subtitle}</span>}
      </span>
      {trailing != null && <span className="shrink-0 text-footnote text-fg-muted tabular-nums">{trailing}</span>}
    </>
  );
  const className = "flex items-center gap-3 rounded-lg px-2 py-2 no-underline transition-colors hover:bg-surface-subtle";
  return (
    <li onPointerEnter={onHover && (() => onHover(true))} onPointerLeave={onHover && (() => onHover(false))}>
      {to ? (
        <Link to={to} className={className}>
          {body}
        </Link>
      ) : (
        <a href={href} className={className}>
          {body}
        </a>
      )}
    </li>
  );
}

/** A vessel's ship-type class, as the dot the map colours it with. */
export function ClassDot({ kind, type }: { kind?: string; type?: number }) {
  const color = CLASS_COLORS[shipClass(kind, type)];
  // The colour is data, not a design token: it is the one inline style components may set.
  return <span className="size-2.5 shrink-0 rounded-full" style={{ background: color }} aria-hidden />;
}

/** A circled icon, for rows that are destinations rather than things. */
export function IconBadge({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-accent-bg text-accent" aria-hidden>
      <Icon className="size-5" />
    </span>
  );
}
