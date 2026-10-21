import { beforeAll, expect, test } from "bun:test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import pg from "pg";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import { controlDatabaseConfig } from "./database";
import { ProductRepository } from "./product-repository";
import { newResourceID } from "../domain/ids";

const url = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = url ? test : test.skip;
const config = () => controlDatabaseConfig({ CANTELOP_DATABASE_URL: url, CANTELOP_DATABASE_SSL: process.env.CANTELOP_DATABASE_SSL ?? "disable" });
beforeAll(async () => {
  if (!url) return;
  const pool = new pg.Pool(config());
  try { await migrate(drizzle(pool), { migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)), migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" }); }
  finally { await pool.end(); }
});

postgresTest("real query results preserve actor status, App status, owner email and admin count", async () => {
  const pool = new pg.Pool(config());
  const client = await pool.connect();
  try {
    await client.query("BEGIN");
    const owner = newResourceID("user"), admin = newResourceID("user"), stranger = newResourceID("user");
    const appID = newResourceID("app"), slug = `access-${randomUUID()}`, email = `${owner}@example.com`;
    for (const id of [owner, admin, stranger]) {
      await client.query(`INSERT INTO cantelop_control.users (id,email,status,created_at,last_login_at)
        VALUES ($1,$2,'active',now(),now())`, [id, `${id}@example.com`]);
    }
    await client.query(`INSERT INTO cantelop_control.apps (id,owner_user_id,slug,status,created_at,updated_at)
      VALUES ($1,$2,$3,'provisioning',now(),now())`, [appID, owner, slug]);
    await client.query(`INSERT INTO cantelop_control.app_session_runtimes (app_id,internal_port,created_at,updated_at)
      VALUES ($1,3000,now(),now())`, [appID]);
    await client.query(`INSERT INTO cantelop_control.app_collaborators (app_id,user_id,role,added_by_user_id,created_at)
      VALUES ($1,$2,'admin',$3,now())`, [appID, admin, owner]);
    const products = new ProductRepository(client);
    for (const [actor, role] of [[owner, "owner"], [admin, "admin"]]) {
      const expected = { id: appID, status: "provisioning", accessRole: role, ownerEmail: email, adminCount: 1 };
      expect(await products.getAppForUser(actor, appID)).toMatchObject(expected);
      expect(await products.getAppBySlugForUser(actor, slug)).toMatchObject(expected);
      expect(await products.listAppsForUser(actor)).toContainEqual(expect.objectContaining(expected));
    }
    await expect(products.getAppForUser(stranger, appID)).rejects.toMatchObject({ code: "not_found" });
    for (const actor of [owner, admin]) {
      await client.query("UPDATE cantelop_control.users SET status='suspended' WHERE id=$1", [actor]);
      await expect(products.getAppForUser(actor, appID)).rejects.toMatchObject({ code: "user_suspended" });
      await expect(products.getAppBySlugForUser(actor, slug)).rejects.toMatchObject({ code: "user_suspended" });
      await client.query("UPDATE cantelop_control.users SET status='active' WHERE id=$1", [actor]);
    }
    await client.query("DELETE FROM cantelop_control.app_collaborators WHERE app_id=$1", [appID]);
    await expect(products.getAppForUser(admin, appID)).rejects.toMatchObject({ code: "not_found" });
    expect(await products.getAppForUser(owner, appID)).toMatchObject({ adminCount: 0 });
  } finally {
    await client.query("ROLLBACK");
    client.release();
    await pool.end();
  }
});
