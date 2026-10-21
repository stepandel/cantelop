import { resolve } from "node:path";

import { closeControlDatabase, controlDatabase } from "./database";
import { runMigrations } from "./run-migrations";

// A string key hashed by runMigrations; the former session-level integer
// locks may remain orphaned on idle pooled backends.
const migrationLockKey = "migrations:cantelop_control";
const migrationsFolder = resolve(
  process.env.CANTELOP_MIGRATIONS_PATH?.trim() || "core/db/drizzle",
);

const pool = controlDatabase();
const client = await pool.connect();

try {
  await runMigrations(client, {
    lockKey: migrationLockKey,
    migrationsFolder,
    migrationsTable: "__drizzle_migrations",
    migrationsSchema: "cantelop_control",
  });
  process.stdout.write("Control-plane migrations are current.\n");
} finally {
  client.release();
  await closeControlDatabase();
}
