import type { Offsets } from "../../lib/ais";

// The drawing in its own units: the hull's drawn length, and the room around it for the
// dimension lines and their labels.
const HULL = 248;
const LEFT = 8;
const TOP = 30;
const RIGHT = 64;
const BOTTOM = 34;

// Drawn length to beam, whatever the vessel's own: at its true 20:1 a long hull would be a
// line, and a dinghy would be a disc.
const MIN_RATIO = 2;
const MAX_RATIO = 6;

const meters = (n: number) => `${Math.round(n * 10) / 10} m`;

/**
 * A vessel from above, bow to the right, so port is the top edge: the hull at its length to
 * beam, the AIS antenna as a dot at its reported distance from the bow and from port, and
 * dimension lines labelled with the numbers the vessel sent. Without an antenna position it
 * draws the hull with its length and beam alone, and says nothing about the antenna. One outline serves every type of vessel.
 */
export function ShipDiagram({ length, beam, antenna }: { length: number; beam: number; antenna?: Offsets }) {
  const ratio = Math.min(MAX_RATIO, Math.max(MIN_RATIO, length / beam));
  const h = HULL / ratio;
  const [x0, x1, y0, y1] = [LEFT, LEFT + HULL, TOP, TOP + h];
  // Meters to drawing units along and across the hull, which the clamp can make differ.
  const dot = antenna && { x: x1 - antenna.toBow * (HULL / length), y: y0 + antenna.toPort * (h / beam) };
  const side = x1 + 16;
  const top = TOP - 12;
  const bottom = y1 + 12;

  return (
    <figure>
      <svg
        viewBox={`0 0 ${x1 + RIGHT} ${y1 + BOTTOM}`}
        className="w-full"
        role="img"
        aria-label={
          `Outline of the vessel, ${meters(length)} long and ${meters(beam)} wide` +
          (antenna ? `, with its AIS antenna ${meters(antenna.toBow)} from the bow and ${meters(antenna.toPort)} from port.` : ".")
        }
      >
        <path d={hullPath(x0, x1, y0, y1)} strokeWidth={1.5} strokeLinejoin="round" className="fill-fg/5 stroke-fg-secondary" />
        {/* A deckhouse aft, where most vessels carry theirs. */}
        <rect x={x0 + HULL * 0.07} y={y0 + h * 0.2} width={HULL * 0.16} height={h * 0.6} rx={2} className="fill-fg/10 stroke-fg-muted" />

        {dot ? (
          <>
            <line x1={dot.x} y1={dot.y} x2={dot.x} y2={top} strokeDasharray="2 2" className="stroke-fg-muted/60" />
            <line x1={dot.x} y1={dot.y} x2={side} y2={dot.y} strokeDasharray="2 2" className="stroke-fg-muted/60" />
            <Dim x1={x0} y1={top} x2={dot.x} y2={top} label={meters(antenna!.toStern)} />
            <Dim x1={dot.x} y1={top} x2={x1} y2={top} label={meters(antenna!.toBow)} />
            <Dim x1={side} y1={y0} x2={side} y2={dot.y} label={meters(antenna!.toPort)} />
            <Dim x1={side} y1={dot.y} x2={side} y2={y1} label={meters(antenna!.toStarboard)} />
            <circle cx={dot.x} cy={dot.y} r={4} strokeWidth={1.5} className="fill-accent stroke-surface" />
          </>
        ) : (
          <Dim x1={side} y1={y0} x2={side} y2={y1} label={meters(beam)} />
        )}
        <Dim x1={x0} y1={bottom} x2={x1} y2={bottom} label={meters(length)} below />
      </svg>
      {antenna && <figcaption className="mt-1 text-footnote text-fg-muted">Dot: AIS antenna position</figcaption>}
    </figure>
  );
}

/** A hull from above with a square stern and a curved bow, bow to the right. */
function hullPath(x0: number, x1: number, y0: number, y1: number): string {
  const h = y1 - y0;
  const ym = y0 + h / 2;
  const shoulder = x1 - (x1 - x0) * 0.22;
  const pull = shoulder + (x1 - shoulder) * 0.6;
  const r = Math.min(h * 0.2, 6);
  return (
    `M${x0 + r} ${y0}L${shoulder} ${y0}C${pull} ${y0} ${x1} ${ym - h * 0.12} ${x1} ${ym}` +
    `C${x1} ${ym + h * 0.12} ${pull} ${y1} ${shoulder} ${y1}L${x0 + r} ${y1}` +
    `Q${x0} ${y1} ${x0} ${y1 - r}L${x0} ${y0 + r}Q${x0} ${y0} ${x0 + r} ${y0}Z`
  );
}

/**
 * A dimension line with an arrowhead at each end, horizontal or vertical, labelled above (or
 * `below`) when horizontal and to the right when vertical.
 */
function Dim({
  x1,
  y1,
  x2,
  y2,
  label,
  below,
}: {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  label: string;
  below?: boolean;
}) {
  const len = Math.hypot(x2 - x1, y2 - y1);
  if (len < 1) return null;
  const [ux, uy] = [(x2 - x1) / len, (y2 - y1) / len];
  // Arrowheads shrink on a short segment rather than overlap.
  const a = Math.min(4, len / 3);
  const head = (x: number, y: number, d: number) =>
    `M${x + d * (ux * a - uy * a * 0.6)} ${y + d * (uy * a + ux * a * 0.6)}L${x} ${y}` +
    `L${x + d * (ux * a + uy * a * 0.6)} ${y + d * (uy * a - ux * a * 0.6)}`;
  const horizontal = y1 === y2;
  return (
    <g>
      <path d={`M${x1} ${y1}L${x2} ${y2}${head(x1, y1, 1)}${head(x2, y2, -1)}`} className="fill-none stroke-fg-muted" />
      <text
        x={horizontal ? (x1 + x2) / 2 : x1 + 6}
        y={horizontal ? (below ? y1 + 13 : y1 - 4) : (y1 + y2) / 2}
        textAnchor={horizontal ? "middle" : "start"}
        dominantBaseline={horizontal ? "auto" : "middle"}
        className="fill-fg-secondary text-subhead tabular-nums"
      >
        {label}
      </text>
    </g>
  );
}
