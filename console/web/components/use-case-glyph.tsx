import { ICON_PATHS, type UseCaseIcon } from "@/lib/use-cases";

/* One use case's glyph: a 1.5-unit stroke on a 24 grid, sized by the caller
   so it sits in the front door's tab cells at 16px and the console's chips
   at 14px without a second drawing. */
export function UseCaseGlyph({ name, className, size = 16 }: { name: UseCaseIcon; className?: string; size?: number }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={ICON_PATHS[name]} />
    </svg>
  );
}
