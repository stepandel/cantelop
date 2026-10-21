import { beforeAll, expect, test } from "bun:test";
import { randomBytes, randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import pg from "pg";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import { controlDatabaseConfig } from "./database";
import { ProductRepository } from "./product-repository";
import { OperationRepository } from "./operation-repository";
import { ReleaseRepository } from "./release-repository";
import { DeploymentUploadRepository } from "./deployment-upload-repository";
import { computeTenantResolver } from "./compute-tenant";
import { newResourceID } from "../domain/ids";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const url = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = url ? test : test.skip;
const config = () => controlDatabaseConfig({ CANTELOP_DATABASE_URL: url, CANTELOP_DATABASE_SSL: process.env.CANTELOP_DATABASE_SSL ?? "disable" });
beforeAll(async () => {
  if (!url) return;
  const pool = new pg.Pool(config());
  try { await migrate(drizzle(pool), { migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)), migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" }); }
  finally { await pool.end(); }
});

async function fixture(run) {
  const pool = new pg.Pool(config());
  const ids = [];
  const products = new ProductRepository(pool);
  const seed = async (provider, email, status = "active") => {
    const id = newResourceID("user"), subject = randomUUID();
    ids.push(id);
    const storedEmail = email ?? `${provider}-${subject}@unverified.invalid`;
    await pool.query(`INSERT INTO cantelop_control.users (id,email,status,created_at,last_login_at)
VALUES ($1,$2,$3,now(),now())`, [id, storedEmail, status]);
    await pool.query(`INSERT INTO cantelop_control.user_identities (user_id,provider,provider_subject,email,created_at,last_login_at)
VALUES ($1,$2,$3,$4,now(),now())`, [id, provider, subject, storedEmail]);
    await pool.query(`INSERT INTO cantelop_control.user_quotas (user_id,max_apps) VALUES ($1,5)`, [id]);
    return { id, provider, subject, email: storedEmail };
  };
  const app = (owner) => products.createAppForUser(owner.id, { slug: `test-${randomUUID()}`, sessionRuntime: { internalPort: 3000 } });
  const login = (identity, email) => products.resolveIdentity({ provider: identity.provider, providerSubject: identity.subject, email });
  try { await run({ pool, products, seed, app, login }); }
  finally {
    await pool.query("DELETE FROM cantelop_control.deployment_uploads WHERE owner_user_id = ANY($1)", [ids]);
    await pool.query("UPDATE cantelop_control.apps SET active_release_id=NULL WHERE owner_user_id = ANY($1)", [ids]);
    await pool.query("DELETE FROM cantelop_control.release_components WHERE release_id IN (SELECT id FROM cantelop_control.releases WHERE owner_user_id = ANY($1))", [ids]);
    await pool.query("DELETE FROM cantelop_control.releases WHERE owner_user_id = ANY($1)", [ids]);
    await pool.query("DELETE FROM cantelop_control.operations WHERE owner_user_id = ANY($1)", [ids]);
    await pool.query("DELETE FROM cantelop_control.workspaces WHERE app_id IN (SELECT id FROM cantelop_control.apps WHERE owner_user_id = ANY($1))", [ids]);
    await pool.query("DELETE FROM cantelop_control.app_session_runtimes WHERE app_id IN (SELECT id FROM cantelop_control.apps WHERE owner_user_id = ANY($1))", [ids]);
    await pool.query("DELETE FROM cantelop_control.apps WHERE owner_user_id = ANY($1)", [ids]);
    await pool.query("DELETE FROM cantelop_control.users WHERE id = ANY($1)", [ids]);
    await pool.end();
  }
}

postgresTest("verified legacy GitHub login consolidates existing Google account and preserves both accounts' resources", async () => fixture(async ({ pool, products, seed, app, login }) => {
  const email = `joined-${randomUUID()}@example.com`;
  const source = await seed("github"), target = await seed("google", email), outsider = await seed("google", `other-${randomUUID()}@example.com`);
  const sourceApp = await app(source), targetApp = await app(target), sharedApp = await app(outsider);
  const workspace = await products.createWorkspaceForUser(source.id, { appID: sourceApp.id, slug: "production" });
  for (const [owner, shared, collaborator] of [
    [outsider, sharedApp, source], [outsider, sharedApp, target],
    [source, sourceApp, target], [source, sourceApp, outsider],
  ]) {
    await pool.query(`INSERT INTO cantelop_control.app_collaborators
      (app_id, user_id, role, added_by_user_id, created_at) VALUES ($1, $2, 'admin', $3, now())`,
    [shared.id, collaborator.id, owner.id]);
  }
  await pool.query("UPDATE cantelop_control.user_quotas SET max_apps=9 WHERE user_id=$1", [source.id]);
  const operations = new OperationRepository(pool);
  await operations.enqueue(source.id, sourceApp.id, "sync_config", "shared-request-key", { revision: 1 });
  await operations.enqueue(target.id, targetApp.id, "sync_config", "shared-request-key", { revision: 1 });

  for (const [owner, ownedApp] of [[source, sourceApp], [target, targetApp]]) {
    const uploadID = newResourceID("deploymentUpload");
    const prefix = `deployment-uploads/${uploadID.slice(4)}`;
    const api = { digest: `sha256:${randomBytes(32).toString("hex")}`, objectKey: `${prefix}/api/worker.mjs`, sizeBytes: 29, manifest: edge };
    const sessionRuntime = { digest: `sha256:${randomBytes(32).toString("hex")}`, objectKey: `${prefix}/session-runtime/image.tar.gz`, sizeBytes: 4096, mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip", manifest: runtimeManifest };
    await new DeploymentUploadRepository(pool).begin(uploadID, owner.id, ownedApp.id, { api, sessionRuntime }, "shared-upload-key", new Date(Date.now() + 600_000));
    await new ReleaseRepository(pool).begin(owner.id, ownedApp.id, api, sessionRuntime, "shared-release-key");
  }
  const result = await login(source, email.toUpperCase());
  expect(result).toMatchObject({ id: target.id, email });
  expect((await pool.query("SELECT id FROM cantelop_control.users WHERE id=$1", [source.id])).rows).toEqual([]);
  expect((await pool.query("SELECT user_id FROM cantelop_control.user_identities WHERE provider_subject=$1", [source.subject])).rows[0].user_id).toBe(target.id);
  expect((await products.listAppsForUser(target.id)).map((item) => item.id).sort()).toEqual([sourceApp.id, targetApp.id, sharedApp.id].sort());
  expect((await products.listWorkspacesForUser(target.id)).map((item) => item.id)).toContain(workspace.id);
  expect(await computeTenantResolver(pool)(sourceApp.id, target.id)).toBe(source.id);
  expect(await computeTenantResolver(pool)(targetApp.id, target.id)).toBe(target.id);
  expect((await pool.query("SELECT max_apps FROM cantelop_control.user_quotas WHERE user_id=$1", [target.id])).rows[0].max_apps).toBe(9);
  expect((await pool.query("SELECT * FROM cantelop_control.app_collaborators WHERE app_id=$1 AND user_id=$2", [sharedApp.id, target.id])).rowCount).toBe(1);
  expect((await pool.query("SELECT * FROM cantelop_control.app_collaborators WHERE app_id=$1 AND user_id=$2", [sourceApp.id, target.id])).rowCount).toBe(0);
  expect((await pool.query("SELECT added_by_user_id FROM cantelop_control.app_collaborators WHERE app_id=$1 AND user_id=$2", [sourceApp.id, outsider.id])).rows[0].added_by_user_id).toBe(target.id);
  for (const table of ["operations", "releases", "deployment_uploads"]) {
    expect((await pool.query(`SELECT 1 FROM cantelop_control.${table} WHERE owner_user_id=$1`, [source.id])).rowCount).toBe(0);
    expect((await pool.query(`SELECT 1 FROM cantelop_control.${table} WHERE owner_user_id=$1 AND idempotency_key LIKE 'reconciled:%'`, [target.id])).rowCount).toBeGreaterThan(0);
  }
  const concurrent = await Promise.all([login(source, email), login(target, email), login(source, email)]);
  expect(concurrent.map((user) => user.id)).toEqual([target.id, target.id, target.id]);
  expect(await products.getActiveUser(source.id)).toMatchObject({ id: target.id, status: "active" });
}), 30_000);

postgresTest("legacy accounts restore an unused verified email without changing their user ID", async () => fixture(async ({ seed, login }) => {
  const source = await seed("github");
  const email = `unique-${randomUUID()}@example.com`;
  expect(await login(source, email)).toMatchObject({ id: source.id, email });
}));

postgresTest("established accounts stay pinned when a provider changes to another account's address", async () => fixture(async ({ seed, login }) => {
  const source = await seed("github", `established-${randomUUID()}@example.com`);
  const target = await seed("google", `existing-${randomUUID()}@example.com`);
  expect(await login(source, target.email)).toMatchObject({ id: source.id, email: source.email });
}));

postgresTest("suspended source or target cannot use reconciliation to recover access", async () => fixture(async ({ seed, login, pool }) => {
  const source = await seed("github"), target = await seed("google", `blocked-${randomUUID()}@example.com`, "suspended");
  // An active legacy account still signs in; it simply stays separate from the
  // suspended one and keeps its placeholder address.
  expect(await login(source, target.email)).toMatchObject({ id: source.id, email: source.email });
  expect((await pool.query("SELECT status FROM cantelop_control.users WHERE id=$1", [target.id])).rows[0].status).toBe("suspended");
  await pool.query("UPDATE cantelop_control.users SET status='active' WHERE id=$1", [target.id]);
  await pool.query("UPDATE cantelop_control.users SET status='suspended' WHERE id=$1", [source.id]);
  await expect(login(source, target.email)).rejects.toMatchObject({ code: "user_suspended" });
  expect((await pool.query("SELECT email FROM cantelop_control.users WHERE id=$1", [source.id])).rows[0].email).toBe(source.email);
}));

postgresTest("in-flight operations defer reconciliation atomically until the next sign-in", async () => fixture(async ({ seed, app, login, pool }) => {
  const source = await seed("github"), target = await seed("google", `busy-${randomUUID()}@example.com`);
  const ownedApp = await app(source);
  await pool.query("UPDATE cantelop_control.operations SET status='running',locked_by='test-worker',locked_at=now() WHERE app_id=$1", [ownedApp.id]);
  // The sign-in still succeeds on the legacy account; only the merge waits.
  expect(await login(source, target.email)).toMatchObject({ id: source.id, email: source.email });
  expect((await pool.query("SELECT owner_user_id,compute_tenant_id FROM cantelop_control.apps WHERE id=$1", [ownedApp.id])).rows[0]).toEqual({ owner_user_id: source.id, compute_tenant_id: null });
  await pool.query("UPDATE cantelop_control.operations SET status='succeeded',output='{}',locked_by=NULL,locked_at=NULL WHERE app_id=$1", [ownedApp.id]);
  expect(await login(source, target.email)).toMatchObject({ id: target.id, email: target.email });
}));

postgresTest("concurrent legacy identities converge on one verified-email account", async () => fixture(async ({ seed, login }) => {
  const one = await seed("github"), two = await seed("github");
  const target = await seed("google", `concurrent-${randomUUID()}@example.com`);
  const results = await Promise.all([login(one, target.email), login(two, target.email), login(target, target.email)]);
  expect(results.map((user) => user.id)).toEqual([target.id, target.id, target.id]);
}));

postgresTest("a failed reconciliation rolls back ownership, memberships, and identity changes", async () => fixture(async ({ pool, seed, app }) => {
  const source = await seed("github"), target = await seed("google", `rollback-${randomUUID()}@example.com`);
  const ownedApp = await app(source);
  const failingPool = {
    async connect() {
      const client = await pool.connect();
      return {
        query(sql, values) {
          if (sql === "DELETE FROM cantelop_control.users WHERE id = $1") throw new Error("injected merge failure");
          return client.query(sql, values);
        },
        release() { client.release(); },
      };
    },
  };
  await expect(new ProductRepository(failingPool).resolveIdentity({ provider: source.provider, providerSubject: source.subject, email: target.email })).rejects.toThrow("injected merge failure");
  expect((await pool.query("SELECT * FROM cantelop_control.user_aliases WHERE source_user_id=$1", [source.id])).rows).toEqual([]);
  expect((await pool.query("SELECT owner_user_id,compute_tenant_id FROM cantelop_control.apps WHERE id=$1", [ownedApp.id])).rows[0]).toEqual({ owner_user_id: source.id, compute_tenant_id: null });
  expect((await pool.query("SELECT user_id FROM cantelop_control.user_identities WHERE provider_subject=$1", [source.subject])).rows[0].user_id).toBe(source.id);
}));

postgresTest("reconciliation preserves invitation references when the invitations feature is installed", async () => fixture(async ({ pool, seed, app, login }) => {
  const source = await seed("github"), target = await seed("google", `invites-${randomUUID()}@example.com`);
  const ownedApp = await app(source);
  const installed = (await pool.query("SELECT to_regclass('cantelop_control.app_invitations') IS NOT NULL AS present")).rows[0].present;
  // Compatibility fixture for the independently deployed invitation table.
  // When testing both branches together, use its real migrated schema instead.
  if (!installed) await pool.query(`CREATE TABLE cantelop_control.app_invitations (
    id text PRIMARY KEY, app_id text REFERENCES cantelop_control.apps(id) ON DELETE CASCADE,
    email text, invited_by_user_id text REFERENCES cantelop_control.users(id), token_hash text,
    state text, delivery text, created_at timestamptz, expires_at timestamptz,
    accepted_by_user_id text REFERENCES cantelop_control.users(id))`);
  try {
    const id = randomUUID();
    await pool.query(`INSERT INTO cantelop_control.app_invitations
(id,app_id,email,invited_by_user_id,token_hash,state,delivery,created_at,expires_at,accepted_by_user_id)
VALUES ($1,$2,$3,$4,$5,'accepted','sent',now(),now()+interval '7 days',$4)`, [id, ownedApp.id, target.email, source.id, randomBytes(32).toString("hex")]);
    await login(source, target.email);
    expect((await pool.query("SELECT invited_by_user_id,accepted_by_user_id FROM cantelop_control.app_invitations WHERE id=$1", [id])).rows[0]).toEqual({ invited_by_user_id: target.id, accepted_by_user_id: target.id });
  } finally {
    if (!installed) await pool.query("DROP TABLE cantelop_control.app_invitations");
  }
}));

postgresTest("old authenticated subjects resolve to the canonical account with current permissions", async () => fixture(async ({ pool, products, seed, app, login }) => {
  const source = await seed("github"), target = await seed("google", `alias-${randomUUID()}@example.com`);
  const owner = await seed("google", `owner-${randomUUID()}@example.com`);
  const owned = await app(source), shared = await app(owner);
  const olderID = newResourceID("user");
  await pool.query("INSERT INTO cantelop_control.user_aliases (source_user_id,user_id,created_at) VALUES ($1,$2,now())", [olderID,source.id]);
  await pool.query(`INSERT INTO cantelop_control.app_collaborators (app_id,user_id,role,added_by_user_id,created_at)
VALUES ($1,$2,'admin',$3,now())`, [shared.id,source.id,owner.id]);
  await login(source,target.email);
  for (const subject of [source.id, olderID, target.id]) {
    const canonical = await products.getActiveUser(subject);
    expect(canonical.id).toBe(target.id);
    expect((await products.getAppForUser(canonical.id,owned.id)).accessRole).toBe("owner");
    expect((await products.getAppForUser(canonical.id,shared.id)).accessRole).toBe("admin");
    expect(await products.resolveAppOwnerForUser(canonical.id,shared.id)).toBe(owner.id);
  }
  await products.removeAppAdminForOwner(owner.id,shared.id,target.id);
  const canonical = await products.getActiveUser(source.id);
  await expect(products.getAppForUser(canonical.id,shared.id)).rejects.toMatchObject({ code: "not_found" });
  await pool.query("UPDATE cantelop_control.users SET status='suspended' WHERE id=$1",[target.id]);
  for (const subject of [source.id,olderID,target.id]) {
    await expect(products.getActiveUser(subject)).rejects.toMatchObject({ code: "user_suspended" });
  }
  await expect(products.getActiveUser(newResourceID("user"))).rejects.toMatchObject({ code: "not_found" });
}));
