"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";

import styles from "./docs.module.css";

/* One flat reading order. SDK implementation details live in the SDK docs. */
const PAGES = [
  { href: "/docs/quick-start", label: "Quick start" },
  { href: "/docs/architecture", label: "Architecture" },
  { href: "/docs/sandbox-isolation", label: "Sandbox isolation" },
  { href: "/docs/ingress", label: "Ingress" },
  { href: "/docs/session-runtime", label: "Session runtime" },
  { href: "/docs/workspaces", label: "Workspaces" },
  { href: "/docs/environments", label: "Environments" },
  { href: "/docs/response-streaming", label: "Streaming output" },
  { href: "/docs/development", label: "Local development" },
  { href: "/docs/deploy", label: "Deployment" },
  { href: "/docs/cli", label: "CLI reference" },
] as const;

export function DocsNav() {
  const pathname = usePathname();
  const [expanded, setExpanded] = useState(false);

  return (
    <aside className={styles.sidebar} data-expanded={expanded}>
      <p className={styles.navTitle}>Documentation</p>
      <button
        type="button"
        className={styles.navToggle}
        aria-expanded={expanded}
        aria-controls="docs-navigation"
        onClick={() => setExpanded(!expanded)}
      >
        <span>{expanded ? "Close docs" : "Browse docs"}</span>
        <span aria-hidden="true">{expanded ? "−" : "+"}</span>
      </button>
      <div id="docs-navigation" className={styles.navContent}>
        <nav className={styles.docsnav} aria-label="Documentation">
          {PAGES.map((page) => (
            <Link
              key={page.href}
              className={styles.docsnavLink}
              href={page.href}
              aria-current={pathname === page.href ? "page" : undefined}
              onClick={() => setExpanded(false)}
            >
              {page.label}
            </Link>
          ))}
        </nav>
        <a className={styles.sdkLink} href="https://github.com/stepandel/cantelop-sdk#readme">
          SDK 1.0 documentation <span aria-hidden="true">↗</span>
        </a>
      </div>
    </aside>
  );
}
