import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import pg from "pg";

import { controlDatabaseConfig } from "./database";
import { DeploymentUploadRepository } from "./deployment-upload-repository";
import { OperationRepository } from "./operation-repository";
import { ProductRepository } from "./product-repository";
import { ReleaseRepository } from "./release-repository";
import { newResourceID } from "../domain/ids";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

postgresTest("identical API bytes retain each owner's manifest", async () => {
  const hostname = new URL(databaseURL).hostname;
  const sslMode = process.env.CANTELOP_DATABASE_SSL ??
    (["localhost", "127.0.0.1", "::1"].includes(hostname) ? "disable" : "verify-full");
  const pool = new pg.Pool({
    ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode }), max: 2,
  });
  const digest = `sha256:${"a".repeat(64)}`;
  const runtimeDigest = `sha256:${"b".repeat(64)}`;
  const cleanup = async () => {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.query("DELETE FROM cantelop_control.api_artifacts WHERE digest = $1", [digest]);
    await pool.query("DELETE FROM cantelop_control.session_runtime_artifacts WHERE digest = $1", [runtimeDigest]);
  };
  try {
    await cleanup();
    const products = ProductRepository.fromPool(pool);
    const uploads = new DeploymentUploadRepository(pool);
    const releases = new ReleaseRepository(pool);
    const routes = [[{ path: "/webhooks/alpha", method: "GET" }], [{ path: "/webhooks/beta", method: "POST" }]];
    const created = [];
    for (const [index, ownerRoutes] of routes.entries()) {
      const user = await products.resolveIdentity({ provider: "github", providerSubject: `manifest-owner-${index}`,
        email: `manifest-owner-${index}@example.com` });
      const app = await products.createAppForUser(user.id, { slug: `manifest-${index}`,
        sessionRuntime: { internalPort: 3000 } });
      const uploadID = newResourceID("deploymentUpload");
      const keyPrefix = `deployment-uploads/${uploadID.slice(4)}`;
      const api = { digest, objectKey: `${keyPrefix}/api/worker.mjs`, sizeBytes: 29,
        manifest: { ...edge, webhooks: ownerRoutes } };
      const sessionRuntime = { digest: runtimeDigest,
        objectKey: `${keyPrefix}/session-runtime/image.tar.gz`, sizeBytes: 4096,
        mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest };
      await uploads.begin(uploadID, user.id, app.id, { api, sessionRuntime }, `manifest-upload-${index}`,
        new Date(Date.now() + 3600_000));
      await uploads.markReady(user.id, app.id, uploadID, `store-${index}`, 64 << 20, 1);
      const release = await releases.begin(user.id, app.id, api, sessionRuntime, `manifest-upload-${index}`, uploadID);
      created.push({ id: release.id, routes: ownerRoutes, api, sessionRuntime, uploadID, key: `manifest-upload-${index}`,
        userID: user.id, appID: app.id });
    }
    const { rows } = await pool.query(`SELECT r.id, a.digest, a.manifest, a.object_key
FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts a ON a.id = r.api_artifact_id
ORDER BY a.object_key`);
    expect(rows).toHaveLength(2);
    expect(new Set(rows.map(row => row.digest))).toEqual(new Set([digest]));
    expect(new Set(rows.map(row => row.object_key)).size).toBe(2);
    for (const row of rows) {
      expect(row.manifest.webhooks).toEqual(created.find(item => item.id === row.id).routes);
    }

    // A release deduped onto another upload's row before migration 0023 is rebound, and its replay succeeds.
    const [first, second] = created;
    const { rows: [orphan] } = await pool.query("SELECT api_artifact_id AS id FROM cantelop_control.releases WHERE id = $1",
      [second.id]);
    await pool.query(`UPDATE cantelop_control.releases
SET api_artifact_id = (SELECT api_artifact_id FROM cantelop_control.releases WHERE id = $1) WHERE id = $2`,
    [first.id, second.id]);
    await pool.query("DELETE FROM cantelop_control.api_artifacts WHERE id = $1", [orphan.id]);
    const migration = readFileSync(new URL("./drizzle/0023_api_artifact_object_identity.sql", import.meta.url), "utf8");
    for (const statement of migration.split("--> statement-breakpoint").map(part => part.trim())) {
      if (/^(--[^\n]*\n)*(INSERT|UPDATE)/.test(statement)) await pool.query(statement);
    }
    const { rows: [rebound] } = await pool.query(`SELECT a.object_key, a.manifest FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts a ON a.id = r.api_artifact_id WHERE r.id = $1`, [second.id]);
    expect(rebound.object_key).toBe(second.api.objectKey);
    expect(rebound.manifest.webhooks).toEqual(second.routes);
    expect(await releases.begin(second.userID, second.appID, second.api, second.sessionRuntime, second.key,
      second.uploadID)).toMatchObject({ id: second.id });
  } finally {
    await cleanup();
    await pool.end();
  }
}, 30_000);

postgresTest("product aggregates and operations execute against PostgreSQL", async () => {
  const hostname = new URL(databaseURL).hostname;
  const sslMode = process.env.CANTELOP_DATABASE_SSL ??
    (["localhost", "127.0.0.1", "::1"].includes(hostname) ? "disable" : "verify-full");
  const pool = new pg.Pool({
    ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode }),
    max: 2,
  });
  try {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    const products = ProductRepository.fromPool(pool);
    const user = await products.resolveIdentity({ provider: "github", providerSubject: "qualification-user", email: "test@example.com" });
    const created = await products.createAppForUser(user.id, {
      slug: "qualification",
      sessionRuntime: { internalPort: 3000 },
    });
    const firstWorkspace = await products.createWorkspaceForUser(user.id, { appID: created.id, slug: "production" });
    const secondWorkspace = await products.createWorkspaceForUser(user.id, { appID: created.id, slug: "staging" });
    // Keep qualification operations outside the live worker's claim window.
    // Repositories below share this logical clock and can still claim them.
    const { rows: [{ now: at }] } = await pool.query("SELECT clock_timestamp() + interval '1 hour' AS now");
    expect(await products.listAppsForUser(user.id)).toHaveLength(1);
    expect(await products.listWorkspacesForUser(user.id, created.id)).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: firstWorkspace.id, slug: "production" }),
      expect.objectContaining({ id: secondWorkspace.id, slug: "staging" }),
    ]));

    const operations = new OperationRepository(pool, () => at);
    const queued = await operations.enqueue(user.id, created.id, "deploy_api", "qualification-request-1", {
      artifact_id: "artifact-1",
    });
    expect((await operations.enqueue(user.id, created.id, "deploy_api", "qualification-request-1", {
      artifact_id: "artifact-1",
    })).id).toBe(queued.id);
    const { rows: [queuedState] } = await pool.query(`SELECT id, kind, status, available_at,
       available_at <= $2 AS claimable, $2::timestamptz AS claim_at
FROM cantelop_control.operations WHERE id = $1`, [queued.id, at]);
    expect(queuedState).toMatchObject({ id: queued.id, kind: "deploy_api", status: "queued", claimable: true });
    const claimed = await operations.claim("qualification-worker", "deploy_api");
    expect(claimed).toMatchObject({ id: queued.id, status: "running", attempts: 1 });
    expect(await operations.succeed(queued.id, "qualification-worker", { release_id: "release-1" }))
      .toMatchObject({ status: "succeeded", output: { release_id: "release-1" } });

    const releases = new ReleaseRepository(pool, () => at);
    const uploadID = "upl_0123456789abcdef0123456789abcdef";
    const releaseAPIDigest = `sha256:${"b".repeat(64)}`;
    const releaseSessionRuntimeDigest = `sha256:${"c".repeat(64)}`;
    const releaseAPIArtifact = {
      digest: releaseAPIDigest,
      objectKey: "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs",
      sizeBytes: 29,
      manifest: edge,
    };
    const releaseSessionRuntimeArtifact = {
      digest: releaseSessionRuntimeDigest,
      objectKey: "deployment-uploads/0123456789abcdef0123456789abcdef/session-runtime/image.tar.gz",
      sizeBytes: 4096,
      mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest,
    };
    const uploads = new DeploymentUploadRepository(pool, () => at);
    await uploads.begin(uploadID, user.id, created.id, {
      api: releaseAPIArtifact,
      sessionRuntime: releaseSessionRuntimeArtifact,
    }, "qualification-release-1", new Date(at.getTime() + 60 * 60_000));
    await uploads.markReady(user.id, created.id, uploadID, "store-upload-1", 64 << 20, 1);
    const release = await releases.begin(user.id, created.id, releaseAPIArtifact, releaseSessionRuntimeArtifact, "qualification-release-1", uploadID);
    expect(await uploads.getForUser(user.id, created.id, uploadID)).toMatchObject({ status: "finalized", releaseID: release.id });
    const apiComponent = await operations.claim("qualification-release-api", "deploy_api");
    await releases.startComponent(apiComponent.id, "qualification-release-api");
    expect(await releases.succeedComponent(apiComponent.id, "qualification-release-api", { provider_release_id: "release-3" }))
      .toMatchObject({ id: release.id, status: "provisioning" });
    const { rows: sessionRuntimeOperations } = await pool.query(`SELECT id, status, kind, available_at, locked_by
FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'materialize_build'
ORDER BY created_at, id`, [created.id]);
    expect(sessionRuntimeOperations).toEqual([
      expect.objectContaining({ status: "queued", kind: "materialize_build", locked_by: null }),
    ]);
    const sessionRuntimeComponent = await operations.claim("qualification-release-session-runtime", "materialize_build");
    expect(sessionRuntimeComponent).toBeDefined();
    await releases.startComponent(sessionRuntimeComponent.id, "qualification-release-session-runtime");
    // Completing every component queues activation; the release stays
    // provisioning until the activation worker commits it behind the gates.
    expect(await releases.succeedComponent(sessionRuntimeComponent.id, "qualification-release-session-runtime", { provider_build_id: "build-1" }))
      .toMatchObject({ id: release.id, status: "provisioning" });
    const activation = await operations.claim("qualification-release-activation", "activate_release");
    expect(activation).toMatchObject({ appID: created.id, input: { target_release_id: release.id, previous_release_id: null } });
    expect(await releases.commitActivation(user.id, created.id, release.id, null))
      .toMatchObject({ id: release.id, status: "active" });
    await operations.succeed(activation.id, "qualification-release-activation", { target_release_id: release.id });
    expect(await releases.getForUser(user.id, created.id, release.id)).toMatchObject({ status: "active" });
    expect(await releases.activeReleasesForUser(user.id)).toEqual([
      { appID: created.id, version: release.version, activatedAt: expect.any(Date) },
    ]);
    await products.archiveAppForUser(user.id, created.id);
    // Provider gate closure is exercised by the archive worker. Complete its
    // database transition here to verify the archived product projections.
    await products.completeArchive(user.id, created.id);
    expect(await products.listAppsForUser(user.id)).toHaveLength(0);
    expect(await products.listWorkspacesForUser(user.id, created.id)).toHaveLength(0);
    expect(await releases.activeReleasesForUser(user.id)).toEqual([]);
  } finally {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.end();
  }
}, 30_000);

postgresTest("upload cancellation releases the App lock and serializes with release creation", async () => {
  const hostname = new URL(databaseURL).hostname;
  const sslMode = process.env.CANTELOP_DATABASE_SSL ??
    (["localhost", "127.0.0.1", "::1"].includes(hostname) ? "disable" : "verify-full");
  const pool = new pg.Pool({
    ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode }), max: 4,
  });
  try {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    const products = ProductRepository.fromPool(pool);
    const user = await products.resolveIdentity({ provider: "github", providerSubject: "cancel-owner", email: "cancel@example.com" });
    const foreign = await products.resolveIdentity({ provider: "github", providerSubject: "other-owner", email: "other@example.com" });
    const at = new Date(Date.now() + 3600_000);
    const expiresAt = new Date(at.getTime() + 3600_000);
    const uploads = new DeploymentUploadRepository(pool, () => at);
    const releases = new ReleaseRepository(pool, () => at);
    function input(id) {
      return {
        api: { digest: `sha256:${"a".repeat(64)}`, objectKey: `deployment-uploads/${id.slice(4)}/api/worker.mjs`, sizeBytes: 27,
          manifest: edge },
        sessionRuntime: { digest: `sha256:${"b".repeat(64)}`, objectKey: `deployment-uploads/${id.slice(4)}/session-runtime/image.tar.gz`,
          sizeBytes: 1024, mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest },
      };
    }
    for (const [index, order] of ["cancel-first", "finalize-first", "race", "race", "race"].entries()) {
      const app = await products.createAppForUser(user.id, { slug: `cancel-${index}`, sessionRuntime: { internalPort: 3000 } });
      const id = newResourceID("deploymentUpload");
      const bundle = input(id);
      const key = `cancel-integration-${index}`;
      await uploads.begin(id, user.id, app.id, bundle, key, expiresAt);
      await uploads.markReady(user.id, app.id, id, "store-1", 64 << 20, 1);
      expect(await uploads.listPendingForUser(foreign.id, app.id)).toEqual([]);
      await expect(uploads.cancelForUser(foreign.id, app.id, id)).rejects.toMatchObject({ code: "not_found" });
      const replayID = newResourceID("deploymentUpload");
      expect((await uploads.begin(replayID, user.id, app.id, input(replayID), key, expiresAt)).id).toBe(id);
      const nextID = newResourceID("deploymentUpload");
      await expect(uploads.begin(nextID, user.id, app.id, input(nextID), `${key}-next`, expiresAt)).rejects.toMatchObject({ code: "conflict" });
      const cancel = () => uploads.cancelForUser(user.id, app.id, id);
      const finalize = () => releases.begin(user.id, app.id, bundle.api, bundle.sessionRuntime, key, id);
      if (order === "cancel-first") {
        await cancel();
        await expect(finalize()).rejects.toMatchObject({ code: "conflict" });
      } else if (order === "finalize-first") {
        await finalize();
        await expect(cancel()).rejects.toMatchObject({ code: "conflict" });
        // Finalization replay remains idempotent.
        await finalize();
      } else {
        const results = await Promise.allSettled([cancel(), finalize()]);
        expect(results.filter(result => result.status === "fulfilled")).toHaveLength(1);
        expect(results.find(result => result.status === "rejected").reason).toMatchObject({ code: "conflict" });
      }
      const finalUpload = await uploads.getForUser(user.id, app.id, id);
      const cancelled = finalUpload.status === "cancelled";
      expect(["cancelled", "finalized"]).toContain(finalUpload.status);
      const releaseRows = await pool.query("SELECT id FROM cantelop_control.releases WHERE app_id = $1", [app.id]);
      expect(releaseRows.rowCount).toBe(cancelled ? 0 : 1);
      const operationRows = await pool.query("SELECT id FROM cantelop_control.operations WHERE app_id = $1 AND kind IN ('deploy_api', 'materialize_build')", [app.id]);
      expect(operationRows.rowCount).toBe(cancelled ? 0 : 2);
      expect(await uploads.listPendingForUser(user.id, app.id)).toEqual([]);
      await uploads.begin(nextID, user.id, app.id, input(nextID), `${key}-next`, expiresAt);
      if (cancelled) {
        await cancel();
        await expect(uploads.markReady(user.id, app.id, id, "late-store", 64 << 20, 1)).rejects.toMatchObject({ code: "conflict" });
      } else {
        await expect(cancel()).rejects.toMatchObject({ code: "conflict" });
      }
      expect(await uploads.listPendingForUser(user.id, app.id)).toMatchObject([{ id: nextID, status: "preparing" }]);
      await uploads.cancelForUser(user.id, app.id, nextID);
      expect(await uploads.listPendingForUser(user.id, app.id)).toEqual([]);
    }
  } finally {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.end();
  }
}, 30_000);
