import type { CSSProperties } from "react";

/* The Cantelop mark: five rounded facets on a circle around an open
   pentagonal well, with the right-hand facet filled cantaloupe. Drawn from
   the brand sheet on the "Cantelop logo" Doop canvas — circle Ø80 in a
   100-unit box, well r17 with a vertex at the top, 2.5 between facets,
   corner radius 6, line 2.2.

   The line is a thick round-joined stroke with a thinner knockout stroke in
   the ground colour drawn over it: that is what gives every corner a true
   radius at any size. The line takes `currentColor`; the knockout must
   match whatever the mark sits on, so callers pass it (it defaults to the
   surface the topbar is painted on). Below 32px the facets' gaps close, so
   `small` swaps in the favicon cut: gap 6 and a heavier line. */

const FACETS = [
  "M57.25 16.78A34 34 0 0 1 79.35 32.84L65.97 37.19L57.25 30.85Z",
  "M83.83 46.63A34 34 0 0 1 75.39 72.61L67.12 61.23L70.45 50.98Z",
  "M63.66 81.14A34 34 0 0 1 36.34 81.14L44.61 69.75L55.39 69.75Z",
  "M24.61 72.61A34 34 0 0 1 16.17 46.63L29.55 50.98L32.88 61.23Z",
  "M20.65 32.84A34 34 0 0 1 42.75 16.78L42.75 30.85L34.03 37.19Z",
] as const;

const FACETS_SMALL = [
  "M59.00 17.21A34 34 0 0 1 78.40 31.31L64.22 35.92L59.00 32.12Z",
  "M83.96 48.43A34 34 0 0 1 76.55 71.24L67.79 59.17L69.78 53.04Z",
  "M61.99 81.82A34 34 0 0 1 38.01 81.82L46.77 69.75L53.23 69.75Z",
  "M23.45 71.24A34 34 0 0 1 16.04 48.43L30.22 53.04L32.21 59.17Z",
  "M21.60 31.31A34 34 0 0 1 41.00 17.21L41.00 32.12L35.78 35.92Z",
] as const;

/* The facet that carries the accent: right of centre, 18° below horizontal. */
const MARKED = 1;

/* For renderers that cannot mount the component (Satori draws the social
   image from plain SVG elements). */
export const MARK_FACETS = FACETS;
export const MARK_ACCENT = MARKED;

export function BrandMark({
  size = 16,
  small = false,
  knockout = "var(--surface)",
  accent = "var(--cantaloupe)",
  title,
  className,
  style,
}: {
  size?: number;
  small?: boolean;
  knockout?: string;
  accent?: string;
  title?: string;
  className?: string;
  style?: CSSProperties;
}) {
  const facets = small ? FACETS_SMALL : FACETS;
  const knockoutWidth = small ? 3 : 7.6;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 100 100"
      className={className}
      style={style}
      role={title ? "img" : undefined}
      aria-hidden={title ? undefined : true}
    >
      {title ? <title>{title}</title> : null}
      <g fill="none" stroke="currentColor" strokeWidth={12} strokeLinejoin="round">
        {facets.map((d) => <path key={d} d={d} />)}
      </g>
      <g fill={knockout} stroke={knockout} strokeWidth={knockoutWidth} strokeLinejoin="round">
        {facets.map((d) => <path key={d} d={d} />)}
      </g>
      <path d={facets[MARKED]} fill={accent} stroke={accent} strokeWidth={knockoutWidth} strokeLinejoin="round" />
    </svg>
  );
}
