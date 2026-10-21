"use client";

import { useCallback, useRef, useState } from "react";
import type { AdminCounts } from "@core/db/admin-counts";

import { AdminCountStats } from "@/components/admin-counts";
import { SandboxTable } from "@/components/instance-tables";
import { InfrastructureMonitoringPanel } from "@/components/infrastructure-monitoring-panel";
import { HeadStat, SectionHead } from "@/components/section-head";
import { calculateCPURates, sortSandboxes } from "@/lib/instances";
import { useClock } from "@/lib/use-clock";
import { appendUsageSamples, type UsageHistory } from "@/lib/usage-history";
import type { Sandbox, Snapshot } from "@/lib/telemetry-types";
import { usePolling } from "@/lib/use-polling";

const POLL_INTERVAL_MS = 5_000;
type DashboardViewId = "home" | "sandboxes";

// The operator's telemetry, mirroring the App screens: the home view is the
// host header over the infrastructure graphs, and sandboxes is a page of its
// own off the sidebar. This component is the shared engine behind both,
// mounted with the `view` it is showing.
export function DashboardView({
  snapshot: initialSnapshot,
  unavailable: initialUnavailable,
  view,
  counts = null,
  initialHistory = {},
  initialSelectedId = null,
  polling = true,
  canStopSandboxes = false,
}: {
  snapshot: Snapshot | null;
  unavailable: boolean;
  // Which operator page this mount is showing.
  view: DashboardViewId;
  counts?: AdminCounts | null;
  initialHistory?: UsageHistory;
  initialSelectedId?: string | null;
  polling?: boolean;
  canStopSandboxes?: boolean;
}) {
  const [snapshot, setSnapshot] = useState(initialSnapshot);
  const snapshotRef = useRef(initialSnapshot);
  const [unavailable, setUnavailable] = useState(initialUnavailable);
  const [refreshing, setRefreshing] = useState(false);
  const [cpuRates, setCPURates] = useState<Record<string, number>>({});
  const [history, setHistory] = useState(initialHistory);
  const [selectedId, setSelectedId] = useState(initialSelectedId);
  const snapshotInFlight = useRef(false);
  const snapshotVersion = useRef(0);
  const { formatDate, formatTime } = useClock();

  const refreshSnapshot = useCallback(async () => {
    if (snapshotInFlight.current) return;
    snapshotInFlight.current = true;
    const version = snapshotVersion.current;
    setRefreshing(true);
    try {
      const response = await fetch("/api/admin/telemetry/snapshot", { cache: "no-store" });
      if (!response.ok) throw new Error("snapshot unavailable");
      const next = (await response.json()) as Snapshot;
      if (version !== snapshotVersion.current) return;
      const rates = calculateCPURates(snapshotRef.current, next);
      setCPURates(rates);
      setHistory((current) => appendUsageSamples(current, next, rates));
      snapshotRef.current = next;
      setSnapshot(next);
      setUnavailable(false);
    } catch {
      setUnavailable(true);
    } finally {
      snapshotInFlight.current = false;
      setRefreshing(false);
    }
  }, []);

  const sandboxStopped = (stopped: Sandbox) => {
    // Discard snapshots requested before this mutation completed.
    snapshotVersion.current += 1;
    const current = snapshotRef.current;
    if (current) {
      const next = { ...current, sandboxes: current.sandboxes.map((sandbox) => sandbox.id === stopped.id ? stopped : sandbox) };
      snapshotRef.current = next;
      setSnapshot(next);
    }
    void refreshSnapshot();
  };

  // Pauses in hidden tabs and refreshes as soon as the tab returns.
  usePolling(refreshSnapshot, POLL_INTERVAL_MS, { enabled: polling, immediate: true });

  const sandboxes = sortSandboxes(snapshot?.sandboxes ?? []);
  const activeSessions = (snapshot?.sessions ?? []).filter(({ state }) => state === "active").length;
  const activeSandboxes = sandboxes.filter(({ state }) => state === "running" || state === "starting").length;
  const warmSlots = snapshot?.capacity?.warm_slots;
  const noSnapshot = unavailable && snapshot === null;

  const platformStat = noSnapshot
    ? { value: "unavailable", cls: "bad" }
    : unavailable
      ? { value: "stale", cls: "warn" }
      : snapshot?.ready
        ? { value: "ready", cls: "ok" }
        : { value: "Not ready", cls: "warn" };

  return (
    <div>
      {view === "home" && (
        <div className="screen-head">
          <div className="head-id">
            <h1>Host telemetry</h1>
            <span>Firecracker inventory · operator</span>
          </div>
          <AdminCountStats counts={counts} />
          {/* fire-fuse readiness on the compute host; storage and the
              builder report their own health in the host cards below. */}
          <div className="stat">
            <span>Runtime</span>
            <strong className={platformStat.cls}>{platformStat.value}</strong>
          </div>
          <div className="stat">
            <span>Sessions</span>
            <strong className={activeSessions === 0 ? "dim" : undefined}>{activeSessions}</strong>
          </div>
          <div className="stat">
            <span>Sandboxes</span>
            <strong className={activeSandboxes === 0 ? "dim" : undefined}>{activeSandboxes}</strong>
          </div>
          <div className="stat" title={warmSlots ? `${warmSlots.claimed} claimed · ${warmSlots.preparing} preparing · ${warmSlots.target} target` : undefined}>
            <span>Slots available</span>
            <strong className={warmSlots?.ready === 0 ? "warn" : undefined}>{warmSlots?.ready ?? "—"}</strong>
          </div>
          <div className="stat">
            <span>Observed</span>
            <strong className="quiet" title={snapshot ? formatDate(snapshot.observed_at) : undefined}>
              {snapshot ? formatTime(snapshot.observed_at) : "—"}
            </strong>
          </div>
        </div>
      )}
      {view === "sandboxes" && (
        <SectionHead title="Sandboxes">
          <HeadStat label="Running" value={activeSandboxes} tone={activeSandboxes === 0 ? "dim" : undefined} />
          <HeadStat label="Slots available" value={warmSlots?.ready ?? "—"} tone={warmSlots?.ready === 0 ? "warn" : undefined} />
        </SectionHead>
      )}
      {unavailable && (
        <div className="notice error" role="alert">
          {noSnapshot ? "Session state is unavailable" : "Live refresh failed — showing the last snapshot"} · retrying every 5s
        </div>
      )}

      {view === "home" && counts === null && (
        <div className="notice error" role="alert">
          User and app counts are unavailable · reload the page to retry
        </div>
      )}

      <div className="tabrow">
        <button
          className="tabrow-action"
          type="button"
          onClick={() => { void refreshSnapshot(); }}
          disabled={refreshing || !polling}
          aria-live="polite"
        >
          {refreshing ? "Refreshing…" : polling ? "Polling 5s · refresh now" : "Static preview"}
        </button>
      </div>

      {view === "home" && (
        <div className="section-panel">
          <InfrastructureMonitoringPanel active polling={polling} hostSnapshot={snapshot} />
        </div>
      )}

      {view === "sandboxes" && !noSnapshot && (
        <div className="section-panel">
          <SandboxTable
            sandboxes={sandboxes}
            onStopped={canStopSandboxes ? sandboxStopped : undefined}
            observedAt={snapshot?.observed_at ?? ""}
            cpuRates={cpuRates}
            history={history}
            polling={polling}
            selectedId={selectedId}
            onSelect={setSelectedId}
            showWorkspace
          />
        </div>
      )}
    </div>
  );
}
