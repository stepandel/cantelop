import { expect, mock, test } from "bun:test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import pg from "pg";

import { controlDatabaseConfig } from "../../core/db/database";
mock.module("server-only", () => ({}));
const { consumeDemoRequestBudget } = await import("./demo-budget");

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

postgresTest("visitor demo budgets serialize concurrent actions without an account", async () => {
  const pool = new pg.Pool(controlDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL,
    CANTELOP_DATABASE_SSL: process.env.CANTELOP_DATABASE_SSL ?? "disable" }));
  const first = "visitor:" + randomUUID().replaceAll("-", "");
  const second = "visitor:" + randomUUID().replaceAll("-", "");
  try {
    await migrate(drizzle(pool), { migrationsFolder: fileURLToPath(new URL("../../core/db/drizzle", import.meta.url)),
      migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" });
    const results = await Promise.all(Array.from({length: 12}, () => consumeDemoRequestBudget(first, pool)));
    expect(results.filter(Boolean)).toHaveLength(10);
    expect(await consumeDemoRequestBudget(second, pool)).toBe(true);
    await pool.query("UPDATE cantelop_control.demo_visitor_request_budgets SET minute_started_at = now() - interval '2 minutes', day_count = 50 WHERE visitor_id = $1", [first]);
    expect(await consumeDemoRequestBudget(first, pool)).toBe(false);
    await pool.query("UPDATE cantelop_control.demo_visitor_request_budgets SET day_started_at = now() - interval '2 days' WHERE visitor_id = $1", [first]);
    expect(await consumeDemoRequestBudget(first, pool)).toBe(true);
  } finally {
    await pool.query("DELETE FROM cantelop_control.demo_visitor_request_budgets WHERE visitor_id IN ($1, $2)", [first, second]);
    await pool.end();
  }
});
