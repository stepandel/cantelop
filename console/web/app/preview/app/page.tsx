import { notFound } from "next/navigation";

import { previewEvents, previewInvocations, previewSnapshot, previewWorkspaces } from "@/app/preview/app/fixture";
import { ApiRouteDetail } from "@/components/api-route-detail";
import { ApiRoutesSection } from "@/components/api-routes-section";
import { appBillingSummary } from "@/lib/instances";
import { AppAccessPanel } from "@/components/app-access-panel";
import { AppScreen } from "@/components/app-screen";
import { BuildsSection } from "@/components/builds-section";
import { ConfigPanel } from "@/components/config-panel";
import { CredentialsPanel } from "@/components/credentials-panel";
import { ObservabilityView } from "@/components/observability-view";
import { SectionHead } from "@/components/section-head";
import { WorkspaceDetail } from "@/components/workspace-detail";
import { WorkspacesSection } from "@/components/workspaces-section";
import { isAppSectionId, type AppSectionId } from "@/lib/app-sections";
import type { ConfigValue, EnvironmentDeclarations } from "@/lib/config-values";
import type { CustomerApp } from "@/lib/customer-telemetry";
import type { Release } from "@/lib/release-status";
import type { AppRouteDetail as AppRouteDetailData } from "@core/api-routes/detail";
import type { AppRouteOverview } from "@core/api-routes/overview";
import type { TraceRequest } from "@core/observability/trace-tree";
import type { AppDefinitionSummary } from "@core/api-deployments/app-definition";

// Development-only styling preview of the App screen — every section it has,
// selected with ?section=<id> so the sidebar navigates within the preview,
// including a day of synthetic runtime across two Workspaces. Never served in
// production.
const appID = "app_0123456789abcdef0123456789abcdef";

const app: CustomerApp = {
  role: "owner",
  owner: { email: "customer@example.com" },
  admin_count: 2,
  fleet_id: null,
  id: appID,
  slug: "base-agent",
  hostname: "base-agent.cantelop.dev",
  created_at: "2026-08-01T12:00:00Z",
  updated_at: "2026-08-04T09:00:00Z",
};

const apiDigest = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";
const sessionRuntimeDigest = "sha256:60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752";

// What `cantelop.app({ name: "base-agent", ... })` compiled into: a webhook, a
// homepage and the packaged Claude runtime with native sign-in.
const definition: AppDefinitionSummary = {
  app_name: "base-agent",
  session_runtime_id: "rt_5d1c0e7a2b9f",
  webhooks: [{ method: "POST", path: "/webhooks/github" }],
  homepage: true,
  native_auth: true,
  token_rotation: true,
  dockerfile: "docker/Dockerfile",
  capabilities: { steering: true, message_cancellation: true, durable_view: true },
};

// A deploy caught mid-flight: v3 is materializing while v2 still serves. v1
// predates SDK 1, so it has no App definition.
const releases: Release[] = [
  {
    id: "rel_00000000000000000000000000000001",
    app_id: appID,
    version: 1,
    status: "superseded",
    api_artifact_digest: apiDigest,
    session_runtime_artifact_digest: sessionRuntimeDigest,
    components: { api: { status: "succeeded" }, session_runtime: { status: "succeeded" } },
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-01T12:02:10Z",
    activated_at: "2026-08-01T12:02:10Z",
  },
  {
    id: "rel_00000000000000000000000000000002",
    app_id: appID,
    version: 2,
    status: "active",
    api_artifact_digest: apiDigest,
    session_runtime_artifact_digest: sessionRuntimeDigest,
    components: { api: { status: "succeeded" }, session_runtime: { status: "succeeded" } },
    created_at: "2026-08-04T08:05:00Z",
    updated_at: "2026-08-04T08:06:44Z",
    activated_at: "2026-08-04T08:06:44Z",
    definition,
  },
  {
    id: "rel_00000000000000000000000000000003",
    app_id: appID,
    version: 3,
    status: "provisioning",
    api_artifact_digest: apiDigest,
    session_runtime_artifact_digest: sessionRuntimeDigest,
    components: { api: { status: "succeeded" }, session_runtime: { status: "running" } },
    created_at: "2026-08-04T09:41:00Z",
    updated_at: "2026-08-04T09:41:12Z",
    definition,
  },
];

// One of each status: set, defaulted, missing, optional, stored as the wrong
// kind, and a value the live build no longer declares.
const configItems: ConfigValue[] = [
  { name: "LOG_LEVEL", kind: "variable", value: "info", updated_at: "2026-08-01T09:00:00Z" },
  { name: "OPENAI_API_KEY", kind: "secret", updated_at: "2026-08-02T16:45:00Z" },
  { name: "SENTRY_DSN", kind: "variable", value: "https://sentry.example/1", updated_at: "2026-08-02T16:45:00Z" },
  { name: "LEGACY_FLAG", kind: "variable", value: "true", updated_at: "2026-07-20T11:00:00Z" },
];

const configDeclarations: EnvironmentDeclarations = {
  release_id: "rel_00000000000000000000000000000002",
  version: 2,
  environment: {
    LOG_LEVEL: { default: "warn" },
    MODEL: { default: "sonnet" },
    OPENAI_API_KEY: { secret: true, required: true },
    SENTRY_DSN: { secret: true },
    SLACK_SIGNING_SECRET: { secret: true, required: true },
    WEBHOOK_TIMEOUT_MS: {},
  },
};

// The live build's generated ingress beside three days of traffic: every SDK
// command (status polls outnumber the dispatches they follow, and one command
// has never been called), the homepage, a webhook, and undeclared paths — a
// probe, and a backend still posting to an earlier release's single /commands.
function previewRoutes(now: number): AppRouteOverview {
  const ago = (seconds: number) => new Date(now - seconds * 1_000).toISOString();
  const command = (type: string, requests: number, client: number, server: number, medianUs: number, lastSeconds: number, messages = 0, failed = 0) =>
    ({ method: "POST", path: `/commands/${type}`, traffic: { requests, client_errors: client, server_errors: server, median_duration_us: medianUs, last_seen_at: ago(lastSeconds), messages, failed_attempts: failed } });
  return {
    release: { id: "rel_00000000000000000000000000000002", version: 2 },
    declaration: "declared",
    routes: [
      { method: "GET", path: "/", traffic: { requests: 312, client_errors: 0, server_errors: 0, median_duration_us: 3_100, last_seen_at: ago(640), messages: 0, failed_attempts: 0 } },
      { method: "HEAD", path: "/" },
      command("cancel", 37, 0, 0, 48_300, 9_200, 37, 0),
      command("dispatch", 8_412, 61, 4, 58_900, 12, 8_347, 191),
      command("request", 2_204, 18, 3, 4_120_000, 140, 2_183, 23),
      command("status", 13_880, 9, 0, 21_400, 9),
      command("steer", 611, 2, 0, 57_100, 1_300, 609, 0),
      command("stop", 18, 0, 0, 132_000, 22_000),
      command("stream", 940, 4, 0, 61_000, 75),
      command("view", 1_502, 0, 0, 18_700, 410),
      { method: "POST", path: "/commands/workspace.database" },
      command("workspace.resolve", 1_210, 6, 0, 26_500, 3_600),
      { method: "POST", path: "/webhooks/github", traffic: { requests: 41, client_errors: 3, server_errors: 0, median_duration_us: 38_400, last_seen_at: ago(5_400), messages: 38, failed_attempts: 0 } },
    ],
    observed_only: [
      { method: "POST", path: "/commands", traffic: { requests: 46, client_errors: 46, server_errors: 0, median_duration_us: 1_900, last_seen_at: ago(86_000), messages: 0, failed_attempts: 0 } },
      { method: "GET", path: "/.env", traffic: { requests: 18, client_errors: 18, server_errors: 0, median_duration_us: 610, last_seen_at: ago(7_300), messages: 0, failed_attempts: 0 } },
    ],
    traffic_window_hours: 72,
  };
}

// One command's outcomes: session.dispatch() is healthy at the edge while a
// slice of the work it delivers fails in sessions. Its recent calls mix
// completed, failed, still-running and rejected ones.
function previewRouteDetail(now: number, path: string): AppRouteDetailData {
  const ago = (seconds: number) => new Date(now - seconds * 1_000).toISOString();
  const id = (n: number) => `req_${String(n).padStart(32, "0")}`;
  const recent = [
    { id: id(100), trace_id: "t0", status_code: 202, duration_us: 54_000, received_at: ago(12), attempt_outcome: "pending", session_id: "support:ticket-4182", message_id: "msg_00000000000000000000000000000100" },
    { id: id(101), trace_id: "t1", status_code: 202, duration_us: 58_000, received_at: ago(95), attempt_outcome: "failed", attempt_error_code: "model_timeout", session_id: "support:ticket-4179", message_id: "msg_00000000000000000000000000000101" },
    { id: id(105), trace_id: "t5", status_code: 202, duration_us: 61_200, received_at: ago(140), attempt_outcome: "completed", session_id: "support:ticket-4177", message_id: "msg_00000000000000000000000000000105" },
    { id: id(102), trace_id: "t2", status_code: 500, duration_us: 1_200, received_at: ago(340) },
    { id: id(106), trace_id: "t6", status_code: 202, duration_us: 57_400, received_at: ago(610), attempt_outcome: "completed", session_id: "support:ticket-4171", message_id: "msg_00000000000000000000000000000106" },
    { id: id(103), trace_id: "t3", status_code: 202, duration_us: 61_000, received_at: ago(1_900), attempt_outcome: "failed", attempt_error_code: "sandbox_lost", session_id: "support:ticket-4160", message_id: "msg_00000000000000000000000000000103" },
    { id: id(104), trace_id: "t4", status_code: 400, duration_us: 900, received_at: ago(4_100) },
  ];
  return {
    method: "POST", path, declared: true,
    http: {
      requests: 8_412,
      status_counts: [{ status: 202, requests: 8_347 }, { status: 400, requests: 56 }, { status: 429, requests: 5 }, { status: 500, requests: 4 }],
      p50_duration_us: 58_900, p95_duration_us: 412_000, first_seen_at: ago(259_000), last_seen_at: ago(12),
    },
    downstream: {
      messages: 8_347, attempts: 8_561, completed: 8_097, failed: 191, pending: 273,
      p50_time_to_start_us: 1_840_000, p50_time_to_finish_us: 23_600_000,
      error_codes: [{ code: "model_timeout", attempts: 118 }, { code: "sandbox_lost", attempts: 51 }, { code: "handler_threw", attempts: 22 }],
    },
    recent_requests: recent,
    recent_failures: recent.filter((request) => (request.status_code ?? 0) >= 400 || request.attempt_outcome === "failed"),
    traffic_window_hours: 72,
  };
}

// The trace a recent call opens to: the request, the message it delivered,
// one attempt on a Sandbox, the handler's span and what it logged.
function previewCallTraces(now: number, path: string): Record<string, TraceRequest> {
  const at = (seconds: number) => new Date(now - seconds * 1_000).toISOString();
  const trace = (n: number, seconds: number, outcome: "completed" | "failed" | "pending", session: string, errorCode?: string): TraceRequest => {
    const pad = String(n).padStart(32, "0");
    const finished = outcome === "pending" ? undefined : at(seconds - 24);
    return {
      id: `req_${pad}`, method: "POST", path, status_code: 202, duration_us: 58_000,
      received_at: at(seconds), completed_at: at(seconds - 0.06), logs: [],
      messages: [{
        id: `msg_${pad}`, accepted_at: at(seconds - 0.05),
        session: { id: `00000000-0000-4000-8000-${pad.slice(-12)}`, logical_session_id: session, workspace_id: previewWorkspaces[0].id, first_seen_at: at(seconds + 600), last_seen_at: at(seconds - 20) },
        attempts: [{
          id: `att_${pad}`, number: 1, outcome, ...(errorCode ? { error_code: errorCode } : {}),
          trace_id: pad, span_id: pad.slice(-16).replace(/^0/, "a"),
          queued_at: at(seconds - 0.05), routed_at: at(seconds - 1.8), ...(finished ? { finished_at: finished } : {}),
          sandbox: { id: `sbx-${pad}`, allocated_at: at(seconds - 1.8), logs: [] },
          spans: [{
            id: pad.slice(-16).replace(/^0/, "b"), name: "session.receive", kind: "internal", status: outcome === "failed" ? "error" : outcome === "completed" ? "ok" : "unset",
            started_at: at(seconds - 1.7), ...(finished ? { finished_at: finished } : {}), attributes: { "cantelop.command": "dispatch" },
          }],
          logs: [
            { id: `log_${pad}`, severity: "info", body: "Triaging ticket with the support playbook", occurred_at: at(seconds - 1.6), attributes: {} },
            ...(outcome === "failed" ? [{ id: `log_${pad.slice(0, -1)}9`, severity: "error", body: `Handler failed: ${errorCode}`, occurred_at: at(seconds - 24), attributes: {} }] : []),
          ],
        }],
      }],
    };
  };
  const id = (n: number) => `req_${String(n).padStart(32, "0")}`;
  return {
    [id(100)]: trace(100, 12, "pending", "support:ticket-4182"),
    [id(101)]: trace(101, 95, "failed", "support:ticket-4179", "model_timeout"),
    [id(105)]: trace(105, 140, "completed", "support:ticket-4177"),
    [id(106)]: trace(106, 610, "completed", "support:ticket-4171"),
    [id(103)]: trace(103, 1_900, "failed", "support:ticket-4160", "sandbox_lost"),
  };
}

// A request just sent from the console: the App accepted it, and the trace
// of the work it started is still filling in.
function previewSent(): Parameters<typeof ApiRouteDetail>[0]["send"] {
  const requestId = "req_00000000000000000000000000000777";
  return {
    state: "answered",
    tracePolling: true,
    response: {
      request_id: requestId, status: 202, duration_ms: 84, body_truncated: false,
      headers: { "content-type": "application/json", "x-cantelop-request-id": requestId },
      body: '{"sessionId":"preview:thread","message":{"id":"msg_00000000000000000000000000000777","state":"accepted"}}',
      sent: { method: "POST", url: `https://${app.hostname}/webhooks/github`, headers: { "content-type": "application/json", "x-cantelop-console": "1" } },
    },
    trace: {
      id: requestId, method: "POST", path: "/webhooks/github", status_code: 202, duration_us: 84_000,
      received_at: new Date(Date.now() - 6_000).toISOString(), completed_at: new Date(Date.now() - 5_900).toISOString(), logs: [],
      messages: [{
        id: "msg_00000000000000000000000000000777", accepted_at: new Date(Date.now() - 5_800).toISOString(),
        session: {
          id: "00000000-0000-4000-8000-000000000777", logical_session_id: "preview:thread", workspace_id: previewWorkspaces[0].id,
          first_seen_at: new Date(Date.now() - 5_800).toISOString(), last_seen_at: new Date(Date.now() - 4_100).toISOString(),
        },
        attempts: [{
          id: "att_00000000000000000000000000000777", number: 1, outcome: "pending", trace_id: "0".repeat(31) + "7", span_id: "0".repeat(15) + "7",
          queued_at: new Date(Date.now() - 5_800).toISOString(), routed_at: new Date(Date.now() - 4_100).toISOString(),
          sandbox: { id: "sbx-00000000000000000000000000000777", allocated_at: new Date(Date.now() - 4_100).toISOString(), logs: [] }, spans: [], logs: [],
        }],
      }],
    },
  };
}

export default async function AppPreviewPage({
  searchParams,
}: {
  searchParams: Promise<{ section?: string | string[]; route?: string | string[]; workspace?: string | string[]; path?: string | string[]; undeployed?: string | string[] }>;
}) {
  if (process.env.NODE_ENV === "production") notFound();
  const { section: raw, route, workspace, path: routePath, undeployed } = await searchParams;
  const section: AppSectionId = typeof raw === "string" && isAppSectionId(raw) ? raw : "home";
  // The route page previews whichever row was opened, a dispatch by default.
  const previewPath = typeof routePath === "string" && routePath.startsWith("/") ? routePath : "/commands/dispatch";
  // ?undeployed shows the Environment of an App before its first deploy.
  const searchDeclarations = undeployed === undefined ? configDeclarations : null;
  return (
    <AppScreen
      app={app}
      workspaces={previewWorkspaces}
      allApps={[app]}
      allWorkspaces={previewWorkspaces}
      runtime={Promise.resolve({ instances: previewSnapshot.sandboxes, billingSummary: appBillingSummary(previewSnapshot, app.id) })}
      releases={releases}
      polling={false}
      section={section}
      sectionHrefBase="/preview/app?section="
      viewer={{ login: "customer", email: "customer@example.com", avatarUrl: "https://avatars.githubusercontent.com/u/9919?s=64&v=4" }}
    >
      {(section === "home" || section === "sandboxes") && (
        <ObservabilityView
          appId={appID}
          workspaces={previewWorkspaces}
          snapshot={previewSnapshot}
          unavailable={false}
          initialEvents={previewEvents}
          initialInvocations={previewInvocations}
          view={section === "home" ? "graphs" : "sandboxes"}
          showHead={section !== "home"}
          polling={false}
        />
      )}
      {section === "workspaces" && workspace !== undefined && (
        <WorkspaceDetail
          appId={appID}
          workspace={previewWorkspaces[0]}
          initialSessions={previewSnapshot.sessions.filter(({ workspace_id }) => workspace_id === previewWorkspaces[0].id)}
        />
      )}
      {section === "workspaces" && workspace === undefined && (
        <WorkspacesSection
          appId={appID}
          appHostname={app.hostname}
          workspaces={previewWorkspaces}
          instances={previewSnapshot.sandboxes}
          databases={Object.fromEntries(previewWorkspaces.map((workspace, index) => [workspace.id, index === 0 ? "ready" : "provisioning"]))}
        />
      )}
      {section === "access" && (
        <>
          <SectionHead title="Access" />
          <div className="section-panel">
            <AppAccessPanel appId={appID} owner={{ userID: "owner", email: "customer@example.com" }}
              initialCollaborators={[{ userID: "admin", email: "alex@example.com", role: "admin", createdAt: "2026-09-20T12:00:00Z" }]}
              initialInvitations={[
                { id: "sent", email: "new-teammate@example.com", delivery: "sent", createdAt: new Date(), expiresAt: new Date(Date.now() + 7 * 86_400_000) },
                { id: "failed", email: "another-teammate@example.com", delivery: "failed", createdAt: new Date(), expiresAt: new Date(Date.now() + 7 * 86_400_000) },
                { id: "expired", email: "expired@example.com", delivery: "sent", createdAt: "2026-01-01T12:00:00Z", expiresAt: "2026-01-08T12:00:00Z" },
              ]} canManage />
          </div>
        </>
      )}
      {section === "builds" && <BuildsSection />}
      {section === "credentials" && (
        <>
          <SectionHead title="Credentials" />
          <CredentialsPanel appId={appID} initial={{
            issued: true, slug: app.slug, edge_url: `https://${app.hostname}`,
            previous_token_expires_at: new Date(Date.now() + 20 * 3_600_000).toISOString(),
            rotation: { revision: 7, rotated_at: new Date(Date.now() - 4 * 3_600_000).toISOString(), status: "applied" },
          }} />
        </>
      )}
      {section === "api" && route !== undefined && (
        <ApiRouteDetail
          appId={appID}
          method="POST"
          path={previewPath}
          origin={`https://${app.hostname}`}
          initial={previewRouteDetail(Date.now(), previewPath)}
          traces={previewCallTraces(Date.now(), previewPath)}
          send={previewSent()}
          apiHref="/preview/app?section=api"
          tracesHrefBase="/preview/app?section=traces&path="
          polling={false}
        />
      )}
      {section === "api" && route === undefined && (
        <ApiRoutesSection
          appId={appID}
          appHostname={app.hostname}
          initial={previewRoutes(Date.now())}
          tracesHrefBase="/preview/app?section=traces&path="
          routeHrefBase="/preview/app?section=api&route=1&"
          polling={false}
        />
      )}
      {section === "configuration" && (
        <>
          <SectionHead title="Environment" />
          <div className="section-panel">
            <ConfigPanel scope="apps" scopeId={appID} scopeLabel="App" initialItems={configItems} declarations={searchDeclarations} unavailable={false} />
          </div>
        </>
      )}
    </AppScreen>
  );
}
