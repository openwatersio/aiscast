import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";
import { CONTRIBUTE, CONTRIBUTE_PROMPT } from "../../lib/links";

/**
 * An invitation to do something beyond the page, such as running a receiver or using the
 * data: an icon, a sentence or two, and the link. Quiet enough to sit at the foot of a page
 * without competing with what the reader came for.
 */
export function Prompt({
  icon: Icon,
  children,
  href,
  action,
  className,
}: {
  icon: LucideIcon;
  children: ReactNode;
  href: string;
  action: string;
  className?: string;
}) {
  return (
    <aside className={cn("flex gap-3 rounded-lg bg-surface-tile p-3", className)}>
      <Icon className="mt-0.5 size-5 shrink-0 text-accent" aria-hidden />
      <div className="min-w-0 text-subhead text-fg-secondary">
        {children}{" "}
        <a href={href} className="font-medium whitespace-nowrap">
          {action} →
        </a>
      </div>
    </aside>
  );
}

/**
 * The ask to add a receiver, as one line on an inset tile, in the page's own ink rather than the
 * accent so it sits under what the reader came for. `href` and `children` fit it to a case, such
 * as the owner of a boat.
 */
export function ReceiverLink({
  href = CONTRIBUTE,
  children = CONTRIBUTE_PROMPT,
  className,
}: {
  href?: string;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <a
      href={href}
      className={cn(
        "block rounded-lg bg-surface-tile px-3 py-2.5 text-subhead text-fg-secondary no-underline transition-colors hover:bg-surface-subtle hover:text-fg",
        className,
      )}
    >
      {children} →
    </a>
  );
}
