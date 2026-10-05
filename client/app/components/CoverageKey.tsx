import type { CoverageTiles } from "../lib/api";
import { COVERAGE_COLORS, COVERAGE_OPACITY, COVERAGE_STEPS, coverageDay } from "../lib/coverage";

/**
 * A step's color in the key, which carries its label in the theme's own text color. Over the
 * light map the map's strongest fill keeps that text above 4.5:1 on every step. Over the dark
 * one its lightest steps would be too close to the text at that strength, so the key draws all
 * four fainter there, still within the range the map fades its cells through.
 */
const KEY_DARK_OPACITY = 0.35;

function keyColor(i: number) {
  const mix = (color: string, opacity: number) => `color-mix(in srgb, ${color} ${opacity * 100}%, transparent)`;
  return `light-dark(${mix(COVERAGE_COLORS.light[i]!, COVERAGE_OPACITY.max)}, ${mix(COVERAGE_COLORS.dark[i]!, KEY_DARK_OPACITY)})`;
}

/**
 * The coverage map's key, a chip centered over the map while it shows coverage: one
 * segment per step of vessels a day, each labeled with the most it holds.
 */
export function CoverageKey({ coverage }: { coverage: CoverageTiles }) {
  const { from, to } = coverage.window;
  return (
    <div
      role="note"
      aria-label={`Coverage key, vessels heard a day: ${COVERAGE_STEPS.map((s) => s.spoken).join(", ")}`}
      title={`Vessel positions from ${coverageDay(from)} to ${coverageDay(to)}`}
      className="coverage-key-dock pane pointer-events-auto fixed z-10 flex h-9 items-center gap-2.5 rounded-full pr-1.5 pl-3.5 text-footnote font-medium whitespace-nowrap text-fg"
    >
      Vessels / day
      <div className="flex h-6 gap-0.5 text-caption font-semibold text-fg tabular-nums" aria-hidden>
        {COVERAGE_STEPS.map((step, i) => (
          <span
            key={step.label}
            className="flex min-w-8 items-center justify-center rounded-sm px-2 first:rounded-l-full last:rounded-r-full"
            // Data colors, one per theme, the one inline style components may set.
            style={{ background: keyColor(i) }}
          >
            {step.label}
          </span>
        ))}
      </div>
    </div>
  );
}
