import { expect, test } from "bun:test";
import { fileURLToPath } from "node:url";

import { readMigrationFiles } from "drizzle-orm/migrator";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import pg from "pg";

import { controlDatabaseConfig } from "./database";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;
const migrationsFolder = fileURLToPath(new URL("./drizzle", import.meta.url));

const hex = (digit) => digit.repeat(32);
const users = {
  google: `usr_${hex("a")}`,
  googleAgain: `usr_${hex("b")}`,
  github: `usr_${hex("c")}`,
  blankOne: `usr_${hex("d")}`,
  blankTwo: `usr_${hex("e")}`,
};
const apps = { google: `app_${hex("1")}`, googleAgain: `app_${hex("2")}`, github: `app_${hex("3")}` };
const operations = { google: `op_${hex("4")}`, googleAgain: `op_${hex("5")}` };

// Runs the linking migration against real rows shaped like the pre-linking
// schema: the one place its temp tables, key rewrites and constraint order
// can actually fail.
postgresTest("identity migration merges Google-verified duplicates and quarantines the rest", async () => {
  const hostname = new URL(databaseURL).hostname;
  const sslMode = process.env.CANTELOP_DATABASE_SSL ??
    (["localhost", "127.0.0.1", "::1"].includes(hostname) ? "disable" : "verify-full");
  const pool = new pg.Pool({
    ...controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode }),
    max: 1,
  });
  const client = await pool.connect();
  const migrations = readMigrationFiles({ migrationsFolder });
  const linking = migrations.findIndex((migration) => migration.sql[0].includes('"user_identities"'));
  expect(linking).toBeGreaterThan(0);
  const restore = async () => {
    await client.query("DROP SCHEMA IF EXISTS cantelop_control CASCADE");
    await migrate(drizzle(client), { migrationsFolder, migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" });
  };
  try {
    await client.query("DROP SCHEMA IF EXISTS cantelop_control CASCADE");
    await client.query("CREATE SCHEMA cantelop_control");
    for (const migration of migrations.slice(0, linking)) {
      for (const statement of migration.sql) await client.query(statement);
    }

    const t0 = "2026-01-01T00:00:00Z";
    const t1 = "2026-02-01T00:00:00Z";
    const seedUser = (id, provider, subject, email, createdAt) => client.query(`INSERT INTO cantelop_control.users
  (id, provider, provider_subject, email, status, created_at, last_login_at)
VALUES ($1, $2, $3, $4, 'active', $5, $5)`, [id, provider, subject, email, createdAt]);
    await seedUser(users.google, "google", "g-1", "alice@example.com", t0);
    await seedUser(users.googleAgain, "google", "g-2", "Alice@Example.com", t1);
    await seedUser(users.github, "github", "583231", "alice@example.com", t1);
    await seedUser(users.blankOne, "github", "1", "", t0);
    await seedUser(users.blankTwo, "github", "2", "", t1);
    // The pre-linking schema predates migration 0020's column defaults.
    const seedQuota = (id, maxApps) => client.query(`INSERT INTO cantelop_control.user_quotas
  (user_id, max_apps, max_vcpus, max_memory_mib) VALUES ($1, $2, 16, 16384)`, [id, maxApps]);
    await seedQuota(users.google, 5);
    await seedQuota(users.googleAgain, 9);
    await seedQuota(users.github, 5);
    const seedApp = (id, owner, slug) => client.query(`INSERT INTO cantelop_control.apps
  (id, owner_user_id, slug, status, created_at, updated_at) VALUES ($1, $2, $3, 'active', $4, $4)`, [id, owner, slug, t1]);
    await seedApp(apps.google, users.google, "first");
    await seedApp(apps.googleAgain, users.googleAgain, "second");
    await seedApp(apps.github, users.github, "third");
    // The older Google account is already an admin of the newer one's App and
    // of the GitHub App; the first grant collapses into ownership.
    await client.query(`INSERT INTO cantelop_control.app_collaborators (app_id, user_id, role, added_by_user_id, created_at)
VALUES ($1, $2, 'admin', $3, $4), ($5, $2, 'admin', $6, $4)`, [apps.googleAgain, users.google, users.googleAgain, t1, apps.github, users.github]);
    const seedOperation = (id, owner, app) => client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'sync_config', 'queued', 'sync-config:shared-key', '{}'::jsonb, $4, $4, $4)`, [id, owner, app, t1]);
    await seedOperation(operations.google, users.google, apps.google);
    await seedOperation(operations.googleAgain, users.googleAgain, apps.googleAgain);

    await client.query("BEGIN");
    for (const statement of migrations[linking].sql) await client.query(statement);
    await client.query("COMMIT");

    const { rows: survivors } = await client.query("SELECT id, email, status FROM cantelop_control.users ORDER BY id");
    expect(survivors).toEqual([
      { id: users.google, email: "alice@example.com", status: "active" },
      { id: users.github, email: "github-583231@unverified.invalid", status: "active" },
      { id: users.blankOne, email: "github-1@unverified.invalid", status: "active" },
      { id: users.blankTwo, email: "github-2@unverified.invalid", status: "active" },
    ]);
    const { rows: identities } = await client.query(
      "SELECT user_id, provider, provider_subject, email FROM cantelop_control.user_identities ORDER BY provider, provider_subject",
    );
    expect(identities).toEqual([
      { user_id: users.blankOne, provider: "github", provider_subject: "1", email: "" },
      { user_id: users.blankTwo, provider: "github", provider_subject: "2", email: "" },
      { user_id: users.github, provider: "github", provider_subject: "583231", email: "alice@example.com" },
      { user_id: users.google, provider: "google", provider_subject: "g-1", email: "alice@example.com" },
      { user_id: users.google, provider: "google", provider_subject: "g-2", email: "alice@example.com" },
    ]);
    const { rows: owners } = await client.query("SELECT id, owner_user_id FROM cantelop_control.apps ORDER BY id");
    expect(owners).toEqual([
      { id: apps.google, owner_user_id: users.google },
      { id: apps.googleAgain, owner_user_id: users.google },
      { id: apps.github, owner_user_id: users.github },
    ]);
    const { rows: collaborators } = await client.query("SELECT app_id, user_id, added_by_user_id FROM cantelop_control.app_collaborators");
    expect(collaborators).toEqual([{ app_id: apps.github, user_id: users.google, added_by_user_id: users.github }]);
    const { rows: quotas } = await client.query("SELECT user_id, max_apps FROM cantelop_control.user_quotas ORDER BY user_id");
    expect(quotas).toEqual([{ user_id: users.google, max_apps: 9 }, { user_id: users.github, max_apps: 5 }]);
    const { rows: keys } = await client.query(
      "SELECT id, owner_user_id, idempotency_key FROM cantelop_control.operations ORDER BY created_at, id",
    );
    expect(keys).toEqual([
      { id: operations.google, owner_user_id: users.google, idempotency_key: "sync-config:shared-key" },
      { id: operations.googleAgain, owner_user_id: users.google, idempotency_key: `sync-config:shared-key:${hex("5")}` },
    ]);
    await expect(client.query(`INSERT INTO cantelop_control.users (id, email, status, created_at, last_login_at)
VALUES ($1, 'alice@example.com', 'active', now(), now())`, [`usr_${hex("f")}`])).rejects.toMatchObject({ code: "23505" });
  } finally {
    try {
      await restore();
    } finally {
      client.release();
      await pool.end();
    }
  }
});
