import { notFound, redirect } from "next/navigation";
import type { ReactNode } from "react";

import { AppScreen, type AppRuntime } from "@/components/app-screen";
import { ConsoleShell } from "@/components/console-shell";
import {
  APP_ID_PATTERN,
  cachedCustomerApps,
  cachedCustomerReleases,
  cachedCustomerSnapshot,
  cachedCustomerQuota,
  cachedCustomerWorkspaces,
} from "@/lib/app-shell-data";
import { cachedCustomerFleet } from "@/lib/customer-fleets";
import { appBillingSummary } from "@/lib/instances";
import type { Release } from "@/lib/release-status";
import { getUserSession } from "@/lib/user-session";
import { viewerOf } from "@/lib/viewer";

export const metadata = { title: "App" };

// The frame around every App section page: one place resolves the App, its
// sidebar nav, and the header's deploy state; the pages below fetch only what
// their own section shows. The frame waits on control-database reads only;
// runtime state comes from the observer and streams in after the first paint.
export default async function AppLayout({
  params,
  children,
}: {
  params: Promise<{ id: string }>;
  children: ReactNode;
}) {
  const { id } = await params;
  if (!APP_ID_PATTERN.test(id)) notFound();

  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/apps/${id}`);

  // Runtime state feeds the header's sandbox count, billing time and the
  // sidebar's App tones; it degrades independently of the App record. The
  // section page shares this request-memoized read.
  const runtimeRead: Promise<AppRuntime> = cachedCustomerSnapshot(session.user.id)
    .then((snapshot) => ({ instances: snapshot.sandboxes, billingSummary: appBillingSummary(snapshot, id) }))
    .catch(() => null);
  // Build history degrades on its own; start it with the App record so the
  // frame waits on the slower read, not their sum.
  const releasesRead = cachedCustomerReleases(session.user.id, id).catch(() => null);
  const quotaRead = cachedCustomerQuota(session.user.id).catch(() => null);

  let appPage: Awaited<ReturnType<typeof cachedCustomerApps>> | null = null;
  let workspacePage: Awaited<ReturnType<typeof cachedCustomerWorkspaces>> | null = null;
  try {
    [appPage, workspacePage] = await Promise.all([
      cachedCustomerApps(session.user.id),
      cachedCustomerWorkspaces(session.user.id),
    ]);
  } catch (error) {
    if (error && typeof error === "object" && "digest" in error) throw error;
  }
  if (!appPage || !workspacePage) {
    return (
      <ConsoleShell viewer={viewerOf(session)} crumbs={[{ label: "Home", href: "/profile" }, { label: id }]}>
        <div className="notice error">App state is temporarily unavailable</div>
      </ConsoleShell>
    );
  }
  const app = appPage.items.find((candidate) => candidate.id === id);
  if (!app) notFound();
  const workspaces = workspacePage.items.filter((workspace) => workspace.app_id === id);

  // Build history is the App's deploy record: it degrades on its own so a
  // release-store outage still leaves the other sections usable.
  const releases: Release[] | null = await releasesRead;
  const quota = await quotaRead;
  // A Fleet installation's trail runs through its Fleet. Only the Fleet's
  // owner can read it; anyone else sees the plain App trail.
  const fleet = app.fleet_id
    ? await cachedCustomerFleet(session.user.id, app.fleet_id).catch(() => null)
    : null;

  return (
    <AppScreen
      app={app}
      workspaces={workspaces}
      allApps={appPage.items}
      allWorkspaces={workspacePage.items}
      maxApps={quota?.maxApps}
      usedApps={quota?.usedApps ?? null}
      runtime={runtimeRead}
      releases={releases}
      viewer={viewerOf(session)}
      fleet={fleet && { id: fleet.id, name: fleet.name }}
    >
      {children}
    </AppScreen>
  );
}
