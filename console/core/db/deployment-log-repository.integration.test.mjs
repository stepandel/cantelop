import { expect, test } from "bun:test";
import pg from "pg";
import { ProductRepository } from "./product-repository";
import { ReleaseRepository } from "./release-repository";
import { OperationRepository } from "./operation-repository";
import { DeploymentLogRepository } from "./deployment-log-repository";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
(databaseURL ? test : test.skip)("deployment logs persist transitions atomically and isolate releases by owner", async () => {
  const pool = new pg.Pool({ connectionString: databaseURL });
  try {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    const products = ProductRepository.fromPool(pool);
    const user = await products.resolveIdentity({ provider: "github", providerSubject: crypto.randomUUID(), email: "logs@example.com" });
    const app = await products.createAppForUser(user.id, { slug: "log-test", sessionRuntime: { internalPort: 3000 } });
    let at = new Date();
    const releases = new ReleaseRepository(pool, () => at);
    const operations = new OperationRepository(pool, () => at);
    const logs = new DeploymentLogRepository(pool);
    const nonce = crypto.randomUUID().replaceAll("-", "");
    const release = await releases.begin(user.id, app.id,
      { digest: `sha256:${nonce.repeat(2)}`, objectKey: `deployment-uploads/${nonce}/api/worker.mjs`, sizeBytes: 29, manifest: edge },
      { digest: `sha256:${"b".repeat(64)}`, objectKey: `deployment-uploads/${nonce}/session-runtime/image.tar.gz`, sizeBytes: 1024, mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest },
      `logs:${crypto.randomUUID()}`);
    const read = () => logs.listForUser(user.id, app.id, release.id);
    expect((await read()).items.map(log => log.message)).toEqual(["Deployment queued", "Queued for deployment", "Queued for deployment"]);
    const op = await operations.claim("log-worker", "deploy_api");
    await releases.startComponent(op.id, "log-worker");
    await operations.recordProgress(op.id, "log-worker", "api_deploying");
    await operations.recordProgress(op.id, "log-worker", "api_deploying");
    await releases.retryComponent(op.id, "log-worker", new Date(at.getTime() + 30_000));
    at = new Date(at.getTime() + 31_000);
    await operations.claim("log-worker", "deploy_api");
    await releases.startComponent(op.id, "log-worker");
    await releases.failComponent(op.id, "log-worker", "provider_deployment_failed");
    const result = await read();
    expect(result.items.filter(log => log.message === "api_deploying")).toHaveLength(1);
    expect(result.items.some(log => log.message === "Started attempt 2")).toBe(true);
    expect(result.items.some(log => log.level === "warning" && log.message.startsWith("Retry scheduled for"))).toBe(true);
    expect(result.items.some(log => log.level === "error" && log.message.includes("provider_deployment_failed"))).toBe(true);
    expect(result.truncated).toBe(false);
    expect(JSON.stringify(result)).not.toContain("object_key");
    expect(result.items.every(log => typeof log.id === "string" && log.created_at.endsWith("Z"))).toBe(true);
    await expect(logs.listForUser("usr_" + "f".repeat(32), app.id, release.id)).rejects.toMatchObject({ code: "not_found" });
    await expect(logs.listForUser(user.id, "app_" + "f".repeat(32), release.id)).rejects.toMatchObject({ code: "not_found" });
    const client = await pool.connect();
    try {
      await client.query("BEGIN");
      await client.query("UPDATE cantelop_control.releases SET status='cancelled', error_code=NULL WHERE id=$1", [release.id]);
      await client.query("ROLLBACK");
    } finally { client.release(); }
    expect((await read()).items).toEqual(result.items);
    await pool.query(`INSERT INTO cantelop_control.deployment_logs(release_id,source,level,message)
SELECT $1,'api','info','Test line ' || n FROM generate_series(1,2001) n`, [release.id]);
    const tail = await read();
    expect(tail.truncated).toBe(true);
    expect(tail.items).toHaveLength(2000);
    expect(tail.items[0].message).toBe("Test line 2");
    expect(tail.items.at(-1).message).toBe("Test line 2001");
  } finally {
    await pool.query("TRUNCATE cantelop_control.users CASCADE");
    await pool.end();
  }
}, 30_000);
