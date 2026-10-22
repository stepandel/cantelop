import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { readMigrationFiles } from "drizzle-orm/migrator";

import { controlDatabaseConfig } from "./database";

test("database configuration requires verified TLS except on loopback", () => {
  expect(controlDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/control?sslmode=require",
  })).toMatchObject({
    connectionString: "postgresql://cantelop:secret@db.example.com/control?sslmode=verify-full",
    ssl: { rejectUnauthorized: true }, max: 10, application_name: "cantelop-control",
  });
  expect(() => controlDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/control",
    CANTELOP_DATABASE_SSL: "disable",
  })).toThrow();
  expect(controlDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@127.0.0.1:5432/control",
    CANTELOP_DATABASE_SSL: "disable",
  })).toMatchObject({
    connectionString: "postgresql://cantelop:secret@127.0.0.1:5432/control?sslmode=disable",
    ssl: false,
  });
});

test("loads the clean-slate schema and compatibility upgrades", () => {
  const migrations = readMigrationFiles({
    migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)),
  });
  expect(migrations).toHaveLength(33);
  for (const migration of migrations) {
    expect(migration.hash).toMatch(/^[0-9a-f]{64}$/);
    expect(migration.sql.length).toBeGreaterThan(0);
  }
});

test("the guarded migration command uses the shared transaction-safe runner", () => {
  const command = readFileSync(
    fileURLToPath(new URL("./migrate-command.ts", import.meta.url)),
    "utf8",
  );
  expect(command).toContain('from "./run-migrations"');
  expect(command).toContain("await runMigrations(client");
  expect(command).toContain('migrationsSchema: "cantelop_control"');
  expect(command).not.toContain("createdAt:");
  expect(command).not.toContain("__drizzle_migrations\" (");
});
