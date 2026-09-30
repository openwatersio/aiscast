import { createContext, useCallback, useContext, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useLocation } from "react-router";
import { cn } from "../lib/cn";
import { PanelHeader } from "./ui/PanelHeader";

// Where each history entry was scrolled to, so going back lands where the reader left it.
const scrolled = new Map<string, number>();

const TitleContext = createContext<(el: HTMLElement | null) => void>(() => undefined);

/**
 * One entry on the panel's stack. `back` names its parent, for after a direct visit; `title`
 * is what the bar shows once the page's own PageTitle is out of sight.
 */
export function Panel({
  back,
  title,
  actions,
  header,
  hero,
  children,
}: {
  back?: string;
  title?: string;
  actions?: ReactNode;
  /** Stays put above the scrolling content, as the search field does. */
  header?: ReactNode;
  /** The page opens with a full-width image, such as a vessel's photo, and the bar floats over it. */
  hero?: boolean;
  children: ReactNode;
}) {
  const { key } = useLocation();
  const scroller = useRef<HTMLDivElement>(null);
  // Assumed visible until measured, so a server render never shows the title twice.
  const [largeTitleVisible, setLargeTitleVisible] = useState(true);

  useLayoutEffect(() => {
    if (scroller.current) scroller.current.scrollTop = scrolled.get(key) ?? 0;
  }, [key]);

  // Relative to the viewport, which counts the clipping of the sheet and of the scroller: the
  // large title is out of sight when scrolled away and when the sheet is too low to show it.
  // Only all of it counts as in sight; a lowered sheet can leave its top few pixels showing.
  const observeTitle = useCallback((el: HTMLElement | null) => {
    if (!el) return;
    const io = new IntersectionObserver(([entry]) => setLargeTitleVisible(entry!.intersectionRatio > 0.99), {
      threshold: [0, 0.99, 1],
    });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  return (
    <TitleContext.Provider value={observeTitle}>
      {/* A hero starts at the top edge of the sheet, under its grabber on a phone. */}
      <div className={cn("relative flex min-h-0 flex-1 flex-col", hero && "max-md:-mt-3")}>
        <PanelHeader back={back} title={title} showTitle={!largeTitleVisible} actions={actions} overlay={hero} />
        {header}
        <div
          ref={scroller}
          data-sheet-scroll
          onScroll={(e) => scrolled.set(key, e.currentTarget.scrollTop)}
          className={cn("sidebar-scroll min-h-0 flex-1 pb-3", header ? "px-2" : "px-4")}
        >
          {children}
        </div>
      </div>
    </TitleContext.Provider>
  );
}

/** A page's large title, which the bar stands in for once it is out of sight. */
export function PageTitle({ children, className }: { children: ReactNode; className?: string }) {
  const observe = useContext(TitleContext);
  return (
    <h1 ref={observe} className={cn("text-title text-fg", className)}>
      {children}
    </h1>
  );
}
