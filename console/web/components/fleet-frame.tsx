import type { ReactNode } from "react";

import { ConsoleShell, type Crumb } from "@/components/console-shell";
import { cachedCustomerApps, cachedCustomerQuota, cachedCustomerWorkspaces } from "@/lib/app-shell-data";
import { cachedCustomerFleets } from "@/lib/customer-fleets";
import { buildShellApps, buildShellWorkspaces } from "@/lib/shell-data";
import { viewerOf, type ViewerSession } from "@/lib/viewer";

// What the shell around the Fleet screens needs. The palette and the slot
// counter are conveniences: each read degrades on its own, and none waits on
// the observer, so App tones read as unknown here.
export async function loadFleetShell(userID: string) {
  const [apps, workspaces, quota, fleets] = await Promise.all([
    cachedCustomerApps(userID).then((page) => page.items, () => []),
    cachedCustomerWorkspaces(userID).then((page) => page.items, () => []),
    cachedCustomerQuota(userID).catch(() => null),
    cachedCustomerFleets(userID).catch(() => []),
  ]);
  return {
    apps: buildShellApps(apps, workspaces, null),
    workspaces: buildShellWorkspaces(apps, workspaces),
    fleets: fleets.map(({ id, name }) => ({ id, name })),
    maxApps: quota?.maxApps,
    usedApps: quota?.usedApps ?? null,
  };
}

// The shell around the Fleet screens.
export async function FleetFrame({ session, crumbs, children }: {
  session: ViewerSession;
  crumbs: Crumb[];
  children: ReactNode;
}) {
  const shell = await loadFleetShell(session.user.id);
  return (
    <ConsoleShell viewer={viewerOf(session)} {...shell} activeNav="fleets" crumbs={crumbs}>
      {children}
    </ConsoleShell>
  );
}
