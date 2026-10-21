import { cache } from "react";

import { getCustomerConfig } from "@/lib/customer-config";
import { getCustomerReleases } from "@/lib/customer-deployments";
import { cachedCustomerWorkspaces, getCustomerApps, getCustomerQuota, getCustomerSnapshot } from "@/lib/customer-telemetry";
import type { Snapshot, Workspace } from "@/lib/telemetry-types";

export const APP_ID_PATTERN = /^app_[0-9a-f]{32}$/;

// Per-request memoized reads. The app layout and the section page it wraps
// both reach for account state on a full load; cache() collapses the double
// read into one fetch per request. The Workspace read is memoized where the
// snapshot reads it too, so all three share one.
export { cachedCustomerWorkspaces };
export const cachedCustomerApps = cache(getCustomerApps);
export const cachedCustomerSnapshot = cache(getCustomerSnapshot);
export const cachedCustomerReleases = cache(getCustomerReleases);
export const cachedCustomerConfig = cache(getCustomerConfig);
export const cachedCustomerQuota = cache(getCustomerQuota);

// The account snapshot narrowed to one App: its Workspaces, Sessions and
// Sandboxes only.
export function appScopedSnapshot(account: Snapshot, appId: string, workspaces: Workspace[]): Snapshot {
  return {
    ...account,
    workspaces,
    sessions: account.sessions.filter((instance) => instance.app_id === appId),
    sandboxes: account.sandboxes.filter((sandbox) => sandbox.app_id === appId),
  };
}
