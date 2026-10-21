"use client";

import { Fragment, useState } from "react";
import { CopyId } from "@/components/copy-id";
import { DeploymentLogs } from "@/components/deployment-logs";
import { useAppReleases, useUpdateAppRelease } from "@/components/app-screen";
import { HeadStat, SectionHead } from "@/components/section-head";
import { formatAgo } from "@/lib/format";
import { buildLabel, byNewest, type Release, componentToneClass, componentProgress, providerProgress, rollbackTarget, summarizeReleases } from "@/lib/release-status";
import { useClock } from "@/lib/use-clock";

/** The App's deploy record. Releases arrive from the surrounding AppScreen,
 *  which polls them for its deploy notices — this table is another reader of
 *  that stream, so it stays live without a poll of its own. */
/** "sha256:c55637220cc6…" — enough to tell builds apart; the full digest is in the title and the details. */
function shortDigest(digest: string) {
  const [algorithm, hex] = digest.includes(":") ? digest.split(":", 2) : ["", digest];
  return `${algorithm ? `${algorithm}:` : ""}${hex.slice(0, 12)}`;
}

export function BuildsSection() {
  const releases = useAppReleases();
  const updateRelease = useUpdateAppRelease();
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState("");
  const [actionError, setActionError] = useState("");
  // Releases whose Build the host no longer stores; they cannot be rolled back to.
  const [unavailable, setUnavailable] = useState<ReadonlySet<string>>(new Set());
  async function recover(release: NonNullable<typeof releases>[number], action: "cancel" | "restart") {
    setBusy(release.id); setNotice(""); setActionError("");
    try {
      const response = await fetch(`/api/customer/apps/${release.app_id}/deployments/${release.id}/${action}`, { method: "POST" });
      if (!response.ok) throw new Error(response.status === 409 ? "The deployment changed or another deployment is pending. Refresh and try again." : "Could not request deployment recovery. Try again.");
      updateRelease(await response.json());
      setNotice(action === "restart" ? "Restart requested. A new release will use the uploaded artifacts after cancellation completes." : "Cancellation requested. Completed provider changes are not rolled back.");
    } catch (error) { setActionError(error instanceof Error ? error.message : "Could not request deployment recovery."); }
    finally { setBusy(null); }
  }
  async function rollback(target: Release) {
    if (!window.confirm(`Roll back to ${buildLabel(target)}? It becomes the live release; the current one stays in history.`)) return;
    setBusy("rollback"); setNotice(""); setActionError("");
    try {
      const response = await fetch(`/api/customer/apps/${target.app_id}/deployments/rollback`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ target_release_id: target.id }),
      });
      if (response.status === 410) {
        setUnavailable((current) => new Set(current).add(target.id));
        throw new Error(`${buildLabel(target)}'s Build is no longer stored on the host, so it can't be rolled back to. Deploy that version again to restore it.`);
      }
      if (!response.ok) throw new Error(response.status === 409 ? "The rollback target changed or another deployment is in progress. Refresh and try again." : "Could not request the rollback. Try again.");
      updateRelease(await response.json());
      setNotice(`Rollback requested. ${buildLabel(target)} becomes live once activation completes.`);
    } catch (error) { setActionError(error instanceof Error ? error.message : "Could not request the rollback."); }
    finally { setBusy(null); }
  }
  // Unfinished and failed deploys open by default — they are the news; a
  // viewer's own click overrides that either way.
  const [toggled, setToggled] = useState<Record<string, boolean>>({});
  const isOpen = (release: Release) => toggled[release.id] ?? ["queued", "provisioning", "failed", "cancelling"].includes(release.status);
  const toggle = (release: Release) => setToggled((current) => ({ ...current, [release.id]: !isOpen(release) }));
  const { formatDate } = useClock();
  const history = byNewest(releases ?? []);
  const { active, pending } = summarizeReleases(releases);
  const deployedAt = active?.activated_at;
  const previous = active && !pending ? rollbackTarget(releases) : undefined;

  return (
    <>
      <SectionHead title="Releases">
        <HeadStat
          label="Live build"
          value={buildLabel(active)}
          tone={active ? undefined : "dim"}
          title={active?.api_artifact_digest}
        />
        <HeadStat
          label="Deployed"
          value={formatAgo(deployedAt)}
          tone={deployedAt ? "quiet" : "dim"}
          title={deployedAt ? formatDate(deployedAt) : undefined}
        />
        {previous && (
          <div className="head-action">
            <button
              type="button"
              className="button"
              disabled={busy !== null || unavailable.has(previous.id)}
              title={unavailable.has(previous.id)
                ? `${buildLabel(previous)}'s Build is no longer stored on the host`
                : `Reactivates ${buildLabel(previous)}'s artifacts — the same as cantelop rollback`}
              onClick={() => void rollback(previous)}
            >
              {busy === "rollback" ? "Requesting rollback…" : `Roll back to ${buildLabel(previous)}`}
            </button>
          </div>
        )}
      </SectionHead>
      {notice && <div className="notice status" role="status">{notice}</div>}
      {actionError && <div className="notice error" role="alert">{actionError}</div>}
      <div className="section-panel">
        {history.length === 0 ? (
          <p className="empty-note">
            {releases === null
              ? "Build history could not be read — the stats above show the last state this page loaded"
              : "No builds yet"}
          </p>
        ) : (
          <table className="data-table builds-table">
            <colgroup>
              <col className="c-num" />
              <col className="c-state" />
              <col className="c-status" />
              <col />
              <col className="c-time" />
            </colgroup>
            <thead>
              <tr>
                <th>Build</th>
                <th>Status</th>
                <th>Components</th>
                <th>Artifact</th>
                <th className="num">Started</th>
              </tr>
            </thead>
            <tbody>
              {history.map((release) => {
                const open = isOpen(release);
                return (
                  <Fragment key={release.id}>
                    <tr className={`selectable${open ? " selected" : ""}`} onClick={() => toggle(release)}>
                      <td>
                        <button
                          type="button"
                          className="build-toggle"
                          aria-expanded={open}
                          aria-controls={`build-${release.id}`}
                          aria-label={`${open ? "Hide" : "Show"} deployment details for ${buildLabel(release)}`}
                          onClick={(event) => { event.stopPropagation(); toggle(release); }}
                        >
                          <span className="build-chevron" aria-hidden="true" />
                          {buildLabel(release)}
                        </button>
                      </td>
                      <td>
                        <span className={`state-cell st-${release.status}`} title={release.error_code}>
                          <span className={`dot dot-${release.status}`} aria-hidden="true" />
                          {release.status}
                        </span>
                      </td>
                      <td><ComponentsCell release={release} /></td>
                      <td className="dim ellip mono" title={release.api_artifact_digest}>
                        {shortDigest(release.api_artifact_digest)}
                      </td>
                      <td className="num dim" title={formatDate(release.created_at)}>{formatAgo(release.created_at)}</td>
                    </tr>
                    {open && (
                      <tr className="build-detail" id={`build-${release.id}`}>
                        <td colSpan={5}>
                          <dl className="build-facts">
                            <dt>Release</dt>
                            <dd><CopyId value={release.id} /></dd>
                            <DefinitionFacts release={release} />
                            {(["api", "session_runtime"] as const).map((kind) => {
                              const component = release.components[kind];
                              const digest = kind === "api" ? release.api_artifact_digest : release.session_runtime_artifact_digest;
                              return (
                                <Fragment key={kind}>
                                  <dt>{kind === "api" ? "Edge" : "Session runtime"}</dt>
                                  <dd>
                                    <span className={componentToneClass(component.status)}>{componentProgress(component)}</span>
                                    {(component.status === "queued" || component.status === "running") && providerProgress(component.progress) && <> · {providerProgress(component.progress)}</>}
                                    {component.updated_at && <span className="dim"> · <time dateTime={component.updated_at} title={formatDate(component.updated_at)}>{formatAgo(component.updated_at)}</time></span>}
                                    {component.status === "queued" && component.next_attempt_at && <span className="dim"> · eligible from {formatDate(component.next_attempt_at)}</span>}
                                    <div className="build-digest"><CopyId value={digest} /></div>
                                  </dd>
                                </Fragment>
                              );
                            })}
                            {release.error_code && <><dt>Error</dt><dd className="bad">{release.error_code}</dd></>}
                          </dl>
                          {release.status === "cancelling" ? <div className="notice status" role="status">Cancelling deployment. Waiting for provider cleanup before another deployment can start.</div> :
                            ["queued", "provisioning", "failed", "cancelled"].includes(release.status) && <div className="deployment-actions">
                              {["queued", "provisioning"].includes(release.status) && <button type="button" className="button" disabled={busy !== null} onClick={() => void recover(release, "cancel")}>Cancel deployment</button>}
                              {release.definition && <button type="button" className="button" disabled={busy !== null || (pending !== undefined && pending.id !== release.id)} onClick={() => void recover(release, "restart")}>Restart deployment</button>}
                            </div>}
                          {busy === release.id && <div className="notice progress" role="status">Requesting recovery…</div>}
                          <DeploymentLogs release={release} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}

/** Edge and session runtime in one cell. Both finished is the expected case
 *  and reads as one quiet word; while either is still working — or failed —
 *  the cell names each, toned by its state. The full progress is in the
 *  row's details. */
function ComponentsCell({ release }: { release: Release }) {
  const { api, session_runtime: runtime } = release.components;
  if (api.status === "succeeded" && runtime.status === "succeeded") {
    return <span className="state-cell st-succeeded">Completed</span>;
  }
  return (
    <span className="state-cell components-cell">
      <span className={componentToneClass(api.status)} title={api.error_code}>Edge {api.status}</span>
      <span className="faint" aria-hidden="true">·</span>
      <span className={componentToneClass(runtime.status)} title={runtime.error_code}>Runtime {runtime.status}</span>
    </span>
  );
}

/** What the App definition compiled into this release — read-only, because it
 *  lives in code: change it in cantelop.app({ ... }) and deploy again. */
function DefinitionFacts({ release }: { release: Release }) {
  const definition = release.definition;
  if (!definition) {
    return <><dt>Definition</dt><dd className="bad">Predates SDK 1.0 — no longer supported, and cannot be restarted or rolled back to</dd></>;
  }
  const capabilities = definition.capabilities;
  const supported = capabilities ? [
    capabilities.steering && "steering",
    capabilities.message_cancellation && "cancellation",
    capabilities.durable_view && "durable view",
  ].filter(Boolean).join(" · ").replace(/^./, (first) => first.toUpperCase()) : "";
  const ingress = [
    "POST /commands/*",
    ...(definition.homepage ? ["GET /"] : []),
    ...definition.webhooks.map(({ method, path }) => `${method} ${path}`),
  ];
  return (
    <>
      <dt>App</dt>
      <dd><span className="mono">{definition.app_name}</span>{definition.dockerfile && <span className="dim"> · image from {definition.dockerfile}</span>}</dd>
      <dt>Runtime ID</dt>
      <dd><CopyId value={definition.session_runtime_id} /></dd>
      <dt>Ingress</dt>
      <dd className="mono">{ingress.join(" · ")}</dd>
      <dt>Runtime</dt>
      <dd>{capabilities === null ? <span className="dim">Not recorded</span> : supported || <span className="dim">Queue only</span>}</dd>
      <dt>Native sign-in</dt>
      <dd className={definition.native_auth ? undefined : "dim"}>{definition.native_auth ? "Declared (auth in cantelop.app)" : "Not declared"}</dd>
    </>
  );
}
