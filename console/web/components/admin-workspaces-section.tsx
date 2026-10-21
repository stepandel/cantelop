"use client";

import type { AdminWorkspace } from "@core/db/admin-workspaces";
import type { DatabaseDrift } from "@core/workspace-databases/drift";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { CopyId } from "@/components/copy-id";
import { HeadStat, SectionHead } from "@/components/section-head";
import { databaseLabels } from "@/lib/database-labels";
import { useClock } from "@/lib/use-clock";

const statusLabels: Record<AdminWorkspace["status"], string> = {
  "active": "active",
  "deleting": "deleting",
  "deletion-failed": "Deletion failed",
  "unpurged": "Not purged",
};

/** Every live workspace and unfinished deletion on the platform. Null means
 *  the control database could not be read; the section stays up and says so. */
export function AdminWorkspacesSection({ workspaces }: { workspaces: AdminWorkspace[] | null }) {
  const { formatDate, formatStamp } = useClock();
  const router = useRouter();
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);
  const active = workspaces?.filter(({ status }) => status === "active").length ?? 0;
  const deleting = workspaces?.filter(({ status }) => status === "deleting").length ?? 0;
  const failed = workspaces?.filter(({ status }) => status === "deletion-failed").length ?? 0;
  const unpurged = workspaces?.filter(({ status }) => status === "unpurged").length ?? 0;

  async function purge(workspace: AdminWorkspace) {
    setBusyId(workspace.id);
    setMessage(null);
    try {
      const response = await fetch(`/api/admin/workspaces/${workspace.id}`, { method: "DELETE" });
      if (!response.ok) {
        setMessage({ kind: "error", content: response.status === 404 ? "The workspace no longer exists" : "The platform could not queue that deletion" });
        return;
      }
      setConfirmingId(null);
      setMessage({ kind: "status", content: `Deletion queued for ${workspace.app_slug}/${workspace.slug} — compute, files, and its database will be removed` });
      router.refresh();
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusyId(null);
    }
  }

  return (
    <>
      <SectionHead title="Workspaces" subtitle="All apps · at page load">
        {workspaces !== null && (
          <>
            <HeadStat label="Active" value={active} tone={active === 0 ? "dim" : undefined} />
            <HeadStat label="Deleting" value={deleting} tone={deleting === 0 ? "dim" : undefined} />
            <HeadStat label="Deletion failed" value={failed} tone={failed === 0 ? "dim" : undefined} />
            {unpurged > 0 && <HeadStat label="Not purged" value={unpurged} />}
          </>
        )}
      </SectionHead>
      <div className="section-panel">
        {message && (
          <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>
            {message.content}
          </div>
        )}
        {workspaces === null ? (
          <p className="empty-note">Workspaces unavailable — the control database could not be read</p>
        ) : workspaces.length === 0 ? (
          <p className="empty-note">No workspaces recorded on this platform</p>
        ) : (
          <div className="access-table-wrap">
            <table className="data-table admin-workspaces-table">
              <colgroup>
                <col className="c-workspace" />
                <col />
                <col className="c-state" />
                <col className="c-state" />
                <col className="c-stamp" />
                <col className="c-actions" />
              </colgroup>
              <thead>
                <tr>
                  <th>Workspace</th>
                  <th>App</th>
                  <th>Status</th>
                  <th>Database</th>
                  <th>Created</th>
                  <th><span className="visually-hidden">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {workspaces.map((workspace) => (
                  <tr key={workspace.id}>
                    <td className="ellip" title={workspace.id}>{workspace.slug}</td>
                    <td className="ellip" title={workspace.app_id}>{workspace.app_slug}</td>
                    <td>
                      <span
                        className={`state-cell st-${workspace.status}`}
                        title={workspace.archived_at ? `Deletion requested ${formatDate(workspace.archived_at)}` : undefined}
                      >
                        <span className={`dot dot-${workspace.status}`} aria-hidden="true" />
                        {statusLabels[workspace.status]}
                      </span>
                    </td>
                    <td>
                      {workspace.database ? (
                        <span className={`state-cell st-${workspace.database}`} title={workspace.database_name ?? "Name not yet reserved"}>
                          <span className={`dot dot-${workspace.database}`} aria-hidden="true" />
                          {databaseLabels[workspace.database]}
                        </span>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                    <td className="sub" title={formatDate(workspace.created_at)}>{formatStamp(workspace.created_at)}</td>
                    <td>
                      <div className="row-actions">
                        {workspace.status === "deleting" ? null : confirmingId === workspace.id ? (
                          <>
                            <button type="button" className="confirm-danger" disabled={busyId === workspace.id} onClick={() => void purge(workspace)}>
                              {busyId === workspace.id ? "Queueing…" : "Confirm"}
                            </button>
                            <button type="button" disabled={busyId === workspace.id} onClick={() => setConfirmingId(null)}>Cancel</button>
                          </>
                        ) : workspace.status === "active" ? (
                          <button type="button" className="danger" aria-label={`Delete ${workspace.app_slug}/${workspace.slug}`} disabled={busyId !== null} onClick={() => setConfirmingId(workspace.id)}>
                            Delete
                          </button>
                        ) : (
                          // Already archived: the data owner asked for deletion, so retrying needs no confirmation.
                          <button type="button" aria-label={`Retry deletion of ${workspace.app_slug}/${workspace.slug}`} disabled={busyId !== null} onClick={() => void purge(workspace)}>
                            {busyId === workspace.id ? "Queueing…" : workspace.status === "unpurged" ? "Purge" : "Retry"}
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <DatabaseDriftPanel />
    </>
  );
}

/** Compares Turso with the control database on request; never at page load. */
export function DatabaseDriftPanel({ initial = null }: { initial?: DatabaseDrift | null }) {
  const { formatStamp } = useClock();
  const [drift, setDrift] = useState<DatabaseDrift | null>(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function check() {
    setBusy(true);
    setError(null);
    try {
      const response = await fetch("/api/admin/workspace-databases/drift", { cache: "no-store" });
      if (!response.ok) {
        setError(response.status === 401 ? "Your operator session has ended — sign in again"
          : response.status === 503 ? "The database provider is not configured for this Console"
          : "The database provider could not be read");
        return;
      }
      setDrift(await response.json());
    } catch {
      setError("The control service is unavailable");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="section-panel">
      {error && <div className="notice error" role="alert">{error}</div>}
      {drift && (
        drift.missing.length === 0 && drift.orphaned.length === 0 ? (
          <p className="empty-note">
            {drift.provider_databases} Turso databases match the control database · checked {formatStamp(drift.checked_at)}
          </p>
        ) : (
          <table className="data-table">
            <colgroup>
              <col className="c-state" />
              <col />
              <col className="c-workspace" />
            </colgroup>
            <thead>
              <tr>
                <th>Drift</th>
                <th>Turso database</th>
                <th>Workspace</th>
              </tr>
            </thead>
            <tbody>
              {drift.missing.map((entry) => (
                <tr key={`missing-${entry.database_name}`}>
                  <td title="Ready in the control database, absent from Turso: credential requests fail">
                    <span className="state-cell st-failed"><span className="dot dot-failed" aria-hidden="true" />Missing</span>
                  </td>
                  <td className="ellip"><CopyId value={entry.database_name} /></td>
                  <td className="ellip" title={entry.workspace_id}>{entry.app_slug}/{entry.workspace_slug}</td>
                </tr>
              ))}
              {drift.orphaned.map((entry) => (
                <tr key={`orphaned-${entry.database_name}`}>
                  <td title={entry.workspace_id ? "Present in Turso after its Workspace was purged" : "Present in Turso with no control record"}>
                    <span className="state-cell st-unpurged"><span className="dot dot-unpurged" aria-hidden="true" />Orphaned</span>
                  </td>
                  <td className="ellip"><CopyId value={entry.database_name} /></td>
                  <td className="ellip" title={entry.workspace_id ?? undefined}>
                    {entry.workspace_id ? `${entry.app_slug}/${entry.workspace_slug}` : <span className="faint">None</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )
      )}
      <div className="add-row">
        <button type="button" onClick={() => void check()} disabled={busy}>
          {busy ? "Checking Turso…" : drift ? "Check database drift again" : "Check database drift"}
        </button>
      </div>
    </div>
  );
}
