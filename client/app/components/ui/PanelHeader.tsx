import { ChevronLeft } from "lucide-react";
import type { ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { cn } from "../../lib/cn";
import { IconLink } from "./IconButton";

/**
 * History state on a vessel opened from the map over another vessel. Vessels tapped one after
 * another are browsing, not descending, so Back skips them all and returns to the page they
 * were opened over; the browser's own back still steps through each. `backSteps` is how far
 * back that page is. Null means it is not in history at all, because the first of them was a
 * direct visit, and Back goes to the parent instead.
 */
export interface StackState {
  backSteps?: number | null;
}

/**
 * Whether the page showing is the one the app loaded on, with nothing of the app's behind it.
 * React Router numbers history entries from 0 at each page load and keeps the number across
 * a reload. Browser only.
 */
function isFirstEntry(): boolean {
  return ((window.history.state as { idx?: number } | null)?.idx ?? 0) === 0;
}

/** The state for a vessel opened from the map while `current` is showing. Browser only. */
export function stackStateFor(current: { state: unknown }, currentIsVessel: boolean): StackState | undefined {
  if (!currentIsVessel) return undefined;
  const steps = (current.state as StackState | null)?.backSteps;
  if (steps === null || (steps === undefined && isFirstEntry())) return { backSteps: null };
  return { backSteps: (steps ?? 1) + 1 };
}

/**
 * The bar at the top of a stack entry: the way back, the entry's title, and whatever acts on
 * the entry. It floats over the top of the page, which scrolls under it. The title shows only
 * while the page's own large title is out of sight, scrolled away or below a lowered sheet, as
 * a navigation bar's does in iOS, and what scrolls under the bar fades out with it.
 */
export function PanelHeader({
  back,
  title,
  showTitle,
  actions,
  overPhoto,
}: {
  back?: string;
  title?: string;
  showTitle: boolean;
  actions?: ReactNode;
  /** The page opens with a photo under the bar, whose buttons then need a scrim of their own. */
  overPhoto?: boolean;
}) {
  if (!back && !title && !actions) return null;
  return (
    <div
      data-over-photo={(overPhoto && !showTitle) || undefined}
      className={cn(
        // No ground of its own: once the title shows, Panel fades out what scrolls under it.
        "pointer-events-none absolute inset-x-0 top-0 z-10 flex items-center gap-1 px-3 pt-3 *:pointer-events-auto",
        // Over a photo the buttons need their own scrim, as the photo's credit has.
        "data-over-photo:*:[a]:bg-black/45 data-over-photo:*:[a]:text-white data-over-photo:*:[a]:backdrop-blur-sm data-over-photo:*:[a]:hover:bg-black/60",
      )}
    >
      {back && <BackButton parent={back} />}
      <span
        className={cn(
          "min-w-0 flex-1 truncate px-1 text-headline text-fg transition-opacity duration-300",
          showTitle ? "opacity-100" : "opacity-0",
        )}
        aria-hidden={!showTitle}
      >
        {title}
      </span>
      {actions}
    </div>
  );
}

/**
 * Within the app it steps back through history to the page this entry was opened over, as
 * many steps as StackState says, and one otherwise. After a direct visit there is nothing to
 * return to, and it goes to `parent` instead.
 */
function BackButton({ parent }: { parent: string }) {
  const navigate = useNavigate();
  const location = useLocation();
  return (
    <IconLink
      icon={ChevronLeft}
      label="Back"
      to={parent}
      onClick={(e) => {
        const steps = (location.state as StackState | null)?.backSteps;
        const back = steps === null ? undefined : (steps ?? (isFirstEntry() ? undefined : 1));
        if (back === undefined) return;
        e.preventDefault();
        navigate(-back);
      }}
    />
  );
}
