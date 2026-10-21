import { test, expect } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createClient } from "@libsql/client";
import { sqliteTable, text, integer } from "drizzle-orm/sqlite-core";
import { createApplicationSchema, synchronizeApplicationSchema } from "./schema";

async function database(run) {
  const directory = await mkdtemp(join(tmpdir(), "cantelop-schema-"));
  const db = createClient({ url: `file:${join(directory, "test.sqlite")}` });
  try { await run(db); } finally { db.close(); await rm(directory, { recursive: true, force: true }); }
}
test("managed changes preserve unrelated application tables without installing mailbox tables", () => database(async db => {
  await db.execute("CREATE TABLE application_queue(id TEXT)");
  await db.execute("INSERT INTO application_queue VALUES('pending')");
  const initial = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey() }) });
  const next = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey(), done: integer().default(0) }) });
  await synchronizeApplicationSchema(db, initial);
  await db.execute("INSERT INTO tasks VALUES('one')");
  await synchronizeApplicationSchema(db, next);
  expect((await db.execute("SELECT done FROM tasks")).rows[0].done).toBe(0);
  expect((await db.execute("SELECT id FROM application_queue")).rows[0].id).toBe("pending");
  expect((await db.execute("SELECT name FROM sqlite_schema WHERE name LIKE 'cantelop_mailbox_%'")).rows).toHaveLength(0);
  expect((await db.execute("SELECT name FROM sqlite_schema WHERE name='cantelop_system_migrations'")).rows).toHaveLength(0);
}));
test("initial collision fails without adopting or changing an unmanaged table", () => database(async db => {
  await db.execute("CREATE TABLE tasks(id TEXT PRIMARY KEY)");
  await db.execute("INSERT INTO tasks VALUES('existing')");
  const schema = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey() }) });
  await expect(synchronizeApplicationSchema(db, schema)).rejects.toMatchObject({ code: "unmanaged_table_conflict" });
  expect((await db.execute("SELECT id FROM tasks")).rows[0].id).toBe("existing");
  expect((await db.execute("SELECT * FROM cantelop_application_migrations")).rows).toHaveLength(0);
}));
test("an unapplied historical release never downgrades a newer schema", () => database(async db => {
  const initial = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey() }) });
  const next = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey(), done: integer().default(0) }) });
  await synchronizeApplicationSchema(db, next);
  expect(await synchronizeApplicationSchema(db, initial, new Set([next.digest]))).toBeUndefined();
  expect((await db.execute("PRAGMA table_info(tasks)")).rows.some(row => row.name === "done")).toBe(true);
}));
