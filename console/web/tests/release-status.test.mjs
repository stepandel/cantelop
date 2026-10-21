import { expect, test } from "bun:test";

import { buildLabel, componentProgress, byNewest, definitionLabel, deployPhases, rollbackTarget, summarizeReleases } from "../lib/release-status.ts";

function release(version, status, overrides = {}) {
  return {
    id: `rel_${String(version).padStart(32, "0")}`,
    app_id: "app_0123456789abcdef0123456789abcdef",
    version,
    status,
    api_artifact_digest: "sha256:aa",
    session_runtime_artifact_digest: "sha256:bb",
    components: { api: { status: "succeeded" }, session_runtime: { status: "succeeded" } },
    created_at: "2026-08-04T08:05:00Z",
    updated_at: "2026-08-04T08:06:44Z",
    ...overrides,
  };
}

test("an app with no build history summarizes to nothing", () => {
  expect(summarizeReleases([])).toEqual({});
  expect(summarizeReleases(null)).toEqual({});
});

test("the active build and an in-flight deploy are reported together", () => {
  const active = release(2, "active", { activated_at: "2026-08-04T08:06:44Z" });
  const pending = release(3, "provisioning");
  const summary = summarizeReleases([release(1, "superseded", { activated_at: "2026-08-01T12:02:10Z" }), active, pending]);

  expect(summary.active).toBe(active);
  expect(summary.pending).toBe(pending);
  expect(summary.latest).toBe(pending);
  expect(summary.failed).toBeUndefined();
});

test("a queued deploy counts as in flight before any component starts", () => {
  expect(summarizeReleases([release(1, "queued")]).pending?.version).toBe(1);
});

test("the newest build failing is reported while the previous one stays live", () => {
  const active = release(2, "active", { activated_at: "2026-08-04T08:06:44Z" });
  const summary = summarizeReleases([active, release(3, "failed", { error_code: "session_runtime_unavailable" })]);

  expect(summary.failed?.version).toBe(3);
  expect(summary.active).toBe(active);
});

test("an older failure is not reported once a later build succeeded", () => {
  const summary = summarizeReleases([
    release(2, "failed", { error_code: "session_runtime_unavailable" }),
    release(3, "active", { activated_at: "2026-08-04T08:06:44Z" }),
  ]);

  expect(summary.failed).toBeUndefined();
  expect(summary.latest?.version).toBe(3);
});

test("history reads newest first without mutating the source list", () => {
  const items = [release(1, "superseded"), release(3, "active"), release(2, "superseded")];
  expect(byNewest(items).map(({ version }) => version)).toEqual([3, 2, 1]);
  expect(items.map(({ version }) => version)).toEqual([1, 3, 2]);
});

test("both components of an in-flight deploy are named", () => {
  expect(deployPhases(release(3, "provisioning", {
    components: { api: { status: "succeeded" }, session_runtime: { status: "running" } },
  }))).toEqual(["Edge deployed", "materializing Session runtime"]);
  expect(deployPhases(release(3, "queued", {
    components: { api: { status: "queued" }, session_runtime: { status: "queued" } },
  }))).toEqual(["Edge queued", "Session runtime queued"]);
});

test("a missing build reads as an em dash rather than v-undefined", () => {
  expect(buildLabel(undefined)).toBe("—");
  expect(buildLabel(release(12, "active"))).toBe("v12");
});

test("deployment activity distinguishes initial queue, retry, processing and failure", () => {
  expect(componentProgress({ status: "queued", attempts: 0 })).toBe("Waiting for a worker");
  expect(componentProgress({ status: "queued", attempts: 3 })).toContain("another attempt (3 attempted)");
  expect(componentProgress({ status: "running", attempts: 4 })).toBe("Processing attempt 4");
  expect(componentProgress({ status: "failed", error_code: "provider_deployment_failed" })).toBe("Failed: provider_deployment_failed");
  expect(componentProgress({ status: "queued" })).toBe("Queued");
});

test("cancelling remains pending and cancelled releases stop polling as pending", () => {
 const cancelling=release(1,"cancelling");
 expect(summarizeReleases([cancelling]).pending).toBe(cancelling);
 expect(deployPhases(cancelling)).toEqual(["waiting for provider cancellation"]);
 expect(summarizeReleases([release(1,"cancelled")]).pending).toBeUndefined();
 expect(componentProgress({status:"failed",error_code:"deployment_cancelled"})).toBe("Cancelled");
});

const definition = {
  app_name: "agent", session_runtime_id: "rt", webhooks: [{ method: "POST", path: "/webhooks/github" }],
  homepage: true, native_auth: true, token_rotation: true,
  capabilities: { steering: true, message_cancellation: true, durable_view: true },
};

test("deploy phases name the generated Edge Worker", () => {
  expect(deployPhases(release(3, "queued", {
    definition, components: { api: { status: "running" }, session_runtime: { status: "queued" } },
  }))).toEqual(["deploying Edge", "Session runtime queued"]);
});

test("rollback targets the newest superseded SDK 1.0 release", () => {
  expect(rollbackTarget([release(1, "superseded", { definition }), release(2, "superseded", { definition }), release(3, "active", { definition })])?.version).toBe(2);
  // A release that predates SDK 1.0 is never reactivated.
  expect(rollbackTarget([release(1, "superseded", { definition }), release(2, "superseded"), release(3, "active", { definition })])?.version).toBe(1);
  expect(rollbackTarget([release(1, "failed"), release(2, "active")])).toBeUndefined();
  expect(rollbackTarget(null)).toBeUndefined();
});

test("the App header summarizes the live definition", () => {
  expect(definitionLabel(definition)).toBe("1 webhook · homepage · native sign-in · durable views");
  expect(definitionLabel({ ...definition, webhooks: [], homepage: false, native_auth: false, capabilities: null })).toBe("0 webhooks");
});
