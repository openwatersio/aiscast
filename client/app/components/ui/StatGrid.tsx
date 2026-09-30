import { cn } from "../../lib/cn";
import { Tile } from "./Section";

export interface Stat {
  label: string;
  /** Absent reads as a dash: the thing is not reported, which is worth saying. */
  value?: string;
  /** A unit word takes a leading non-breaking space; a degree sign sits tight. */
  unit?: string;
}

/** Figures read at a glance rather than looked up, in a row of three. */
export function StatGrid({ stats, tiles = false }: { stats: Stat[]; tiles?: boolean }) {
  const cells = stats.map((s) => (
    <div key={s.label} className={cn("text-center", tiles && "rounded-lg bg-surface-tile p-3")}>
      <div className="text-title font-semibold text-fg tabular-nums">
        {s.value ?? "–"}
        {s.value != null && s.unit && <span className="font-light text-fg-muted">{s.unit}</span>}
      </div>
      <div className="text-caption text-fg-muted uppercase">{s.label}</div>
    </div>
  ));
  return tiles ? <div className="grid grid-cols-3 gap-2">{cells}</div> : <Tile className="grid grid-cols-3 gap-2">{cells}</Tile>;
}
