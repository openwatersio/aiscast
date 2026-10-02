import type { ReactNode } from "react";
import type { Offsets } from "../../lib/ais";

// The drawing in its own units: the hull's drawn length, and the room around it for the
// dimension lines and their labels. Length is above and beam to the left, always; the
// antenna's offsets are below and to the right, when there are any.
const HULL = 248;
const LEFT = 58;
const TOP = 30;
const RIGHT = 64;
const BOTTOM = 34;
const EDGE = 8;
const GAP = 2;

// Drawn length to beam, whatever the vessel's own: at its true 20:1 a long hull would be a
// line, and a dinghy would be a disc.
const MIN_RATIO = 2;
const MAX_RATIO = 6;

const meters = (n: number) => `${Math.round(n * 10) / 10} m`;

/**
 * Something on deck, in unit coordinates that stretch with the hull: x from 0 at the stern to
 * 1 at the bow, y from 0 at port to 1 at starboard. A house is superstructure; a detail is a
 * fitting drawn fainter; a dot keeps its size however the hull stretches, as a mast does.
 */
type Shape =
  | { house: [x: number, y: number, w: number, h: number] }
  | { detail: [x: number, y: number, w: number, h: number] }
  | { dot: [x: number, y: number] };

/**
 * A type of vessel as drawn. `bow` is how much of the length the bow takes to narrow to its
 * tip, and `blunt` how round that tip is, 0 for a point. `stern` is the transom's width as a
 * fraction of the beam, 1 for square.
 */
interface Template {
  bow: number;
  blunt: number;
  stern: number;
  deck: Shape[];
}

/** `n` hatch covers in a row between `from` and `to`, as a cargo ship's holds. */
function hatches(n: number, from: number, to: number): Shape[] {
  const pitch = (to - from) / n;
  return Array.from({ length: n }, (_, i) => ({ detail: [from + i * pitch + pitch * 0.1, 0.18, pitch * 0.8, 0.64] }));
}

// Silhouettes by type, drawn to be recognized, not to be any particular ship: most vessels
// carry their deckhouse aft, a fishing boat's is forward, a tug's amidships.
const GENERIC: Template = { bow: 0.22, blunt: 0.12, stern: 1, deck: [{ house: [0.07, 0.2, 0.16, 0.6] }] };
const TEMPLATES: Record<string, Template> = {
  cargo: { bow: 0.16, blunt: 0.18, stern: 0.95, deck: [{ house: [0.04, 0.12, 0.12, 0.76] }, ...hatches(6, 0.2, 0.82)] },
  tanker: {
    bow: 0.16,
    blunt: 0.2,
    stern: 0.95,
    deck: [
      { house: [0.04, 0.12, 0.12, 0.76] },
      // The pipe run along the centerline and the manifold across it amidships.
      { detail: [0.18, 0.46, 0.66, 0.08] },
      { detail: [0.48, 0.16, 0.04, 0.68] },
    ],
  },
  passenger: {
    bow: 0.24,
    blunt: 0.1,
    stern: 0.9,
    deck: [{ house: [0.06, 0.14, 0.72, 0.72] }, { detail: [0.16, 0.34, 0.12, 0.32] }, { detail: [0.42, 0.3, 0.26, 0.4] }],
  },
  fishing: { bow: 0.3, blunt: 0.1, stern: 0.9, deck: [{ house: [0.5, 0.2, 0.2, 0.6] }, { detail: [0.08, 0.26, 0.3, 0.48] }] },
  tug: { bow: 0.34, blunt: 0.3, stern: 0.85, deck: [{ house: [0.34, 0.2, 0.3, 0.6] }, { dot: [0.16, 0.5] }] },
  sailing: {
    bow: 0.45,
    blunt: 0.02,
    stern: 0.7,
    deck: [{ detail: [0.08, 0.3, 0.2, 0.4] }, { house: [0.3, 0.26, 0.24, 0.48] }, { dot: [0.6, 0.5] }],
  },
  pleasure: { bow: 0.4, blunt: 0.06, stern: 0.85, deck: [{ detail: [0.06, 0.22, 0.26, 0.56] }, { house: [0.34, 0.2, 0.26, 0.6] }] },
};

/** The silhouette for an ITU ship type code, and the generic one for the rest. */
function templateFor(type: number | undefined): Template {
  if (type == null) return GENERIC;
  if (type === 30) return TEMPLATES.fishing!;
  if (type === 31 || type === 32 || type === 52) return TEMPLATES.tug!;
  if (type === 36) return TEMPLATES.sailing!;
  if (type === 37) return TEMPLATES.pleasure!;
  if (type >= 60 && type <= 69) return TEMPLATES.passenger!;
  if (type >= 70 && type <= 79) return TEMPLATES.cargo!;
  if (type >= 80 && type <= 89) return TEMPLATES.tanker!;
  return GENERIC;
}

/**
 * A vessel from above, bow to the right, so port is the top edge: the hull at its length to
 * beam, the AIS antenna as a dot at its reported distance from the bow and from port, and
 * dimension lines labelled with the numbers the vessel sent. Without an antenna position it
 * draws the hull with its length and beam alone, and says nothing about the antenna. The
 * outline and deck are the silhouette for the vessel's ITU `type`.
 */
export function ShipDiagram({
  length,
  beam,
  antenna,
  type,
  aside,
}: {
  length: number;
  beam: number;
  antenna?: Offsets;
  type?: number;
  /** Shown at the end of the caption row, such as the vessel's draught. */
  aside?: ReactNode;
}) {
  const t = templateFor(type);
  const ratio = Math.min(MAX_RATIO, Math.max(MIN_RATIO, length / beam));
  const h = HULL / ratio;
  const [x0, x1, y0, y1] = [LEFT, LEFT + HULL, TOP, TOP + h];
  // Meters to drawing units along and across the hull, which the clamp can make differ.
  const dot = antenna && { x: x1 - antenna.toBow * (HULL / length), y: y0 + antenna.toPort * (h / beam) };
  const [top, left, bottom, right] = [TOP - 12, LEFT - 16, y1 + 12, x1 + 16];
  const width = x1 + (dot ? RIGHT : EDGE);

  return (
    // A container, so the key's dot can be sized in its units and scale with the drawing.
    <figure className="@container">
      <svg
        viewBox={`0 0 ${width} ${y1 + (dot ? BOTTOM : EDGE)}`}
        className="w-full"
        role="img"
        aria-label={
          `Outline of the vessel, ${meters(length)} long and ${meters(beam)} wide` +
          (antenna ? `, with its AIS antenna ${meters(antenna.toBow)} from the bow and ${meters(antenna.toPort)} from port.` : ".")
        }
      >
        <path d={hullPath(t, x0, x1, y0, y1)} strokeWidth={1.5} strokeLinejoin="round" className="fill-fg/5 stroke-fg-secondary" />
        {t.deck.map((shape, i) => {
          if ("dot" in shape) return <circle key={i} cx={x0 + shape.dot[0] * HULL} cy={y0 + shape.dot[1] * h} r={2.5} className="fill-fg-muted" />;
          const [x, y, w, sh] = "house" in shape ? shape.house : shape.detail;
          return (
            <rect
              key={i}
              x={x0 + x * HULL}
              y={y0 + y * h}
              width={w * HULL}
              height={sh * h}
              rx={2}
              className={"house" in shape ? "fill-fg/10 stroke-fg-muted" : "fill-none stroke-fg-muted/50"}
            />
          );
        })}

        <Dim x1={x0} y1={top} x2={x1} y2={top} label={meters(length)} at="above" />
        <Dim x1={left} y1={y0} x2={left} y2={y1} label={meters(beam)} at="left" />
        {dot && (
          <>
            <line x1={dot.x} y1={dot.y} x2={dot.x} y2={bottom} strokeDasharray="2 2" className="stroke-highlight" />
            <line x1={dot.x} y1={dot.y} x2={right} y2={dot.y} strokeDasharray="2 2" className="stroke-highlight" />
            {/* Each pair stops short of the antenna, so their arrowheads do not touch. */}
            <Dim x1={x0} y1={bottom} x2={dot.x - GAP} y2={bottom} label={meters(antenna!.toStern)} at="below" />
            <Dim x1={dot.x + GAP} y1={bottom} x2={x1} y2={bottom} label={meters(antenna!.toBow)} at="below" />
            <Dim x1={right} y1={y0} x2={right} y2={dot.y - GAP} label={meters(antenna!.toPort)} at="right" />
            <Dim x1={right} y1={dot.y + GAP} x2={right} y2={y1} label={meters(antenna!.toStarboard)} at="right" />
            <AntennaDot cx={dot.x} cy={dot.y} />
          </>
        )}
      </svg>
      {(antenna || aside) && (
        <figcaption className="mt-1 flex items-center justify-between gap-3 text-footnote text-fg-muted">
          {antenna ? (
            <span className="flex items-center gap-1.5">
              {/* The drawing's own dot, at the drawing's scale: 10 units of its width. */}
              <svg aria-hidden viewBox="0 0 10 10" className="shrink-0" style={{ width: `${(10 / width) * 100}cqw` }}>
                <AntennaDot cx={5} cy={5} />
              </svg>
              AIS antenna position
            </span>
          ) : (
            <span />
          )}
          {aside}
        </figcaption>
      )}
    </figure>
  );
}

function AntennaDot({ cx, cy }: { cx: number; cy: number }) {
  return <circle cx={cx} cy={cy} r={4} strokeWidth={1.5} className="fill-highlight stroke-surface" />;
}

/** A hull from above in the template's shape, bow to the right. */
function hullPath(t: Template, x0: number, x1: number, y0: number, y1: number): string {
  const [len, h] = [x1 - x0, y1 - y0];
  const ym = y0 + h / 2;
  const shoulder = x1 - len * t.bow;
  const pull = shoulder + (x1 - shoulder) * 0.6;
  // The stern: how far in from the sides the transom's corners sit, and how far forward the
  // quarters curve out to the full beam. A square stern keeps only a rounded corner.
  const r = Math.min(h * 0.2, 6);
  const tuck = Math.max(r, (h * (1 - t.stern)) / 2);
  const reach = Math.max(r, len * 0.6 * (1 - t.stern));
  return (
    `M${x0} ${y0 + tuck}Q${x0} ${y0} ${x0 + reach} ${y0}L${shoulder} ${y0}` +
    `C${pull} ${y0} ${x1} ${ym - h * t.blunt} ${x1} ${ym}C${x1} ${ym + h * t.blunt} ${pull} ${y1} ${shoulder} ${y1}` +
    `L${x0 + reach} ${y1}Q${x0} ${y1} ${x0} ${y1 - tuck}Z`
  );
}

/** A dimension line with an arrowhead at each end, horizontal or vertical, labelled `at` one side. */
function Dim({
  x1,
  y1,
  x2,
  y2,
  label,
  at,
}: {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  label: string;
  at: "above" | "below" | "left" | "right";
}) {
  const len = Math.hypot(x2 - x1, y2 - y1);
  if (len < 1) return null;
  const [ux, uy] = [(x2 - x1) / len, (y2 - y1) / len];
  // Arrowheads shrink on a short segment rather than overlap.
  const a = Math.min(4, len / 3);
  const head = (x: number, y: number, d: number) =>
    `M${x + d * (ux * a - uy * a * 0.6)} ${y + d * (uy * a + ux * a * 0.6)}L${x} ${y}` +
    `L${x + d * (ux * a + uy * a * 0.6)} ${y + d * (uy * a - ux * a * 0.6)}`;
  return (
    <g>
      <path d={`M${x1} ${y1}L${x2} ${y2}${head(x1, y1, 1)}${head(x2, y2, -1)}`} className="fill-none stroke-fg-muted" />
      <text
        x={at === "left" ? x1 - 6 : at === "right" ? x1 + 6 : (x1 + x2) / 2}
        y={at === "above" ? y1 - 4 : at === "below" ? y1 + 13 : (y1 + y2) / 2}
        textAnchor={at === "left" ? "end" : at === "right" ? "start" : "middle"}
        dominantBaseline={at === "left" || at === "right" ? "middle" : "auto"}
        className="fill-fg-secondary text-subhead tabular-nums"
      >
        {label}
      </text>
    </g>
  );
}
