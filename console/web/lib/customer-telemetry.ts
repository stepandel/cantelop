import "server-only";

import { cache } from "react";

import { productControl } from "@core/product-control";
import type { PublicApp, PublicWorkspace } from "@core/product-service";
import type { HostWorkspace, RequestEventPage, Session, Snapshot, Workspace } from "@/lib/telemetry-types";
import { getRequestEvents, getSnapshot, maxSnapshotWorkspaces } from "@/lib/fire-fuse";

export type CustomerApp = PublicApp;

interface CustomerTelemetryDependencies {
  workspaces(userID: string): Promise<{ items: PublicWorkspace[] }>;
  requestEvents(search: URLSearchParams): Promise<RequestEventPage>;
  // Without workspaceIDs the observer returns the whole host.
  snapshot(workspaceIDs?: readonly string[]): Promise<Snapshot<HostWorkspace>>;
}

export async function getCustomerWorkspaces(userID: string): Promise<{ items: PublicWorkspace[] }> {
  return productControl().listWorkspaces(userID);
}

// Request-memoized, so the App layout, its section page and the snapshot below
// share one Workspace read per render. Outside a render it reads every time.
export const cachedCustomerWorkspaces = cache(getCustomerWorkspaces);

const defaultDependencies: CustomerTelemetryDependencies = {
  workspaces: cachedCustomerWorkspaces,
  requestEvents: getRequestEvents,
  snapshot: getSnapshot,
};

// Every runtime screen is now App-scoped, but the account roll-ups (dashboard
// sidebar, profile) still read the whole account, so the scope is optional and
// composable rather than a required argument.
export interface SnapshotScope {
  appID?: string;
  workspaceID?: string;
}

export async function getCustomerSnapshot(
  userID: string,
  scope: SnapshotScope = {},
  dependencies: CustomerTelemetryDependencies = defaultDependencies,
): Promise<Snapshot<Workspace>> {
  const workspacePage = await dependencies.workspaces(userID);
  if (!workspacePage || !Array.isArray(workspacePage.items)) {
    throw new Error("product control returned invalid customer Workspaces");
  }
  const workspaces = workspacePage.items.filter((workspace) =>
    (scope.workspaceID === undefined || workspace.id === scope.workspaceID) &&
    (scope.appID === undefined || workspace.app_id === scope.appID));
  // The observer reads only the tenant's Workspaces. A tenant too large for
  // one scoped query reads the whole host; the ownership filter below applies
  // either way.
  const workspaceIDs = workspaces.map((workspace) => workspace.id);
  const observerSnapshot = await dependencies.snapshot(
    workspaceIDs.length <= maxSnapshotWorkspaces ? workspaceIDs : undefined,
  );
  if (!observerSnapshot || typeof observerSnapshot.ready !== "boolean" ||
      !Array.isArray(observerSnapshot.sessions) || !Array.isArray(observerSnapshot.sandboxes)) {
    throw new Error("observer returned invalid customer runtime state");
  }
  const ownedByID = new Map(workspaces.map((workspace) => [workspace.id, workspace]));
  const sessions = observerSnapshot.sessions
    .filter((instance) => ownedByID.has(instance.workspace_id))
    .map((instance) => withOwnedWorkspace(instance, ownedByID.get(instance.workspace_id)!));
  const sandboxes = observerSnapshot.sandboxes
    .filter((sandbox) => ownedByID.has(sandbox.workspace_id))
    .map((sandbox) => ({ ...sandbox, app_id: ownedByID.get(sandbox.workspace_id)!.app_id }));
  return {
    ready: observerSnapshot.ready,
    observed_at: observerSnapshot.observed_at,
    workspaces,
    sessions,
    sandboxes,
  };
}

export async function getCustomerRequestEvents(
  userID: string,
  search: URLSearchParams,
  dependencies: CustomerTelemetryDependencies = defaultDependencies,
): Promise<RequestEventPage> {
  const workspaceID = search.get("workspace_id");
  const appID = search.get("app_id");
  if (workspaceID === null && appID === null) throw new TypeError("request history requires an App or Workspace scope");
  const workspacePage = await dependencies.workspaces(userID);
  if (!workspacePage || !Array.isArray(workspacePage.items)) {
    throw new Error("product control returned invalid customer Workspaces");
  }
  const workspaces = workspacePage.items.filter((workspace) =>
    (workspaceID === null || workspace.id === workspaceID) &&
    (appID === null || workspace.app_id === appID),
  );
  if (workspaces.length === 0) return { items: [] };
  const observerSearch = new URLSearchParams({ limit: search.get("limit") ?? "50" });
  if (workspaceID !== null) observerSearch.set("workspace_id", workspaceID);
  else if (appID !== null) observerSearch.set("app_id", appID);
  const before = search.get("before_sequence");
  if (before !== null) observerSearch.set("before_sequence", before);
  const after = search.get("after_sequence");
  if (after !== null) observerSearch.set("after_sequence", after);
  const page = await dependencies.requestEvents(observerSearch);
  if (!page || !Array.isArray(page.items)) throw new Error("observer returned invalid request history");
  // The observer limits before returning the page. App history includes
  // archived Workspaces until host cleanup, so filtering those events here
  // could hide active events behind an apparently empty first page.
  if (workspaceID === null) return page;
  const owned = new Set(workspaces.map((workspace) => workspace.id));
  return { ...page, items: page.items.filter((event) => owned.has(event.workspace_id)) };
}

export async function getCustomerQuota(userID: string) {
  return productControl().getUserQuota(userID);
}

export async function getCustomerApps(userID: string) {
  return productControl().listApps(userID) as Promise<{ items: CustomerApp[] }>;
}

function withOwnedWorkspace(instance: Session, workspace: PublicWorkspace): Session {
  return {
    ...instance,
    app_id: workspace.app_id,
    workspace_id: workspace.id,
  };
}
