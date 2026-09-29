import { useCallback, useEffect, useRef, useState } from "react";
import { browserAuth, getTrack } from "../lib/api";
import { useLive } from "../lib/live";

// The server keeps a rolling window and clamps anything longer, reporting what it actually
// covered. A range past that returns the same positions as the window itself, so the menu
// says which ones the archive does not reach yet rather than offering identical views.
const WINDOW_HOURS = 48;
const SLIDER_STEPS = 1000;

const RANGES = [
  { hours: 48, label: "Last 2 days" },
  { hours: 24 * 7, label: "Last week" },
  { hours: 24 * 30, label: "Last month" },
  { hours: 24 * 365, label: "Last year" },
];

/** What the vessel pane says about the track this bar loaded. */
export interface TrackSummary {
  mmsi: number;
  points: number;
  from?: number;
}

interface Loaded {
  coords: Array<[number, number]>;
  times: number[];
}

const EMPTY: Loaded = { coords: [], times: [] };

function formatMoment(at: number | undefined): string {
  return at
    ? new Date(at).toLocaleString("en-GB", {
        day: "numeric",
        month: "short",
        hour: "2-digit",
        minute: "2-digit",
        timeZone: "UTC",
      })
    : "—";
}

/** The days the rolling window reaches, newest first. */
function windowDays(now: number): Array<{ label: string; endsAt: number }> {
  const days: Array<{ label: string; endsAt: number }> = [];
  for (let back = 0; back * 24 < WINDOW_HOURS + 24; back++) {
    const day = new Date(now - back * 86_400_000);
    const endsAt = Math.min(Date.parse(`${day.toISOString().slice(0, 10)}T23:59:59Z`), now);
    if (now - endsAt > WINDOW_HOURS * 3600e3) break;
    days.push({
      label:
        back === 0
          ? "Today"
          : back === 1
            ? "Yesterday"
            : day.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" }),
      endsAt,
    });
  }
  return days;
}

/**
 * Docked over the map whenever a vessel is open. It fetches its own track: it needs the
 * times for the scrubber, it refetches when the range changes, and running in the browser
 * lets it send this browser's token, which raises the server's cap from 200 positions to 1,000.
 * Keyed by MMSI, so a different vessel starts over at the default range.
 */
export function TrackBar({
  mmsi,
  onLoaded,
}: {
  mmsi: number;
  onLoaded(summary: TrackSummary | undefined): void;
}) {
  const live = useLive();
  const [hours, setHours] = useState(48);
  const [endingAt, setEndingAt] = useState<number>();
  const [loaded, setLoaded] = useState<Loaded>(EMPTY);
  const [value, setValue] = useState(SLIDER_STEPS);
  const [playing, setPlaying] = useState(false);
  const [menu, setMenu] = useState<"range" | "day" | undefined>();
  const bar = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!live) return;
    let current = true;
    const end = endingAt ?? Date.now();
    void getTrack(browserAuth(), mmsi, { from: end - hours * 3600e3, to: end, limit: 1000 }).then((t) => {
      // A failed fetch leaves the track as it was.
      if (!current || !t) return;
      const coords = t.geometry?.coordinates ?? [];
      const times = t.properties.times.map((x) => Date.parse(x));
      setLoaded({ coords, times });
      setValue(SLIDER_STEPS);
      live.ctl.setTrack(coords, times[times.length - 1] ?? 0, times);
      live.ctl.scrubTo(null);
      onLoaded({ mmsi, points: coords.length, from: times[0] });
    });
    return () => {
      current = false;
    };
  }, [live, mmsi, hours, endingAt, onLoaded]);

  useEffect(() => () => live?.ctl.scrubTo(null), [live]);

  const { times } = loaded;
  const momentAt = useCallback(
    (v: number) => (times.length ? times[0]! + (v / SLIDER_STEPS) * (times[times.length - 1]! - times[0]!) : undefined),
    [times],
  );

  /**
   * The slider runs over time, not over the count of positions. A vessel can report every
   * thirty seconds for an hour and then go unheard for fourteen, and stepping by position
   * would spend the whole bar on the busy hour and one step on the silence.
   */
  const apply = useCallback(
    (v: number) => {
      setValue(v);
      const at = momentAt(v);
      // At the end, hand the map back to live: the line should follow the vessel again.
      if (at !== undefined) live?.ctl.scrubTo(v >= SLIDER_STEPS ? null : at);
    },
    [live, momentAt],
  );

  const valueRef = useRef(value);
  valueRef.current = value;
  useEffect(() => {
    if (!playing) return;
    const t = setInterval(() => {
      const next = valueRef.current + SLIDER_STEPS / 240;
      if (next > SLIDER_STEPS) setPlaying(false);
      else apply(next);
    }, 90);
    return () => clearInterval(t);
  }, [playing, apply]);

  // Menus close on any click outside them.
  useEffect(() => {
    if (!menu) return;
    const close = (e: MouseEvent) => {
      if (!bar.current?.contains(e.target as Node)) setMenu(undefined);
    };
    document.addEventListener("click", close);
    return () => document.removeEventListener("click", close);
  }, [menu]);

  if (loaded.coords.length < 2) return null;

  function togglePlay() {
    if (playing) return setPlaying(false);
    if (value >= SLIDER_STEPS) apply(0);
    // Replay is useless off-screen, and a track can run well outside the current view.
    live?.ctl.fitTrack();
    setPlaying(true);
  }

  const buttonClass = "flex size-8 shrink-0 items-center justify-center rounded-full hover:bg-[var(--surface-subtle)]";
  const menuItem =
    "flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-sm hover:bg-[var(--surface-subtle)]";

  return (
    <div
      ref={bar}
      className="pointer-events-auto fixed bottom-3 z-10 hidden w-[min(560px,calc(100%-var(--map-left,0px)-1.5rem))] md:block"
      style={{ left: "var(--map-left, 0px)", right: 0, marginInline: "auto" }}
    >
      <div className="pane flex items-center gap-2 rounded-full px-2 py-1.5">
        <div className="relative">
          <button
            type="button"
            className={buttonClass}
            style={{ color: "var(--text-secondary)" }}
            aria-label="Choose a time range"
            aria-haspopup="true"
            aria-expanded={menu === "range"}
            onClick={() => setMenu(menu === "range" ? undefined : "range")}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="size-5">
              <path d="M3 12a9 9 0 1 0 3-6.7L3 8" />
              <path d="M3 3v5h5" />
              <path d="M12 7v5l3 2" />
            </svg>
          </button>
          {menu === "range" && (
            <div className="pane absolute bottom-10 left-0 w-56 p-1.5" role="menu">
              {RANGES.map((r) => {
                const beyond = r.hours > WINDOW_HOURS;
                return (
                  <button
                    key={r.hours}
                    type="button"
                    className={menuItem}
                    style={{ color: r.hours === hours ? "var(--accent)" : "var(--text)", opacity: beyond ? 0.55 : 1 }}
                    role="menuitem"
                    onClick={() => {
                      setHours(r.hours);
                      setPlaying(false);
                      setMenu(undefined);
                    }}
                  >
                    <span>{r.label}</span>
                    <span className="text-[10px]" style={{ color: "var(--text-muted)" }}>
                      {beyond ? `${WINDOW_HOURS} h available` : ""}
                    </span>
                  </button>
                );
              })}
            </div>
          )}
        </div>

        <button
          type="button"
          className={buttonClass}
          style={{ color: "var(--text-secondary)" }}
          aria-label={playing ? "Pause" : "Play"}
          onClick={togglePlay}
        >
          <svg viewBox="0 0 24 24" fill="currentColor" className="size-4">
            <path d={playing ? "M6 5h4v14H6zM14 5h4v14h-4z" : "M8 5v14l11-7z"} />
          </svg>
        </button>

        <input
          type="range"
          data-scrub
          min={0}
          max={SLIDER_STEPS}
          value={value}
          onChange={(e) => {
            setPlaying(false);
            apply(Number(e.target.value));
          }}
          className="h-1 min-w-0 flex-1 cursor-pointer appearance-none rounded-full"
          style={{ backgroundColor: "var(--border)" }}
          aria-label="Position in track"
        />

        <div className="relative shrink-0">
          <button
            type="button"
            className="w-[8.5rem] rounded-md px-1 py-0.5 text-right text-xs tabular-nums hover:bg-[var(--surface-subtle)]"
            style={{ color: "var(--text-secondary)" }}
            aria-haspopup="true"
            aria-expanded={menu === "day"}
            onClick={() => setMenu(menu === "day" ? undefined : "day")}
          >
            {formatMoment(momentAt(value))}
          </button>
          {/* Our own list rather than <input type="date">: the native picker anchors to its
              input and opens downward, which from a bar pinned to the bottom lands off-screen.
              It also offers every date in history when the server holds a rolling window. */}
          {menu === "day" && (
            <div className="pane absolute right-0 bottom-10 w-40 p-1.5" role="menu">
              {windowDays(Date.now()).map((d) => (
                <button
                  key={d.endsAt}
                  type="button"
                  className={menuItem}
                  style={{ color: "var(--text)" }}
                  onClick={() => {
                    setEndingAt(d.endsAt);
                    setPlaying(false);
                    setMenu(undefined);
                  }}
                >
                  {d.label}
                </button>
              ))}
              <p className="mt-1 border-t px-2 pt-1.5 text-[10px]" style={{ color: "var(--text-muted)" }}>
                {WINDOW_HOURS} hours of history available
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
