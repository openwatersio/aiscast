import { ChevronDown, Pause, Play } from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { speedAt, TRACK_GAP_MS } from "../lib/ais";
import { publicApiBase } from "../lib/api";
import { useLive } from "../lib/live";
import { TRACK_WINDOW_HOURS, type LoadedTrack } from "../lib/useTrack";
import { IconButton } from "./ui/IconButton";
import { Menu, MenuRadioGroup, MenuRadioItem } from "./ui/Menu";
import { Section } from "./ui/Section";

const RANGES = [6, 12, 24, 48].filter((h) => h <= TRACK_WINDOW_HOURS);

// Replay covers the whole track in about this many steps, one every PLAY_TICK_MS.
const PLAY_STEPS = 240;
const PLAY_TICK_MS = 90;

const utcTime = (t: number) => new Date(t).toISOString().slice(11, 16);

/**
 * The Track section: the vessel's speed over a chosen range, which is also how its past
 * positions are replayed on the map. Pointing at a moment on the chart puts the vessel where
 * it was then; letting go hands the map back to the live position.
 */
export function VesselTrack({
  mmsi,
  track,
  loading,
  hours,
  onHoursChange,
}: {
  mmsi: number;
  track: LoadedTrack | undefined;
  loading: boolean;
  hours: number;
  onHoursChange(hours: number): void;
}) {
  const live = useLive();
  const [scrubAt, setScrubAt] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);

  // The map draws the track and, while scrubbing, the vessel at the moment being shown.
  useEffect(() => {
    if (!live || !track) return;
    live.ctl.setTrack(track.coords, track.times[track.times.length - 1] ?? 0, track.times);
  }, [live, track]);
  useEffect(() => live?.ctl.scrubTo(scrubAt), [live, scrubAt]);
  useEffect(() => () => live?.ctl.scrubTo(null), [live]);

  const first = track?.times[0];
  const last = track?.times[track.times.length - 1];
  const at = useRef(scrubAt);
  at.current = scrubAt;
  useEffect(() => {
    if (!playing || first == null || last == null) return;
    const step = (last - first) / PLAY_STEPS;
    const tick = setInterval(() => {
      const next = (at.current ?? first) + step;
      if (next >= last) {
        setPlaying(false);
        setScrubAt(null);
      } else setScrubAt(next);
    }, PLAY_TICK_MS);
    return () => clearInterval(tick);
  }, [playing, first, last]);

  const speeds = (track?.sog ?? []).filter((s): s is number => s != null);
  const avg = speeds.length ? speeds.reduce((a, b) => a + b, 0) / speeds.length : undefined;
  const max = speeds.length ? Math.max(...speeds) : undefined;
  const points = track?.coords.length ?? 0;

  return (
    <Section
      label="Track"
      aside={
        <Menu
          side="bottom"
          align="end"
          trigger={
            <button type="button" className="inline-flex items-center gap-0.5 rounded-md text-fg-muted hover:text-fg">
              Last {hours} hours
              <ChevronDown className="size-3.5" aria-hidden />
            </button>
          }
        >
          <MenuRadioGroup
            value={hours}
            onChange={(h: number) => {
              setPlaying(false);
              setScrubAt(null);
              onHoursChange(h);
            }}
          >
            {RANGES.map((h) => (
              <MenuRadioItem key={h} value={h}>
                Last {h} hours
              </MenuRadioItem>
            ))}
          </MenuRadioGroup>
        </Menu>
      }
    >
      <div className="flex items-center gap-2">
        <IconButton
          icon={playing ? Pause : Play}
          label={playing ? "Pause" : "Replay the track"}
          small
          disabled={points < 2}
          onClick={() => {
            if (playing) return setPlaying(false);
            // Replay is useless off-screen, and a track can run well outside the view.
            live?.ctl.fitTrack();
            setPlaying(true);
          }}
        />
        {/* The moment being scrubbed takes the summary's place, as a stock chart shows the
            value under the finger in its header. */}
        <span className="min-w-0 flex-1 text-footnote text-fg-muted" aria-live="polite">
          {track && scrubAt != null ? (
            <>
              <span className="font-semibold text-fg tabular-nums">{utcTime(scrubAt)} UTC</span>
              {speedAt(track, scrubAt) != null && (
                <>
                  {" · "}
                  <span className="font-semibold text-fg tabular-nums">{speedAt(track, scrubAt)!.toFixed(1)} kn</span>
                </>
              )}
            </>
          ) : avg != null && max != null ? (
            <>
              Avg <span className="font-semibold text-fg tabular-nums">{avg.toFixed(1)} kn</span> · Max{" "}
              <span className="font-semibold text-fg tabular-nums">{max.toFixed(1)} kn</span>
            </>
          ) : null}
        </span>
      </div>

      {track && points > 1 ? (
        <TrackChart
          track={track}
          scrubAt={scrubAt}
          onScrub={(t) => {
            setPlaying(false);
            // Starting to scrub frames the whole route, so every moment on the chart is on
            // the map; moving along it, the map holds still.
            if (at.current === null && t !== null) live?.ctl.fitTrack();
            setScrubAt(t);
          }}
        />
      ) : (
        <p className="py-8 text-center text-body text-fg-muted">
          {loading ? "Loading…" : `Not heard in the last ${hours} hours.`}
        </p>
      )}

      <div className="mt-1 flex items-baseline justify-between text-footnote text-fg-muted">
        <span>Times in UTC</span>
        <a href={`${publicApiBase()}/v1/vessels/${mmsi}/track?format=gpx`} download={`${mmsi}.gpx`}>
          Download GPX
        </a>
      </div>
    </Section>
  );
}

const HEIGHT = 128;
const PAD = { top: 10, right: 6, bottom: 20, left: 24 };

/** Gridline spacing for speeds up to `top`: at most four lines, on a round step. */
function scale(top: number): { max: number; step: number } {
  let step = top <= 10 ? 2 : top <= 25 ? 5 : 10;
  let max = Math.max(step, Math.ceil(top / step) * step);
  if (max / step > 4) {
    step *= 2;
    max = Math.ceil(top / step) * step;
  }
  return { max, step };
}

/**
 * Speed over time as a line over a soft fill, broken wherever the vessel went unheard for
 * longer than TRACK_GAP_MS, since joining across a silence draws speeds nobody reported.
 * The chart is also the scrubber: a mouse scrubs by hovering, a finger by pressing and
 * dragging, and the keyboard with the arrow keys.
 */
function TrackChart({
  track,
  scrubAt,
  onScrub,
}: {
  track: LoadedTrack;
  scrubAt: number | null;
  onScrub(at: number | null): void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const pressed = useRef(false);
  const gradient = useId();

  useLayoutEffect(() => {
    const el = box.current!;
    const observer = new ResizeObserver(() => setWidth(el.clientWidth));
    observer.observe(el);
    setWidth(el.clientWidth);
    return () => observer.disconnect();
  }, []);

  const { from, to, times, sog } = track;
  const first = times[0]!;
  const last = times[times.length - 1]!;
  const innerW = Math.max(0, width - PAD.left - PAD.right);
  const innerH = HEIGHT - PAD.top - PAD.bottom;
  const { max, step } = scale(Math.max(1, ...sog.filter((s): s is number => s != null)));
  const x = (t: number) => PAD.left + ((t - from) / (to - from)) * innerW;
  const y = (v: number) => PAD.top + innerH - (v / max) * innerH;
  const base = PAD.top + innerH;

  const segments: Array<Array<[number, number]>> = [];
  let run: Array<[number, number]> = [];
  times.forEach((t, i) => {
    const v = sog[i];
    if (v == null || (i > 0 && t - times[i - 1]! > TRACK_GAP_MS)) {
      if (run.length > 1) segments.push(run);
      run = [];
    }
    if (v != null) run.push([x(t), y(v)]);
  });
  if (run.length > 1) segments.push(run);
  const line = segments.map((s) => `M${s.map((p) => p.join(",")).join("L")}`).join("");
  const area = segments
    .map((s) => `M${s[0]![0]},${base}L${s.map((p) => p.join(",")).join("L")}L${s[s.length - 1]![0]},${base}Z`)
    .join("");

  const gridlines: number[] = [];
  for (let v = 0; v <= max; v += step) gridlines.push(v);
  const ticks = [0, 1, 2, 3].map((i) => from + ((to - from) * i) / 3);

  /** The moment under a pointer, kept within the stretch the track covers. */
  const timeAt = (clientX: number) => {
    const rect = box.current!.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (clientX - rect.left - PAD.left) / innerW));
    return Math.min(last, Math.max(first, from + frac * (to - from)));
  };

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    pressed.current = true;
    e.currentTarget.setPointerCapture(e.pointerId);
    onScrub(timeAt(e.clientX));
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse" || pressed.current) onScrub(timeAt(e.clientX));
  };
  const release = (e: PointerEvent<HTMLDivElement>) => {
    pressed.current = false;
    // A finger lifted is done looking; a mouse keeps scrubbing until it leaves the chart.
    if (e.pointerType !== "mouse") onScrub(null);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const stepMs = (last - first) / 100;
    const now = scrubAt ?? last;
    const next =
      e.key === "ArrowLeft" ? now - stepMs : e.key === "ArrowRight" ? now + stepMs : e.key === "Home" ? first : e.key === "End" ? last : undefined;
    if (e.key === "Escape") onScrub(null);
    else if (next !== undefined) {
      e.preventDefault();
      onScrub(Math.min(last, Math.max(first, next)));
    }
  };

  const speed = scrubAt != null ? speedAt(track, scrubAt) : undefined;
  const readout = scrubAt != null ? `${utcTime(scrubAt)}${speed != null ? ` · ${speed.toFixed(1)} kn` : ""}` : undefined;
  const cursorX = scrubAt != null ? x(scrubAt) : undefined;

  return (
    <div
      ref={box}
      // A finger on the chart scrubs; it must not also drag the sheet or scroll the panel.
      data-sheet-ignore
      role="slider"
      tabIndex={0}
      aria-label="Position along the track"
      aria-valuemin={first}
      aria-valuemax={last}
      aria-valuenow={scrubAt ?? last}
      aria-valuetext={readout ?? "Now"}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={release}
      onPointerCancel={(e) => (release(e), onScrub(null))}
      onPointerLeave={(e) => e.pointerType === "mouse" && !pressed.current && onScrub(null)}
      onKeyDown={onKeyDown}
      onBlur={() => onScrub(null)}
      className="relative mt-2 cursor-crosshair touch-none rounded-md outline-none select-none focus-visible:ring-2 focus-visible:ring-accent"
    >
      <svg width={width} height={HEIGHT} className="block overflow-visible text-accent" aria-hidden>
        <defs>
          <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity="0.35" />
            <stop offset="100%" stopColor="currentColor" stopOpacity="0.02" />
          </linearGradient>
        </defs>
        {gridlines.map((v) => (
          <g key={v}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(v)} y2={y(v)} className="stroke-line-subtle" />
            <text x={PAD.left - 6} y={y(v)} dy="0.32em" textAnchor="end" className="fill-fg-muted text-caption">
              {v}
            </text>
          </g>
        ))}
        {ticks.map((t, i) => (
          <text
            key={t}
            x={x(t)}
            y={HEIGHT - 4}
            textAnchor={i === 0 ? "start" : i === ticks.length - 1 ? "end" : "middle"}
            className="fill-fg-muted text-caption"
          >
            {utcTime(t)}
          </text>
        ))}
        <path d={area} fill={`url(#${gradient})`} />
        <path d={line} fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinejoin="round" strokeLinecap="round" />
        {cursorX != null && (
          <>
            <line x1={cursorX} x2={cursorX} y1={PAD.top} y2={base} className="stroke-fg-muted" strokeDasharray="2 3" />
            {speed != null && <circle cx={cursorX} cy={y(speed)} r={4} fill="currentColor" className="stroke-surface" strokeWidth={2} />}
          </>
        )}
      </svg>
    </div>
  );
}
