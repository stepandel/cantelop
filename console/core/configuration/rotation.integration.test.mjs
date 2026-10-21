import { expect, test } from "bun:test";
import pg from "pg";
import { controlDatabaseConfig } from "../db/database";
import { ProductRepository } from "../db/product-repository";
import { OperationRepository } from "../db/operation-repository";
import { newResourceID } from "../domain/ids";
import { ConfigCipher } from "./cipher";
import { ConfigRepository } from "./repository";
import { edge } from "../api-deployments/protocol-artifact.test.mjs";

const url = process.env.CANTELOP_TEST_DATABASE_URL;
(url ? test : test.skip)("Edge progress survives retry and compute failure under real PostgreSQL constraints", async () => {
  const pool = new pg.Pool({ ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: url, CANTELOP_DATABASE_SSL: "disable" }) });
  let at = new Date();
  const apiID = newResourceID("artifact"), runtimeID = newResourceID("sessionRuntimeArtifact"), releaseID = newResourceID("release");
  try {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    const products = ProductRepository.fromPool(pool);
    const user = await products.resolveIdentity({ provider: "github", providerSubject: "rotation-pg-test", email: "rotation@example.com" });
    const app = await products.createAppForUser(user.id, { slug: "rotation-pg-test", sessionRuntime: { internalPort: 3000 } });
    at = new Date(Date.now() + 1000);
    const configuration = new ConfigRepository(pool, new ConfigCipher(Buffer.alloc(32, 9)), () => at);
    const operations = new OperationRepository(pool, () => at);
    await configuration.integrationForUser(user.id, app.id);
    const rotated = await configuration.rotateIntegrationForUser(user.id, app.id, 0);
    const prefix = `deployment-uploads/${newResourceID("deploymentUpload").slice(4)}`;
    await pool.query(`INSERT INTO cantelop_control.api_artifacts (id,digest,object_key,size_bytes,manifest,created_at)
      VALUES ($1,$2,$3,29,$4,$5)`, [apiID, `sha256:${"a".repeat(64)}`, `${prefix}/api/worker.mjs`, edge, at]);
    await pool.query(`INSERT INTO cantelop_control.session_runtime_artifacts (id,digest,object_key,size_bytes,media_type,created_at)
      VALUES ($1,$2,$3,4096,'application/vnd.cantelop.session-runtime.image.v1.tar+gzip',$4)`, [runtimeID, `sha256:${"b".repeat(64)}`, `${prefix}/session-runtime/image.tar.gz`, at]);
    await pool.query(`INSERT INTO cantelop_control.releases (id,owner_user_id,app_id,api_artifact_id,session_runtime_artifact_id,version,status,idempotency_key,created_at,updated_at,activated_at)
      VALUES ($1,$2,$3,$4,$5,1,'active','rotation-pg-release',$6,$6,$6)`, [releaseID,user.id,app.id,apiID,runtimeID,at]);
    await pool.query("UPDATE cantelop_control.apps SET active_release_id=$2 WHERE id=$1", [app.id,releaseID]);
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("pending");
    const operation = await operations.claim("rotation-worker", "sync_config");
    expect(operation.input.revision).toBe(rotated.rotation.revision);
    await expect(operations.recordEdgeConfiguration(operation.id, "rotation-worker", operation.input.revision + 1))
      .rejects.toMatchObject({ code: "conflict" });
    await operations.recordEdgeConfiguration(operation.id, "rotation-worker", operation.input.revision);
    const running = await operations.getForUser(user.id, operation.id);
    expect(running.status).toBe("running");
    expect(running.output).toBeUndefined();
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("applied");
    await operations.retry(operation.id, "rotation-worker", new Date(at.getTime() + 1000));
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("applied");
    at = new Date(at.getTime() + 2000);
    await operations.claim("rotation-worker", "sync_config");
    await operations.fail(operation.id, "rotation-worker", "provider_configuration_rejected");
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("applied");
    const retry = await configuration.rotateIntegrationForUser(user.id, app.id, "retry");
    expect(retry.token).toBe(rotated.token);
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("pending");
    const next = await operations.claim("rotation-worker", "sync_config");
    await operations.recordEdgeConfiguration(next.id, "rotation-worker", next.input.revision);
    await operations.succeed(next.id, "rotation-worker", { revision: next.input.revision });
    expect((await configuration.integrationCredentialsForUser(user.id, app.id)).rotation.status).toBe("applied");
  } finally {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.query("DELETE FROM cantelop_control.api_artifacts WHERE id=$1", [apiID]);
    await pool.query("DELETE FROM cantelop_control.session_runtime_artifacts WHERE id=$1", [runtimeID]);
    await pool.end();
  }
});
