import { expect, test } from "bun:test";

import { ReleaseRepository } from "./release-repository";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const releaseID = "rel_0123456789abcdef0123456789abcdef";
const apiArtifactID = "art_0123456789abcdef0123456789abcdef";
const sessionRuntimeArtifactID = "sra_0123456789abcdef0123456789abcdef";
const apiOperationID = "op_0123456789abcdef0123456789abcdef";
const sessionRuntimeOperationID = "op_fedcba9876543210fedcba9876543210";
const apiDigest = `sha256:${"a".repeat(64)}`;
const sessionRuntimeDigest = `sha256:${"b".repeat(64)}`;
const apiObjectKey = "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs";
const sessionRuntimeObjectKey = "deployment-uploads/0123456789abcdef0123456789abcdef/session-runtime/image.tar.gz";
const apiManifest = edge;
// What a pre-1.0 customer-authored API Worker recorded; it stays in history only.
const legacyManifest = { schema_version: 2, kind: "cantelop-edge-api", main_module: "worker.mjs" };
const sessionRuntimeMediaType = "application/vnd.cantelop.session-runtime.image.v1.tar+gzip";
const at = new Date("2026-08-12T12:00:00Z");

// Every release read is the one drizzle query; replays filter on the owner's
// idempotency key and the transactional reads on the release ID alone.
const isReleaseRead = (sql) => sql.startsWith("select") && sql.includes('"api_component"');
const isReleaseReplay = (sql) => isReleaseRead(sql) && sql.includes('"cantelop_control"."releases"."idempotency_key" =');
const isReleaseByID = (sql) => isReleaseRead(sql) && sql.includes('where "cantelop_control"."releases"."id" = $3');

function fakePool(handler) {
  const statements = [];
  const client = {
    async query(sql, values = []) {
      statements.push({ sql, values });
      if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql)) return { rows: [], rowCount: null };
      return handler(sql, values);
    },
    release() {},
  };
  return { statements, async connect() { return client; }, query: client.query };
}

function releaseRow(overrides = {}) {
  return {
    id: releaseID,
    app_id: appID,
    version: 1,
    status: "queued",
    api_artifact_digest: apiDigest,
    session_runtime_artifact_digest: sessionRuntimeDigest,
    api_operation_id: apiOperationID,
    api_status: "queued",
    api_output: null,
    api_error_code: null,
    api_attempts: 2,
    api_updated_at: at,
    api_next_attempt_at: at,
    session_runtime_operation_id: sessionRuntimeOperationID,
    session_runtime_status: "queued",
    session_runtime_output: null,
    session_runtime_error_code: null,
    error_code: null,
    created_at: at,
    updated_at: at,
    activated_at: null,
    ...overrides,
  };
}

test("reads releases through typed component aliases and owner scope", async () => {
  const pool = fakePool((sql, values) => {
    expect(sql).toContain('inner join "cantelop_control"."release_components" "api_component"');
    expect(sql).toContain('inner join "cantelop_control"."release_components" "session_runtime_component"');
    expect(sql).toContain('as "api_status"');
    expect(sql).toContain('as "session_runtime_status"');
    expect(values).toEqual(["api", "session_runtime", releaseID, appID, userID]);
    return { rowCount: 1, rows: [releaseRow()] };
  });
  const repository = new ReleaseRepository(pool);
  expect(await repository.getForUser(userID, appID, releaseID)).toMatchObject({
    id: releaseID,
    components: {
      api: { operationID: apiOperationID, attempts: 2, updatedAt: at, nextAttemptAt: at },
      session_runtime: { operationID: sessionRuntimeOperationID },
    },
  });
});

test("reads every operable App's live release in one manifest-free statement", async () => {
  const pool = fakePool((sql, values) => {
    expect(sql).toContain("r.status = 'active'");
    expect(sql).toContain("c.role = 'admin'");
    expect(sql).not.toContain("manifest");
    expect(values).toEqual([userID]);
    return { rowCount: 2, rows: [
      { app_id: appID, version: 3, activated_at: at },
      { app_id: "app_fedcba9876543210fedcba9876543210", version: 1, activated_at: null },
    ] };
  });
  expect(await new ReleaseRepository(pool).activeReleasesForUser(userID)).toEqual([
    { appID, version: 3, activatedAt: at },
    { appID: "app_fedcba9876543210fedcba9876543210", version: 1 },
  ]);
  expect(pool.statements).toHaveLength(1);
});

test("reads the active release's declared routes in owner scope", async () => {
  const routes = [
    { method: "GET", path: "/" }, { method: "HEAD", path: "/" },
    { method: "POST", path: "/commands/app.environment.set" },
    { method: "POST", path: "/commands/app.environment.status" },
    { method: "POST", path: "/commands/app.environment.unset" },
    { method: "POST", path: "/commands/cancel" },
    { method: "POST", path: "/commands/dispatch" },
    { method: "POST", path: "/commands/request" },
    { method: "POST", path: "/commands/status" },
    { method: "POST", path: "/commands/steer" },
    { method: "POST", path: "/commands/stop" },
    { method: "POST", path: "/commands/stream" },
    { method: "POST", path: "/commands/view" },
    { method: "POST", path: "/commands/workspace.database" },
    { method: "POST", path: "/commands/workspace.delete" },
    { method: "POST", path: "/commands/workspace.inspect" },
    { method: "POST", path: "/commands/workspace.resolve" },
    { method: "POST", path: "/webhooks/github" },
  ];
  let manifest = apiManifest;
  const pool = fakePool((sql, values) => {
    expect(sql).toContain('inner join "cantelop_control"."api_artifacts"');
    expect(values).toEqual([appID, userID, "active"]);
    return manifest === undefined ? { rowCount: 0, rows: [] } : { rowCount: 1, rows: [{ id: releaseID, version: 4, manifest }] };
  });
  const repository = new ReleaseRepository(pool);
  expect(await repository.activeRoutesForUser(userID, appID)).toEqual({ releaseID, version: 4, routes });
  manifest = legacyManifest;
  expect(await repository.activeRoutesForUser(userID, appID)).toEqual({ releaseID, version: 4, routes: null });
  manifest = undefined;
  expect(await repository.activeRoutesForUser(userID, appID)).toBeNull();
});

test.each(["direct", "finalize", "cancelled"])("creates release and upload finalization atomically: %s", async (outcome) => {
  const pool = fakePool((sql, values) => {
    if (isReleaseReplay(sql)) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT u.status")) return { rowCount: 1, rows: [{ status: "active" }] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.operations")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.api_artifacts")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("SELECT id, digest, object_key, size_bytes, manifest")) return {
      rowCount: 1,
      rows: [{ id: apiArtifactID, digest: apiDigest, object_key: apiObjectKey, size_bytes: 27, manifest: apiManifest }],
    };
    if (sql.startsWith("INSERT INTO cantelop_control.session_runtime_artifacts")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("SELECT id, object_key, size_bytes, media_type")) return {
      rowCount: 1,
      rows: [{ id: sessionRuntimeArtifactID, object_key: sessionRuntimeObjectKey, size_bytes: 2048, media_type: sessionRuntimeMediaType, digest: sessionRuntimeDigest, manifest: runtimeManifest }],
    };
    if (sql.startsWith("SELECT coalesce")) return { rowCount: 1, rows: [{ version: 1 }] };
    if (sql.startsWith("INSERT INTO cantelop_control.releases")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.release_components")) return { rowCount: 2, rows: [] };
    if (sql.startsWith("UPDATE cantelop_control.deployment_uploads")) {
      expect(sql).toContain("status = 'uploading' AND expires_at > $5");
      expect(sql).toContain("owner_user_id = $2 AND app_id = $3 AND idempotency_key = $6");
      expect(values.slice(0, 3)).toEqual(["upl_0123456789abcdef0123456789abcdef", userID, appID]);
      expect(values.slice(5)).toEqual(["release-request-012345", apiDigest, sessionRuntimeDigest]);
      return { rowCount: outcome === "cancelled" ? 0 : 1, rows: [] };
    }
    if (isReleaseByID(sql)) {
      return { rowCount: 1, rows: [releaseRow({ id: values[2] })] };
    }
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  const pending = repository.begin(userID, appID, {
    digest: apiDigest,
    objectKey: apiObjectKey,
    sizeBytes: 27,
    manifest: apiManifest,
  }, {
    digest: sessionRuntimeDigest,
    objectKey: sessionRuntimeObjectKey,
    sizeBytes: 2048,
    mediaType: sessionRuntimeMediaType,
    manifest: runtimeManifest,
  }, "release-request-012345", outcome === "direct" ? undefined : "upl_0123456789abcdef0123456789abcdef");
  if (outcome === "cancelled") {
    await expect(pending).rejects.toMatchObject({ code: "conflict" });
    expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
    expect(pool.statements.some(({ sql }) => sql === "COMMIT")).toBe(false);
    return;
  }
  const release = await pending;

  expect(release).toMatchObject({
    appID,
    version: 1,
    status: "queued",
    apiArtifactDigest: apiDigest,
    sessionRuntimeArtifactDigest: sessionRuntimeDigest,
    components: { api: { status: "queued" }, session_runtime: { status: "queued" } },
  });
  const operations = pool.statements.filter(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(operations).toHaveLength(2);
  expect(operations.map(({ values }) => values[3])).toEqual(["deploy_api", "materialize_build"]);
  expect(operations.map(({ values }) => JSON.parse(values[5]).release_id)).toEqual([release.id, release.id]);
  expect(JSON.parse(operations[1].values[5]).size_bytes).toBe(2048);
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
  expect(pool.statements.filter(({ sql }) => sql.startsWith("UPDATE cantelop_control.deployment_uploads")))
    .toHaveLength(outcome === "direct" ? 0 : 1);
});

test("keeps manifests distinct for equal module bytes in separate uploads", async () => {
  const artifactRows = new Map();
  const storedReleaseArtifactIDs = [];
  const pool = fakePool((sql, values) => {
    if (isReleaseReplay(sql)) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT u.status")) return { rowCount: 1, rows: [{ status: "active" }] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.releases") ||
        sql.startsWith("SELECT 1 FROM cantelop_control.operations")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.api_artifacts")) {
      expect(sql).toContain("ON CONFLICT (object_key) DO NOTHING");
      if (!artifactRows.has(values[2])) artifactRows.set(values[2], {
        id: values[0], digest: values[1], object_key: values[2], size_bytes: values[3], manifest: JSON.parse(values[4]),
      });
      return { rowCount: 1, rows: [] };
    }
    if (sql.startsWith("SELECT id, digest, object_key, size_bytes, manifest")) {
      expect(sql).toContain("WHERE object_key = $1");
      const row = artifactRows.get(values[0]);
      return { rowCount: row ? 1 : 0, rows: row ? [row] : [] };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.session_runtime_artifacts")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("SELECT id, object_key, size_bytes, media_type")) return { rowCount: 1, rows: [{
      id: sessionRuntimeArtifactID, object_key: sessionRuntimeObjectKey, size_bytes: 2048,
      media_type: sessionRuntimeMediaType, digest: sessionRuntimeDigest, manifest: runtimeManifest,
    }] };
    if (sql.startsWith("SELECT coalesce")) return { rowCount: 1, rows: [{ version: 1 }] };
    if (sql.startsWith("INSERT INTO cantelop_control.releases")) {
      storedReleaseArtifactIDs.push(values[3]);
      return { rowCount: 1, rows: [] };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.operations") ||
        sql.startsWith("INSERT INTO cantelop_control.release_components")) return { rowCount: 1, rows: [] };
    if (isReleaseByID(sql)) return {
      rowCount: 1, rows: [releaseRow({ id: values[2] })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  const otherKey = "deployment-uploads/fedcba9876543210fedcba9876543210/api/worker.mjs";
  const manifestWithRoutes = { ...apiManifest, webhooks: [{ path: "/webhooks/slack", method: "POST" }] };
  for (const [objectKey, manifest, key] of [
    [apiObjectKey, apiManifest, "first-upload-key"],
    [otherKey, manifestWithRoutes, "second-upload-key"],
  ]) {
    await repository.begin(userID, appID, { digest: apiDigest, objectKey, sizeBytes: 27, manifest }, {
      digest: sessionRuntimeDigest, objectKey: sessionRuntimeObjectKey, sizeBytes: 2048,
      mediaType: sessionRuntimeMediaType, manifest: runtimeManifest,
    manifest: runtimeManifest,
    }, key);
  }
  expect(artifactRows.size).toBe(2);
  expect(storedReleaseArtifactIDs).toHaveLength(2);
  expect(storedReleaseArtifactIDs[0]).not.toBe(storedReleaseArtifactIDs[1]);
  expect([...artifactRows.values()].map(({ manifest }) => manifest)).toEqual([apiManifest, manifestWithRoutes]);
});

test("release replay binds the original API manifest and upload object", async () => {
  const pool = fakePool((sql) => {
    if (isReleaseReplay(sql)) return {
      rowCount: 1,
      rows: [releaseRow({ api_object_key: apiObjectKey, api_size_bytes: 27, api_manifest: apiManifest, session_runtime_manifest: runtimeManifest })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  const runtime = { digest: sessionRuntimeDigest, objectKey: sessionRuntimeObjectKey,
    sizeBytes: 2048, mediaType: sessionRuntimeMediaType, manifest: runtimeManifest };
  const api = { digest: apiDigest, objectKey: apiObjectKey, sizeBytes: 27, manifest: apiManifest };
  expect(await repository.begin(userID, appID, api, runtime, "original-upload-key")).toMatchObject({ id: releaseID });
  await expect(repository.begin(userID, appID, { ...api, manifest: {
    ...apiManifest, webhooks: [{ path: "/webhooks/slack", method: "POST" }],
  } }, runtime, "original-upload-key")).rejects.toMatchObject({ code: "conflict" });
  await expect(repository.begin(userID, appID, { ...api,
    objectKey: "deployment-uploads/fedcba9876543210fedcba9876543210/api/worker.mjs",
  }, runtime, "original-upload-key")).rejects.toMatchObject({ code: "conflict" });
});

test("queues activation only when both serialized child completions succeed", async () => {
  const states = { api: "running", sessionRuntime: "running" };
  const operationKind = new Map([[apiOperationID, "api"], [sessionRuntimeOperationID, "sessionRuntime"]]);
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT c.release_id")) return { rowCount: 1, rows: [{ release_id: releaseID }] };
    if (sql.startsWith("UPDATE cantelop_control.release_components c")) {
      states[operationKind.get(values[0])] = "succeeded";
      return { rowCount: 1, rows: [{ release_id: releaseID }] };
    }
    if (sql.startsWith("UPDATE cantelop_control.operations")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.release_components")) {
      const remaining = Object.values(states).filter((status) => status !== "succeeded").length;
      return { rowCount: remaining, rows: Array.from({ length: remaining }, () => ({ "?column?": 1 })) };
    }
    if (sql.startsWith("SELECT a.owner_user_id")) return { rowCount: 1, rows: [{ owner_user_id: userID, id: appID, active_release_id: null }] };
    if (sql.startsWith("SELECT nextval")) return { rowCount: 1, rows: [{ generation: "1" }] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    if (isReleaseRead(sql)) return {
      rowCount: 1,
      rows: [releaseRow({
        status: "provisioning",
        api_status: states.api,
        api_output: states.api === "succeeded" ? { provider_release_id: "cf-1" } : null,
        session_runtime_status: states.sessionRuntime,
        session_runtime_output: states.sessionRuntime === "succeeded" ? { provider_build_id: "build-1" } : null,
        activated_at: null,
      })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);

  const afterAPI = await repository.succeedComponent(apiOperationID, "api-worker", { provider_release_id: "cf-1" });
  expect(afterAPI).toMatchObject({ status: "provisioning", components: { api: { status: "succeeded" }, session_runtime: { status: "running" } } });
  expect(pool.statements.filter(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"))).toHaveLength(0);

  const afterSessionRuntime = await repository.succeedComponent(sessionRuntimeOperationID, "session-runtime-worker", { provider_build_id: "build-1" });
  expect(afterSessionRuntime).toMatchObject({ status: "provisioning", components: { api: { status: "succeeded" }, session_runtime: { status: "succeeded" } } });
  expect(pool.statements.filter(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"))).toHaveLength(1);
  expect(pool.statements.filter(({ sql }) => sql.startsWith("UPDATE cantelop_control.apps"))).toHaveLength(0);
  expect(pool.statements.filter(({ sql }) => sql.startsWith("UPDATE cantelop_control.workspaces"))).toHaveLength(0);
});

test("failing one component durably cancels its unfinished sibling", async () => {
  let releaseFailed = false;
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT c.release_id")) return { rowCount: 1, rows: [{ release_id: releaseID }] };
    if (sql.startsWith("UPDATE cantelop_control.release_components c")) {
      return { rowCount: 1, rows: [{ release_id: releaseID }] };
    }
    if (sql.startsWith("UPDATE cantelop_control.operations\nSET status = 'failed'")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("UPDATE cantelop_control.release_components\nSET status = 'failed'")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("UPDATE cantelop_control.operations o")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("UPDATE cantelop_control.releases\nSET status = 'failed'")) {
      releaseFailed = true;
      return { rowCount: 1, rows: [] };
    }
    if (isReleaseRead(sql)) return {
      rowCount: 1,
      rows: [releaseRow({
        status: "failed",
        api_status: "failed",
        api_error_code: "provider_rejected",
        session_runtime_status: "failed",
        session_runtime_error_code: "release_failed",
        error_code: "provider_rejected",
      })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  const release = await repository.failComponent(apiOperationID, "api-worker", "provider_rejected");

  expect(release).toMatchObject({
    status: "failed",
    errorCode: "provider_rejected",
    components: {
      api: { status: "failed", errorCode: "provider_rejected" },
      session_runtime: { status: "failed", errorCode: "release_failed" },
    },
  });
  expect(releaseFailed).toBe(true);
  expect(pool.statements.some(({ sql }) => sql.startsWith("UPDATE cantelop_control.apps"))).toBe(false);
});

test("requeues release component state with its durable operation", async () => {
  const availableAt = new Date("2026-08-12T12:00:02Z");
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT c.release_id")) return { rowCount: 1, rows: [{ release_id: releaseID }] };
    if (sql.startsWith("UPDATE cantelop_control.release_components c")) {
      return { rowCount: 1, rows: [{ release_id: releaseID }] };
    }
    if (sql.startsWith("UPDATE cantelop_control.operations")) {
      expect(values[2]).toEqual(availableAt);
      return { rowCount: 1, rows: [] };
    }
    if (isReleaseRead(sql)) return {
      rowCount: 1,
      rows: [releaseRow({ status: "provisioning", api_status: "queued", session_runtime_status: "running" })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  expect(await repository.retryComponent(apiOperationID, "api-worker", availableAt)).toMatchObject({
    status: "provisioning",
    components: { api: { status: "queued" }, session_runtime: { status: "running" } },
  });
});

test("queues rollback to the most recent superseded release", async () => {
  const previousReleaseID = "rel_fedcba9876543210fedcba9876543210";
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT active_release_id")) return { rowCount: 1, rows: [{ active_release_id: releaseID }] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.operations")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT id\nFROM cantelop_control.releases")) {
      expect(values).toEqual([appID, userID]);
      return { rowCount: 1, rows: [{ id: previousReleaseID }] };
    }
    if (sql.startsWith("SELECT nextval")) return { rowCount: 1, rows: [{ generation: "2" }] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    // The rollback target lookup, then the target release itself.
    if (sql.startsWith("SELECT r.id") || isReleaseRead(sql)) return {
      rowCount: 1,
      rows: [releaseRow({ id: previousReleaseID, version: 1, status: "superseded", activated_at: at })],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  expect(await repository.rollbackForUser(userID, appID)).toMatchObject({
    id: previousReleaseID,
    status: "superseded",
    activatedAt: at,
  });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("rejects rollback while a deployment is pending", async () => {
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT active_release_id")) return { rowCount: 1, rows: [{ active_release_id: releaseID }] };
    if (sql.includes("kind = 'archive_app'")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) return { rowCount: 1, rows: [{ "?column?": 1 }] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ReleaseRepository(pool, () => at);
  expect(repository.rollbackForUser(userID, appID)).rejects.toMatchObject({ code: "conflict" });
  expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
});

test("rejects a console rollback when its displayed target is no longer newest", async () => {
  const displayedID = "rel_fedcba9876543210fedcba9876543210";
  const newerID = "rel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT active_release_id")) return { rowCount: 1, rows: [{ active_release_id: releaseID }] };
    if (sql.includes("kind = 'archive_app'")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT 1 FROM cantelop_control.operations")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT r.id")) return { rowCount: 1, rows: [{ id: newerID }] };
    throw new Error(`unexpected query ${sql}`);
  });
  await expect(new ReleaseRepository(pool, () => at).rollbackForUser(userID, appID, displayedID))
    .rejects.toMatchObject({ code: "conflict" });
  expect(pool.statements.some(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"))).toBe(false);
  expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
});

test("projects an SDK 1 release's App definition from its stored manifests", async () => {
  const pool = fakePool(() => ({ rowCount: 1, rows: [releaseRow({
    api_manifest: {
      kind: "cantelop-protocol-edge", app_name: "agent", session_runtime_id: "rt_1",
      webhooks: [{ path: "/webhooks/github", method: "POST" }], homepage: false,
    },
    session_runtime_manifest: { capabilities: { steering: true, messageCancellation: true, durableView: false } },
  })] }));
  const release = await new ReleaseRepository(pool).getForUser(userID, appID, releaseID);
  expect(release.definition).toMatchObject({
    app_name: "agent",
    webhooks: [{ path: "/webhooks/github", method: "POST" }],
    capabilities: { steering: true, message_cancellation: true, durable_view: false },
  });
});

test("releases that predate SDK 1.0 carry no App definition", async () => {
  const pool = fakePool(() => ({ rowCount: 1, rows: [releaseRow({ api_manifest: legacyManifest, session_runtime_manifest: null })] }));
  expect(await new ReleaseRepository(pool).getForUser(userID, appID, releaseID)).not.toHaveProperty("definition");
});
