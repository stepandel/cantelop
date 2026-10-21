import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { readMigrationFiles } from "drizzle-orm/migrator";

const migrationURL = new URL("./drizzle/0000_dashing_blonde_phantom.sql", import.meta.url);
const migration = (await readFile(migrationURL, "utf8")).replaceAll('"', "");

test("observability data is isolated from Fire Fuse and the control schema", () => {
  expect(migration).toContain("CREATE SCHEMA IF NOT EXISTS cantelop_observability");
  for (const table of ["requests", "sessions", "sandboxes", "messages", "message_attempts", "spans", "logs"]) {
    expect(migration).toContain(`CREATE TABLE cantelop_observability.${table}`);
  }
  expect(migration).not.toContain("cantelop_control");
  expect(migration).not.toContain("request_events.sqlite");
});

test("the execution hierarchy uses typed parent pointers", () => {
  expect(migration).toContain("session_id uuid NOT NULL");
  expect(migration).toContain("message_id text NOT NULL");
  expect(migration).toContain("sandbox_id text");
  expect(migration).toContain("attempt_id text NOT NULL");
  expect(migration).toContain("parent_span_id text");
  expect(migration).not.toContain("relationships");
  expect(migration).not.toContain("edges");
});

test("message attempts preserve retries independently of Messages", () => {
  expect(migration).toContain("message_attempts_message_attempt_unique");
  expect(migration).toContain("attempt integer NOT NULL");
  expect(migration).toContain("message_attempts_sandbox_id_sandboxes_id_fk");
});

test("the initial migration includes runtime logs and retention", () => {
  expect(migration).toContain("CREATE TABLE cantelop_observability.retention_state");
  expect(migration).toContain("app_id text NOT NULL");
  expect(migration).toContain("logs_scope_check");
  expect(migration).toContain("logs_sandbox_occurred");
  expect(migration).toContain("ON DELETE cascade");
  expect(migration).toContain("REVOKE ALL ON ALL TABLES IN SCHEMA cantelop_observability FROM PUBLIC");
});

test("loads the standalone observability migration", () => {
  const migrations = readMigrationFiles({
    migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)),
  });
  expect(migrations).toHaveLength(3);
  expect(migrations.every(({ hash }) => /^[0-9a-f]{64}$/.test(hash))).toBe(true);
});

test("the guarded migration command uses the shared transaction-safe runner", async () => {
  const command = await readFile(new URL("./migrate-command.ts", import.meta.url), "utf8");
  expect(command).toContain('from "../db/run-migrations"');
  expect(command).toContain("await runMigrations(client");
  expect(command).toContain('migrationsSchema: "cantelop_observability"');
  expect(command).not.toContain("createdAt:");
  expect(command).not.toContain('__drizzle_migrations" (');
});
