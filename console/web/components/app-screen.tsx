"use client";

import Link from "next/link";
import { useSelectedLayoutSegment } from "next/navigation";
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";

import { ConsoleShell } from "@/components/console-shell";
import { CopyId } from "@/components/copy-id";
import { accessLabel } from "@/lib/app-access";
import { appSections, isAppSectionId, type AppSectionId } from "@/lib/app-sections";
import type { CustomerApp } from "@/lib/customer-telemetry";
import { billingTimeLabel, billingTimeTitle } from "@/lib/billing-time";
import type { AppBillingSummary } from "@/lib/instances";
import { formatAgo } from "@/lib/format";
import { buildLabel, byNewest, definitionLabel, deployPhases, summarizeReleases, type Release } from "@/lib/release-status";
import { buildShellApps, buildShellWorkspaces } from "@/lib/shell-data";
import type { Sandbox, Workspace } from "@/lib/telemetry-types";
import { useClock } from "@/lib/use-clock";
import type { Viewer } from "@/lib/viewer";
import { usePolling } from "@/lib/use-polling";
import { useStreamed } from "@/lib/use-streamed";

// A deploy resolves in tens of seconds, so watch it closely while one is in
// flight and only keep a slow heartbeat otherwise — a deploy started from the
// CLI still surfaces here without a reload.
const DEPLOYING_POLL_MS = 5_000;
const RESTING_POLL_MS = 30_000;

// The screen already polls build history for its header; the builds section
// reads the same stream instead of running a second poll at the same endpoint.
const UpdateReleaseContext = createContext<(release: Release) => void>(() => {});
export function useUpdateAppRelease() { return useContext(UpdateReleaseContext); }

const BillingTimeContext = createContext<(summary: AppBillingSummary | undefined) => void>(() => {});
export function useUpdateBillingTime() { return useContext(BillingTimeContext); }

const ReleasesContext = createContext<Release[] | null>(null);
export function useAppReleases() {
  return useContext(ReleasesContext);
}

/** The account's runtime state as the frame shows it: every App's Sandboxes
 *  for the sidebar, and this App's billing time. Null when it is unavailable. */
export type AppRuntime = { instances: Sandbox[]; billingSummary?: AppBillingSummary } | null;

// A page below a section names itself as the breadcrumb leaf while mounted.
const CrumbContext = createContext<(leaf: string | undefined) => void>(() => {});
export function useSectionCrumb(leaf: string) {
  const setLeaf = useContext(CrumbContext);
  useEffect(() => {
    setLeaf(leaf);
    return () => setLeaf(undefined);
  }, [leaf, setLeaf]);
}

/** The frame every App section page renders inside: the shell with the App's
 *  section nav, the App header with its deploy state, and the section body as
 *  children. The Next layout at /apps/[id] owns the real instance; previews
 *  mount it directly with fixtures. Runtime state arrives as a promise: the
 *  observer is the slowest read, so the frame paints without it. */
export function AppScreen({
  app,
  workspaces,
  allApps,
  allWorkspaces,
  maxApps,
  usedApps,
  runtime: runtimeRead,
  releases: initialReleases,
  viewer,
  polling = true,
  section,
  sectionHrefBase,
  children,
}: {
  app: CustomerApp;
  workspaces: Workspace[];
  allApps: CustomerApp[];
  allWorkspaces: Workspace[];
  maxApps?: number | null;
  usedApps?: number | null;
  runtime: Promise<AppRuntime>;
  releases: Release[] | null;
  viewer: Viewer;
  polling?: boolean;
  // The active section. Defaults to the routed segment under /apps/[id];
  // previews, which render elsewhere, pass it explicitly.
  section?: AppSectionId;
  // Everything before the section id in a section link; previews swap the
  // path prefix for their own query prefix.
  sectionHrefBase?: string;
  children: ReactNode;
}) {
  const segment = useSelectedLayoutSegment();
  // No segment means /apps/[id] itself — the App's home.
  const activeSection: AppSectionId =
    section ?? (segment !== null && isAppSectionId(segment) ? segment : "home");
  // A page below a section, such as one route under the API section, adds
  // itself to the trail: the section becomes a link and the page is the leaf.
  // The page announces its leaf through CrumbContext once mounted.
  const [leaf, setLeaf] = useState<string | undefined>(undefined);

  const runtime = useStreamed(runtimeRead);
  const runtimePending = runtime === undefined;
  const instances = runtime?.instances ?? null;
  const [billingSummary, setBillingSummary] = useState<AppBillingSummary | undefined>(undefined);
  // A section page that reads its own snapshot may report billing time first.
  useEffect(() => {
    if (runtime?.billingSummary) setBillingSummary((current) => current ?? runtime.billingSummary);
  }, [runtime]);
  const [releases, setReleases] = useState(initialReleases);
  const { formatDate } = useClock();

  const { active, pending, failed } = summarizeReleases(releases);
  const deploying = pending !== undefined;

  const pollReleases = useCallback(async (signal: AbortSignal) => {
    try {
      const response = await fetch(`/api/customer/apps/${app.id}/deployments`, { cache: "no-store" });
      if (!response.ok) return;
      const page = await response.json() as { items?: Release[] };
      // A failed poll keeps the last good history rather than blanking the
      // screen; the next tick retries.
      if (!signal.aborted && Array.isArray(page.items)) setReleases(page.items);
    } catch { /* retried on the next tick */ }
  }, [app.id]);
  usePolling(pollReleases, deploying ? DEPLOYING_POLL_MS : RESTING_POLL_MS, { enabled: polling });

  const shellApps = buildShellApps(allApps, allWorkspaces, instances);
  const tone = shellApps.find(({ id }) => id === app.id)?.tone ?? "idle";
  const appRunning = (instances ?? []).filter(
    (instance) => instance.app_id === app.id && instance.state === "running",
  ).length;
  const history = byNewest(releases ?? []);
  const appUrl = `https://${app.hostname}`;
  const deployedAt = active?.activated_at;

  const sections = appSections(sectionHrefBase ?? `/apps/${app.id}/`, {
    workspaces: workspaces.length,
    builds: history.length,
  });
  const homeHref = sectionHrefBase === undefined ? `/apps/${app.id}` : `${sectionHrefBase}home`;

  return (
    <ConsoleShell
      viewer={viewer}
      apps={shellApps}
      maxApps={maxApps}
      usedApps={usedApps}
      workspaces={buildShellWorkspaces(allApps, allWorkspaces)}
      appContext={{ id: app.id, slug: app.slug, tone, sections, activeSection, homeHref }}
      crumbs={[
        { label: "Home", href: "/profile" },
        ...(activeSection === "home"
          ? [{ label: app.slug }]
          : [
            { label: app.slug, href: homeHref },
            ...(leaf === undefined
              ? [{ label: sections.find(({ id }) => id === activeSection)?.label ?? activeSection }]
              : [
                { label: sections.find(({ id }) => id === activeSection)?.label ?? activeSection, href: sections.find(({ id }) => id === activeSection)?.href },
                { label: leaf },
              ]),
          ]),
      ]}
    >
      {activeSection === "home" && (
        <div className="screen-head">
          <div className="head-id">
            <h1>
              {active
                ? <a href={appUrl} target="_blank" rel="noreferrer">{appUrl}</a>
                : appUrl}
            </h1>
            {/* The viewer's standing on the app, beside its id: an owner
                with their admin count, or an admin with the owner's name.
                The header is full at its five stats, so the fact lives on
                the subtitle line and links to the Access section. */}
            <span>
              <CopyId value={app.id} />
              {" · "}
              <Link href={sections.find(({ id }) => id === "access")?.href ?? homeHref}>{accessLabel(app)}</Link>
              {active?.definition && (
                <>
                  {" · "}
                  <Link href={sections.find(({ id }) => id === "builds")?.href ?? homeHref} title="Declared in cantelop.app({ ... }) — see the live release">
                    {definitionLabel(active.definition)}
                  </Link>
                </>
              )}
            </span>
          </div>
          <div className="stat">
            <span>Status</span>
            <strong className={`state ${tone === "degraded" ? "bad" : tone === "running" ? "ok" : tone === "provisioning" ? "info" : "dim"}`}>{runtimePending ? "—" : tone}</strong>
          </div>
          <div className="stat">
            <span>Release</span>
            <strong className={active ? undefined : "dim"} title={active?.api_artifact_digest}>{buildLabel(active)}</strong>
          </div>
          <div className="stat">
            <span>Deployed</span>
            <strong className={deployedAt ? "quiet" : "dim"} title={deployedAt ? formatDate(deployedAt) : undefined}>
              {formatAgo(deployedAt)}
            </strong>
          </div>
          {(runtimePending || instances !== null) && (
            <div className="stat">
              <span>Sandboxes</span>
              <strong className={appRunning === 0 ? "dim" : undefined}>{runtimePending ? "—" : appRunning}</strong>
            </div>
          )}
          <div className="stat">
            <span>Billing time</span>
            <strong title={billingTimeTitle(billingSummary)}>{billingTimeLabel(billingSummary)}</strong>
          </div>
        </div>
      )}

      {pending && (
        <div className="notice progress" role="status" aria-live="polite">
          <span className="spinner" aria-hidden="true" />
          {pending.status === "cancelling" ? "Cancelling" : "Deploying"} {buildLabel(pending)} · {deployPhases(pending).join(" · ")} · started {formatAgo(pending.created_at)}
        </div>
      )}
      {failed && (
        <div className="notice error" role="status">
          {buildLabel(failed)} failed to deploy{failed.error_code ? ` · ${failed.error_code}` : ""}
          {active ? ` · ${buildLabel(active)} is still live` : " · nothing is live for this app"}
        </div>
      )}
      {releases === null && (
        <div className="notice error" role="status">Build history is temporarily unavailable</div>
      )}

      <UpdateReleaseContext.Provider value={(release) => setReleases((current) => [release, ...(current ?? []).filter((item) => item.id !== release.id)])}>
      <ReleasesContext.Provider value={releases}>
      <BillingTimeContext.Provider value={setBillingSummary}>
      <CrumbContext.Provider value={setLeaf}>
        {children}
      </CrumbContext.Provider>
      </BillingTimeContext.Provider>
      </ReleasesContext.Provider>
      </UpdateReleaseContext.Provider>
    </ConsoleShell>
  );
}
