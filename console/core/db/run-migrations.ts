import { readMigrationFiles } from "drizzle-orm/migrator";

import type { SQLClient } from "./database";

interface MigrationRunConfig {
  lockKey: string;
  migrationsFolder: string;
  migrationsSchema: string;
  migrationsTable?: string;
}

const queryCanceledCode = "57014";

function identifier(value: string): string {
  if (!/^[a-z_][a-z0-9_]*$/.test(value)) throw new TypeError("invalid migration identifier");
  return `"${value}"`;
}

/** Run every pending Drizzle migration under a transaction-scoped lock.
 *
 * Transaction locks work through PostgreSQL connection poolers and disappear
 * automatically if a build exits. The lock is acquired before migration state
 * is read, so a waiter observes the migration committed by the preceding build.
 */
export async function runMigrations(client: SQLClient, config: MigrationRunConfig): Promise<void> {
  const migrations = readMigrationFiles({ migrationsFolder: config.migrationsFolder });
  const schema = identifier(config.migrationsSchema);
  const table = identifier(config.migrationsTable ?? "__drizzle_migrations");
  let transactionOpen = false;

  try {
    await client.query("BEGIN");
    transactionOpen = true;
    // Pin both timeouts: a pooled backend may carry session settings left by
    // an earlier build, and lock_timeout also bounds advisory lock waits.
    await client.query("SET LOCAL statement_timeout = '10min'");
    await client.query("SET LOCAL lock_timeout = 0");
    try {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [config.lockKey]);
    } catch (error) {
      if ((error as { code?: unknown }).code !== queryCanceledCode) throw error;
      throw new Error(`another ${config.migrationsSchema} migration is still in progress`, { cause: error });
    }
    await client.query("SET LOCAL lock_timeout = '15s'");
    await client.query("SET LOCAL statement_timeout = '5min'");
    await client.query(`CREATE SCHEMA IF NOT EXISTS ${schema}`);
    await client.query(`CREATE TABLE IF NOT EXISTS ${schema}.${table} (
  id serial PRIMARY KEY,
  hash text NOT NULL,
  created_at bigint
)`);
    const applied = await client.query<{ created_at: string }>(
      `SELECT created_at FROM ${schema}.${table} ORDER BY created_at DESC LIMIT 1`,
    );
    const lastAppliedAt = Number(applied.rows[0]?.created_at ?? 0);

    for (const migration of migrations) {
      if (lastAppliedAt >= migration.folderMillis) continue;
      for (const statement of migration.sql) await client.query(statement);
      await client.query(
        `INSERT INTO ${schema}.${table} (hash, created_at) VALUES ($1, $2)`,
        [migration.hash, migration.folderMillis],
      );
    }
    await client.query("COMMIT");
    transactionOpen = false;
  } catch (error) {
    if (transactionOpen) {
      // A failed rollback (for example a dropped connection) must not hide
      // the error that actually broke the migration.
      try {
        await client.query("ROLLBACK");
      } catch {}
    }
    throw error;
  }
}
