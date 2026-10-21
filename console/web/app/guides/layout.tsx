import type { Metadata } from "next";
import type { ReactNode } from "react";
import { SiteTopbar } from "@/components/site-topbar";
import { SiteFooter } from "@/components/site-footer";
import styles from "./guides.module.css";

export const metadata: Metadata = {
  title: "Guides",
  description: "Step-by-step guides to building and deploying agents on Cantelop.",
  robots: { index: true, follow: true },
};

export default function GuidesLayout({ children }: { children: ReactNode }) {
  return (
    <div className={`site ${styles.page}`}>
      <a className={styles.skipLink} href="#guide-content">Skip to content</a>
      <SiteTopbar />
      <main id="guide-content" className={styles.content} tabIndex={-1}>{children}</main>
      <SiteFooter />
    </div>
  );
}
