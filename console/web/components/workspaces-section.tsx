"use client";

import { useAppReleases } from "@/components/app-screen";
import { CopyId } from "@/components/copy-id";
import { HeadStat, SectionHead } from "@/components/section-head";
import { databaseLabels } from "@/lib/database-labels";
import { formatAgo } from "@/lib/format";
import { summarizeReleases } from "@/lib/release-status";
import { useClock } from "@/lib/use-clock";
import type { Sandbox, Workspace } from "@/lib/telemetry-types";
import type { WorkspaceDatabaseState } from "@core/db/workspace-database-states";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";

/** The App's workspace inventory. Workspaces are created by the SDK the first
 *  time a backend dispatches to `app.workspace({ slug })`, so the console lists,
 *  opens and deletes them but does not create them. As the landing section,
 *  its header also carries the App's public URL and id. */
export function WorkspacesSection({
  appId,
  appHostname,
  workspaces,
  instances,
  databases = null,
}: {
  appId: string;
  appHostname: string;
  workspaces: Workspace[];
  // Null degrades the sessions column alone; the list still renders.
  instances: Sandbox[] | null;
  // Null degrades the database column alone.
  databases?: Record<string, WorkspaceDatabaseState> | null;
}) {
  const router = useRouter();
  const { formatDate, formatStamp } = useClock();
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);
  const { active } = summarizeReleases(useAppReleases());
  const appUrl = `https://${appHostname}`;
  const runningOf = (workspaceId: string) =>
    (instances ?? []).filter(
      (instance) => instance.workspace_id === workspaceId && instance.state === "running",
    ).length;
  const liveSessions = workspaces.reduce((total, workspace) => total + runningOf(workspace.id), 0);

  async function deleteWorkspace(workspace: Workspace) {
    setBusyId(workspace.id);
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/workspaces/${workspace.id}`, { method: "DELETE" });
      if (!response.ok) {
        setMessage({
          kind: "error",
          content: response.status === 404 ? "The workspace is no longer available" : "The platform could not delete that workspace",
        });
        return;
      }
      setConfirmingId(null);
      setMessage({ kind: "status", content: "Workspace deletion queued — sessions, messages, and durable files will be permanently removed" });
      router.refresh();
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusyId(null);
    }
  }

  return (
    <>
      <SectionHead
        title="Workspaces"
        subtitle={
          <>
            {active ? <a href={appUrl} target="_blank" rel="noreferrer">{appUrl}</a> : appUrl}
            {" · "}
            <CopyId value={appId} />
          </>
        }
      >
        <HeadStat label="Workspaces" value={workspaces.length} tone={workspaces.length === 0 ? "dim" : undefined} />
        {instances !== null && (
          <HeadStat label="Live sessions" value={liveSessions} tone={liveSessions === 0 ? "dim" : undefined} />
        )}
      </SectionHead>
      <div className="section-panel">
        {message && (
          <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>
            {message.content}
          </div>
        )}
        {workspaces.length === 0 ? (
          <p className="empty-note">No workspaces yet</p>
        ) : (
          <div className="access-table-wrap">
            <table className={`data-table${databases !== null ? " workspaces-table" : ""}`}>
              <colgroup>
                <col className="c-id" />
                {instances !== null && <col className="c-num" />}
                {databases !== null && <col />}
                <col className="c-stamp" />
                <col className="c-time" />
                <col className="c-actions" />
              </colgroup>
              <thead>
                <tr>
                  <th>Workspace</th>
                  {instances !== null && <th className="num">Sessions</th>}
                  {databases !== null && <th>Database</th>}
                  <th>Created</th>
                  <th className="num">Activity</th>
                  <th><span className="visually-hidden">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {workspaces.map((workspace) => {
                  const live = runningOf(workspace.id);
                  return (
                    <tr key={workspace.id}>
                      <td className="ellip" title={workspace.id}>
                        <Link href={`/apps/${appId}/workspaces/${workspace.id}`}>{workspace.slug ?? workspace.id}</Link>
                      </td>
                      {instances !== null && <td className={`num${live === 0 ? " faint" : ""}`}>{live}</td>}
                      {databases !== null && <DatabaseCell state={databases[workspace.id]} />}
                      <td className="sub" title={workspace.created_at && formatDate(workspace.created_at)}>
                        {workspace.created_at ? formatStamp(workspace.created_at) : "—"}
                      </td>
                      <td className="num dim">{formatAgo(workspace.last_activity_at)}</td>
                      <td>
                        <div className="row-actions">
                          {confirmingId === workspace.id ? (
                            <>
                              <button type="button" className="confirm-danger" disabled={busyId === workspace.id} onClick={() => void deleteWorkspace(workspace)}>
                                {busyId === workspace.id ? "Deleting…" : "Delete permanently"}
                              </button>
                              <button type="button" disabled={busyId === workspace.id} onClick={() => setConfirmingId(null)}>Cancel</button>
                            </>
                          ) : (
                            <button type="button" className="danger" aria-label={`Delete ${workspace.slug ?? workspace.id}`} disabled={busyId !== null} onClick={() => setConfirmingId(workspace.id)}>
                              Delete
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

const databaseHints: Partial<Record<WorkspaceDatabaseState, string>> = {
  "on-demand": "The database is created when the app first requests database access",
  provisioning: "Database credentials are available once provisioning finishes",
  failed: "The platform stopped retrying database provisioning; contact support",
};

function DatabaseCell({ state }: { state: WorkspaceDatabaseState | undefined }) {
  // A Workspace created after the page's database read has no state yet.
  const shown = state ?? "provisioning";
  return (
    <td>
      <span className={`state-cell st-${shown}`} title={databaseHints[shown]}>
        <span className={`dot dot-${shown}`} aria-hidden="true" />
        {databaseLabels[shown]}
      </span>
    </td>
  );
}
