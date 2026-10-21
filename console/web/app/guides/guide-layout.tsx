import type { Metadata } from "next";
import type { ReactNode } from "react";
import Link from "next/link";
import { CopyPage, Prose } from "@/components/markdown";
import { markdownSources } from "@/components/markdown/source";
import { headingId } from "@/components/markdown/components";
import { Contents } from "./contents";
import type { Guide } from "./guides";
import styles from "./guides.module.css";

/* Every guide reads the same way: a back link and the guide's own contents,
   built from its ## headings, beside the article. */

export function guideMetadata(guide: Guide): Metadata {
  const url = `https://cantelop.com/guides/${guide.slug}`;
  return {
    title: guide.title,
    description: guide.description,
    alternates: { canonical: url },
    openGraph: { title: guide.title, description: guide.description, type: "article", url },
  };
}

export function GuideLayout({ guide, children }: { guide: Guide; children: ReactNode }) {
  const sources = markdownSources("web/app/guides/(guides)", "/guides");
  const source = sources[`/guides/${guide.slug}`];
  const sections = [...source.matchAll(/^## (.+)$/gm)].map(([, title]) => ({ id: headingId(title), title }));
  return (
    <div className={styles.readingLayout}>
      <aside className={styles.sidebar}>
        <Link className={styles.back} href="/guides">← All guides</Link>
        <div className={styles.desktopContents}>
          <p className={styles.eyebrow}>In this guide</p>
          <Contents sections={sections} />
        </div>
        <details className={styles.mobileContents}>
          <summary>In this guide</summary>
          <Contents sections={sections} />
        </details>
        <a className={styles.sourceLink} href={guide.repository}>Example on GitHub ↗</a>
      </aside>
      <article className={styles.article}>
        <Prose measure="wide">
          <CopyPage sources={sources} placement="above-title" />
          {children}
        </Prose>
      </article>
    </div>
  );
}
