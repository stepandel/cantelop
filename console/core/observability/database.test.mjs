import { expect, test } from "bun:test";

import { observabilityDatabaseConfig, observabilityRetentionHours } from "./database";

test("observability storage reuses the control database by default", () => {
  expect(observabilityDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/control",
  })).toMatchObject({
    application_name: "cantelop-observability",
    connectionString: "postgresql://cantelop:secret@db.example.com/control?sslmode=verify-full",
    ssl: { rejectUnauthorized: true },
  });
});

test("observability storage supports a dedicated database override", () => {
  expect(observabilityDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/control",
    CANTELOP_OBSERVABILITY_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/observability",
  })).toMatchObject({
    application_name: "cantelop-observability",
    connectionString: "postgresql://cantelop:secret@db.example.com/observability?sslmode=verify-full",
    ssl: { rejectUnauthorized: true },
  });
});

test("unencrypted observability storage is allowed only on loopback", () => {
  expect(() => observabilityDatabaseConfig({
    CANTELOP_OBSERVABILITY_DATABASE_URL: "postgresql://cantelop:secret@db.example.com/observability",
    CANTELOP_OBSERVABILITY_DATABASE_SSL: "disable",
  })).toThrow();
  expect(observabilityDatabaseConfig({
    CANTELOP_DATABASE_URL: "postgresql://cantelop:secret@127.0.0.1/observability",
    CANTELOP_DATABASE_SSL: "disable",
  })).toMatchObject({ ssl: false });
});

test("observability retention defaults to 72 hours and remains bounded", () => {
  expect(observabilityRetentionHours({})).toBe(72);
  expect(observabilityRetentionHours({ CANTELOP_OBSERVABILITY_RETENTION_HOURS: "24" })).toBe(24);
  expect(observabilityRetentionHours({ CANTELOP_OBSERVABILITY_RETENTION_HOURS: "720" })).toBe(720);
  expect(() => observabilityRetentionHours({ CANTELOP_OBSERVABILITY_RETENTION_HOURS: "23" })).toThrow();
  expect(() => observabilityRetentionHours({ CANTELOP_OBSERVABILITY_RETENTION_HOURS: "72.5" })).toThrow();
});
