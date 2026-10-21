"use client";

import Link from "next/link";
import { useCallback, useState } from "react";

import { useAppReleases } from "@/components/app-screen";
import { HeadStat, SectionHead } from "@/components/section-head";
import { formatAgo, formatDuration } from "@/lib/format";
import { commandGroupLabel, commandInfo, commandOrder, consoleSendable, ingressRole, ingressRoleLabel, type CommandGroup, type CommandInfo } from "@/lib/ingress";
import { summarizeReleases } from "@/lib/release-status";
import { curlCommand } from "@/lib/route-curl";
import { useClock } from "@/lib/use-clock";
import { usePolling } from "@/lib/use-polling";
import { useCopy } from "@/lib/use-copy";
import type { AppDefinitionSummary } from "@core/api-deployments/app-definition";
import type { AppRoute, AppRouteOverview } from "@core/api-routes/overview";

// Route tables change on deploy and traffic totals drift slowly; this is a
// reference page, not a live monitor.
const POLL_MS = 30_000;

const UNDECLARED_NOTES: Record<Exclude<AppRouteOverview["declaration"], "declared">, string> = {
  unsupported: "The live release predates SDK 1.0 and is no longer supported",
  none: "Nothing is live for this app yet",
};

/** The App's generated ingress beside the traffic observed on it: first the
 *  SDK commands the backend sends (one POST /commands/{type} each), then the
 *  declared webhooks and homepage, and below them any path that received
 *  requests without being served. */
export function ApiRoutesSection({
  appId,
  appHostname,
  initial,
  tracesHrefBase,
  routeHrefBase,
  polling = true,
}: {
  appId: string;
  appHostname: string;
  // Null when the first read failed; the section keeps retrying.
  initial: AppRouteOverview | null;
  // Everything before the encoded path in a link to the route's traces.
  tracesHrefBase: string;
  // Everything before the route query in a link to the route's own page.
  routeHrefBase: string;
  polling?: boolean;
}) {
  const [overview, setOverview] = useState(initial);
  const [unavailable, setUnavailable] = useState(initial === null);
  const definition = summarizeReleases(useAppReleases()).active?.definition;

  const pollRoutes = useCallback(async (signal: AbortSignal) => {
    try {
      const response = await fetch(`/api/customer/apps/${appId}/routes`, { cache: "no-store" });
      if (!response.ok) throw new Error("routes unavailable");
      const next = await response.json() as AppRouteOverview;
      if (signal.aborted) return;
      setOverview(next);
      setUnavailable(false);
    } catch {
      // Keep the last good table; the next tick retries.
      if (!signal.aborted) setUnavailable(true);
    }
  }, [appId]);
  usePolling(pollRoutes, POLL_MS, { enabled: polling, immediate: initial === null });

  const origin = `https://${appHostname}`;
  const declared = overview?.declaration === "declared";
  const routes = overview?.routes ?? [];
  const observedOnly = overview?.observed_only ?? [];
  const trafficWindow = overview?.traffic_window_hours;
  const commands = routes.flatMap((route) => {
    const info = commandInfo(route);
    return info ? [{ route, info }] : [];
  }).sort((a, b) => commandOrder(a.info) - commandOrder(b.info));
  const otherRoutes = routes.filter((route) => commandInfo(route) === undefined);
  const total = (pick: (traffic: NonNullable<AppRoute["traffic"]>) => number) =>
    commands.reduce((sum, { route }) => sum + (route.traffic ? pick(route.traffic) : 0), 0);
  const calls = total(({ requests }) => requests);
  const errors = total(({ client_errors, server_errors }) => client_errors + server_errors);
  const failedWork = total(({ failed_attempts }) => failed_attempts);
  const windowText = trafficWindow === undefined ? "" : windowLabel(trafficWindow);

  return (
    <>
      <SectionHead
        title="Ingress"
        subtitle={<>{origin}{overview?.release ? ` · Build v${overview.release.version}` : ""}</>}
      >
        {declared && trafficWindow !== undefined && (
          <>
            <HeadStat label={`SDK calls · ${windowText}`} value={calls.toLocaleString("en-US")} tone={calls === 0 ? "dim" : undefined} />
            <HeadStat label="Errors" value={errors.toLocaleString("en-US")} tone={errors === 0 ? "dim" : "bad"} title="Commands answered with a 4xx or 5xx" />
            <HeadStat label="Failed work" value={failedWork.toLocaleString("en-US")} tone={failedWork === 0 ? "dim" : "bad"} title="Session work started by commands that failed" />
          </>
        )}
      </SectionHead>
      {unavailable && (
        <div className="notice error" role="alert">
          Routes are temporarily unavailable{overview ? " — showing the last table" : ""} · retrying every 30s
        </div>
      )}
      {overview && trafficWindow === undefined && (
        <div className="notice error" role="status">Observability storage is unavailable · routes are shown without traffic</div>
      )}
      {overview && !declared && (
        <div className="section-panel">
          <p className="empty-note">{UNDECLARED_NOTES[overview.declaration as Exclude<AppRouteOverview["declaration"], "declared">]}</p>
        </div>
      )}
      {declared && (
        <>
          <h2 className="section-label">SDK commands</h2>
          <div className="section-panel">
            <CommandTable commands={commands} tracesHrefBase={tracesHrefBase} routeHrefBase={routeHrefBase} />
          </div>
          <h2 className="section-label">Webhooks and homepage</h2>
          <div className="section-panel">
            {otherRoutes.length === 0
              ? <p className="empty-note">No webhooks or homepage</p>
              : <RouteTable routes={otherRoutes} origin={origin} definition={definition} tracesHrefBase={tracesHrefBase} routeHrefBase={routeHrefBase} />}
          </div>
        </>
      )}
      {observedOnly.length > 0 && (
        <>
          <h2 className="section-label">{declared ? "Undeclared traffic" : "Observed routes"}</h2>
          <div className="section-panel">
            <RouteTable routes={observedOnly} origin={origin} definition={definition} tracesHrefBase={tracesHrefBase} routeHrefBase={routeHrefBase} />
          </div>
        </>
      )}
    </>
  );
}

const COMMAND_GROUPS: CommandGroup[] = ["messages", "session", "workspace", "environment"];

/** The SDK commands the live build serves, grouped by what they act on. Each
 *  row opens the command's own page: its outcomes and its recent calls. */
function CommandTable({ commands, tracesHrefBase, routeHrefBase }: {
  commands: { route: AppRoute; info: CommandInfo }[]; tracesHrefBase: string; routeHrefBase: string;
}) {
  const { formatDate } = useClock();
  return (
    <table className="data-table route-table command-table">
      <colgroup>
        <col />
        <col className="c-num" />
        <col className="c-num" />
        <col className="c-count" />
        <col className="c-num" />
        <col className="c-time" />
        <col className="c-route-actions" />
      </colgroup>
      <thead>
        <tr>
          <th>Command</th>
          <th className="num">Calls</th>
          <th className="num">Errors</th>
          <th className="num" title="Session work the command started that failed">Failed work</th>
          <th className="num">Median</th>
          <th className="num">Last call</th>
          <th><span className="visually-hidden">Actions</span></th>
        </tr>
      </thead>
      {COMMAND_GROUPS.map((group) => {
        const rows = commands.filter(({ info }) => info.group === group);
        if (rows.length === 0) return null;
        return (
          <tbody key={group}>
            <tr className="group-row">
              <th scope="rowgroup" colSpan={7}>{commandGroupLabel[group]}</th>
            </tr>
            {rows.map(({ route, info }) => {
              const traffic = route.traffic;
              const errors = traffic ? traffic.client_errors + traffic.server_errors : 0;
              return (
                  <tr key={route.path} className={traffic ? undefined : "unused"}>
                    <td className="ellip" title={`${info.call} · POST ${route.path}`}>
                      <Link className="route-link command-link" href={routeHref(routeHrefBase, route)}>
                        <code>{info.call}</code>
                        <span className="command-path">{route.path}</span>
                      </Link>
                    </td>
                    <td className={`num${traffic ? "" : " faint"}`}>{traffic ? traffic.requests.toLocaleString("en-US") : "—"}</td>
                    <td
                      className={`num ${errors === 0 ? "faint" : (traffic?.server_errors ?? 0) > 0 ? "http-5xx" : "http-4xx"}`}
                      title={traffic ? `${traffic.client_errors} client · ${traffic.server_errors} server` : undefined}
                    >
                      {traffic ? errors.toLocaleString("en-US") : "—"}
                    </td>
                    <td
                      className={`num ${!traffic || traffic.failed_attempts === 0 ? "faint" : "http-5xx"}`}
                      title={!info.work ? "Reads and lifecycle calls start no Session work" : traffic ? `${traffic.failed_attempts} failed of ${traffic.messages} messages delivered` : undefined}
                    >
                      {info.work && traffic && traffic.messages > 0 ? traffic.failed_attempts.toLocaleString("en-US") : "—"}
                    </td>
                    <td className="num dim">
                      {formatDuration(traffic?.median_duration_us === undefined ? undefined : traffic.median_duration_us / 1_000)}
                    </td>
                    <td className="num dim" title={traffic ? formatDate(traffic.last_seen_at) : undefined}>
                      {traffic ? formatAgo(traffic.last_seen_at) : "Never"}
                    </td>
                    <td>
                      <div className="row-actions">
                        {traffic ? <Link href={`${tracesHrefBase}${encodeURIComponent(route.path)}`}>Traces</Link> : <span />}
                      </div>
                    </td>
                  </tr>
              );
            })}
          </tbody>
        );
      })}
    </table>
  );
}

function RouteTable({ routes, origin, definition, tracesHrefBase, routeHrefBase }: {
  routes: AppRoute[]; origin: string; definition?: AppDefinitionSummary; tracesHrefBase: string; routeHrefBase: string;
}) {
  const { formatDate } = useClock();
  return (
    <table className="data-table route-table">
      <colgroup>
        <col />
        <col className="c-num" />
        <col className="c-num" />
        <col className="c-count" />
        <col className="c-num" />
        <col className="c-time" />
        <col className="c-route-actions" />
      </colgroup>
      <thead>
        <tr>
          <th>Route</th>
          <th className="num">Requests</th>
          <th className="num">Errors</th>
          <th className="num" title="Session work the route started that failed">Failed work</th>
          <th className="num">Median</th>
          <th className="num">Last seen</th>
          <th><span className="visually-hidden">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {routes.map((route) => {
          const traffic = route.traffic;
          const errors = traffic ? traffic.client_errors + traffic.server_errors : 0;
          const role = ingressRole(definition, route);
          return (
            <tr key={`${route.method} ${route.path}`}>
              <td className="ellip" title={`${route.method} ${route.path}`}>
                <Link className="route-link" href={routeHref(routeHrefBase, route)}>
                  <span className="route-method">{route.method}</span>
                  <code>{route.path}</code>
                  {ingressRoleLabel[role] && <span className="route-role">{ingressRoleLabel[role]}</span>}
                </Link>
              </td>
              <td className={`num${traffic ? "" : " faint"}`}>{traffic ? traffic.requests.toLocaleString("en-US") : "—"}</td>
              <td
                className={`num ${errors === 0 ? "faint" : (traffic?.server_errors ?? 0) > 0 ? "http-5xx" : "http-4xx"}`}
                title={traffic ? `${traffic.client_errors} client · ${traffic.server_errors} server` : undefined}
              >
                {traffic ? errors.toLocaleString("en-US") : "—"}
              </td>
              <td
                className={`num ${!traffic || traffic.failed_attempts === 0 ? "faint" : "http-5xx"}`}
                title={traffic ? `${traffic.failed_attempts} failed of ${traffic.messages} messages dispatched` : undefined}
              >
                {traffic && traffic.messages > 0 ? traffic.failed_attempts.toLocaleString("en-US") : "—"}
              </td>
              <td className="num dim">
                {formatDuration(traffic?.median_duration_us === undefined ? undefined : traffic.median_duration_us / 1_000)}
              </td>
              <td className="num dim" title={traffic ? formatDate(traffic.last_seen_at) : undefined}>
                {traffic ? formatAgo(traffic.last_seen_at) : "Never"}
              </td>
              <td>
                <div className="row-actions route-actions">
                  {consoleSendable(role) ? <CopyCurl command={curlCommand(origin, route)} label={`${route.method} ${route.path}`} /> : <span />}
                  {traffic ? <Link href={`${tracesHrefBase}${encodeURIComponent(route.path)}`}>Traces</Link> : <span />}
                </div>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function routeHref(base: string, route: { method: string; path: string }): string {
  return `${base}method=${route.method}&path=${encodeURIComponent(route.path)}`;
}

function CopyCurl({ command, label }: { command: string; label: string }) {
  const { copied, copy } = useCopy();

  return (
    <button type="button" onClick={() => void copy(command)} title={command} aria-label={`Copy curl command for ${label}`}>
      {copied ? "Copied" : "Copy curl"}
      <span className="visually-hidden" role="status">{copied ? "Copied to clipboard" : ""}</span>
    </button>
  );
}

function windowLabel(hours: number): string {
  return hours % 24 === 0 ? `${hours / 24}d` : `${hours}h`;
}
