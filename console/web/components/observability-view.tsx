"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { useUpdateBillingTime } from "@/components/app-screen";
import { SandboxTable } from "@/components/instance-tables";
import { HeadStat, SectionHead } from "@/components/section-head";
import { TelemetryGraphs } from "@/components/telemetry-graphs";

import { appBillingSummary, calculateCPURates, sortSandboxes } from "@/lib/instances";
import { billingTimeLabel, billingTimeTitle } from "@/lib/billing-time";
import { coversWindow, MAX_HISTORY_PAGES, mergeEvents, REQUEST_PAGE_SIZE, REQUEST_POLL_SIZE } from "@/lib/request-history";
import { mergeRequests } from "@/lib/request-merge";
import { useClock } from "@/lib/use-clock";
import { appendUsageSamples, type UsageHistory } from "@/lib/usage-history";
import type { AppRequestInvocation, RequestEvent, RequestEventPage, Snapshot, Workspace } from "@/lib/telemetry-types";
import { usePolling } from "@/lib/use-polling";

// Everything an App does at runtime. The graphs are the App's home page; the
// sandboxes view is the sidebar's monitoring page. This component is the
// shared engine behind both, mounted with the `view` it is showing. Runtime
// views always show the whole App.

const POLL_INTERVAL_MS = 5_000;

type ObservabilityViewId = "graphs" | "sandboxes";

export function ObservabilityView({
  appId,
  workspaces,
  snapshot: initialSnapshot,
  unavailable: initialUnavailable,
  initialEvents = [],
  initialInvocations = [],
  view,
  polling = true,
  showHead = true,
}: {
  appId: string;
  workspaces: Workspace[];
  snapshot: Snapshot | null;
  unavailable: boolean;
  initialEvents?: RequestEvent[];
  initialInvocations?: AppRequestInvocation[];
  // Which runtime page this mount is showing.
  view: ObservabilityViewId;
  polling?: boolean;
  // False when a surrounding screen owns the header — the App home renders
  // the graphs under the App-wide header instead of a section one.
  showHead?: boolean;
}) {
  const [snapshot, setSnapshot] = useState(initialSnapshot);
  const [unavailable, setUnavailable] = useState(initialUnavailable);
  const [refreshing, setRefreshing] = useState(false);
  const [events, setEvents] = useState(initialEvents);
  const [invocations, setInvocations] = useState(initialInvocations);
  const [historyComplete, setHistoryComplete] = useState(true);
  const { formatDate, formatTime } = useClock();
  const [cpuRates, setCPURates] = useState<Record<string, number>>({});
  const [usage, setUsage] = useState<UsageHistory>({});
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const snapshotRef = useRef(initialSnapshot);
  const snapshotInFlight = useRef(false);
  // The head poll and the backwards walk hold separate locks so that starting
  // one on mount cannot starve the other; both merge by sequence, so they are
  // safe to run at the same time.
  const latestInFlight = useRef(false);
  const backfillInFlight = useRef(false);
  const edgeInFlight = useRef(false);
  // Read inside callbacks that must not re-run when events change.
  const eventsRef = useRef(initialEvents);
  const attemptedStart = useRef<number | null>(null);

  useEffect(() => {
    eventsRef.current = events;
  }, [events]);

  const appQuery = `app_id=${encodeURIComponent(appId)}`;

  const refreshSnapshot = useCallback(async () => {
    if (snapshotInFlight.current) return;
    snapshotInFlight.current = true;
    setRefreshing(true);
    try {
      const response = await fetch(`/api/customer/telemetry/snapshot?${appQuery}`, { cache: "no-store" });
      if (!response.ok) throw new Error("snapshot unavailable");
      const next = (await response.json()) as Snapshot;
      // cpu is a rate: it needs the delta between two consecutive readings,
      // and the sandbox monitor plots the samples this poll accumulates.
      const rates = calculateCPURates(snapshotRef.current, next);
      setCPURates(rates);
      setUsage((current) => appendUsageSamples(current, next, rates));
      snapshotRef.current = next;
      setSnapshot(next);
      setUnavailable(false);
    } catch {
      setUnavailable(true);
    } finally {
      snapshotInFlight.current = false;
      setRefreshing(false);
    }
  }, [appQuery]);

  const fetchPage = useCallback(
    async (limit: number, before?: number) => {
      const search = `limit=${limit}&${appQuery}${before ? `&before_sequence=${before}` : ""}`;
      const response = await fetch(`/api/customer/telemetry/requests?${search}`, { cache: "no-store" });
      if (!response.ok) throw new Error("history unavailable");
      return (await response.json()) as RequestEventPage;
    },
    [appQuery],
  );

  // The poll only takes the head of the log — anything recorded since the last
  // tick — and merges it by sequence.
  const loadLatest = useCallback(async () => {
    if (latestInFlight.current) return;
    latestInFlight.current = true;
    try {
      const page = await fetchPage(REQUEST_POLL_SIZE);
      setEvents((current) => mergeEvents(current, page.items));
    } catch {
      setUnavailable(true);
    } finally {
      latestInFlight.current = false;
    }
  }, [fetchPage]);

  // The edge leg. It is a separate store with a separate failure mode — a 6h
  // Worker observability window against the durable gateway log — so it
  // degrades on its own without costing the request history.
  const loadInvocations = useCallback(async () => {
    if (edgeInFlight.current) return;
    edgeInFlight.current = true;
    try {
      const response = await fetch(`/api/customer/apps/${appId}/requests`, { cache: "no-store" });
      const page = await response.json() as { items?: AppRequestInvocation[] };
      if (!response.ok || !Array.isArray(page.items)) throw new Error("edge requests unavailable");
      setInvocations(page.items);
    } catch {
      // Keep the last invocations; the gateway history still counts requests.
    } finally {
      edgeInFlight.current = false;
    }
  }, [appId]);

  // Walk backwards from the oldest event already loaded until the window is
  // covered, the log runs out, or the page bound is hit. Resuming from the
  // oldest cursor means a widened range extends the history instead of
  // refetching it, and the bound keeps a busy App from sweeping the whole
  // 100k-event log.
  const backfill = useCallback(
    async (coverTo: number) => {
      // A window change that lands mid-walk is dropped; the next one retries,
      // which is why the attempt is only recorded once the walk begins.
      if (backfillInFlight.current) return;
      backfillInFlight.current = true;
      attemptedStart.current = coverTo;
      try {
        const collected: RequestEvent[] = [];
        let cursor = eventsRef.current[eventsRef.current.length - 1]?.sequence;
        let complete = false;
        for (let page = 0; page < MAX_HISTORY_PAGES; page += 1) {
          const result = await fetchPage(REQUEST_PAGE_SIZE, cursor);
          collected.push(...result.items);
          if (result.next_before_sequence === undefined) {
            complete = true;
            break;
          }
          if (coversWindow(collected, coverTo)) {
            complete = true;
            break;
          }
          cursor = result.next_before_sequence;
        }
        setEvents((current) => mergeEvents(current, collected));
        setHistoryComplete(complete);
      } catch {
        setUnavailable(true);
      } finally {
        backfillInFlight.current = false;
      }
    },
    [fetchPage],
  );

  // The range picker decides how far back the history has to reach. The window
  // slides forward on every poll, so a backfill is only warranted when the
  // range widens past the oldest start already attempted. A static view holds
  // every event it will ever have, so it never reaches for the network.
  const onWindowChange = useCallback(
    (start: number) => {
      if (!polling || coversWindow(eventsRef.current, start)) {
        setHistoryComplete(true);
        return;
      }
      if (attemptedStart.current !== null && start >= attemptedStart.current) return;
      void backfill(start);
    },
    [backfill, polling],
  );

  const refreshAll = useCallback(async () => {
    await Promise.allSettled([refreshSnapshot(), loadLatest(), loadInvocations()]);
  }, [loadInvocations, loadLatest, refreshSnapshot]);

  // Pauses in hidden browser tabs and refreshes as soon as the tab returns.
  usePolling(refreshAll, POLL_INTERVAL_MS, { enabled: polling, immediate: true });

  const workspaceNames = useMemo(
    () => Object.fromEntries(workspaces.map((workspace) => [workspace.id, workspace.slug ?? workspace.id])),
    [workspaces],
  );

  const requests = useMemo(() => mergeRequests(events, invocations), [events, invocations]);

  const noSnapshot = unavailable && snapshot === null;
  const sandboxes = sortSandboxes(snapshot?.sandboxes ?? []);
  const billingSummary = useMemo(() => appBillingSummary(snapshot, appId), [snapshot, appId]);
  const updateBillingTime = useUpdateBillingTime();
  useEffect(() => { updateBillingTime(billingSummary); }, [billingSummary, updateBillingTime]);

  // The header's numbers, per view.
  const runningSandboxes = sandboxes.filter(({ state }) => state === "running").length;
  const requestCount = `${historyComplete ? "" : "≥"}${requests.length}`;

  return (
    <>
      {view === "graphs" && showHead && (
        <SectionHead title="Graphs">
          <HeadStat label="Sandboxes" value={runningSandboxes} tone={runningSandboxes === 0 ? "dim" : undefined} />
          <HeadStat label="Billing time" value={billingTimeLabel(billingSummary)} title={billingTimeTitle(billingSummary)} />
          <HeadStat label="Requests" value={requestCount} tone={requests.length === 0 ? "dim" : undefined} />
        </SectionHead>
      )}
      {view === "sandboxes" && (
        <SectionHead title="Sandboxes">
          {/* One number only: the state summary above the table carries the
              full running/failed/terminated breakdown. */}
          <HeadStat label="Running" value={runningSandboxes} tone={runningSandboxes === 0 ? "dim" : undefined} />
          <HeadStat label="Billing time" value={billingTimeLabel(billingSummary)} title={billingTimeTitle(billingSummary)} />
        </SectionHead>
      )}

      {unavailable && (
        <div className="notice error" role="alert">
          {noSnapshot ? "Runtime state is unavailable" : "Live refresh failed — showing the last snapshot"} · retrying every 5s
        </div>
      )}

      <div className="tabrow">
        <button className="tabrow-action" type="button" onClick={refreshAll} disabled={refreshing || !polling} aria-live="polite">
          {refreshing ? "Refreshing…" : polling ? "Polling 5s · refresh now" : "Static preview"}
        </button>
      </div>

      {view === "graphs" && (
        <div className={snapshot ? "graph-host" : "section-panel"}>
          {snapshot ? (
            <TelemetryGraphs
              snapshot={snapshot}
              events={events}
              historyComplete={historyComplete}
              onWindowChange={onWindowChange}
            />
          ) : (
            <p className="empty-note">No runtime state for this app yet</p>
          )}
          {snapshot && (
            <p className="table-note">
              Window ends at the last observation, <span title={formatDate(snapshot.observed_at)}>{formatTime(snapshot.observed_at)}</span>
              {snapshot.ready ? "" : " · platform not ready"}
            </p>
          )}
        </div>
      )}

      {view === "sandboxes" && !noSnapshot && (
        <div className="section-panel">
          <SandboxTable
            sandboxes={sandboxes}
            observedAt={snapshot?.observed_at ?? ""}
            cpuRates={cpuRates}
            history={usage}
            polling={polling}
            selectedId={selectedId}
            onSelect={setSelectedId}
            showWorkspace
            workspaceNames={workspaceNames}
            showLimits={false}
          />
        </div>
      )}
    </>
  );
}
