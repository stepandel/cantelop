import type { ReactNode } from "react";
import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import styles from "./blog.module.css";

export const metadata = { robots: { index: true, follow: true } };

/* The blog's shell: the front door's bar over the section. The index lays
   its cards out on a wide canvas; posts, grouped under (posts), add the
   docs' reading measure in their own layout. */
export default function BlogLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className={`site ${styles.page}`}>
      <a className={styles.skipLink} href="#blog-content">Skip to content</a>
      <SiteTopbar />
      <main id="blog-content" className={styles.content} tabIndex={-1}>
        {children}
      </main>
      <SiteFooter />
    </div>
  );
}
