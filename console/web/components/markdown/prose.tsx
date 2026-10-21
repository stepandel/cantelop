import type { ReactNode } from "react";

import { VariantProvider } from "./variants";
import styles from "./prose.module.css";

/* The reading measure that rendered markdown lives in. A route's layout wraps
   its MDX children in this and gets the whole element vocabulary styled, plus
   the state <Variants> needs — so a markdown page is only ever a .mdx file.
   Long-form editorial pages can opt into the slightly wider measure. */
export function Prose({
  children,
  measure = "default",
}: {
  children: ReactNode;
  measure?: "default" | "wide";
}) {
  return (
    <div className={`${styles.prose} ${measure === "wide" ? styles.wide : ""}`}>
      <VariantProvider>{children}</VariantProvider>
    </div>
  );
}
