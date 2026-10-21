import { notFound, redirect } from "next/navigation";

import { APP_ID_PATTERN, appScopedSnapshot, cachedCustomerSnapshot, cachedCustomerWorkspaces } from "@/lib/app-shell-data";
import type { Snapshot, Workspace } from "@/lib/telemetry-types";
import { getUserSession } from "@/lib/user-session";

// The guard every section page opens with. The layout runs the same checks
// for the frame; the page still owns its own access to the data it shows.
// No section means the App's home, /apps/[id] itself.
export async function requireAppSession(id: string, section?: string) {
  if (!APP_ID_PATTERN.test(id)) notFound();
  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/apps/${id}${section ? `/${section}` : ""}`);
  return session;
}

// What every observability page mounts on: the App's Workspaces and its slice
// of the account snapshot. The reads are request-memoized, so on a full load
// they collapse into the layout's own fetches.
export async function loadRuntime(userId: string, appId: string): Promise<{
  workspaces: Workspace[];
  snapshot: Snapshot | null;
}> {
  // Start both reads together. The snapshot scopes its observer read with the
  // same request-memoized Workspace read, so the inventory is read once.
  const [workspacePage, account] = await Promise.all([
    cachedCustomerWorkspaces(userId).catch(() => null),
    cachedCustomerSnapshot(userId).catch(() => null),
  ]);
  // An unavailable workspace list only hides the scope picker; the snapshot
  // read degrades the screen.
  const workspaces = workspacePage?.items.filter((workspace) => workspace.app_id === appId) ?? [];
  let snapshot: Snapshot | null = null;
  if (account) {
    try {
      snapshot = appScopedSnapshot(account, appId, workspaces);
    } catch {
      snapshot = null;
    }
  }
  return { workspaces, snapshot };
}
