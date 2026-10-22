import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";

import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import bar from "@/components/site-topbar.module.css";
import { CHANGELOG } from "@/lib/changelog";
import { PLATFORM_REPO } from "@/lib/repositories";
import { AgentPerUserDiagram } from "./home-diagram";
import { ParallelSessionsSnippet } from "./home-snippet";
import { ClaudePlanExample } from "./blog/(posts)/hosting-claude-code-bring-your-own-subscription/claude-plan-example";
import { POST_ART } from "./blog/post-art";
import { POSTS } from "./blog/posts";
import { AutoSignIn, SignInButton, SignInErrorNotice } from "./sign-in";
import styles from "./home.module.css";

const TITLE = "Cantelop — give every user their own agent";
const DESCRIPTION =
  "Give every user their own agent: an isolated workspace with persistent files, running Claude Code, Codex, or your own harness. One SDK, no servers to manage.";

export const metadata: Metadata = {
  title: { absolute: TITLE },
  description: DESCRIPTION,
  alternates: { canonical: "https://cantelop.com/" },
  openGraph: {
    type: "website",
    url: "https://cantelop.com/",
    siteName: "Cantelop",
    title: TITLE,
    description: DESCRIPTION,
  },
  twitter: { card: "summary_large_image", title: TITLE, description: DESCRIPTION },
  // The console is deliberately unindexed; the front door is the exception.
  robots: { index: true, follow: true },
};

/* What search engines read as the product, beside the page's own metadata. */
const STRUCTURED_DATA = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: "Cantelop",
  url: "https://cantelop.com/",
  applicationCategory: "DeveloperApplication",
  operatingSystem: "Web",
  description: DESCRIPTION,
  sameAs: [PLATFORM_REPO],
  subjectOf: {
    "@type": "SoftwareSourceCode",
    codeRepository: PLATFORM_REPO,
    license: "https://www.gnu.org/licenses/agpl-3.0.html",
  },
};

/* "Sep 18, 2026", read as a calendar date so no reader's time zone moves it. */
const DATE = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const formatDate = (iso: string) => DATE.format(new Date(`${iso}T00:00:00Z`));

/* Backticked spans in a one-line summary are set in the typed face. */
function inlineCode(text: string): ReactNode[] {
  return text.split(/(`[^`]+`)/).map((part, index) =>
    part.startsWith("`") && part.endsWith("`") ? <code key={index}>{part.slice(1, -1)}</code> : part,
  );
}

/* Three places the same kind of agent shows up, stacked like windows on a
   desk: a Slack thread at the back, an assistant inside your own app in the
   middle, a Telegram chat in front. One description covers the stack; the
   windows themselves are decoration. */
const FRONTENDS = [
  { src: "/home/frontend-slack.webp", width: 1100, height: 815, className: styles.stackSlack },
  { src: "/home/frontend-app.webp", width: 1100, height: 825, className: styles.stackApp },
  { src: "/home/frontend-telegram.webp", width: 1100, height: 812, className: styles.stackTelegram },
] as const;

function FrontendStack() {
  return (
    <div className={styles.stack}>
      {FRONTENDS.map((window) => (
        <img
          key={window.src}
          className={`${styles.stackWindow} ${window.className}`}
          src={window.src}
          width={window.width}
          height={window.height}
          alt=""
          loading="lazy"
          decoding="async"
        />
      ))}
    </div>
  );
}

/* A picture's place on the page: a white card with a faint dot grid, the
   figure set into it. A described slot with no figure yet is pending and
   shows the hatched ground instead, at the size the picture will take. A
   live slot holds something to use rather than look at, so it keeps its own
   frame and takes the pointer. */
function Visual({
  label,
  className,
  live = false,
  bleed = false,
  children,
}: {
  label?: string;
  className?: string;
  live?: boolean;
  /* The picture fills the card edge to edge and lays itself out. */
  bleed?: boolean;
  children?: ReactNode;
}) {
  const described = (!children || bleed) && label;
  return (
    <div
      className={`${styles.visual} ${className ?? ""}`}
      data-pending={described && !children ? true : undefined}
      data-live={live || undefined}
      role={described ? "img" : undefined}
      aria-label={described ? label : undefined}
      aria-hidden={!children && !label ? true : undefined}
    >
      {children && bleed ? children : null}
      {children && !bleed ? <div className={live ? styles.visualLive : styles.visualInner}>{children}</div> : null}
    </div>
  );
}

function Feature({
  id,
  title,
  link,
  visual,
  reverse = false,
  children,
}: {
  id: string;
  title: ReactNode;
  link?: { href: string; label: string };
  visual: ReactNode;
  reverse?: boolean;
  children: ReactNode;
}) {
  return (
    <section className={styles.feature} data-reverse={reverse} aria-labelledby={id}>
      <div className={styles.featureText}>
        <h2 id={id}>{title}</h2>
        {children}
        {link ? (
          <Link className={styles.featureLink} href={link.href}>
            {link.label} <span aria-hidden="true">→</span>
          </Link>
        ) : null}
      </div>
      {visual}
    </section>
  );
}

export default function Home() {
  return (
    <main className={`site ${styles.page}`}>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: JSON.stringify(STRUCTURED_DATA) }}
      />
      <AutoSignIn />
      <SiteTopbar />

      <div className={styles.content}>
        <section className={styles.hero} aria-labelledby="lede">
          {/* The open-source announcement; retire it once it is old news. */}
          <a className={styles.announce} href={PLATFORM_REPO} target="_blank" rel="noopener noreferrer">
            Cantelop is now open source <span aria-hidden="true">→</span>
          </a>
          <h1 id="lede">Give every user their own agent.</h1>
          <p className={styles.lede}>
            Each user gets an isolated workspace with persistent files and state, running Claude Code,
            Codex, or your own harness. Connect it to any frontend with one SDK. No servers, no
            sandboxes to manage.
          </p>
          <SignInErrorNotice />
          <div className={styles.ctaRow}>
            <SignInButton className={`${bar.cta} ${bar.pill} ${styles.ctaPrimary}`}>
              Get started <span aria-hidden="true">→</span>
            </SignInButton>
            <Link className={styles.ctaSecondary} href="/docs">Read the docs</Link>
          </div>
        </section>

        <Visual
          className={styles.heroVisual}
          label="Each user reaches one App, which routes them to their own Workspace and the Sandboxes that mount it."
        >
          <AgentPerUserDiagram />
        </Visual>

        <div className={styles.features}>
          <Feature
            id="feature-frontend"
            title="Plug into any frontend"
            link={{ href: "/docs/ingress", label: "See how it works" }}
            visual={
              <Visual
                bleed
                label="The same kind of agent in three places: a model assistant inside a CAD app, a CRM assistant answering in a Slack thread, and a homebuyer assistant chatting in Telegram."
              >
                <FrontendStack />
              </Visual>
            }
          >
            <p>
              Connect your own app, Slack, Telegram, or anything that can call an API. Every user's
              conversation lands in their own workspace, with your MCP servers and tools on hand.
            </p>
          </Feature>

          <Feature
            id="feature-byo"
            title="Let users bring their own Claude / Codex subscriptions"
            link={{ href: "/blog/hosting-claude-code-bring-your-own-subscription", label: "Read how it works" }}
            reverse
            visual={
              <Visual live>
                <ClaudePlanExample />
              </Visual>
            }
          >
            <p>
              Cantelop can host unmodified versions of Claude Code and Codex, which lets users sign in
              and use their own subscriptions.
            </p>
          </Feature>

          <Feature
            id="feature-scale"
            title="Infrastructure that scales with your users"
            link={{ href: "/docs/architecture", label: "Read the architecture" }}
            visual={
              <Visual live>
                <ParallelSessionsSnippet />
              </Visual>
            }
          >
            <p>
              Run many sessions in parallel, all connected to the same workspace. Cantelop spins up a
              fully elastic sandbox for each session, so you never have to think about resource
              provisioning.
            </p>
          </Feature>
        </div>

        <section className={styles.listing} aria-labelledby="blog">
          <div className={styles.listingHead}>
            <h2 id="blog">From the blog</h2>
            <Link className={styles.listingMore} href="/blog">All posts <span aria-hidden="true">→</span></Link>
          </div>
          <div className={styles.posts}>
            {POSTS.slice(0, 4).map((post) => {
              const Art = POST_ART[post.slug];
              return (
                <article key={post.slug} className={styles.post}>
                  <Link className={styles.postLink} href={`/blog/${post.slug}`}>
                    <Visual className={styles.postArt}>
                      {Art ? <Art /> : null}
                    </Visual>
                    <time className={styles.date} dateTime={post.date}>{formatDate(post.date)}</time>
                    <h3 className={styles.postTitle}>{post.title}</h3>
                    <p className={styles.postSummary}>{post.summary}</p>
                  </Link>
                </article>
              );
            })}
          </div>
        </section>

        <section id="changelog" className={styles.listing} aria-labelledby="changelog-title">
          <div className={styles.listingHead}>
            <h2 id="changelog-title">Changelog</h2>
          </div>
          {/* Focusable so the row scrolls with the arrow keys. */}
          <ol className={styles.changes} tabIndex={0} aria-labelledby="changelog-title">
            {CHANGELOG.map((entry) => (
              <li key={`${entry.date}-${entry.title}`} className={styles.change}>
                <p className={styles.changeMeta}>
                  <time className={styles.date} dateTime={entry.date}>{formatDate(entry.date)}</time>
                  {entry.sdk ? <span className={styles.changeTag}>SDK {entry.sdk}</span> : null}
                </p>
                <h3 className={styles.changeTitle}>{entry.title}</h3>
                <p className={styles.changeSummary}>{inlineCode(entry.summary)}</p>
              </li>
            ))}
          </ol>
        </section>
      </div>

      <SiteFooter />
    </main>
  );
}
