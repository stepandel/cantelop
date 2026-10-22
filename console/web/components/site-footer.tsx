import Link from "next/link";

import { SignInButton } from "@/app/sign-in";
import { PLATFORM_REPO, SDK_REPO } from "@/lib/repositories";
import { BrandMark } from "./brand-mark";
import { GitHubMark } from "./provider-marks";
import styles from "./site-footer.module.css";

/* The front door's footer: what Cantelop is and the beta caveat on the left,
   the site map in three columns on the right, and a closing line with the
   copyright and the platform's repository. */
export function SiteFooter() {
  return (
    <footer className={styles.footer}>
      <div className={styles.inner}>
        <div className={styles.top}>
          <div className={styles.about}>
            <Link className={styles.brand} href="/" aria-label="Cantelop home">
              <span className="brand-mark"><BrandMark size={20} knockout="var(--bg)" /></span>
              <span className="brand-name">cantelop</span>
            </Link>
            <p className={styles.blurb}>
              Serverless infrastructure for your agents. Every user gets an isolated sandbox, bound
              to a workspace that survives restarts.
            </p>
            <p className={styles.note}>
              Cantelop is in beta. Capacity is limited while the platform is prepared to scale;
              service interruptions can happen without warning.
            </p>
          </div>

          <nav className={styles.columns} aria-label="Footer">
            <div className={styles.column}>
              <h2 className={styles.heading}>Product</h2>
              <Link className={styles.link} href="/docs">Docs</Link>
              <Link className={styles.link} href="/guides">Guides</Link>
              <Link className={styles.link} href="/#changelog">Changelog</Link>
            </div>
            <div className={styles.column}>
              <h2 className={styles.heading}>Resources</h2>
              <Link className={styles.link} href="/blog">Blog</Link>
              <a className={styles.link} href={PLATFORM_REPO} target="_blank" rel="noopener noreferrer">
                Cantelop on GitHub
              </a>
              <a className={styles.link} href={SDK_REPO} target="_blank" rel="noopener noreferrer">
                Cantelop SDK on GitHub
              </a>
              <Link className={styles.link} href="/contact">Contact</Link>
              <Link className={styles.link} href="/privacy">Privacy</Link>
              <Link className={styles.link} href="/source">Source code</Link>
            </div>
            <div className={styles.column}>
              <h2 className={styles.heading}>Account</h2>
              <SignInButton className={styles.link}>Sign in</SignInButton>
              <SignInButton className={styles.link}>Get started</SignInButton>
            </div>
          </nav>
        </div>

        <div className={styles.bottom}>
          <p className={styles.legal}>
            <span>© {new Date().getFullYear()} Cantelop</span>
            <span className={styles.dot} aria-hidden="true">•</span>
            <Link className={styles.legalLink} href="/privacy">Privacy</Link>
          </p>
          <a
            className={styles.repo}
            href={PLATFORM_REPO}
            target="_blank"
            rel="noopener noreferrer"
            aria-label="Cantelop on GitHub"
            title="Cantelop on GitHub"
          >
            <GitHubMark />
          </a>
        </div>
      </div>
    </footer>
  );
}
