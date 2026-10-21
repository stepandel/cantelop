"use client";

import Link from "next/link";
import { useState } from "react";

import { useSectionCrumb } from "@/components/app-screen";
import { CopyId } from "@/components/copy-id";
import { HeadStat, SectionHead } from "@/components/section-head";
import { formatAgo } from "@/lib/format";
import type { Session, Workspace } from "@/lib/telemetry-types";
import { useClock } from "@/lib/use-clock";

/** One Workspace: the Sessions living in it. Workspaces and Sessions are
 *  created by the SDK (`app.workspace({ slug }).session()`), so this page
 *  inspects and stops — it does not create. */
export function WorkspaceDetail({
  appId,
  workspace,
  initialSessions,
}: {
  appId: string;
  workspace: Workspace;
  // Null when the runtime snapshot could not be read.
  initialSessions: Session[] | null;
}) {
  const label = workspace.slug ?? workspace.id;
  useSectionCrumb(label);
  const { formatDate } = useClock();
  const [sessions, setSessions] = useState(initialSessions);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);
  const active = (sessions ?? []).filter(({ state }) => state === "active").length;

  async function stop(session: Session) {
    setBusyId(session.id); setMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/sessions/${session.id}`, { method: "DELETE" });
      if (!response.ok) throw new Error(response.status === 404 ? "That Session is no longer running." : "Could not stop the Session. Try again.");
      setSessions((current) => (current ?? []).filter(({ id }) => id !== session.id));
      setMessage({ kind: "status", content: `Stopped ${session.id}. Its history stays in the Workspace; the next dispatch starts it again.` });
    } catch (error) {
      setMessage({ kind: "error", content: error instanceof Error ? error.message : "Could not stop the Session." });
    } finally { setBusyId(null); }
  }

  return (
    <>
      <SectionHead
        title={label}
        subtitle={<><Link href={`/apps/${appId}/workspaces`}>Workspaces</Link>{" · "}<CopyId value={workspace.id} /></>}
      >
        {sessions !== null && <HeadStat label="Sessions" value={sessions.length} tone={sessions.length === 0 ? "dim" : undefined} />}
        {sessions !== null && <HeadStat label="Active" value={active} tone={active === 0 ? "dim" : undefined} />}
        <HeadStat label="Last activity" value={formatAgo(workspace.last_activity_at)} tone="quiet" title={workspace.last_activity_at && formatDate(workspace.last_activity_at)} />
      </SectionHead>

      <h2 className="section-label">Sessions</h2>
      <div className="section-panel">
        {message && <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>{message.content}</div>}
        {sessions === null ? (
          <p className="empty-note">Sessions are temporarily unavailable</p>
        ) : sessions.length === 0 ? (
          <p className="empty-note">No live Sessions</p>
        ) : (
          <table className="data-table">
            <colgroup>
              <col />
              <col className="c-state" />
              <col className="c-num" />
              <col className="c-time" />
              <col className="c-time" />
              <col className="c-actions" />
            </colgroup>
            <thead>
              <tr>
                <th>Session</th>
                <th>State</th>
                <th className="num" title="Seconds an idle Session stays warm before its Sandbox stops">Keep-alive</th>
                <th className="num">Started</th>
                <th className="num">Updated</th>
                <th><span className="visually-hidden">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((session) => (
                <tr key={session.id}>
                  <td className="ellip mono"><CopyId value={session.id} /></td>
                  <td>
                    <span className={`state-cell st-${session.state}`}>
                      <span className={`dot dot-${session.state}`} aria-hidden="true" />
                      {session.state}
                    </span>
                  </td>
                  <td className="num dim">{session.keep_alive_seconds}s</td>
                  <td className="num dim" title={formatDate(session.created_at)}>{formatAgo(session.created_at)}</td>
                  <td className="num dim" title={formatDate(session.updated_at)}>{formatAgo(session.updated_at)}</td>
                  <td>
                    <div className="row-actions">
                      <button type="button" className="danger" disabled={busyId !== null} onClick={() => void stop(session)}>
                        {busyId === session.id ? "stopping…" : "Stop"}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
