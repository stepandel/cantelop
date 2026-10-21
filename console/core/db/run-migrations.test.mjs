import { expect, test } from "bun:test";

import { readMigrationFiles } from "drizzle-orm/migrator";

import { runMigrations } from "./run-migrations";

const migrationsFolder = new URL("./drizzle", import.meta.url).pathname;
const config = { lockKey: "migrations:cantelop_control", migrationsFolder, migrationsSchema: "cantelop_control" };

test("serializes migration state and work in one transaction-scoped lock", async () => {
  const queries = [];
  const client = {
    async query(sql, values = []) {
      queries.push({ sql, values });
      if (sql.startsWith("SELECT created_at")) return { rows: [{ created_at: "9999999999999" }], rowCount: 1 };
      return { rows: [], rowCount: null };
    },
  };

  await runMigrations(client, config);

  expect(queries.map(({ sql }) => sql)).toEqual([
    "BEGIN",
    "SET LOCAL statement_timeout = '10min'",
    "SET LOCAL lock_timeout = 0",
    "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
    "SET LOCAL lock_timeout = '15s'",
    "SET LOCAL statement_timeout = '5min'",
    'CREATE SCHEMA IF NOT EXISTS "cantelop_control"',
    expect.stringContaining('CREATE TABLE IF NOT EXISTS "cantelop_control"."__drizzle_migrations"'),
    'SELECT created_at FROM "cantelop_control"."__drizzle_migrations" ORDER BY created_at DESC LIMIT 1',
    "COMMIT",
  ]);
  expect(queries[3].values).toEqual(["migrations:cantelop_control"]);
});

test("applies only the migrations newer than the recorded state, then records each", async () => {
  const migrations = readMigrationFiles({ migrationsFolder });
  const previous = migrations.at(-2);
  const pending = migrations.at(-1);
  const queries = [];
  const client = {
    async query(sql, values = []) {
      queries.push({ sql, values });
      if (sql.startsWith("SELECT created_at")) return { rows: [{ created_at: String(previous.folderMillis) }], rowCount: 1 };
      return { rows: [], rowCount: null };
    },
  };

  await runMigrations(client, config);

  const work = queries.slice(queries.findIndex(({ sql }) => sql.startsWith("SELECT created_at")) + 1);
  expect(work).toEqual([
    ...pending.sql.map((sql) => ({ sql, values: [] })),
    {
      sql: 'INSERT INTO "cantelop_control"."__drizzle_migrations" (hash, created_at) VALUES ($1, $2)',
      values: [pending.hash, pending.folderMillis],
    },
    { sql: "COMMIT", values: [] },
  ]);
  expect(work.some(({ values }) => values[0] === previous.hash)).toBe(false);
});

test("rolls back and releases the transaction lock when migration setup fails", async () => {
  const queries = [];
  const client = {
    async query(sql) {
      queries.push(sql);
      if (sql.startsWith("CREATE SCHEMA")) throw new Error("database unavailable");
      return { rows: [], rowCount: null };
    },
  };

  await expect(runMigrations(client, config)).rejects.toThrow("database unavailable");
  expect(queries.at(-1)).toBe("ROLLBACK");
});

test("a failed rollback does not hide the migration error", async () => {
  const client = {
    async query(sql) {
      if (sql.startsWith("CREATE SCHEMA")) throw new Error("database unavailable");
      if (sql === "ROLLBACK") throw new Error("Connection terminated");
      return { rows: [], rowCount: null };
    },
  };

  await expect(runMigrations(client, config)).rejects.toThrow("database unavailable");
});

test("a timed-out lock wait names the overlapping migration", async () => {
  const client = {
    async query(sql) {
      if (sql.startsWith("SELECT pg_advisory_xact_lock")) {
        throw Object.assign(new Error("canceling statement due to statement timeout"), { code: "57014" });
      }
      return { rows: [], rowCount: null };
    },
  };

  await expect(runMigrations(client, config)).rejects.toThrow(
    "another cantelop_control migration is still in progress",
  );
});
