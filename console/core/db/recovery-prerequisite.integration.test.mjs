import { expect, test } from "bun:test";
import pg from "pg";
import { ProductRepository } from "./product-repository";
import { ReleaseRepository } from "./release-repository";
import { OperationRepository } from "./operation-repository";
import { RecoveryPrerequisiteRepository } from "./recovery-prerequisite-repository";
import { DeploymentRecoveryWorker } from "../deployment-recovery/worker";
import { ComputeRegistrationWorker } from "../compute-registrations/worker";
import { ConfigurationProjectionWorker } from "../configuration/worker";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
(databaseURL ? test : test.skip)("recovery repairs missing registration and current configuration before cancellation and restart", async () => {
  const pool = new pg.Pool({ connectionString: databaseURL });
  try {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    const products = ProductRepository.fromPool(pool);
    const user = await products.resolveIdentity({ provider: "github", providerSubject: crypto.randomUUID(), email: "repair@example.com" });
    const app = await products.createAppForUser(user.id, { slug: `repair-${Date.now()}`, sessionRuntime: { internalPort: 57321 } });
    let at = new Date(Date.now() + 3600_000);
    const operations = new OperationRepository(pool, () => at);
    const releases = new ReleaseRepository(pool, () => at);
    const prerequisites = new RecoveryPrerequisiteRepository(pool);
    const oldRegistration = await operations.claim("old", "register_app");
    await operations.fail(oldRegistration.id, "old", "invalid_operation_input");
    await pool.query("UPDATE cantelop_control.apps SET config_revision=3 WHERE id=$1", [app.id]);
    const oldConfig = await operations.enqueue(user.id, app.id, "sync_config", `old-config:${app.id}`, { revision: 3 });
    await operations.claim("old", "sync_config");
    await operations.fail(oldConfig.id, "old", "provider_configuration_timeout");
    const nonce = crypto.randomUUID().replaceAll("-", "");
    const api = { digest: `sha256:${nonce.repeat(2)}`, objectKey: `deployment-uploads/${nonce}/api/worker.mjs`, sizeBytes: 29,
      manifest: edge };
    const runtime = { digest: `sha256:${"b".repeat(64)}`, objectKey: `deployment-uploads/${nonce}/session-runtime/image.tar.gz`,
      sizeBytes: 1024, mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest };
    const release = await releases.begin(user.id, app.id, api, runtime, `initial:${nonce}`);
    await releases.requestRecovery(user.id, app.id, release.id, true);
    let registered = false, configRevision = 0, cancellations = 0, providerAvailable = true;
    const provider = {
      async cancel() {
        if (!registered) throw Error("app_not_found");
        if (!providerAvailable) throw Error("offline");
        expect(configRevision).toBe(4);
        cancellations++;
      },
    };
    const recovery = new DeploymentRecoveryWorker("recovery", operations, releases, provider, prerequisites, () => at);
    const retry = async () => { at = new Date(at.getTime() + 31_000); await recovery.runOnce(); };
    await recovery.runOnce();
    await retry(); // repeated polls must not create duplicate registration work
    expect(cancellations).toBe(0);
    expect((await pool.query("SELECT id FROM cantelop_control.operations WHERE app_id=$1 AND kind='register_app'", [app.id])).rows).toHaveLength(2);
    expect((await releases.getForUser(user.id, app.id, release.id)).status).toBe("cancelling");
    const rejected = await operations.claim("registration", "register_app");
    await operations.fail(rejected.id, "registration", "provider_registration_rejected");
    await retry();
    await retry();
    expect((await pool.query("SELECT progress FROM cantelop_control.operations WHERE app_id=$1 AND kind='cancel_release'", [app.id])).rows[0].progress).toBe("recovery_registration_failed");
    expect((await pool.query("SELECT id FROM cantelop_control.operations WHERE app_id=$1 AND kind='register_app'", [app.id])).rows).toHaveLength(2);
    expect(cancellations).toBe(0);
    // Simulate an operator correcting a permanent provider rejection, without
    // touching the original failed registration or releasing the safety fence.
    await pool.query("UPDATE cantelop_control.operations SET status='queued',error_code=NULL WHERE id=$1", [rejected.id]);
    const registration = new ComputeRegistrationWorker("registration", operations, {
      async registerApp(command) {
        expect(command).toEqual({ ownerUserID: user.id, appID: app.id, appSlug: app.slug, internalPort: 57321 });
        registered = true;
      },
    }, { async publish() {} }, () => at);
    await registration.runOnce();
    await retry();
    expect(cancellations).toBe(0);
    expect((await operations.getForUser(user.id, oldRegistration.id)).errorCode).toBe("invalid_operation_input");
    const configuration = new ConfigurationProjectionWorker("config", operations, {
      async projection() { return { revision: 4, values: [] }; },
      async revision() { return 4; },
    }, { async replaceConfiguration() {} }, { async replaceConfiguration(command) { configRevision = command.revision; } }, releases, () => at);
    // A user edit supersedes the repair's revision. Recovery must follow the
    // current revision instead of getting stuck on the failed old sync.
    await pool.query("UPDATE cantelop_control.apps SET config_revision=4 WHERE id=$1", [app.id]);
    await configuration.runOnce();
    await retry();
    await configuration.runOnce();
    providerAvailable = false;
    await retry();
    expect((await releases.getForUser(user.id, app.id, release.id)).status).toBe("cancelling");
    expect(await releases.listForUser(user.id, app.id)).toHaveLength(1);
    providerAvailable = true;
    await retry();
    const history = await releases.listForUser(user.id, app.id);
    expect(history).toHaveLength(2);
    expect(history.find(item => item.id === release.id).status).toBe("cancelled");
    expect(history.find(item => item.id !== release.id)).toMatchObject({ version: 2, status: "queued", apiArtifactDigest: api.digest });
    expect(cancellations).toBe(1);
    expect((await operations.getForUser(user.id, oldConfig.id)).errorCode).toBe("provider_configuration_timeout");
    expect(await recovery.runOnce()).toBe(false);

    // A registered App can now have an active Build: replaying its original
    // empty-Build registration would be rejected. Cancel-only reuses the
    // successful registration and does not wait on configuration or routing.
    const replacement = history.find(item => item.id !== release.id);
    await releases.requestRecovery(user.id, app.id, replacement.id, false);
    const configCount = (await pool.query("SELECT count(*) FROM cantelop_control.operations WHERE app_id=$1 AND kind='sync_config'", [app.id])).rows[0].count;
    await recovery.runOnce();
    expect((await releases.getForUser(user.id, app.id, replacement.id)).status).toBe("cancelled");
    expect(await releases.listForUser(user.id, app.id)).toHaveLength(2);
    expect((await pool.query("SELECT id FROM cantelop_control.operations WHERE app_id=$1 AND kind='register_app'", [app.id])).rows).toHaveLength(2);
    expect((await pool.query("SELECT count(*) FROM cantelop_control.operations WHERE app_id=$1 AND kind='sync_config'", [app.id])).rows[0].count).toBe(configCount);

    // A stale worker or archived App must never enqueue provider mutations.
    await releases.requestRecovery(user.id, app.id, replacement.id, true);
    at = new Date(at.getTime() + 31_000);
    const recoveryClaim = await operations.claim("recovery", "cancel_release");
    await expect(prerequisites.prepare(recoveryClaim.id, "stale-worker")).rejects.toMatchObject({ code: "conflict" });
    await pool.query("UPDATE cantelop_control.apps SET status='archived',archived_at=now() WHERE id=$1", [app.id]);
    await expect(prerequisites.prepare(recoveryClaim.id, "recovery")).rejects.toMatchObject({ code: "not_found" });
  } finally {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.end();
  }
}, 30_000);
