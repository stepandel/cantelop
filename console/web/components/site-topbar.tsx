import Link from "next/link";

import { SignInButton, SignInDialog } from "@/app/sign-in";
import { BetaTag } from "./beta-tag";
import { BrandMark } from "./brand-mark";
import styles from "./site-topbar.module.css";

/* The front door's bar, shared by the home page, the docs, and the blog:
   brand and the pre-release tag (which explains itself on click) on the
   left, the section links centered on the page, and the two ways in on the
   right — Sign in for someone returning, Get started for someone new. Both
   open the provider dialog the bar also carries. 48px, the height the docs
   and guides offset their sticky sidebars by; sticky, so the way in
   is always one click away. */
export function SiteTopbar() {
  return (
    <header className={styles.topbar}>
      <div className={styles.brand}>
        <span className="brand-mark"><BrandMark size={20} /></span>
        <Link className="brand-name" href="/">cantelop</Link>
        <BetaTag />
      </div>
      <nav className={styles.nav} aria-label="Main navigation">
        <Link className={styles.link} href="/docs">Docs</Link>
        <Link className={styles.link} href="/guides">Guides</Link>
        <Link className={styles.link} href="/blog">Blog</Link>
        <Link className={styles.link} href="/#changelog">Changelog</Link>
      </nav>
      <div className={styles.actions}>
        <SignInButton className={styles.link}>Sign in</SignInButton>
        <SignInButton className={`${styles.cta} ${styles.pill}`}>Get started</SignInButton>
      </div>
      <SignInDialog />
    </header>
  );
}
