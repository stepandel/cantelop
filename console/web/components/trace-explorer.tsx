"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { SectionHead } from "@/components/section-head";
import { includeLinkedRequest } from "@/lib/linked-trace";
import { useClock } from "@/lib/use-clock";
import { usePolling } from "@/lib/use-polling";
import type {
  AppTraceTree,
  TraceAttempt,
  TraceLog,
  TraceMessage,
  TraceRequest,
  TraceSession,
  TraceSpan,
} from "@core/observability/trace-tree";

const POLL_INTERVAL_MS = 5_000;

export function TraceExplorer({
  appId,
  initial,
  appIds,
  operator = false,
  initialPath = "",
  initialSession = "",
  linkedRequestId = "",
}: {
  appId: string;
  initial: AppTraceTree | null;
  // Opens the explorer already filtered to one request path or Session.
  initialPath?: string;
  initialSession?: string;
  linkedRequestId?: string;
  appIds?: string[];
  operator?: boolean;
}) {
  const [selectedAppId, setSelectedAppId] = useState(appId);
  const [tree, setTree] = useState(initial);
  const [unavailable, setUnavailable] = useState(initial === null);
  const [refreshing, setRefreshing] = useState(false);
  const [sessionFilter, setSessionFilter] = useState(initialSession);
  const [workspaceFilter, setWorkspaceFilter] = useState("");
  const [pathFilter, setPathFilter] = useState(initialPath);
  const initialSelection = useRef(true);

  const refresh = useCallback(async () => {
    if (!selectedAppId) return;
    setRefreshing(true);
    try {
      const endpoint = operator
        ? `/api/admin/traces?app_id=${encodeURIComponent(selectedAppId)}`
        : `/api/customer/apps/${selectedAppId}/traces`;
      const response = await fetch(endpoint, { cache: "no-store" });
      if (!response.ok) throw new Error("traces unavailable");
      let next = await response.json() as AppTraceTree;
      if (linkedRequestId && !next.requests.some(({ id }) => id === linkedRequestId)) {
        const linkedResponse = await fetch(`/api/customer/apps/${selectedAppId}/requests/${linkedRequestId}/trace`, { cache: "no-store" });
        if (!linkedResponse.ok) throw new Error("linked trace unavailable");
        next = includeLinkedRequest(next, await linkedResponse.json() as AppTraceTree);
      }
      setTree(next);
      setUnavailable(false);
    } catch {
      setUnavailable(true);
    } finally {
      setRefreshing(false);
    }
  }, [operator, selectedAppId, linkedRequestId]);

  useEffect(() => {
    if (initialSelection.current) {
      initialSelection.current = false;
      return;
    }
    setTree(null);
    setUnavailable(false);
    void refresh();
  }, [refresh, selectedAppId]);

  usePolling(refresh, POLL_INTERVAL_MS);

  const filterOptions = useMemo(() => traceFilterOptions(tree), [tree]);
  const { requests, unlinkedMessages } = useMemo(() => filterTraceTree(
    tree,
    sessionFilter,
    workspaceFilter,
    pathFilter,
  ), [tree, sessionFilter, workspaceFilter, pathFilter]);
  const hasFilters = Boolean(sessionFilter.trim() || workspaceFilter.trim() || pathFilter.trim());

  // A link to one request (`#req_…`, from a route's recent calls) lands on it
  // once the tree that holds it has rendered; :target marks it there.
  const landed = useRef(false);
  useEffect(() => {
    if (landed.current || !/^#req_[0-9a-f]{32}$/.test(window.location.hash)) return;
    const target = document.getElementById(window.location.hash.slice(1));
    if (!target) return;
    landed.current = true;
    target.scrollIntoView({ block: "start" });
  }, [requests]);

  return (
    <>
      <SectionHead title="Traces" />
      <div className="tabrow">
        <div className="trace-filters" role="search" aria-label="Trace filters">
          {appIds && (
            <label>
              <span>App ID</span>
              <select value={selectedAppId} onChange={(event) => {
                setSelectedAppId(event.target.value);
                setSessionFilter("");
                setWorkspaceFilter("");
                setPathFilter("");
              }}>
                {appIds.length === 0 && <option value="">No traced apps</option>}
                {appIds.map((id) => <option value={id} key={id}>{id}</option>)}
              </select>
            </label>
          )}
          <TraceFilter label="Session ID" value={sessionFilter} options={filterOptions.sessions} onChange={setSessionFilter} />
          <TraceFilter label="Workspace ID" value={workspaceFilter} options={filterOptions.workspaces} onChange={setWorkspaceFilter} />
          <TraceFilter label="Request path" value={pathFilter} options={filterOptions.paths} onChange={setPathFilter} />
        </div>
        <button className="tabrow-action" type="button" onClick={() => void refresh()} disabled={refreshing}>
          {refreshing ? "Refreshing…" : "Polling 5s · refresh now"}
        </button>
      </div>
      {unavailable && (
        <div className="notice error" role="alert">
          Observability storage is unavailable{tree ? " — showing the last trace tree" : ""} · retrying every 5s
        </div>
      )}
      <div className="section-panel trace-explorer">
        {requests.length === 0 && unlinkedMessages.length === 0
          ? <p className="empty-note">{hasFilters ? "No traces match these filters" : operator && !selectedAppId ? "No traced Apps yet" : "No traced Requests or Messages for this app yet"}</p>
          : requests.map((request) => <RequestNode key={request.id} request={request} />)}
        {unlinkedMessages.length > 0 && (
          <section className="trace-unlinked">
            <p className="table-note">Messages awaiting or missing their Request observation</p>
            {unlinkedMessages.map((message) => <MessageNode key={message.id} message={message} />)}
          </section>
        )}
      </div>
    </>
  );
}

function traceFilterOptions(tree: AppTraceTree | null): { sessions: string[]; workspaces: string[]; paths: string[] } {
  const requests = tree?.requests ?? [];
  const messages = [...requests.flatMap(({ messages: linked }) => linked), ...(tree?.unlinked_messages ?? [])];
  return {
    sessions: uniqueSorted(messages.map(({ session }) => session.logical_session_id)),
    workspaces: uniqueSorted(messages.map(({ session }) => session.workspace_id)),
    paths: uniqueSorted(requests.flatMap(({ path }) => path ? [path] : [])),
  };
}

function uniqueSorted(values: string[]): string[] {
  return [...new Set(values)].sort((left, right) => left.localeCompare(right));
}

function filterTraceTree(
  tree: AppTraceTree | null,
  sessionFilter: string,
  workspaceFilter: string,
  pathFilter: string,
): { requests: TraceRequest[]; unlinkedMessages: TraceMessage[] } {
  const session = sessionFilter.trim();
  const workspace = workspaceFilter.trim();
  const path = pathFilter.trim();
  const matchesMessage = (message: TraceMessage) => (
    (!session || message.session.logical_session_id === session)
    && (!workspace || message.session.workspace_id === workspace)
  );
  const requests = (tree?.requests ?? []).flatMap((request) => {
    if (path && request.path !== path) return [];
    if (!session && !workspace) return [request];
    const messages = request.messages.filter(matchesMessage);
    return messages.length > 0 ? [{ ...request, messages }] : [];
  });
  const unlinkedMessages = path
    ? []
    : (tree?.unlinked_messages ?? []).filter(matchesMessage);
  return { requests, unlinkedMessages };
}

function TraceFilter({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: string[];
  onChange: (value: string) => void;
}) {
  return (
    <label>
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        <option value="">All</option>
        {options.map((option) => <option value={option} key={option}>{option}</option>)}
      </select>
    </label>
  );
}

export function RequestNode({ request }: { request: TraceRequest }) {
  return (
    <details className="trace-request-root" id={request.id} open>
      <summary>
        <NodeKind value="Request" />
        <strong>{request.method ?? "REQUEST"} {request.path ?? request.id}</strong>
        {request.status_code !== undefined && <Outcome value={String(request.status_code)} bad={request.status_code >= 400} good={request.status_code >= 200 && request.status_code < 300} />}
        {request.duration_us !== undefined && <span>{formatDuration(request.duration_us)}</span>}
        <Instant value={request.received_at} />
      </summary>
      <div className="trace-children">
        {request.logs.length > 0 && (
          <section className="trace-edge-logs">
            <div className="trace-surface-head">
              <NodeKind value="Edge" />
              <strong>Worker logs</strong>
              <span>{request.logs.length}</span>
            </div>
            {request.logs.map((log) => <LogNode key={log.id} log={log} />)}
          </section>
        )}
        {request.messages.map((message) => <MessageNode key={message.id} message={message} />)}
      </div>
    </details>
  );
}

function MessageNode({ message }: { message: TraceMessage }) {
  return (
    <article className="trace-message">
      <header>
        <NodeKind value="Message" />
        <strong>{message.id}</strong>
        <Instant value={message.accepted_at} />
      </header>
      <div className="trace-attempts">
        {message.attempts.length === 0 ? <div className="trace-attempt-waiting"><span className="dim">Awaiting attempt</span><SessionIdentity session={message.session} /></div> : message.attempts.map((attempt) => (
          <AttemptNode attempt={attempt} session={message.session} key={attempt.id} />
        ))}
      </div>
    </article>
  );
}

function AttemptNode({ attempt, session }: { attempt: TraceAttempt; session: TraceSession }) {
  const roots = attempt.spans.filter(({ parent_span_id: parent }) => !parent);
  const unscopedLogs = attempt.logs.filter(({ span_id: span }) => !span);
  return (
    <details className="trace-attempt" open>
      <summary>
        <span>Attempt {attempt.number}</span>
        <SessionIdentity session={session} />
        <Outcome value={attempt.outcome} bad={attempt.outcome === "failed"} />
        {attempt.error_code && <code>{attempt.error_code}</code>}
      </summary>
      <div className="trace-runtime">
        <div className="trace-sandbox-head">
          <NodeKind value="Sandbox" />
          <strong>{attempt.sandbox?.id ?? "Awaiting route"}</strong>
          {attempt.sandbox?.concrete_session_id && <span>{attempt.sandbox.concrete_session_id}</span>}
          {attempt.sandbox?.build_id && <span>{attempt.sandbox.build_id}</span>}
        </div>
        {attempt.sandbox?.logs.map((log) => <LogNode key={log.id} log={log} />)}
        {roots.map((span) => <SpanNode key={span.id} span={span} attempt={attempt} />)}
        {unscopedLogs.map((log) => <LogNode key={log.id} log={log} />)}
        {roots.length === 0 && unscopedLogs.length === 0 && <span className="dim">No SDK observations</span>}
      </div>
    </details>
  );
}

function SessionIdentity({ session }: { session: TraceSession }) {
  return (
    <span className="trace-attempt-session">
      <NodeKind value="Session" />
      <strong>{session.logical_session_id}</strong>
      <NodeKind value="Workspace" />
      <span>{session.workspace_id}</span>
    </span>
  );
}

function SpanNode({ span, attempt }: { span: TraceSpan; attempt: TraceAttempt }) {
  const children = attempt.spans.filter(({ parent_span_id: parent }) => parent === span.id);
  const logs = attempt.logs.filter(({ span_id: owner }) => owner === span.id);
  return (
    <div className="trace-span">
      <div className="trace-span-head">
        <span>Span</span><strong>{span.name}</strong>
        {/* The SDK names the command that started the handler's span. */}
        {typeof span.attributes["cantelop.command"] === "string" && <code className="trace-span-command">{span.attributes["cantelop.command"]}</code>}
        <Outcome value={span.status} bad={span.status === "error"} />
        {span.finished_at && <time>{durationBetween(span.started_at, span.finished_at)}</time>}
      </div>
      {(logs.length > 0 || children.length > 0) && (
        <div className="trace-span-children">
          {logs.map((log) => <LogNode key={log.id} log={log} />)}
          {children.map((child) => <SpanNode key={child.id} span={child} attempt={attempt} />)}
        </div>
      )}
    </div>
  );
}

function LogNode({ log }: { log: TraceLog }) {
  return <div className={`trace-log ${log.severity}`}><span>{log.severity}</span><p>{log.body}</p><Instant value={log.occurred_at} /></div>;
}

function NodeKind({ value }: { value: string }) {
  return <span className="trace-kind">{value}</span>;
}

function Outcome({ value, bad = false, good = false }: { value: string; bad?: boolean; good?: boolean }) {
  return <span className={bad ? "trace-outcome bad" : good ? "trace-outcome good" : "trace-outcome"}>{value}</span>;
}

/** A trace instant as a clock reading in the viewer's zone; the full date is
 *  in the title. */
function Instant({ value }: { value: string }) {
  const { formatTime, formatDate } = useClock();
  return <time dateTime={value} title={formatDate(value)}>{formatTime(value)}</time>;
}

function formatDuration(microseconds: number): string {
  return microseconds < 1_000 ? `${microseconds} µs` : `${(microseconds / 1_000).toFixed(1)} ms`;
}

function durationBetween(start: string, finish: string): string {
  return formatDuration(Math.max(0, (Date.parse(finish) - Date.parse(start)) * 1_000));
}
