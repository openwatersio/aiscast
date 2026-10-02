import { useEffect, useLayoutEffect, useRef, type ReactNode } from "react";

export type Detent = "peek" | "half" | "full";

// Kept in step with .sheet in app.css, which positions the sheet before any script runs.
const GAP = 60; // the header and the space under it, above a full sheet: --sheet-gap in app.css
const PEEK = 76; // the grabber and the search field, above the home indicator

const PHONE = "(max-width: 767px)";

const NEXT: Record<Detent, Detent> = { peek: "half", half: "full", full: "peek" };

/**
 * How far the sheet is pushed down, at rest, for a detent. `safeBottom` is the home
 * indicator's inset, which script cannot read from env() directly, so it comes from the
 * sheet's own padding.
 */
function restOffset(detent: Detent, safeBottom: number): number {
  const height = window.innerHeight - GAP;
  const visible = detent === "peek" ? PEEK + safeBottom : detent === "half" ? window.innerHeight / 2 : height;
  return Math.max(0, height - visible);
}

function safeBottom(el: Element): number {
  return parseFloat(getComputedStyle(el).paddingBottom) || 0;
}

/** Resistance past either end, so a drag there moves less the further it goes. */
function rubber(distance: number): number {
  return Math.sqrt(distance) * 4;
}

/**
 * The panel. On a phone it is a bottom sheet with three heights, dragged the way a maps app's
 * sheet is; on a wider screen it is a panel floating down the left edge. Either way it is part
 * of the page, not a dialog, so the server renders it and its content like any other element.
 *
 * Content scrolls only at full height. Below that, a drag anywhere moves the sheet, and at
 * full height a drag down with the content at its top does too. Touch listeners rather than
 * pointer events decide this, because only a touchmove can still cancel native scrolling once
 * the gesture has begun.
 */
export function Sheet({
  detent,
  onDetentChange,
  children,
}: {
  detent: Detent;
  onDetentChange(detent: Detent): void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLElement>(null);
  const current = useRef(detent);
  current.current = detent;
  const change = useRef(onDetentChange);
  change.current = onDetentChange;

  // Where the map should treat the sheet's top as being: its resting place, not wherever a
  // transition has it this frame. The map reads this when it next moves the camera.
  useLayoutEffect(() => {
    const el = ref.current!;
    const phone = window.matchMedia(PHONE);
    const mark = () => {
      if (phone.matches) el.dataset.restTop = String(GAP + restOffset(detent, safeBottom(el)));
      else delete el.dataset.restTop;
    };
    mark();
    window.addEventListener("resize", mark);
    return () => window.removeEventListener("resize", mark);
  }, [detent]);

  // The software keyboard covers the bottom of the screen without resizing the layout, and iOS
  // pans the page to keep the focused field above it. The pan is decided when the field takes
  // focus, which in a peeking sheet is at the bottom of the screen; the sheet then rises to
  // full height and carries the field off the top of the panned view. So while a field in
  // the sheet has focus the page is put back at the top, where the field now is, and the
  // content is padded by what the keyboard covers so its end can still be scrolled to.
  useEffect(() => {
    const el = ref.current!;
    const vv = window.visualViewport;
    if (!vv) return;
    const sync = () => {
      const covered = Math.max(0, window.innerHeight - vv.height - vv.offsetTop);
      document.documentElement.style.setProperty("--keyboard-inset", `${Math.round(covered)}px`);
      if (el.contains(document.activeElement) && (window.scrollY !== 0 || vv.offsetTop !== 0)) {
        window.scrollTo(0, 0);
      }
    };
    vv.addEventListener("resize", sync);
    vv.addEventListener("scroll", sync);
    el.addEventListener("transitionend", sync);
    return () => {
      vv.removeEventListener("resize", sync);
      vv.removeEventListener("scroll", sync);
      el.removeEventListener("transitionend", sync);
    };
  }, []);

  useEffect(() => {
    const el = ref.current!;
    const phone = window.matchMedia(PHONE);
    let gesture:
      | { startY: number; startOffset: number; mode?: "drag" | "scroll"; lastY: number; lastT: number; velocity: number; offset: number }
      | undefined;

    const offsetNow = () => new DOMMatrixReadOnly(getComputedStyle(el).transform).m42;

    const start = (e: TouchEvent) => {
      if (!phone.matches || e.touches.length !== 1) return;
      // Content that takes a drag for itself, such as the track chart's scrubber.
      if ((e.target as Element).closest("[data-sheet-ignore]")) return;
      const y = e.touches[0]!.clientY;
      const offset = offsetNow();
      gesture = { startY: y, startOffset: offset, lastY: y, lastT: e.timeStamp, velocity: 0, offset };
    };

    const move = (e: TouchEvent) => {
      if (!gesture) return;
      const y = e.touches[0]!.clientY;
      const dy = y - gesture.startY;
      if (!gesture.mode) {
        if (dy === 0) return;
        // Decided on the first movement: after that, a scroll the browser has begun can no
        // longer be cancelled.
        const scroller = (e.target as Element).closest<HTMLElement>("[data-sheet-scroll]");
        const scrolls =
          current.current === "full" && scroller != null && !(dy > 0 && scroller.scrollTop <= 0);
        gesture.mode = scrolls ? "scroll" : "drag";
        if (gesture.mode === "drag") el.dataset.dragging = "";
        // Moving the sheet or its results means done typing, as in a maps app.
        const active = document.activeElement;
        if (active instanceof HTMLInputElement && el.contains(active)) active.blur();
      }
      if (gesture.mode !== "drag") return;
      if (e.cancelable) e.preventDefault();
      const max = restOffset("peek", safeBottom(el));
      let offset = gesture.startOffset + dy;
      if (offset < 0) offset = -rubber(-offset);
      if (offset > max) offset = max + rubber(offset - max);
      gesture.offset = offset;
      gesture.velocity = (y - gesture.lastY) / Math.max(1, e.timeStamp - gesture.lastT);
      gesture.lastY = y;
      gesture.lastT = e.timeStamp;
      el.style.setProperty("--sheet-drag", `${offset}px`);
    };

    const end = () => {
      const g = gesture;
      gesture = undefined;
      if (g?.mode !== "drag") return;
      // Where the flick would carry the sheet, so a fast short swipe still changes detent.
      const projected = g.offset + g.velocity * 200;
      const detents: Detent[] = ["full", "half", "peek"];
      const inset = safeBottom(el);
      const next = detents.reduce((best, d) =>
        Math.abs(restOffset(d, inset) - projected) < Math.abs(restOffset(best, inset) - projected) ? d : best,
      );
      delete el.dataset.dragging;
      el.style.removeProperty("--sheet-drag");
      change.current(next);
    };

    el.addEventListener("touchstart", start, { passive: true });
    el.addEventListener("touchmove", move, { passive: false });
    el.addEventListener("touchend", end);
    el.addEventListener("touchcancel", end);
    return () => {
      el.removeEventListener("touchstart", start);
      el.removeEventListener("touchmove", move);
      el.removeEventListener("touchend", end);
      el.removeEventListener("touchcancel", end);
    };
  }, []);

  return (
    <section ref={ref} data-map-inset data-detent={detent} aria-label="Panel" className="sheet pane flex flex-col overflow-hidden rounded-2xl">
      {/* Tapping the handle steps up through the heights and wraps back to the lowest. */}
      <button
        type="button"
        className="sheet-grabber"
        aria-label={`Panel height: ${detent}. Change to ${NEXT[detent]}.`}
        onClick={() => onDetentChange(NEXT[detent])}
      />
      {children}
    </section>
  );
}
