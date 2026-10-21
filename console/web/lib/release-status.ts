import type { PublicRelease } from "@core/deployment-uploads/service";

export type Release = PublicRelease;
export type ReleaseStatus = Release["status"];
export type ComponentStatus = Release["components"]["api"]["status"];

export interface BuildSummary {
  /** The Build currently serving traffic. */
  active?: Release;
  /** An unfinished deploy — the "update in progress". */
  pending?: Release;
  /** The newest Build, only when it failed and nothing newer answered it. */
  failed?: Release;
  /** The newest Build, whatever became of it. */
  latest?: Release;
}

// One App has at most one queued/provisioning release and at most one active
// release — partial unique indexes on cantelop_control.releases enforce both —
// so the summary is a pair of lookups, not a search for the best candidate.
export function summarizeReleases(items: readonly Release[] | null): BuildSummary {
  if (!items || items.length === 0) return {};
  const newestFirst = byNewest(items);
  const latest = newestFirst[0];
  return {
    active: newestFirst.find(({ status }) => status === "active"),
    pending: newestFirst.find(({ status }) => status === "queued" || status === "provisioning" || status === "cancelling"),
    // A failure is news only while it is still the last thing that happened;
    // a later successful deploy has already answered it.
    failed: latest.status === "failed" ? latest : undefined,
    latest,
  };
}

/** Newest first: release history reads top-down like every other console log. */
export function byNewest(items: readonly Release[]): Release[] {
  return [...items].sort((left, right) => right.version - left.version);
}

// A release deploys the Edge Worker the SDK compiler generated from the App
// definition, beside the Session runtime.
const edgePhase: Record<ComponentStatus, string> = {
  queued: "Edge queued",
  running: "deploying Edge",
  succeeded: "Edge deployed",
  failed: "Edge failed",
};

const sessionRuntimePhase: Record<ComponentStatus, string> = {
  queued: "Session runtime queued",
  running: "materializing Session runtime",
  succeeded: "Session runtime materialized",
  failed: "Session runtime failed",
};

// Both components of a release advance independently, so a deploy in progress
// is described by naming where each one stands rather than by one spinner.
export function deployPhases(release: Release): string[] {
  if (release.status === "cancelling") return ["waiting for provider cancellation"];
  return [
    edgePhase[release.components.api.status],
    sessionRuntimePhase[release.components.session_runtime.status],
  ];
}

/** The release `cantelop rollback` would reactivate: the newest superseded
 *  one built with SDK 1.0 — older releases are never reactivated. */
export function rollbackTarget(items: readonly Release[] | null): Release | undefined {
  return byNewest(items ?? []).find(({ status, definition }) => status === "superseded" && definition !== undefined);
}

// Component statuses share names with Sandbox states but not their meaning: a
// component that finished is the boring case and stays quiet, while one that is
// still working is the news. Mapping to the existing state classes keeps the
// palette to the families already in globals.css.
const componentTone: Record<ComponentStatus, string> = {
  queued: "st-queued",
  running: "st-provisioning",
  succeeded: "st-succeeded",
  failed: "st-failed",
};

export function componentToneClass(status: ComponentStatus) {
  return componentTone[status];
}

/** Version as the console writes it everywhere: v12, or an em dash when there is none. */
export function buildLabel(release?: Release) {
  return release ? `v${release.version}` : "—";
}

/** Describe queue activity without guessing provider errors or worker health. */
export function componentProgress(component: Release["components"]["api"]): string {
  if (component.error_code === "deployment_cancelled") return "Cancelled";
  if (component.status === "failed") return component.error_code ? `Failed: ${component.error_code}` : "Failed";
  if (component.status === "succeeded") return "Completed";
  if (component.attempts === undefined) return component.status === "queued" ? "Queued" : "In progress";
  if (component.status === "running") return `Processing attempt ${component.attempts}`;
  return component.attempts === 0 ? "Waiting for a worker" : `Waiting for another attempt (${component.attempts} attempted)`;
}


export function providerProgress(progress?: string): string | undefined {
  if (!progress) return undefined;
  const labels: Record<string, string> = {
    provider_receiving: "Runtime provider is downloading the image",
    provider_queued: "Image is queued at the runtime provider",
    provider_processing: "Runtime provider is building the image",
    provider_unreachable: "Cannot reach the runtime provider",
    provider_invalid_response: "Runtime provider returned an invalid response",
    provider_unavailable: "Runtime provider is unavailable",
    artifact_or_provider_unavailable: "Artifact storage or runtime provider is unavailable",
    provider_http_404: "Runtime provider returned not found (HTTP 404)",
    provider_http_409: "Runtime provider reports a conflicting build",
    provider_http_429: "Runtime provider is rate limiting requests",
  };
  if (labels[progress]) return labels[progress];
  const http = /^provider_http_([1-5][0-9]{2})$/.exec(progress);
  return http ? `Runtime provider returned HTTP ${http[1]}` : undefined;
}

/** One line on what the live App definition declares, for the App header. */
export function definitionLabel(definition: NonNullable<Release["definition"]>): string {
  const webhooks = definition.webhooks.length;
  return [
    webhooks === 1 ? "1 webhook" : `${webhooks} webhooks`,
    definition.homepage && "homepage",
    definition.native_auth && "native sign-in",
    definition.capabilities?.durable_view && "durable views",
  ].filter(Boolean).join(" · ");
}
