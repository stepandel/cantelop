"use client";

import Link from "next/link";
import { Fragment, useCallback, useEffect, useState } from "react";

import { ApiRouteSend } from "@/components/api-route-send";
import { useAppReleases, useSectionCrumb } from "@/components/app-screen";
import { HeadStat, SectionHead } from "@/components/section-head";
import { RequestNode } from "@/components/trace-explorer";
import { formatAgo, formatDuration } from "@/lib/format";
import { commandInfo, consoleSendable, ingressRole, ingressRoleLabel } from "@/lib/ingress";
import { summarizeReleases } from "@/lib/release-status";
import { watchRequestTrace } from "@/lib/request-trace-polling";
import { useClock } from "@/lib/use-clock";
import { usePolling } from "@/lib/use-polling";
import type { AppRouteDetail, AppRouteRecentRequest } from "@core/api-routes/detail";
import type { TraceRequest } from "@core/observability/trace-tree";

import styles from "./api-route-detail.module.css";

const POLL_MS = 30_000;

/** One route of the App — for an SDK command, one SDK call: how its requests
 *  fared at the edge, what became of the Session work they started, and its
 *  recent calls, each opening its trace in place. A `202` only says a message
 *  was accepted; this page is where the outcome of that work is visible. */
export function ApiRouteDetail({
  appId,
  method,
  path,
  origin,
  initial,
  apiHref,
  tracesHrefBase,
  polling = true,
  send,
  traces,
}: {
  appId: string;
  method: string;
  path: string;
  origin: string;
  // Null when the first read failed; the page keeps retrying.
  initial: AppRouteDetail | null;
  apiHref: string;
  // Previews render the send panel with a finished exchange.
  send?: Parameters<typeof ApiRouteSend>[0]["initialSent"];
  // Previews render recent calls' traces without fetching them.
  traces?: Record<string, TraceRequest>;
  // Everything before the encoded path in a link to the route's traces.
  tracesHrefBase: string;
  polling?: boolean;
}) {
  const [detail, setDetail] = useState(initial);
  const [unavailable, setUnavailable] = useState(initial === null);
  const { formatDate } = useClock();
  const definition = summarizeReleases(useAppReleases()).active?.definition;
  const role = ingressRole(definition, { method, path });
  const command = role === "command" ? commandInfo({ method, path }) : undefined;
  const title = command ? command.call : `${method} ${path}`;
  useSectionCrumb(title);

  const pollDetail = useCallback(async (signal: AbortSignal) => {
    try {
      const query = `method=${encodeURIComponent(method)}&path=${encodeURIComponent(path)}`;
      const response = await fetch(`/api/customer/apps/${appId}/routes/detail?${query}`, { cache: "no-store" });
      if (!response.ok) throw new Error("route unavailable");
      const next = await response.json() as AppRouteDetail;
      if (signal.aborted) return;
      setDetail(next);
      setUnavailable(false);
    } catch {
      if (!signal.aborted) setUnavailable(true);
    }
  }, [appId, method, path]);
  usePolling(pollDetail, POLL_MS, { enabled: polling, immediate: initial === null });

  const http = detail?.http;
  const work = detail?.downstream;
  const httpErrors = (http?.status_counts ?? []).filter(({ status }) => status >= 400).reduce((total, { requests }) => total + requests, 0);
  const tracesHref = `${tracesHrefBase}${encodeURIComponent(path)}`;
  // Reads and lifecycle commands never deliver a message, so a Session panel would only ever be empty.
  const startsWork = command === undefined || command.work;
  const windowLabel = detail ? (detail.traffic_window_hours % 24 === 0 ? `${detail.traffic_window_hours / 24}d` : `${detail.traffic_window_hours}h`) : "";

  return (
    <>
      <SectionHead
        title={title}
        subtitle={
          <>
            <Link href={apiHref}>Ingress</Link>
            {command ? ` · POST ${path}` : ingressRoleLabel[role] && ` · ${ingressRoleLabel[role]}`}
            {detail && !detail.declared && " · not declared by the live build"}
            {detail && ` · traffic over the last ${windowLabel}`}
          </>
        }
      >
        {http && <HeadStat label="Requests" value={http.requests.toLocaleString("en-US")} tone={http.requests === 0 ? "dim" : undefined} />}
        {http && <HeadStat label="HTTP errors" value={httpErrors.toLocaleString("en-US")} tone={httpErrors === 0 ? "dim" : "bad"} />}
        {work && startsWork && <HeadStat label="Messages" value={work.messages.toLocaleString("en-US")} tone={work.messages === 0 ? "dim" : undefined} />}
        {work && startsWork && <HeadStat label="Failed work" value={work.failed.toLocaleString("en-US")} tone={work.failed === 0 ? "dim" : "bad"} />}
      </SectionHead>
      {unavailable && (
        <div className="notice error" role="alert">
          Route outcomes are temporarily unavailable{detail ? " — showing the last reading" : ""} · retrying every 30s
        </div>
      )}
      {detail && consoleSendable(role) && (
        <ApiRouteSend appId={appId} method={method} path={path} origin={origin} declared={detail.declared} tracesHrefBase={tracesHrefBase} initialSent={send} />
      )}
      {detail && http && work && (
        <>
          <div className={`${styles.routeOutcomes}${startsWork ? "" : ` ${styles.single}`}`}>
            <section>
              <h2 className="section-label">At the edge</h2>
              <div className="section-panel">
                {http.requests === 0 ? (
                  <p className="empty-note">No requests reached this route in the last {windowLabel}</p>
                ) : (
                  <>
                    <div className={`monitor-head ${styles.routeFacts}`}>
                      <dl className="monitor-meta"><dt>p50</dt><dd>{formatDuration(microsToMillis(http.p50_duration_us))}</dd></dl>
                      <dl className="monitor-meta"><dt>p95</dt><dd>{formatDuration(microsToMillis(http.p95_duration_us))}</dd></dl>
                      <dl className="monitor-meta"><dt>First seen</dt><dd title={http.first_seen_at ? formatDate(http.first_seen_at) : undefined}>{formatAgo(http.first_seen_at)}</dd></dl>
                      <dl className="monitor-meta"><dt>Last seen</dt><dd title={http.last_seen_at ? formatDate(http.last_seen_at) : undefined}>{formatAgo(http.last_seen_at)}</dd></dl>
                    </div>
                    <table className="data-table">
                      <colgroup>
                        <col className="c-kind" />
                        <col />
                        <col className="c-num" />
                        <col className="c-share" />
                      </colgroup>
                      <thead>
                        <tr>
                          <th>Status</th>
                          <th><span className="visually-hidden">Share</span></th>
                          <th className="num">Requests</th>
                          <th className="num">Share</th>
                        </tr>
                      </thead>
                      <tbody>
                        {http.status_counts.map(({ status, requests }) => (
                          <tr key={status}>
                            <td className={httpClass(status)}>{status}</td>
                            <td><ShareBar share={requests / http.requests} tone={status >= 500 ? "bad" : status >= 400 ? "warn" : "ok"} /></td>
                            <td className="num">{requests.toLocaleString("en-US")}</td>
                            <td className="num dim">{percent(requests / http.requests)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </>
                )}
              </div>
            </section>

            {startsWork && <section>
              <h2 className="section-label">In sessions</h2>
              <div className="section-panel">
                {work.messages === 0 ? (
                  <p className="empty-note">
                    {http.requests === 0 ? "Nothing was dispatched" : "Requests to this route did not dispatch messages to sessions"}
                  </p>
                ) : (
                  <>
                    <div className={`monitor-head ${styles.routeFacts}`}>
                      <dl className="monitor-meta"><dt>Attempts</dt><dd>{work.attempts.toLocaleString("en-US")}</dd></dl>
                      <dl className="monitor-meta"><dt>Time to start</dt><dd>{formatDuration(microsToMillis(work.p50_time_to_start_us))}</dd></dl>
                      <dl className="monitor-meta"><dt>Time to finish</dt><dd>{formatDuration(microsToMillis(work.p50_time_to_finish_us))}</dd></dl>
                    </div>
                    <table className="data-table">
                      <colgroup>
                        <col className="c-kind" />
                        <col />
                        <col className="c-num" />
                        <col className="c-share" />
                      </colgroup>
                      <thead>
                        <tr>
                          <th>Outcome</th>
                          <th><span className="visually-hidden">Share</span></th>
                          <th className="num">Attempts</th>
                          <th className="num">Share</th>
                        </tr>
                      </thead>
                      <tbody>
                        {([["completed", work.completed, "ok"], ["failed", work.failed, "bad"], ["pending", work.pending, "info"]] as const)
                          .filter(([, count]) => count > 0)
                          .map(([outcome, count, tone]) => (
                            <tr key={outcome}>
                              <td className={`state ${tone}`}>{outcome}</td>
                              <td><ShareBar share={work.attempts === 0 ? 0 : count / work.attempts} tone={tone === "info" ? "ok" : tone} /></td>
                              <td className="num">{count.toLocaleString("en-US")}</td>
                              <td className="num dim">{percent(work.attempts === 0 ? 0 : count / work.attempts)}</td>
                            </tr>
                          ))}
                      </tbody>
                    </table>
                    {work.error_codes.length > 0 && (
                      <>
                        <div className="section-label">Failure reasons</div>
                        <table className="data-table">
                          <colgroup>
                            <col />
                            <col className="c-num" />
                          </colgroup>
                          <thead>
                            <tr>
                              <th>Error code</th>
                              <th className="num">Attempts</th>
                            </tr>
                          </thead>
                          <tbody>
                            {work.error_codes.map(({ code, attempts }) => (
                              <tr key={code}>
                                <td><code>{code}</code></td>
                                <td className="num">{attempts.toLocaleString("en-US")}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </>
                    )}
                  </>
                )}
              </div>
            </section>}
          </div>

          <RecentCalls
            appId={appId}
            detail={detail}
            noun={command ? "calls" : "requests"}
            windowLabel={windowLabel}
            tracesHref={tracesHref}
            // Both trace links' bases end in their path query; a Session's traces swap it for the Session.
            sessionTracesHrefBase={tracesHrefBase.replace(/path=$/, "session=")}
            startsWork={startsWork}
            traces={traces}
          />
        </>
      )}
    </>
  );
}

/** The route's latest calls, or only the ones that failed. A row opens the
 *  call's trace in place — request, the message it delivered, its attempts,
 *  Sandbox, spans and logs — without leaving the route. */
function RecentCalls({ appId, detail, noun, windowLabel, tracesHref, sessionTracesHrefBase, startsWork, traces }: {
  appId: string;
  detail: AppRouteDetail;
  noun: "calls" | "requests";
  windowLabel: string;
  tracesHref: string;
  sessionTracesHrefBase: string;
  startsWork: boolean;
  traces?: Record<string, TraceRequest>;
}) {
  const { formatDate } = useClock();
  const [filter, setFilter] = useState<"all" | "failed">("all");
  const [openId, setOpenId] = useState<string | null>(null);
  const rows = filter === "all" ? detail.recent_requests : detail.recent_failures;
  const columns = startsWork ? 6 : 5;
  const toggle = (id: string) => setOpenId((current) => (current === id ? null : id));

  return (
    <>
      <h2 className="section-label">Recent {noun}</h2>
      <div className="tabrow" role="tablist" aria-label={`Recent ${noun}`}>
        {(["all", "failed"] as const).map((value) => (
          <button
            key={value}
            type="button"
            role="tab"
            className="tab-chip"
            aria-selected={filter === value}
            onClick={() => { setFilter(value); setOpenId(null); }}
          >
            {value === "all" ? "Latest" : "Failed"}
            <b>{(value === "all" ? detail.recent_requests : detail.recent_failures).length}</b>
          </button>
        ))}
        <Link className="tabrow-action" href={tracesHref}>All traces for this {noun === "calls" ? "command" : "route"}</Link>
      </div>
      <div className="section-panel" role="tabpanel">
        {rows.length === 0 ? (
          <p className="empty-note">
            {filter === "all" ? `No ${noun} in the last ${windowLabel}` : `No failed ${noun} in the last ${windowLabel}`}
          </p>
        ) : (
          <table className={`data-table ${styles.recentCalls}`}>
            <colgroup>
              <col className="c-stamp" />
              <col className="c-num" />
              <col className="c-num" />
              {startsWork && <col className={styles.cCallSession} />}
              <col />
              <col className="c-actions" />
            </colgroup>
            <thead>
              <tr>
                <th>Received</th>
                <th className="num">Status</th>
                <th className="num">Duration</th>
                {startsWork && <th>Session</th>}
                <th>{startsWork ? "Work" : "Request"}</th>
                <th><span className="visually-hidden">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((request) => {
                const open = openId === request.id;
                return (
                  <Fragment key={request.id}>
                    <tr className={`selectable${open ? " selected" : ""}`} onClick={() => toggle(request.id)}>
                      <td title={formatDate(request.received_at)}>
                        <button
                          type="button"
                          className="build-toggle"
                          aria-expanded={open}
                          aria-controls={`call-${request.id}`}
                          aria-label={`${open ? "Hide" : "Show"} the trace of the ${noun === "calls" ? "call" : "request"} received ${formatAgo(request.received_at)}`}
                          onClick={(event) => { event.stopPropagation(); toggle(request.id); }}
                        >
                          <span className="build-chevron" aria-hidden="true" />
                          {formatAgo(request.received_at)}
                        </button>
                      </td>
                      <td className={`num ${request.status_code === undefined ? "faint" : httpClass(request.status_code)}`}>{request.status_code ?? "—"}</td>
                      <td className="num dim">{formatDuration(microsToMillis(request.duration_us))}</td>
                      {startsWork && (
                        <td className="ellip mono" title={request.session_id}>
                          {request.session_id === undefined
                            ? <span className="faint">—</span>
                            : <Link className="route-link" href={`${sessionTracesHrefBase}${encodeURIComponent(request.session_id)}`} onClick={(event) => event.stopPropagation()}>{request.session_id}</Link>}
                        </td>
                      )}
                      <td className="ellip">
                        <CallOutcome request={request} startsWork={startsWork} />
                      </td>
                      <td>
                        <div className="row-actions">
                          <Link href={`${tracesHref}&request=${encodeURIComponent(request.id)}#${request.id}`} onClick={(event) => event.stopPropagation()}>Traces</Link>
                        </div>
                      </td>
                    </tr>
                    {open && (
                      <tr className={styles.callTrace} id={`call-${request.id}`}>
                        <td colSpan={columns}>
                          <CallTrace appId={appId} requestId={request.id} initial={traces?.[request.id]} />
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

function CallOutcome({ request, startsWork }: { request: AppRouteRecentRequest; startsWork: boolean }) {
  if (!startsWork || request.message_id === undefined) {
    if (request.status_code !== undefined && request.status_code >= 500) return <span className="faint">Failed at the edge</span>;
    if (request.status_code !== undefined && request.status_code >= 400) return <span className="faint">Rejected at the edge</span>;
    return <span className="faint">{startsWork ? "Nothing delivered" : "Answered at the edge"}</span>;
  }
  if (request.attempt_outcome === undefined) return <span className="state info">queued</span>;
  const tone = request.attempt_outcome === "failed" ? "bad" : request.attempt_outcome === "completed" ? "ok" : "info";
  return (
    <>
      <span className={`state ${tone}`}>{request.attempt_outcome}</span>
      {request.attempt_error_code && <> · <code>{request.attempt_error_code}</code></>}
    </>
  );
}

/** One call's trace, fetched when its row opens and followed while its work
 *  is still pending. */
function CallTrace({ appId, requestId, initial }: { appId: string; requestId: string; initial?: TraceRequest }) {
  const [trace, setTrace] = useState<TraceRequest | null>(initial ?? null);
  const [settled, setSettled] = useState(initial !== undefined);

  useEffect(() => {
    if (initial !== undefined) return;
    return watchRequestTrace(appId, requestId, (next, done) => {
      if (next) setTrace(next);
      if (done) setSettled(true);
    });
  }, [appId, requestId, initial]);

  if (trace) return <div className={styles.callTraceBody}><RequestNode request={trace} /></div>;
  return (
    <p className="empty-note" role="status">
      {settled ? "This call's trace is no longer retained" : "Loading the trace…"}
    </p>
  );
}

function ShareBar({ share, tone }: { share: number; tone: "ok" | "warn" | "bad" }) {
  // Width is the one value here that is genuinely data-driven.
  return (
    <div className={styles.shareTrack} aria-hidden="true">
      <div className={`${styles.shareBar} ${tone}`} style={{ width: `${Math.max(1, Math.round(share * 100))}%` }} />
    </div>
  );
}

function httpClass(status: number): string {
  return `http-${Math.floor(status / 100)}xx`;
}

function percent(share: number): string {
  const value = share * 100;
  if (value > 0 && value < 0.1) return "<0.1%";
  return `${value.toFixed(value < 10 ? 1 : 0)}%`;
}

function microsToMillis(value: number | undefined): number | undefined {
  return value === undefined ? undefined : value / 1_000;
}
