import { notFound } from "next/navigation";

import { listWorkspaceDatabaseStates, type WorkspaceDatabaseState } from "@core/db/workspace-database-states";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { WorkspacesSection } from "@/components/workspaces-section";
import { cachedCustomerApps, cachedCustomerSnapshot, cachedCustomerWorkspaces } from "@/lib/app-shell-data";
import type { Sandbox, Workspace } from "@/lib/telemetry-types";

export const dynamic = "force-dynamic";

export const metadata = { title: "Workspaces" };

export default async function WorkspacesPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "workspaces");

  // Runtime and database state are optional columns. Start their reads with
  // the App inventory instead of adding their latency after it.
  const snapshotRead = cachedCustomerSnapshot(session.user.id).catch((error) => {
    console.error("workspaces page: runtime snapshot unavailable", error);
    return null;
  });
  const databasesRead = listWorkspaceDatabaseStates(session.user.id, id).catch((error) => {
    console.error("workspaces page: database states unavailable", error);
    return null;
  });

  let app: Awaited<ReturnType<typeof cachedCustomerApps>>["items"][number] | undefined;
  let workspaces: Workspace[] | null = null;
  try {
    const [appPage, workspacePage] = await Promise.all([
      cachedCustomerApps(session.user.id),
      cachedCustomerWorkspaces(session.user.id),
    ]);
    app = appPage.items.find((candidate) => candidate.id === id);
    workspaces = workspacePage.items.filter((workspace) => workspace.app_id === id);
  } catch (error) {
    if (error && typeof error === "object" && "digest" in error) throw error;
    workspaces = null;
  }
  if (workspaces === null) {
    return <div className="notice error">Workspace records are temporarily unavailable</div>;
  }
  if (!app) notFound();

  // Null degrades the sessions column alone.
  const instances: Sandbox[] | null =
    (await snapshotRead)?.sandboxes.filter((sandbox) => sandbox.app_id === id) ?? null;

  // Null degrades the database column alone.
  const databases: Record<string, WorkspaceDatabaseState> | null = await databasesRead;

  return <WorkspacesSection appId={id} appHostname={app.hostname} workspaces={workspaces} instances={instances} databases={databases} />;
}
