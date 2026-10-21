import { useEffect, useRef, useState } from "react";

// Width of a chart's container, for SVGs that have to be laid out in pixels.
// A hidden tab panel reports zero width, so the last real measurement is kept
// and the charts survive tab switches without collapsing to the empty state.
export function useMeasuredWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    setWidth(element.getBoundingClientRect().width);
    const observer = new ResizeObserver((entries) => {
      const next = entries[0].contentRect.width;
      if (next > 0) setWidth(next);
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return { ref, width };
}
