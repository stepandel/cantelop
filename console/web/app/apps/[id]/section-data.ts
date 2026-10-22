import { notFound, redirect } from "next/navigation";

import { APP_ID_PATTERN, appScopedSnapshot, cachedCustomerSnapshot, cachedCustomerWorkspaces } from "@/lib/app-shell-data";
import { includeLinkedRequest } from "@/lib/linked-trace";
import type { Snapshot, Workspace } from "@/lib/telemetry-types";
import { getUserSession } from "@/lib/user-session";
import { observationRepository } from "@core/observability/control";
import type { AppTraceTree } from "@core/observability/trace-tree";

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

export type TraceSearchParams = { path?: string | string[]; session?: string | string[]; request?: string | string[] };

// The trace explorer's starting filters, from the page's query: one request
// path or Session, and a linked request a route's recent calls point at.
export function traceSearch({ path, session, request }: TraceSearchParams) {
  return {
    path: typeof path === "string" ? path : "",
    session: typeof session === "string" ? session : "",
    requestID: typeof request === "string" && /^req_[0-9a-f]{32}$/.test(request) ? request : "",
  };
}

// The App's trace tree, with a linked request that has aged out of it. The
// caller has already established the viewer may read the App. Null when the
// isolated store is unavailable: the client keeps the section up and retries.
export async function loadTraces(appId: string, requestID: string): Promise<AppTraceTree | null> {
  let initial: AppTraceTree | null = null;
  const repository = observationRepository();
  try {
    initial = await repository.traceTree(appId);
  } catch (error) {
    console.error("app_traces_unavailable", error);
  }
  if (requestID && !initial?.requests.some(({ id: seenID }) => seenID === requestID)) {
    try {
      const linked = await repository.requestTrace(appId, requestID);
      initial = initial ? includeLinkedRequest(initial, linked) : linked;
    } catch (error) {
      console.error("app_linked_trace_unavailable", error);
      // The client retries the linked request with the rest of the trace tree.
    }
  }
  return initial;
}
