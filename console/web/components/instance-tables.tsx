"use client";

import { useEffect, useRef } from "react";

import { SandboxStop } from "@/components/sandbox-stop";
import { SandboxMonitor } from "@/components/sandbox-monitor";
import { formatDuration, formatElapsed, formatMemory, formatVCPU, shortBuild } from "@/lib/format";
import { sandboxRuntimeDuration, SANDBOX_STATE_ORDER } from "@/lib/instances";
import { useClock } from "@/lib/use-clock";
import type { UsageHistory } from "@/lib/usage-history";
import type { Sandbox, SandboxState } from "@/lib/telemetry-types";

// The record-level tables: the operator screen opens on them, and the App's
// observability pages keep them beside its graphs for the detail a shape can't
// carry — which sandbox, which build.


function InventoryRow({ sandboxes }: { sandboxes: Sandbox[] }) {
  const counts = new Map<SandboxState, number>();
  for (const { state } of sandboxes) counts.set(state, (counts.get(state) ?? 0) + 1);

  return (
    <div className="inv-row">
      <ul aria-label="Sandboxes by state">
        {/* Only states some sandbox is in; a row of zeros reads as noise. */}
        {SANDBOX_STATE_ORDER.filter((state) => counts.has(state)).map((state) => (
          <li key={state}>
            <span className={`dot dot-${state}`} aria-hidden="true" />
            {state}
            <b>{counts.get(state)}</b>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function SandboxTable({
  sandboxes,
  observedAt,
  cpuRates,
  history,
  polling,
  selectedId,
  onSelect,
  showWorkspace = false,
  workspaceNames,
  showLimits = true,
  onStopped,
}: {
  sandboxes: Sandbox[];
  onStopped?: (sandbox: Sandbox) => void;
  observedAt: string;
  cpuRates: Record<string, number>;
  history: UsageHistory;
  polling: boolean;
  // The opened row's live cpu/memory monitor; null when none is open.
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  // Hidden when the view is filtered to one Workspace, where every row shares it.
  showWorkspace?: boolean;
  // Workspace slugs by ID. A customer names their workspaces "production" and
  // "staging"; only the operator screen, which has no naming to read, falls
  // back to the raw ID.
  workspaceNames?: Record<string, string>;
  // Whether the open monitor draws the sandbox's vcpu and memory ceilings.
  showLimits?: boolean;
}) {
  const monitorRef = useRef<HTMLDivElement>(null);
  const selected = selectedId ? sandboxes.find(({ id }) => id === selectedId) : undefined;
  const { formatDate, formatStamp } = useClock();

  useEffect(() => {
    if (selectedId) monitorRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }, [selectedId]);

  // CPU and memory are only read from running sandboxes; when none is, the
  // columns would be a stack of dashes.
  const showUsage = sandboxes.some((sandbox) => sandbox.state === "running");

  const toggleSelected = (id: string) => onSelect(selectedId === id ? null : id);

  return (
    <>
      {sandboxes.length > 0 && <InventoryRow sandboxes={sandboxes} />}

      {selected && (
        <div ref={monitorRef}>
          <SandboxMonitor
            instance={selected}
            samples={history[selected.id] ?? []}
            live={polling && selected.state === "running"}
            showLimits={showLimits}
            onClose={() => onSelect(null)}
          />
        </div>
      )}

      {sandboxes.length === 0 ? (
        <p className="empty-note">No sandboxes</p>
      ) : (
        <table className="data-table">
          {/* Identifier, workspace and build share the flexible space;
              state, times and counts are fixed so the numbers sit right after
              the content instead of flexing toward the edge. */}
          <colgroup>
            <col />
            {showWorkspace ? <col /> : null}
            <col className="c-state" />
            <col />
            {showUsage && <col className="c-num" />}
            {showUsage && <col className="c-num" />}
            <col className="c-stamp" />
            <col className="c-num" />
            <col className="c-num" />
            {onStopped && <col className="c-stop-actions" />}
          </colgroup>
          <thead>
            <tr>
              <th>Sandbox</th>
              {showWorkspace && <th>Workspace</th>}
              <th>State</th>
              <th>Build</th>
              {showUsage && <th className="num">CPU</th>}
              {showUsage && <th className="num">Memory</th>}
              <th>Created</th>
              <th className="num">Startup</th>
              <th className="num">Uptime</th>
              {onStopped && <th className="num">Actions</th>}
            </tr>
          </thead>
          <tbody>
            {sandboxes.map((sandbox) => {
              const rate = cpuRates[sandbox.id];
              const uptime = sandboxRuntimeDuration(sandbox, observedAt);
              const sampling = sandbox.state === "running" && sandbox.usage;
              return (
                <tr key={sandbox.id} className={`selectable${sandbox.id === selectedId ? " selected" : ""}`} onClick={() => toggleSelected(sandbox.id)}>
                  <td className="ellip" title={sandbox.id}>
                    <button type="button" className="row-toggle" aria-expanded={sandbox.id === selectedId} aria-label={`${sandbox.id === selectedId ? "Close" : "Open"} live monitor for ${sandbox.id}`} onClick={(event) => { event.stopPropagation(); toggleSelected(sandbox.id); }}>
                      {sandbox.id}
                    </button>
                  </td>
                  {showWorkspace && (
                    <td className="dim ellip" title={sandbox.workspace_id}>
                      {workspaceNames?.[sandbox.workspace_id] ?? sandbox.workspace_id}
                    </td>
                  )}
                  <td>
                    <span className={`state-cell st-${sandbox.state}`} title={sandbox.failure?.reason}>
                      <span className={`dot dot-${sandbox.state}`} aria-hidden="true" />
                      {sandbox.state}
                    </span>
                  </td>
                  <td className="dim" title={sandbox.build_id}>{shortBuild(sandbox.build_id)}</td>
                  {showUsage && <td className={`num${sampling ? (rate !== undefined && rate > 80 ? " warn" : " sub") : " faint"}`}>{sampling ? (rate === undefined ? "…" : formatVCPU((rate / 100) * sandbox.vcpus)) : "—"}</td>}
                  {showUsage && <td className={`num${sampling && sandbox.usage!.guest_memory_used_bytes !== undefined ? " sub" : " faint"}`} title={sampling ? `Memory used inside the sandbox, excluding available memory and reclaimable cache. Allocated: ${sandbox.usage!.guest_memory_mib ?? sandbox.memory_mib} MiB` : undefined}>{sampling && sandbox.usage!.guest_memory_used_bytes !== undefined ? formatMemory(sandbox.usage!.guest_memory_used_bytes) : "—"}</td>}
                  <td className="sub" title={formatDate(sandbox.created_at)}>{formatStamp(sandbox.created_at)}</td>
                  <td className={`num${sandbox.startup ? " sub" : " faint"}`} title={sandbox.startup ? `completed ${formatDate(sandbox.startup.completed_at)}` : undefined}>{formatDuration(sandbox.startup?.duration_ms)}</td>
                  <td className={`num ${uptime === undefined ? "faint" : "dim"}`} title={uptime === undefined ? "Runtime timing unknown" : undefined}>{uptime === undefined ? "—" : formatElapsed(uptime)}</td>
                  {onStopped && <td><SandboxStop sandbox={sandbox} onStopped={onStopped} /></td>}
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </>
  );
}
