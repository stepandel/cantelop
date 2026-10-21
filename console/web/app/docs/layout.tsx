import type { ReactNode } from "react";
import { CopyPage, Prose } from "@/components/markdown";
import { markdownSources } from "@/components/markdown/source";
import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import { DocsNav } from "./docs-nav";
import styles from "./docs.module.css";

export const metadata = { robots: { index: true, follow: true } };

/* /docs is a wrapper, not a page: the shell, the section nav, the reading
   measure every docs page renders into, and each page's own markdown, offered
   to anyone who would rather hand that to a model. */
export default function DocsLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className={`site ${styles.page}`}>
      <a className={styles.skipLink} href="#docs-content">Skip to content</a>
      <SiteTopbar />

      <div className={styles.body}>
        <DocsNav />
        <main id="docs-content" className={styles.content} tabIndex={-1}>
          <Prose>
            <CopyPage sources={markdownSources("web/app/docs", "/docs")} />
            {children}
          </Prose>
        </main>
      </div>
      <SiteFooter />
    </div>
  );
}
