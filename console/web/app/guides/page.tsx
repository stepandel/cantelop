import type { ReactNode } from "react";
import Link from "next/link";
import { GUIDES } from "./guides";
import { ClaudeLockup } from "./lockup";
import styles from "./guides.module.css";

export const metadata = {
  alternates: { canonical: "https://cantelop.com/guides" },
};

/* Each card's art, by guide slug. */
const ART: Record<string, ReactNode> = {
  "claude-code": <ClaudeLockup />,
};

export default function GuidesIndex() {
  return (
    <div className={styles.index}>
      <h1 className={styles.title}>Guides</h1>
      <p className={styles.intro}>Build with Cantelop. Runnable examples, explained from the first request to deployment.</p>
      {GUIDES.map((guide) => (
        <article key={guide.slug} className={styles.card}>
          <div className={styles.cardArt} aria-hidden="true">{ART[guide.slug]}</div>
          <div className={styles.cardBody}>
            <p className={styles.eyebrow}>{guide.eyebrow}</p>
            <h2><Link href={`/guides/${guide.slug}`}>{guide.title}</Link></h2>
            <p>{guide.description}</p>
            <div className={styles.actions}>
              <Link className={styles.primaryLink} href={`/guides/${guide.slug}`}>Start building <span aria-hidden="true">→</span></Link>
              <a href={guide.repository}>View on GitHub <span aria-hidden="true">↗</span></a>
            </div>
          </div>
        </article>
      ))}
    </div>
  );
}
