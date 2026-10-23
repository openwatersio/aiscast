import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { CLASS_COLORS, shipClass, type ShipClass, type StationName, type StationStatus } from "../../lib/ais";
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

/**
 * A vessel's ship-type class, as the dot the map colours it with: from its kind and type, or the class itself. Gear is
 * the map's square, not a vessel's dot.
 */
export function ClassDot({ kind, type, cls }: { kind?: string; type?: number; cls?: ShipClass }) {
  const c = cls ?? shipClass(kind, type);
  // The colour is data, not a design token: it is the one inline style components may set.
  return <span className={cn("size-2.5 shrink-0", c === "gear" ? "rounded-[1px]" : "rounded-full")} style={{ background: CLASS_COLORS[c] }} aria-hidden />;
}

const STATUS_DOT: Record<StationStatus, { className: string; label: string }> = {
  live: { className: "fill-positive", label: "Live" },
  quiet: { className: "fill-caution", label: "Quiet" },
  offline: { className: "fill-fg-muted", label: "Offline" },
};

/**
 * Whether a station is sending, as a dot in the colors of the status chip over the map, ringed by a
 * gauge of its uptime: green for the share of hours it sent in, filled clockwise from the top, and
 * faint gray for the rest. `className` sizes it. Hidden from screen readers, which hear the status
 * and uptime beside it instead.
 */
export function StatusDot({ status, uptime, className = "size-5" }: { status: StationStatus; uptime?: number; className?: string }) {
  const { className: fill, label } = STATUS_DOT[status];
  const R = 8; // the gauge's radius in a 20-unit box; the dot inside is 3.5
  const C = 2 * Math.PI * R;
  // The tooltip is an attribute on a wrapper, not an SVG <title>, which would join the text of a heading.
  return (
    <span className="inline-flex shrink-0" title={uptime == null ? label : `${label} · ${uptime}% uptime`} aria-hidden>
      <svg viewBox="0 0 20 20" className={className}>
        {uptime != null && (
          <>
            <circle cx={10} cy={10} r={R} fill="none" strokeWidth={2} className="stroke-line" />
            <circle
              cx={10}
              cy={10}
              r={R}
              fill="none"
              strokeWidth={2}
              strokeDasharray={`${(uptime / 100) * C} ${C}`}
              transform="rotate(-90 10 10)"
              className="stroke-positive"
            />
          </>
        )}
        <circle cx={10} cy={10} r={uptime == null ? 5 : 3.5} className={fill} />
      </svg>
    </span>
  );
}

/**
 * A station's title, with the end of its id smaller and lighter after it: the id tells stations
 * apart but is not their name. `suffixClassName` sizes it for where the title sits.
 */
export function StationTitle({ name, suffixClassName = "text-subhead" }: { name: StationName; suffixClassName?: string }) {
  return (
    <>
      {name.title}
      {name.suffix && (
        <>
          {" "}
          <span className={cn("ml-0.5 font-normal text-fg-muted", suffixClassName)}>{name.suffix}</span>
        </>
      )}
    </>
  );
}

/** A circled icon, for rows that are destinations rather than things. */
export function IconBadge({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-accent-bg text-accent" aria-hidden>
      <Icon className="size-5" />
    </span>
  );
}
