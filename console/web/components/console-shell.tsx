"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { signOut } from "next-auth/react";
import { Fragment, type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { defaultUserQuota } from "@core/domain/model";
import { fragmentLinkClick, navigateToFragment } from "@/lib/fragment";
import { initialsOf } from "@/lib/format";
import { viewerName, type Viewer } from "@/lib/viewer";
import { BetaTag } from "./beta-tag";
import bar from "./site-topbar.module.css";

export interface ShellApp {
  id: string;
  slug: string;
  /** Rolled-up status; colors the sidebar dot. */
  tone?: "running" | "degraded" | "provisioning" | "idle" | "unknown";
  /** Right-aligned counter shown next to the slug (e.g. running Sandboxes). */
  count?: string;
  /** Apps the viewer only administers do not count against their own quota. */
  role?: "owner" | "admin";
}

export interface ShellWorkspace {
  id: string;
  appId: string;
  appSlug: string;
  slug: string;
}

/** One sidebar entry while an App is open: a section page of that App. */
export interface ShellSection {
  id: string;
  label: string;
  href: string;
  /** Renders a group heading above this item (e.g. "observability"). */
  group?: string;
  count?: string;
}

/** Set while an App is open: the sidebar lists the App's section pages
 *  instead of the account's apps, which live as cards on the home screen. */
export interface ShellAppContext {
  id: string;
  slug: string;
  tone?: ShellApp["tone"];
  sections: ShellSection[];
  activeSection?: string;
  /** The App's own home — its name in the sidebar links here. */
  homeHref: string;
}

export interface Crumb {
  label: string;
  href?: string;
}

interface PaletteItem {
  kind: "app" | "ws" | "nav" | "cmd";
  label: string;
  hint: string;
  href: string;
}

const APP_QUOTA = defaultUserQuota.maxApps;

function CommandPalette({
  items,
  onClose,
}: {
  items: PaletteItem[];
  onClose: () => void;
}) {
  const router = useRouter();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);

  // The input is the palette's only focusable control: keep focus there
  // while it is open and hand it back to the opener on close.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    return () => opener?.focus();
  }, []);

  const q = query.trim().toLowerCase();
  const visible = (q
    ? items.filter((item) => item.label.toLowerCase().includes(q) || item.kind.includes(q))
    : items
  ).slice(0, 8);
  const hot = Math.min(cursor, Math.max(0, visible.length - 1));

  const open = (item: PaletteItem | undefined) => {
    if (!item) return;
    onClose();
    navigateToFragment(router, item.href);
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setCursor((current) => Math.min(current + 1, visible.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setCursor((current) => Math.max(current - 1, 0));
    } else if (event.key === "Enter") {
      event.preventDefault();
      open(visible[hot]);
    } else if (event.key === "Tab") {
      // Nothing else focusable in the dialog; keep focus on the input.
      event.preventDefault();
    }
  };

  return (
    <div className="palette-overlay" onClick={onClose}>
      <div className="palette" role="dialog" aria-modal="true" aria-label="Go to" onClick={(event) => event.stopPropagation()}>
        <div className="palette-input">
          <span className="chev" aria-hidden="true">›</span>
          <input
            autoFocus
            role="combobox"
            aria-label="Go to app, workspace, or command"
            aria-expanded="true"
            aria-controls="palette-options"
            aria-activedescendant={visible.length > 0 ? `palette-option-${hot}` : undefined}
            value={query}
            placeholder="Go to app, workspace…"
            onChange={(event) => {
              setQuery(event.target.value);
              setCursor(0);
            }}
            onKeyDown={onKeyDown}
          />
          <span className="kbd">Esc</span>
        </div>
        <div className="palette-list" id="palette-options" role="listbox" aria-label="Destinations">
          {visible.map((item, index) => (
            <div
              key={`${item.kind}:${item.label}`}
              id={`palette-option-${index}`}
              role="option"
              aria-selected={index === hot}
              className={`palette-item${index === hot ? " hot" : ""}`}
              onMouseEnter={() => setCursor(index)}
              onClick={() => open(item)}
            >
              <span className="kind">{item.kind}</span>
              <span className="label">{item.label}</span>
              <span className="hint">{item.hint}</span>
            </div>
          ))}
          {visible.length === 0 && <div className="palette-item"><span className="kind">—</span><span className="label dim">No matches</span></div>}
        </div>
      </div>
    </div>
  );
}

/** The head of the sidebar: who is signed in, the provider's picture and login,
 *  standing in for the product mark that used to sit here. */
function ViewerMark({ viewer }: { viewer: Viewer }) {
  // A plain <img>: one small remote avatar per page earns no optimizer, and
  // no host allowlist has to stay in step with GitHub's or Google's CDN.
  return viewer.avatarUrl ? (
    <img className="viewer-mark" src={viewer.avatarUrl} alt="" width={20} height={20} />
  ) : (
    <span className="viewer-mark initials" aria-hidden="true">{initialsOf(viewer.email ?? viewer.login)}</span>
  );
}

export function ConsoleShell({
  viewer,
  apps = [],
  maxApps = APP_QUOTA,
  usedApps,
  workspaces = [],
  appContext,
  activeNav,
  crumbs,
  operator = false,
  children,
}: {
  viewer: Viewer;
  // Every app/workspace of the account: the command palette's index. The
  // sidebar itself no longer lists apps — they are cards on the home screen.
  apps?: ShellApp[];
  maxApps?: number | null;
  usedApps?: number | null;
  workspaces?: ShellWorkspace[];
  appContext?: ShellAppContext;
  activeNav?: "profile";
  crumbs: Crumb[];
  operator?: boolean;
  children: ReactNode;
}) {
  // Slot usage is server accounting (a Fleet counts once); never derive it
  // from listed Apps, which include Fleet installations.
  const usedAppSlots = usedApps ?? null;
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const paletteOpenRef = useRef(paletteOpen);
  paletteOpenRef.current = paletteOpen;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setPaletteOpen(!paletteOpenRef.current);
      }
      if (event.key === "Escape") {
        setPaletteOpen(false);
        setNavOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const paletteItems = useMemo<PaletteItem[]>(() => {
    const workspaceCounts = new Map<string, number>();
    for (const workspace of workspaces) {
      workspaceCounts.set(workspace.appId, (workspaceCounts.get(workspace.appId) ?? 0) + 1);
    }
    const items: PaletteItem[] = [];
    // Inside an App, its own screens come first — they are what "go to"
    // most likely means from here.
    if (appContext) {
      items.push({
        kind: "nav",
        label: "Overview",
        hint: appContext.slug,
        href: appContext.homeHref,
      });
      for (const section of appContext.sections) {
        items.push({
          kind: "nav",
          label: section.label,
          hint: appContext.slug,
          href: section.href,
        });
      }
    }
    for (const app of apps) {
      items.push({
        kind: "app",
        label: app.slug,
        hint: `${workspaceCounts.get(app.id) ?? 0} workspaces`,
        href: `/apps/${app.id}`,
      });
    }
    // A Workspace is no longer a destination of its own — it is a scope inside
    // its App's observability pages — but it is still the name people search by,
    // so the palette keeps the entry and lands on the App that owns it.
    for (const workspace of workspaces) {
      items.push({
        kind: "ws",
        label: `${workspace.appSlug} / ${workspace.slug}`,
        hint: `Workspace of ${workspace.appSlug}`,
        href: `/apps/${workspace.appId}`,
      });
    }
    if (!operator) {
      items.push({ kind: "cmd", label: "Create app", hint: `${usedAppSlots ?? "—"} / ${maxApps ?? "—"} used`, href: "/profile#new-app" });
      items.push({ kind: "cmd", label: "Start from an example", hint: "Prompt for your coding agent", href: "/profile#example" });
      items.push({ kind: "cmd", label: "Home", hint: "Apps & account", href: "/profile" });
    }
    return items;
  }, [appContext, apps, workspaces, operator, maxApps, usedAppSlots]);

  return (
    <div className="shell">
      <aside
        id="shell-sidebar"
        className={`sidebar${navOpen ? " open" : ""}`}
        onClick={(event) => {
          // Any navigation inside the drawer closes it.
          if ((event.target as HTMLElement).closest("a")) setNavOpen(false);
        }}
      >
        <div className="sidebar-head">
          <Link
            className={`side-viewer${activeNav === "profile" ? " active" : ""}`}
            href={operator ? "/admin" : "/profile"}
            title={operator ? "Operator console" : `${viewer.email ?? "account"} · Account usage`}
          >
            <ViewerMark viewer={viewer} />
            <span className="viewer-name">{viewerName(viewer)}</span>
          </Link>
          <BetaTag />
        </div>
        <div className="side-label">
          {/* The section head doubles as the way back up a level, a role the
              product mark used to hold. */}
          {operator ? (
            <span>Host</span>
          ) : appContext ? (
            <Link className="side-index" href="/profile">‹ Home</Link>
          ) : (
            <>
              <span>Console</span>
              {/* A Fleet counts once; shared Apps consume no slots here. */}
              <b title={`${usedAppSlots ?? "—"} of ${maxApps ?? "—"} app slots`}>{usedAppSlots ?? "—"} / {maxApps ?? "—"}</b>
            </>
          )}
        </div>
        {appContext && (
          <Link
            className={`side-app${appContext.activeSection === "home" ? " active" : ""}`}
            href={appContext.homeHref}
            title={appContext.id}
            aria-current={appContext.activeSection === "home" ? "page" : undefined}
          >
            <span className={`dot dot-${appContext.tone ?? "idle"}`} aria-hidden="true" />
            <span className="slug">{appContext.slug}</span>
          </Link>
        )}
        <nav className="side-nav" aria-label={appContext ? `${appContext.slug} sections` : operator ? "Operator" : "Console"}>
          {operator && !appContext ? (
            <span className="side-item active">
              <span className="dot dot-running" aria-hidden="true" />
              <span className="slug">Telemetry</span>
            </span>
          ) : appContext ? (
            appContext.sections.map((section) => (
              <Fragment key={section.id}>
                {section.group && <span className="side-group">{section.group}</span>}
                <Link
                  className={`side-item${section.id === appContext.activeSection ? " active" : ""}`}
                  href={section.href}
                  aria-current={section.id === appContext.activeSection ? "page" : undefined}
                >
                  <span className="slug">{section.label}</span>
                  {section.count !== undefined && <span className="count">{section.count}</span>}
                </Link>
              </Fragment>
            ))
          ) : (
            <>
              <Link className={`side-item${activeNav === "profile" ? " active" : ""}`} href="/profile">
                <span className="slug">Home</span>
              </Link>
              <Link className="side-new" href="/profile#new-app" onClick={fragmentLinkClick}>
                <i aria-hidden="true">+</i>
                <span>New app</span>
              </Link>
            </>
          )}
        </nav>
      </aside>

      {navOpen && <button type="button" className="nav-scrim" aria-label="Close menu" onClick={() => setNavOpen(false)} />}

      <div className="workarea">
        <header className="topbar">
          <div className="topbar-inner">
            <button
              type="button"
              className="nav-toggle"
              aria-expanded={navOpen}
              aria-controls="shell-sidebar"
              onClick={() => setNavOpen((current) => !current)}
            >
              Menu
            </button>
            <nav className="crumbs" aria-label="Breadcrumb">
              {crumbs.map((crumb, index) => (
                <Fragment key={`${crumb.label}-${index}`}>
                  {index > 0 && <span className="sep" aria-hidden="true">/</span>}
                  {crumb.href ? <Link href={crumb.href}>{crumb.label}</Link> : <span className="here">{crumb.label}</span>}
                </Fragment>
              ))}
            </nav>
            <div className="topbar-right">
              <Link href="/docs">Docs</Link>
              <Link href="/source">Source code</Link>
              {/* Who is signed in is the sidebar head's job; this end of the
                  bar only holds the way out. It is the front door's Sign in
                  button, so the two ends of a session look alike. No
                  confirmation page: signing out is one click, and the session
                  ends on the home page it started from. */}
              <button type="button" className={bar.cta} onClick={() => void signOut({ callbackUrl: "/" })}>
                Sign out
              </button>
            </div>
          </div>
        </header>
        <div className="content"><div className="pane">{children}</div></div>
      </div>

      {paletteOpen && <CommandPalette items={paletteItems} onClose={() => setPaletteOpen(false)} />}
    </div>
  );
}
