import { ChevronLeft, ChevronRight } from "lucide-react";
import { useRef, useState } from "react";
import type { Photo } from "../../lib/media";
import { cn } from "../../lib/cn";

// A scrim over the photo that reads on any of them, light or dark, in either theme.
const PILL = "pointer-events-auto rounded-full bg-black/55 px-2.5 py-1 text-caption text-white backdrop-blur-sm";

/**
 * Photographs in a row, one at a time, filling the element it is in: swipe on touch, arrows
 * with a mouse. Each carries its credit over it, which the licences require wherever the
 * photo shows.
 */
export function PhotoCarousel({
  photos,
  alt,
}: {
  photos: Photo[];
  /** What the photographs are of, for their alt text. */
  alt: string;
}) {
  const track = useRef<HTMLDivElement>(null);
  const [index, setIndex] = useState(0);
  // A file renamed or deleted on Commons leaves a dead thumbnail until the cache expires.
  const [broken, setBroken] = useState<Set<string>>(() => new Set());
  const shown = photos.filter((p) => !broken.has(p.thumb));
  if (!shown.length) return null;

  const go = (i: number) => {
    const el = track.current;
    if (el) el.scrollTo({ left: i * el.clientWidth, behavior: "smooth" });
  };
  const current = shown[Math.min(index, shown.length - 1)]!;

  return (
    <div className="group relative h-full overflow-hidden">
      {/* Native scrolling does the swiping, snapped a photo at a time. Touches that start
            here are the carousel's, not the sheet's. */}
      <div
        ref={track}
        data-sheet-ignore
        onScroll={(e) => {
          const el = e.currentTarget;
          const i = Math.round(el.scrollLeft / el.clientWidth);
          if (i !== index) setIndex(i);
        }}
        className="no-scrollbar flex h-full snap-x snap-mandatory overflow-x-auto overscroll-x-contain"
      >
        {shown.map((p, i) => (
          <img
            key={p.thumb}
            src={p.thumb}
            width={p.width}
            height={p.height}
            alt={p.description ?? `${alt}, photo ${i + 1}`}
            loading={i === 0 ? "eager" : "lazy"}
            decoding="async"
            draggable={false}
            onError={() => setBroken((b) => new Set(b).add(p.thumb))}
            className="h-full w-full shrink-0 snap-center object-cover"
          />
        ))}
      </div>
      {shown.length > 1 && (
        <>
          <Arrow side="left" hidden={index === 0} onClick={() => go(index - 1)} />
          <Arrow side="right" hidden={index >= shown.length - 1} onClick={() => go(index + 1)} />
        </>
      )}
      <div className="pointer-events-none absolute inset-x-2 bottom-2 flex items-end justify-between gap-2">
        <p className={cn(PILL, "min-w-0 truncate [&_a]:text-white [&_a]:no-underline [&_a:hover]:underline")}>
          ©{" "}
          <a href={current.page} target="_blank" rel="noopener">
            {current.artist}
          </a>{" "}
          ·{" "}
          {current.licenseUrl ? (
            <a href={current.licenseUrl} target="_blank" rel="noopener license">
              {current.license}
            </a>
          ) : (
            current.license
          )}
        </p>
        {shown.length > 1 && (
          <p className={cn(PILL, "shrink-0 tabular-nums")} aria-label={`Photo ${index + 1} of ${shown.length}`}>
            {Math.min(index, shown.length - 1) + 1} / {shown.length}
          </p>
        )}
      </div>
    </div>
  );
}

/** Shown on hover where there is a mouse; touch swipes instead. */
function Arrow({ side, hidden, onClick }: { side: "left" | "right"; hidden: boolean; onClick(): void }) {
  const Icon = side === "left" ? ChevronLeft : ChevronRight;
  return (
    <button
      type="button"
      aria-label={side === "left" ? "Previous photo" : "Next photo"}
      onClick={onClick}
      className={cn(
        "pane absolute top-1/2 flex size-8 -translate-y-1/2 items-center justify-center rounded-full text-fg opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100",
        side === "left" ? "left-2" : "right-2",
        hidden && "invisible",
      )}
    >
      <Icon className="size-4" aria-hidden />
    </button>
  );
}
