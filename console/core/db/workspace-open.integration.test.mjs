import { expect, test } from "bun:test";
import pg from "pg";

import { controlDatabaseConfig } from "./database";
import { maxWorkspacesPerApp, ProductRepository } from "./product-repository";
import { ControlError } from "../domain/errors";
import { newResourceID } from "../domain/ids";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

async function withApp(run) {
  const hostname = new URL(databaseURL).hostname;
  const pool = new pg.Pool({
    ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL,
      CANTELOP_DATABASE_SSL: process.env.CANTELOP_DATABASE_SSL ??
        (["localhost", "127.0.0.1", "::1"].includes(hostname) ? "disable" : "verify-full") }),
    max: 8,
  });
  const products = ProductRepository.fromPool(pool);
  let user;
  try {
    const suffix = newResourceID("app").slice(4);
    user = await products.resolveIdentity({ provider: "github", providerSubject: `workspace-batch-${suffix}`,
      email: `workspace-batch-${suffix}@example.com` });
    const app = await products.createAppForUser(user.id, { slug: `batch-${suffix}`, sessionRuntime: { internalPort: 3000 } });
    await run(pool, products, app);
  } finally {
    try {
      if (user) {
        await pool.query("DELETE FROM cantelop_control.operations WHERE owner_user_id = $1", [user.id]);
        await pool.query("DELETE FROM cantelop_control.workspaces WHERE app_id IN (SELECT id FROM cantelop_control.apps WHERE owner_user_id = $1)", [user.id]);
        await pool.query("DELETE FROM cantelop_control.apps WHERE owner_user_id = $1", [user.id]);
        await pool.query("DELETE FROM cantelop_control.users WHERE id = $1", [user.id]);
      }
    } finally {
      await pool.end();
    }
  }
}

postgresTest("concurrent workspace opens create exactly one complete aggregate", async () => {
  await withApp(async (pool, products, app) => {
    const opened = await Promise.all(Array.from({ length: 8 }, () =>
      products.openWorkspaceForApp({ appID: app.id, slug: "concurrent" })));
    expect(new Set(opened.map(workspace => workspace.id)).size).toBe(1);
    const { rows } = await pool.query(`SELECT w.id, d.workspace_id, o.idempotency_key, o.input
FROM cantelop_control.workspaces w
JOIN cantelop_control.workspace_databases d ON d.workspace_id = w.id
JOIN cantelop_control.operations o ON o.app_id = w.app_id AND o.kind = 'register_workspace'
WHERE w.app_id = $1`, [app.id]);
    expect(rows).toEqual([expect.objectContaining({ id: opened[0].id, workspace_id: opened[0].id,
      idempotency_key: `register-workspace:${opened[0].id}`,
      input: { workspace_id: opened[0].id, workspace_slug: "concurrent" } })]);
  });
}, 30_000);

postgresTest("failed registration insertion rolls back workspace and database insertion", async () => {
  await withApp(async (pool, products, app) => {
    // Supply a nonexistent owner through a wrapped transaction client. The
    // real FK rejects registration, exercising statement and transaction rollback.
    const invalidOwner = newResourceID("user");
    const wrapped = {
      query: pool.query.bind(pool),
      async connect() {
        const client = await pool.connect();
        return {
          release: () => client.release(),
          async query(sql, values) {
            const result = await client.query(sql, values);
            // Match the owner lookup by result shape, not SQL formatting.
            if (result.rows[0] && "owner_user_id" in result.rows[0]) {
              return { ...result, rows: [{ owner_user_id: invalidOwner }] };
            }
            return result;
          },
        };
      },
    };
    await expect(new ProductRepository(wrapped).openWorkspaceForApp({ appID: app.id, slug: "rollback" }))
      .rejects.toMatchObject({ code: "23503" });
    expect((await pool.query("SELECT id FROM cantelop_control.workspaces WHERE app_id = $1", [app.id])).rows).toEqual([]);
    // A clean retry also proves that the failed write left no unique-key or
    // database record behind.
    const retried = await products.openWorkspaceForApp({ appID: app.id, slug: "rollback" });
    expect((await pool.query("SELECT workspace_id FROM cantelop_control.workspace_databases WHERE workspace_id = $1",
      [retried.id])).rows).toEqual([{ workspace_id: retried.id }]);
  });
}, 30_000);

postgresTest("concurrent workspace creation preserves the quota and existing-workspace replay", async () => {
  await withApp(async (pool, products, app) => {
    for (let i = 0; i < maxWorkspacesPerApp - 1; i++) {
      await pool.query(`INSERT INTO cantelop_control.workspaces (id, app_id, slug, created_at, updated_at)
VALUES ($1, $2, $3, now(), now())`, [newResourceID("workspace"), app.id, `existing-${i}`]);
    }
    const results = await Promise.allSettled(["last-a", "last-b"].map(slug =>
      products.openWorkspaceForApp({ appID: app.id, slug })));
    const accepted = results.filter(result => result.status === "fulfilled");
    expect(accepted).toHaveLength(1);
    expect(results.find(result => result.status === "rejected").reason).toEqual(new ControlError("quota_exceeded"));
    const workspace = accepted[0].value;
    expect(await products.openWorkspaceForApp({ appID: app.id, slug: workspace.slug })).toEqual(workspace);
    expect((await pool.query("SELECT count(*)::int AS count FROM cantelop_control.workspaces WHERE app_id = $1", [app.id]))
      .rows[0].count).toBe(maxWorkspacesPerApp);
    expect((await pool.query("SELECT count(*)::int AS count FROM cantelop_control.operations WHERE app_id = $1 AND kind = 'register_workspace'",
      [app.id])).rows[0].count).toBe(1);
  });
}, 30_000);
