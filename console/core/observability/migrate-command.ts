import { resolve } from "node:path";

import { runMigrations } from "../db/run-migrations";
import { closeObservabilityDatabase, observabilityDatabase } from "./database";

// A string key hashed by runMigrations; the former session-level integer
// locks may remain orphaned on idle pooled backends.
const migrationLockKey = "migrations:cantelop_observability";
const migrationsFolder = resolve(
  process.env.CANTELOP_OBSERVABILITY_MIGRATIONS_PATH?.trim() || "core/observability/drizzle",
);

const pool = observabilityDatabase();
const client = await pool.connect();

try {
  await runMigrations(client, {
    lockKey: migrationLockKey,
    migrationsFolder,
    migrationsTable: "__drizzle_migrations",
    migrationsSchema: "cantelop_observability",
  });
  process.stdout.write("Observability migrations are current.\n");
} finally {
  client.release();
  await closeObservabilityDatabase();
}
