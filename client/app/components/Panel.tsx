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
  const hasBar = Boolean(back || title || actions);
  const scroller = useRef<HTMLDivElement>(null);
  // Assumed visible until measured, so a server render never shows the title twice.
  const [largeTitleVisible, setLargeTitleVisible] = useState(true);

  useLayoutEffect(() => {
    if (scroller.current) scroller.current.scrollTop = scrolled.get(key) ?? 0;
  }, [key]);

  // The large title is out of sight when the sheet is too low to show it, which only the
  // viewport sees, and when it scrolls under the bar's buttons, which only the scroller less
  // the bar sees. Only all of it counts as in sight; a lowered sheet can leave its top few
  // pixels showing.
  const observeTitle = useCallback(
    (el: HTMLElement | null) => {
      if (!el) return;
      const inView = { viewport: true, scroller: true };
      const watch = (key: keyof typeof inView, options: IntersectionObserverInit) => {
        const io = new IntersectionObserver(
          ([entry]) => {
            inView[key] = entry!.intersectionRatio > 0.99;
            setLargeTitleVisible(inView.viewport && inView.scroller);
          },
          { threshold: [0, 0.99, 1], ...options },
        );
        io.observe(el);
        return io;
      };
      const observers = [
        watch("viewport", {}),
        // The bar's buttons and the space above them, as pt-11 below.
        watch("scroller", { root: el.closest("[data-sheet-scroll]"), rootMargin: hasBar ? "-44px 0px 0px 0px" : "0px" }),
      ];
      return () => observers.forEach((io) => io.disconnect());
    },
    [hasBar],
  );

  return (
    <TitleContext.Provider value={observeTitle}>
      {/* A hero starts at the top edge of the sheet, under its grabber on a phone. */}
      <div className={cn("relative flex min-h-0 flex-1 flex-col", hero && "max-md:-mt-3")}>
        <PanelHeader back={back} title={title} showTitle={!largeTitleVisible} actions={actions} overPhoto={hero} />
        {header}
        <div
          ref={scroller}
          data-sheet-scroll
          onScroll={(e) => scrolled.set(key, e.currentTarget.scrollTop)}
          className={cn(
            "sidebar-scroll min-h-0 flex-1 pb-3",
            header ? "px-2" : "px-4",
            // The bar floats over the page. A photo starts under it; anything else starts below
            // it, the height of its buttons and the space above them.
            hasBar && !hero && "pt-11",
            // Once the bar shows the title, what is under it fades out so the title reads on the glass.
            hasBar && "bar-fade",
            hasBar && !largeTitleVisible && "[--bar-fade:0]",
          )}
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
