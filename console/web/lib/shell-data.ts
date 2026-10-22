import type { ShellApp, ShellWorkspace } from "@/components/console-shell";
import type { CustomerApp } from "@/lib/customer-telemetry";
import type { Workspace } from "@/lib/telemetry-types";

interface RuntimePresence {
  app_id: string;
  state: string;
}

// Rolls an App's sidebar dot up from its Sandbox state. Without
// telemetry, no runtime status can be inferred safely.
export function appTone(
  appId: string,
  instances: RuntimePresence[] | null,
): ShellApp["tone"] {
  if (instances === null) {
    return "unknown";
  }

  const owned = instances.filter((instance) => instance.app_id === appId);
  if (owned.some(({ state }) => state === "running")) return "running";
  return "idle";
}

// Fleet installations stay out of the palette: an unlimited Fleet can hold
// thousands, and each is reached through its Fleet instead.
function standalone(apps: CustomerApp[]): CustomerApp[] {
  return apps.filter((app) => !app.fleet_id);
}

export function buildShellApps(
  apps: CustomerApp[],
  _workspaces: Workspace[],
  instances: RuntimePresence[] | null,
): ShellApp[] {
  return standalone(apps).map((app) => {
    const live = (instances ?? []).filter(
      (instance) => instance.app_id === app.id && instance.state === "running",
    ).length;
    return {
      id: app.id,
      slug: app.slug,
      tone: appTone(app.id, instances),
      count: instances === null ? undefined : String(live),
      role: app.role,
    };
  });
}

export function buildShellWorkspaces(apps: CustomerApp[], workspaces: Workspace[]): ShellWorkspace[] {
  const slugByAppId = new Map(standalone(apps).map((app) => [app.id, app.slug]));
  return workspaces.filter((workspace) => slugByAppId.has(workspace.app_id)).map((workspace) => ({
    id: workspace.id,
    appId: workspace.app_id,
    appSlug: slugByAppId.get(workspace.app_id) ?? workspace.app_id,
    slug: workspace.slug ?? workspace.id,
  }));
}
