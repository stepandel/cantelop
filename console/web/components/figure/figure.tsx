import type { CSSProperties, ReactNode } from "react";

import styles from "./figure.module.css";

/* Primitives for the docs' and blog's figures. A figure is one inline SVG
   laid out by hand in its own units: nodes for things, a context for where
   they sit, connectors for what moves between them, an axis when the point
   is time. The stylesheet next to this file says what each shape means;
   this file only draws. */

export type Point = readonly [number, number];

const cx = (...names: Array<string | false | undefined>) => names.filter(Boolean).join(" ");

/* ---- The frame ---- */

export function Figure({
  width,
  height,
  label,
  caption,
  className,
  children,
}: {
  width: number;
  height: number;
  /* The claim the picture makes, for readers who cannot see it. */
  label: string;
  caption?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <figure className={cx(styles.figure, className)}>
      <div className={styles.frame}>
        <div className={styles.scroll}>
          <svg
            className={styles.svg}
            style={{ "--fig-w": width } as CSSProperties}
            viewBox={`0 0 ${width} ${height}`}
            width={width}
            height={height}
            role="img"
            aria-label={label}
          >
            {children}
          </svg>
        </div>
      </div>
      {caption ? <figcaption className={styles.caption}>{caption}</figcaption> : null}
    </figure>
  );
}

/* ---- Nodes ---- */

export type NodeKind = "component" | "subject" | "durable" | "absent" | "solid";

/* Type has two sizes and one weight. A name says what a thing is — a box's
   title, a lane, a step — at 16. A note says something about it — a
   connector's label, a panel's label, an aside — at 13. The stylesheet sets
   both; these are the same sizes for layout. */
const NAME = 16;

/* Corners scale with the shape, as on a whiteboard: a chip is barely
   rounded, a card clearly so, a panel never more than a card. */
const radius = (w: number, h: number, max = 12) => Math.min(max, Math.min(w, h) / 4);

/* A box says one thing: its title. There is no subtext; what a reader needs
   beyond the name goes in the prose, the caption, or a connector's label. */
export function Node({
  x,
  y,
  w,
  h,
  kind = "component",
  title,
  align = "center",
  children,
}: {
  x: number;
  y: number;
  w: number;
  h: number;
  kind?: NodeKind;
  /* A bar or a slot may carry its text elsewhere and have no title. */
  title?: string;
  /* A node that holds other nodes keeps its title at the top. */
  align?: "center" | "top";
  children?: ReactNode;
}) {
  const top = align === "top" ? y + 14 : y + (h - NAME) / 2;
  return (
    <g className={cx(styles.node, kind !== "component" && styles[kind])}>
      <rect x={x} y={y} width={w} height={h} rx={radius(w, h)} />
      {children}
      <text className={cx(styles.name, styles.title)} x={x + w / 2} y={top + NAME * 0.85} textAnchor="middle">
        {title}
      </text>
    </g>
  );
}

/* ---- Context: the panel things sit in ---- */

export function Context({
  x,
  y,
  w,
  h,
  label,
  children,
}: {
  x: number;
  y: number;
  w: number;
  h: number;
  label: string;
  children?: ReactNode;
}) {
  return (
    <g className={styles.context}>
      <rect x={x} y={y} width={w} height={h} rx={radius(w, h, 16)} />
      <text className={styles.note} x={x + 16} y={y + 24}>
        {label}
      </text>
      {children}
    </g>
  );
}

/* ---- Connectors ---- */

const HEAD = 10;
const SPREAD = 5;
const BEND = 10;

/* An open head: two strokes meeting at the tip, the way a whiteboard draws
   an arrow, rather than a filled wedge. */
function head(from: Point, to: Point) {
  const [x1, y1] = from;
  const [x2, y2] = to;
  const angle = Math.atan2(y2 - y1, x2 - x1);
  const p = (spread: number): Point => [
    x2 - HEAD * Math.cos(angle) + spread * Math.sin(angle),
    y2 - HEAD * Math.sin(angle) - spread * Math.cos(angle),
  ];
  const [ax, ay] = p(SPREAD);
  const [bx, by] = p(-SPREAD);
  return `${ax},${ay} ${x2},${y2} ${bx},${by}`;
}

/* Move a point along a segment, toward its other end, by a distance. */
function shorten(from: Point, to: Point, by: number): Point {
  const [x1, y1] = from;
  const [x2, y2] = to;
  const len = Math.hypot(x2 - x1, y2 - y1) || 1;
  return [x2 - ((x2 - x1) / len) * by, y2 - ((y2 - y1) / len) * by];
}

/* A polyline whose corners are rounded, so an elbow reads as one line
   turning rather than three lines meeting. */
function rounded(points: Point[]) {
  let d = `M${points[0][0]},${points[0][1]}`;
  for (let i = 1; i < points.length - 1; i++) {
    const [px, py] = points[i - 1];
    const [cx0, cy0] = points[i];
    const [nx, ny] = points[i + 1];
    const r = Math.min(BEND, Math.hypot(cx0 - px, cy0 - py) / 2, Math.hypot(nx - cx0, ny - cy0) / 2);
    const [ax, ay] = shorten(points[i - 1], points[i], r);
    const [bx, by] = shorten(points[i + 1], points[i], r);
    d += ` L${ax},${ay} Q${cx0},${cy0} ${bx},${by}`;
  }
  const [lx, ly] = points[points.length - 1];
  return `${d} L${lx},${ly}`;
}

/* A note's width, estimated without a browser: capitals run wider than the
   rest. Close enough to size a knockout and decide whether a label fits. */
function noteWidth(text: string) {
  let w = 0;
  for (const ch of text) w += /[A-Z]/.test(ch) ? 9.2 : 6.8;
  return w;
}

export function Connector({
  from,
  to,
  via = [],
  label,
  heads = "end",
  tone = "ink",
  dashed = false,
  blocked = false,
  ground = "surface",
}: {
  from: Point;
  to: Point;
  /* Corners between the two ends, for an elbow. */
  via?: Point[];
  label?: string;
  heads?: "end" | "both" | "none";
  tone?: "ink" | "quiet";
  dashed?: boolean;
  /* The line is red and ends in a cross at `to` instead of reaching it. */
  blocked?: boolean;
  /* What the line crosses where its label sits, so the knockout matches. */
  ground?: "surface" | "panel";
}) {
  const points: Point[] = [from, ...via, to];
  const last = points.length - 1;
  const endHead = heads !== "none";
  const startHead = heads === "both";

  /* The label sits on the longest segment: on the line, in a knockout, when
     the segment runs sideways; beside it when the segment runs up or down. */
  let seg = 0;
  let best = -1;
  for (let i = 0; i < last; i++) {
    const d = Math.hypot(points[i + 1][0] - points[i][0], points[i + 1][1] - points[i][1]);
    if (d > best) {
      best = d;
      seg = i;
    }
  }
  const [sx, sy] = points[seg];
  const [ex, ey] = points[seg + 1];
  const mx = (sx + ex) / 2;
  const my = (sy + ey) / 2;
  const sideways = Math.abs(ex - sx) >= Math.abs(ey - sy);
  const labelW = label ? noteWidth(label) + 14 : 0;
  /* A label that would not fit on its segment sits just above it instead. */
  const onLine = sideways && labelW + 2 * HEAD + 20 <= best;

  return (
    <g className={cx(styles.connector, dashed && styles.dashed, tone === "quiet" && styles.quiet, blocked && styles.blocked)}>
      <path d={rounded(points)} />
      {endHead ? <polyline className={styles.head} points={head(points[last - 1], points[last])} /> : null}
      {startHead ? <polyline className={styles.head} points={head(points[1], points[0])} /> : null}
      {blocked ? <Cross at={shorten(points[last - 1], points[last], -8)} /> : null}
      {label && onLine ? (
        <>
          <rect
            className={cx(styles.knockout, ground === "panel" && styles.onPanel)}
            x={mx - labelW / 2}
            y={my - 10}
            width={labelW}
            height={20}
          />
          <text className={cx(styles.note, styles.connectorLabel)} x={mx} y={my + 4.5} textAnchor="middle">
            {label}
          </text>
        </>
      ) : null}
      {label && sideways && !onLine ? (
        <text className={cx(styles.note, styles.connectorLabel)} x={mx} y={my - 9} textAnchor="middle">
          {label}
        </text>
      ) : null}
      {label && !sideways ? (
        <text className={cx(styles.note, styles.connectorLabel)} x={mx + 10} y={my + 4.5}>
          {label}
        </text>
      ) : null}
    </g>
  );
}

/* The mark where a blocked path stops, centered on the point. A blocked
   connector places it a step past its head, so the head touches the wall and
   the cross sits on it. */
export function Cross({ at }: { at: Point }) {
  const [x, y] = at;
  const r = 6;
  return (
    <g className={styles.cross}>
      <line x1={x - r} y1={y - r} x2={x + r} y2={y + r} />
      <line x1={x - r} y1={y + r} x2={x + r} y2={y - r} />
    </g>
  );
}

/* A boundary that traffic crosses or stops at, drawn as a wall between the
   things on either side rather than a box around them. */
export function Wall({ x, y, w, h, label }: { x: number; y: number; w: number; h: number; label: string }) {
  return (
    <g>
      <rect className={styles.wall} x={x} y={y} width={w} height={h} rx={Math.min(6, w / 4)} />
      <Label x={x + w / 2} y={y - 10}>{label}</Label>
    </g>
  );
}

/* A wide thing narrowing onto the share of something beneath it. */
export function Funnel({ top, bottom, y1, y2 }: { top: readonly [number, number]; bottom: readonly [number, number]; y1: number; y2: number }) {
  return <polygon className={styles.funnel} points={`${top[0]},${y1} ${top[1]},${y1} ${bottom[1]},${y2} ${bottom[0]},${y2}`} />;
}

/* ---- Time ---- */

export function Axis({ x1, x2, y, label = "time" }: { x1: number; x2: number; y: number; label?: string }) {
  return (
    <>
      <Connector from={[x1, y]} to={[x2, y]} tone="quiet" />
      <Label x={x2} y={y + 22} anchor="end">{label}</Label>
    </>
  );
}

/* ---- Free text ---- */

/* A name (a lane, a step, a ring) or a note (an aside). A note can be red,
   for what a blocked path says; nothing else changes color. */
export function Label({
  x,
  y,
  anchor = "middle",
  kind = "note",
  red = false,
  children,
}: {
  x: number;
  y: number;
  anchor?: "start" | "middle" | "end";
  kind?: "name" | "note";
  red?: boolean;
  children: ReactNode;
}) {
  return (
    <text className={cx(styles[kind], red && styles.red)} x={x} y={y} textAnchor={anchor}>
      {children}
    </text>
  );
}
