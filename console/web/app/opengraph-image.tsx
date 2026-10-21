import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { ImageResponse } from "next/og";

import { MARK_ACCENT, MARK_FACETS } from "@/components/brand-mark";
import { FigureDrawing, OG_PALETTE } from "./blog/og";
import { AgentPerUserDiagram } from "./home-diagram";

/* The link preview for every page, at 1200x630: the home page's first screen
   restated as a card. The lockup and the headline on the left, set the way
   the hero sets them; the hero's own diagram on the right, on the white card
   with the fading dot grid the page gives its pictures. A feed shows this at
   about half size, so every size is the site's at roughly 2x.

   Satori draws this, not a browser, so the tokens in globals.css are restated
   as literals (it has no custom properties). The diagram goes through the
   blog's FigureDrawing, which flattens the figure grammar into shapes and
   lifts its labels out of the SVG, since Satori refuses SVG <text>. The
   faces are the static Geist instances vendored in web/assets/fonts, and for
   the wordmark alone Sora Medium, as the site sets .brand-name. Sora ships
   only as a variable font, so its instance is cut the same way as Geist's
   and subset to lowercase, the only letters a wordmark uses:

     f = instantiateVariableFont(TTFont("Sora[wght].ttf"), {"wght": 500})
     del f["GPOS"]; del f["DSIG"]
     subset to "abcdefghijklmnopqrstuvwxyz", save("Sora-Medium.ttf")

   next build runs from console/. */

export const alt =
  "Cantelop, in beta: give every user their own agent. A diagram shows two users, one on a laptop and one on a phone, reaching your app, which routes each to their own workspace and the sandboxes that run their agent.";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

const BG = "#fafafa";
const INK = "#0b2e22";
const MUTED = "#4a5a52";
const CANTALOUPE = "#df7a2e";
const TAG = "#a35200";
const DOT = "#cfcfcf";

/* The card's size, and home.module.css .visual::before's dot grid drawn into
   it: Satori tiles neither a radial gradient nor a mask, so each dot is a
   circle whose opacity is the mask's radial fade worked out at its centre —
   solid inside 30% of the ellipse, gone by 80%. */
const CARD = { width: 548, height: 500 };
const PITCH = 24;

function Dots() {
  const dots = [];
  const cx = CARD.width / 2;
  const cy = CARD.height / 2;
  for (let y = cy % PITCH; y < CARD.height; y += PITCH) {
    for (let x = cx % PITCH; x < CARD.width; x += PITCH) {
      const r = Math.hypot((x - cx) / cx, (y - cy) / cy) / Math.SQRT2;
      const opacity = Math.max(0, Math.min(1, (0.8 - r) / 0.5));
      if (opacity > 0) dots.push(<circle key={`${x}:${y}`} cx={x} cy={y} r={1.6} fill={DOT} fillOpacity={opacity} />);
    }
  }
  return (
    <svg width={CARD.width} height={CARD.height} style={{ position: "absolute", top: 0, left: 0 }}>
      {dots}
    </svg>
  );
}

async function font(file: string): Promise<Buffer> {
  return readFile(join(process.cwd(), "web/assets/fonts", file));
}

/* brand-mark.tsx's construction: the thick ink stroke, the ground-coloured
   knockout over it, and the marked facet in cantaloupe. */
function Mark({ size: px }: { size: number }) {
  return (
    <svg width={px} height={px} viewBox="0 0 100 100">
      {MARK_FACETS.map((d) => (
        <path key={d} d={d} fill="none" stroke={INK} strokeWidth={12} strokeLinejoin="round" />
      ))}
      {MARK_FACETS.map((d) => (
        <path key={`k${d}`} d={d} fill={BG} stroke={BG} strokeWidth={7.6} strokeLinejoin="round" />
      ))}
      <path d={MARK_FACETS[MARK_ACCENT]} fill={CANTALOUPE} stroke={CANTALOUPE} strokeWidth={7.6} strokeLinejoin="round" />
    </svg>
  );
}

export default async function OpenGraphImage() {
  const [regular, medium, brand] = await Promise.all([
    font("Geist-Regular.ttf"),
    font("Geist-Medium.ttf"),
    font("Sora-Medium.ttf"),
  ]);

  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          alignItems: "center",
          gap: 40,
          padding: "0 48px 0 64px",
          background: BG,
          color: INK,
          fontFamily: "Geist",
          fontWeight: 400,
        }}
      >
        {/* The words: lockup, headline, one line of support. */}
        <div style={{ display: "flex", flexDirection: "column", width: 500, flexShrink: 0 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 14 }}>
            <Mark size={44} />
            <span style={{ fontFamily: "Sora", fontSize: 36, fontWeight: 500, letterSpacing: "-0.02em" }}>cantelop</span>
            <div style={{ display: "flex", alignItems: "center", marginLeft: 6, color: TAG, lineHeight: 1 }}>
              <span style={{ fontSize: 26 }}>[</span>
              <span style={{ fontSize: 19, fontWeight: 500, letterSpacing: "0.08em" }}>BETA</span>
              <span style={{ fontSize: 26, marginLeft: -2 }}>]</span>
            </div>
          </div>

          <div
            style={{
              display: "flex",
              flexDirection: "column",
              marginTop: 56,
              fontSize: 64,
              fontWeight: 500,
              lineHeight: 1.06,
              letterSpacing: "-0.035em",
            }}
          >
            <span>Give every user</span>
            <span>their own agent.</span>
          </div>

          <div style={{ marginTop: 28, fontSize: 26, lineHeight: 1.4, color: MUTED }}>
            Isolated, persistent workspaces running Claude Code, Codex, or your own harness.
          </div>
        </div>

        {/* The picture: home.module.css .visual — white, hairline border, the
            fading dot grid, and the hero's diagram set into it. */}
        <div
          style={{
            position: "relative",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            width: CARD.width,
            height: CARD.height,
            flexShrink: 0,
            boxSizing: "border-box",
            borderRadius: 18,
            border: `2px solid ${OG_PALETTE.line}`,
            backgroundColor: OG_PALETTE.surface,
            overflow: "hidden",
          }}
        >
          <Dots />
          <FigureDrawing figure={<AgentPerUserDiagram />} maxWidth={CARD.width - 40} maxHeight={CARD.height - 40} textScale={1.2} />
        </div>
      </div>
    ),
    {
      ...size,
      fonts: [
        { name: "Geist", data: regular, weight: 400, style: "normal" },
        { name: "Geist", data: medium, weight: 500, style: "normal" },
        { name: "Sora", data: brand, weight: 500, style: "normal" },
      ],
    },
  );
}
