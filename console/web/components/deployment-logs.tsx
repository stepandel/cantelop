"use client";

import { useEffect, useRef, useState } from "react";
import type { DeploymentLog } from "@core/db/deployment-log-repository";
import { providerProgress, type Release } from "@/lib/release-status";
import { useClock } from "@/lib/use-clock";

const sourceLabel: Record<DeploymentLog["source"], string> = { deployment: "Deployment", api: "Edge", session_runtime: "Session runtime" };
const messageLabel: Record<string, string> = {
  artifact_verifying: "Downloading and verifying the Edge artifact",
  api_deploying: "Uploading the Edge Worker to the provider",
  api_provider_failed: "Edge provider request failed",
  provider_succeeded: "Runtime provider completed the build",
  provider_failed: "Runtime provider reported a failed build",
};
function message(log: DeploymentLog) {
  const http = /^api_(provider|settings)_http_([1-5][0-9]{2})$/.exec(log.message);
  if (http) return `Edge ${http[1] === "settings" ? "settings lookup" : "deployment"} returned HTTP ${http[2]}`;
  return messageLabel[log.message] ?? providerProgress(log.message) ?? log.message;
}

export function DeploymentLogs({ release }: { release: Release }) {
  const [open, setOpen] = useState(false);
  return <details className="build-logs" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Build &amp; deployment logs</summary>
    {open && <LogViewer release={release} />}
  </details>;
}

function LogViewer({ release }: { release: Release }) {
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [query, setQuery] = useState("");
  const [level, setLevel] = useState("all");
  const [follow, setFollow] = useState(true);
  const viewport = useRef<HTMLDivElement>(null);
  const { formatDate, formatStamp } = useClock();
  const live = ["queued", "provisioning", "cancelling"].includes(release.status);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const response = await fetch(`/api/customer/apps/${release.app_id}/deployments/${release.id}/logs`, { signal: controller.signal, cache: "no-store" });
        if (!response.ok) throw new Error("Could not load logs");
        const data: { items: DeploymentLog[]; truncated: boolean } = await response.json();
        if (controller.signal.aborted) return;
        setLogs(data.items); setTruncated(data.truncated); setLoaded(true); setError("");
        if (live) timer = setTimeout(refresh, 2500);
      } catch {
        if (!controller.signal.aborted) {
          setError("Could not refresh logs. Retrying…");
          timer = setTimeout(refresh, 5000);
        }
      }
    }
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [release.app_id, release.id, release.status, live]);
  useEffect(() => {
    if (follow && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [logs, follow]);
  const filtered = logs.filter(log => (level === "all" || log.level === level) &&
    `${sourceLabel[log.source]} ${message(log)}`.toLowerCase().includes(query.toLowerCase()));
  const text = logs.map(log => `${log.created_at} [${log.source}] [${log.level}] ${message(log)}`).join("\n");
  async function copy() {
    try { await navigator.clipboard.writeText(text); setNotice("Logs copied."); }
    catch { setNotice("Could not copy logs. Use Download instead."); }
  }
  function download() {
    const url = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
    const anchor = document.createElement("a");
    anchor.href = url; anchor.download = `${release.id}.log`; anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return <>
    <div className="build-log-toolbar inline-form">
      <span className="build-log-count">{logs.length} lines{live ? " · Live" : ""}</span>
      <label>Find <input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="Search logs…" /></label>
      <label>Level <select value={level} onChange={event => setLevel(event.target.value)}>
        <option value="all">All levels</option>
        <option value="error">Errors ({logs.filter(log => log.level === "error").length})</option>
        <option value="warning">Warnings ({logs.filter(log => log.level === "warning").length})</option>
      </select></label>
      <label><input type="checkbox" checked={follow} onChange={event => setFollow(event.target.checked)} /> Follow</label>
      <span className="build-log-buttons">
        <button type="button" className="button" disabled={!logs.length} onClick={() => void copy()}>Copy</button>
        <button type="button" className="button" disabled={!logs.length} onClick={download}>Download</button>
      </span>
    </div>
    {truncated && <p className="table-note">Showing the latest 2,000 lines. Copy and download include these lines only.</p>}
    {error && <div className="notice error" role="alert">{error}{loaded ? " Previously loaded logs are shown below." : ""}</div>}
    {notice && <div className="notice status" role="status">{notice}</div>}
    {(loaded || !error) && <div className="build-log-output" ref={viewport} tabIndex={0} role="region" aria-label="Deployment log output">
      {!loaded && !error ? <p>Loading logs…</p> : loaded && logs.length === 0 ?
        <p>No logs</p> :
        filtered.length === 0 && loaded ? <p>No lines match your filters.</p> :
        filtered.map(log => <div key={log.id} className={`build-log-line log-${log.level}`}>
          <time dateTime={log.created_at} title={formatDate(log.created_at)}>{formatStamp(log.created_at)}</time>
          <span className="build-log-source">{sourceLabel[log.source]}</span>
          <span>{log.level !== "info" && <strong>{log.level === "error" ? "Error" : "Warning"} · </strong>}{message(log)}</span>
        </div>)}
    </div>}
  </>;
}
