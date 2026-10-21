import type { ReactNode } from "react";
import Link from "next/link";

import styles from "./prose.module.css";

/* The two things markdown cannot say on its own, for any page that needs
   them.

   A success checklist is an ordinary list that means something different from
   a list of requirements — ticks, not bullets — and markdown has no way to
   mark that. A page's closing link is a control rather than a link in a
   sentence. Both wrap plain markdown; the prose inside stays markdown. */

export function Checks({ children }: { children: ReactNode }) {
  return <div className={styles.checks}>{children}</div>;
}

export function Next({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link className={styles.next} href={href}>
      {children}
    </Link>
  );
}
