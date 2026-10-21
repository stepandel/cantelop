import { notFound } from "next/navigation";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { WorkspaceDetail } from "@/components/workspace-detail";
import { cachedCustomerWorkspaces } from "@/lib/app-shell-data";
import { listCustomerSessions, type CustomerSession } from "@/lib/customer-operations";

export const dynamic = "force-dynamic";
export const metadata = { title: "Workspace" };

export default async function WorkspacePage({ params }: { params: Promise<{ id: string; workspace: string }> }) {
  const { id, workspace: workspaceID } = await params;
  const session = await requireAppSession(id, "workspaces");
  if (!/^wsp_[0-9a-f]{32}$/.test(workspaceID)) notFound();

  let workspace;
  try {
    workspace = (await cachedCustomerWorkspaces(session.user.id)).items
      .find((candidate) => candidate.id === workspaceID && candidate.app_id === id);
  } catch (error) {
    console.error("workspace_records_unavailable", error);
    return <div className="notice error">Workspace records are temporarily unavailable</div>;
  }
  if (!workspace) notFound();

  // Null degrades the Sessions table alone.
  let sessions: CustomerSession[] | null = null;
  try {
    sessions = (await listCustomerSessions(session.user.id, id)).items.filter((item) => item.workspace_id === workspaceID);
  } catch (error) {
    console.error("workspace_sessions_unavailable", error);
    sessions = null;
  }

  return <WorkspaceDetail appId={id} workspace={workspace} initialSessions={sessions} />;
}
