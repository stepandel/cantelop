import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { Children, Fragment, isValidElement, type ReactElement, type ReactNode } from "react";
import { ImageResponse } from "next/og";

import figureStyles from "@/components/figure/figure.module.css";

/* A post's social image is its card's visual band from the index, drawn at
   the size the platforms want: the panel ground with its fine grid, and the
   post's own card figure set into it. The figure is decoration here as it is
   on the card, so its caption stays in the post.

   The figure is the post's own, from its diagrams.tsx, so the image follows
   the figure grammar wherever it goes. Satori cannot draw it as the browser
   does: it reads inline styles and presentation attributes, not
   stylesheets, and it refuses SVG <text>. So figureImage() walks the
   figure's element tree once, gives every shape the fill and stroke
   figure.module.css would, and lifts every label out of the SVG into an
   absolutely placed line of HTML text over it, at the same baseline.

   Fonts are the static Geist instances the site's own social image vendors
   in web/assets/fonts; `next dev|build|start web` run from console/, so the
   path is relative to that, like the markdown sources. */

export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

/* The public site's theme from globals.css (:root, re-pointed by .site), by
   value: the console's neutral grounds and lines, the brand's ink and text
   tiers, and the figure grammar's own fills. */
const palette = {
  panel: "#f5f5f5",
  surface: "#ffffff",
  line: "#e3e3e3",
  lineStrong: "#cfcfcf",
  ink: "#0b2e22",
  muted: "#4a5a52",
  grey: "#51615a",
  amber: "#a35200",
  red: "#cb2a2f",
  figSubject: "#fce6d3",
  figDurable: "#dfe9e3",
  figPanel: "#f4f3ef",
};

/* A figure drawn in the diagrams' own units sets custom properties inline
   now and then (a ground under a stacked card); these are their values. */
const VARS: Record<string, string> = {
  "var(--fig-panel)": palette.figPanel,
  "var(--surface)": palette.surface,
  "var(--ink)": palette.ink,
};

/* Presentation a plain <g> sets for the shapes inside it (a device glyph
   drawn by hand rather than from the grammar's classes). */
const INHERITED = ["fill", "stroke", "strokeWidth", "strokeLinejoin", "strokeLinecap"] as const;
type Inherited = Partial<Record<(typeof INHERITED)[number], unknown>>;

/* The room kept clear around the figure, so it sits in the band the way it
   sits on the card, with the tag in the corner below it. */
const MARGIN = { x: 96, y: 72 };

/* ---- Walking the figure ---- */

/* CSS module class names are hashed; this reads them back to the names
   figure.module.css gives them. */
const CLASS_NAMES = new Map(Object.entries(figureStyles).map(([name, hashed]) => [hashed, name]));
const classesOf = (className: unknown) =>
  new Set(typeof className === "string" ? className.split(/\s+/).map((c) => CLASS_NAMES.get(c) ?? c) : []);

type Shape = ReactElement<Record<string, unknown>>;
type TextRun = { x: number; y: number; anchor: string; size: number; color: string; text: string };

/* The presentation figure.module.css gives a shape, from its own classes and
   its group's. */
function paint(tag: string, own: Set<string>, group: Set<string>): Record<string, unknown> {
  const stroke = { strokeWidth: 2, strokeLinecap: "round", strokeLinejoin: "round" };
  if (own.has("knockout")) return { fill: own.has("onPanel") ? palette.figPanel : palette.surface };
  if (own.has("wall")) return { fill: palette.line, stroke: palette.grey, strokeWidth: 2 };
  if (own.has("funnel")) return { fill: palette.line };
  if (tag === "rect" && group.has("node")) {
    if (group.has("absent")) return { fill: "none", stroke: palette.grey, strokeWidth: 2, strokeDasharray: "6 5" };
    const fill = group.has("subject") ? palette.figSubject
      : group.has("durable") ? palette.figDurable
      : group.has("solid") ? palette.ink
      : palette.surface;
    return { fill, stroke: palette.ink, strokeWidth: 2 };
  }
  if (tag === "rect" && group.has("context")) return { fill: palette.figPanel, stroke: palette.lineStrong, strokeWidth: 2 };
  if (group.has("connector")) {
    const color = group.has("blocked") ? palette.red : group.has("quiet") ? palette.lineStrong : palette.ink;
    return { fill: "none", stroke: color, ...stroke, ...(group.has("dashed") && tag === "path" ? { strokeDasharray: "5 5" } : {}) };
  }
  if (group.has("cross")) return { stroke: palette.red, ...stroke };
  return {};
}

/* The color and size figure.module.css gives a label. */
function voice(own: Set<string>, group: Set<string>) {
  const size = own.has("name") ? 16 : 13;
  if (own.has("red") || (own.has("connectorLabel") && group.has("blocked"))) return { size, color: palette.red };
  if (own.has("title") && group.has("absent")) return { size, color: palette.muted };
  if (own.has("title") && group.has("solid")) return { size, color: palette.surface };
  return { size, color: own.has("name") ? palette.ink : palette.muted };
}

const textOf = (children: ReactNode): string =>
  Children.toArray(children).map((c) => (typeof c === "string" || typeof c === "number" ? String(c) : "")).join("");

/* Flatten the figure into shapes Satori draws and the text laid over them,
   calling the grammar's components as it goes (they are plain functions of
   their props). */
function walk(node: ReactNode, group: Set<string>, shapes: Shape[], texts: TextRun[], inherited: Inherited = {}) {
  Children.forEach(node, (child) => {
    if (!isValidElement(child)) return;
    const el = child as ReactElement<Record<string, unknown>>;
    const props = el.props;
    if (typeof el.type === "function") {
      walk((el.type as (p: typeof props) => ReactNode)(props), group, shapes, texts, inherited);
      return;
    }
    if ((el.type as unknown) === Fragment) {
      walk(props.children as ReactNode, group, shapes, texts, inherited);
      return;
    }
    const own = classesOf(props.className);
    if (el.type === "g") {
      const passed: Inherited = { ...inherited };
      for (const key of INHERITED) if (props[key] !== undefined) passed[key] = props[key];
      walk(props.children as ReactNode, own, shapes, texts, passed);
      return;
    }
    if (el.type === "text") {
      const text = textOf(props.children as ReactNode);
      if (text) {
        texts.push({
          x: Number(props.x),
          y: Number(props.y),
          anchor: String(props.textAnchor ?? "start"),
          text,
          ...voice(own, group),
        });
      }
      return;
    }
    const { className: _, children: __, ...ownAttrs } = props;
    const attrs: Record<string, unknown> = { ...inherited, ...ownAttrs };
    for (const [k, v] of Object.entries(attrs)) if (typeof v === "string" && v in VARS) attrs[k] = VARS[v];
    shapes.push(<el.type key={shapes.length} {...attrs} {...paint(String(el.type), own, group)} />);
  });
}

/* Geist's ascender sits at 1.005 em and its descender at 0.295: in a line
   box one em tall, the baseline falls 0.855 em below the top. */
const BASELINE = 0.855;

type FigureProps = { width: number; height: number; children: ReactNode };

function Drawing({ width, height, children, scale, textScale = 1 }: FigureProps & { scale: number; textScale?: number }) {
  const shapes: Shape[] = [];
  const texts: TextRun[] = [];
  walk(children, new Set(), shapes, texts);

  /* A label's box is as wide as the drawing, placed so its anchor lands on
     the label's x: text-align does the rest. */
  const box = width * scale;
  return (
    <div style={{ position: "relative", display: "flex", width: width * scale, height: height * scale }}>
      <svg width={width * scale} height={height * scale} viewBox={`0 0 ${width} ${height}`}>
        {shapes}
      </svg>
      {texts.map((t, i) => {
        const fontSize = t.size * scale * textScale;
        const x = t.x * scale;
        const left = t.anchor === "middle" ? x - box / 2 : t.anchor === "end" ? x - box : x;
        return (
          <div
            key={i}
            style={{
              position: "absolute",
              display: "flex",
              justifyContent: t.anchor === "middle" ? "center" : t.anchor === "end" ? "flex-end" : "flex-start",
              left,
              top: t.y * scale - fontSize * BASELINE,
              width: box,
              fontSize,
              lineHeight: 1,
              color: t.color,
              whiteSpace: "pre",
            }}
          >
            {t.text}
          </div>
        );
      })}
    </div>
  );
}

/* A figure component drawn to fit a box, for images composed elsewhere. */
/* `textScale` sets the labels larger than the drawing around them, for an
   image a feed shows at half size; the figure must leave its labels room. */
export function FigureDrawing({
  figure,
  maxWidth,
  maxHeight,
  textScale = 1,
}: {
  figure: ReactElement<object>;
  maxWidth: number;
  maxHeight: number;
  textScale?: number;
}) {
  const { width, height, children } = (figure.type as (p: object) => ReactElement<FigureProps>)(figure.props).props;
  const scale = Math.min(maxWidth / width, maxHeight / height);
  return <Drawing width={width} height={height} scale={scale} textScale={textScale}>{children}</Drawing>;
}

export const OG_PALETTE = palette;

/* ---- The image ---- */

/* The pre-release tag in the image's bottom corner, drawn as globals.css sets
   .beta-tag, the word in the theme's amber and the brackets larger and a
   weight lighter so they frame it, but large enough to still read once a
   feed shrinks the image. The brand itself stays off the image; the platform
   names the site beside it. */
function BetaTag() {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        position: "absolute",
        right: 26,
        bottom: 26,
        color: palette.amber,
        lineHeight: 1,
      }}
    >
      <div style={{ fontSize: 45, fontWeight: 400 }}>[</div>
      <div style={{ fontSize: 35, letterSpacing: "0.08em" }}>BETA</div>
      <div style={{ fontSize: 45, fontWeight: 400, marginLeft: "-0.08em" }}>]</div>
    </div>
  );
}

export async function figureImage(figure: ReactElement<object>) {
  const [regular, medium] = await Promise.all([
    readFile(join(process.cwd(), "web/assets/fonts/Geist-Regular.ttf")),
    readFile(join(process.cwd(), "web/assets/fonts/Geist-Medium.ttf")),
  ]);

  /* The diagram component returns its <Figure>; the drawing is its
     children, set as large as the room around it allows. */
  const { width, height, children } = (figure.type as (p: object) => ReactElement<FigureProps>)(figure.props).props;
  const scale = Math.min((size.width - 2 * MARGIN.x) / width, (size.height - 2 * MARGIN.y) / height);

  /* blog.module.css .art: the panel ground under a 24px grid of hairlines,
     here at the scale the figure is drawn at so the two stay in step. */
  const grid = 24 * scale;
  const hair = Math.round(scale);
  return new ImageResponse(
    (
      <div
        style={{
          position: "relative",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          width: "100%",
          height: "100%",
          backgroundColor: palette.panel,
          backgroundImage:
            `linear-gradient(${palette.line} ${hair}px, transparent ${hair}px), ` +
            `linear-gradient(90deg, ${palette.line} ${hair}px, transparent ${hair}px)`,
          backgroundSize: `${grid}px ${grid}px`,
          backgroundPosition: "center",
          fontFamily: "geist",
          fontWeight: 500,
          color: palette.ink,
        }}
      >
        <Drawing width={width} height={height} scale={scale}>{children}</Drawing>
        <BetaTag />
      </div>
    ),
    {
      ...size,
      fonts: [
        { name: "geist", data: regular, weight: 400, style: "normal" },
        { name: "geist", data: medium, weight: 500, style: "normal" },
      ],
    },
  );
}
