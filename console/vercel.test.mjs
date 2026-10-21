import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";

import { vercelBuildScripts } from "./scripts/vercel-build";

const packageDocument = JSON.parse(
  await readFile(new URL("./package.json", import.meta.url), "utf8"),
);
const vercelDocument = JSON.parse(
  await readFile(new URL("./vercel.json", import.meta.url), "utf8"),
);

test("Vercel migrates both PostgreSQL databases before building", () => {
  expect(vercelDocument.buildCommand).toBe("bun run vercel-build");
  expect(packageDocument.scripts["vercel-build"]).toBe("bun scripts/vercel-build.ts");
  expect(vercelBuildScripts({
    VERCEL_ENV: "production",
    VERCEL_GIT_COMMIT_SHA: "0123456789abcdef0123456789abcdef01234567",
    CANTELOP_DATABASE_URL: "postgresql://control",
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: "b".repeat(64),
    CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN: "c".repeat(64),
    CANTELOP_TURSO_ORGANIZATION: "cantelop",
    CANTELOP_TURSO_GROUP: "production",
    CANTELOP_TURSO_ENVIRONMENT: "production",
    CANTELOP_TURSO_PLATFORM_TOKEN: "test-platform-token",
    CANTELOP_CONFIG_ENCRYPTION_KEY: Buffer.alloc(32, 1).toString("base64url"),
    CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY: "e".repeat(64),
    CANTELOP_COMPUTE_CONTROL_TOKEN: "d".repeat(64),
  })).toEqual(["db:migrate", "observability:db:migrate", "build"]);
});

test("production cannot deploy without the shared database", () => {
  expect(() => vercelBuildScripts({ VERCEL_ENV: "production" })).toThrow(
    "production requires CANTELOP_DATABASE_URL",
  );
});

test("production cannot deploy without the host ingestion credential", () => {
  expect(() => vercelBuildScripts({
    VERCEL_ENV: "production",
    CANTELOP_DATABASE_URL: "postgresql://control",
  })).toThrow("production requires a valid CANTELOP_OBSERVABILITY_INGEST_TOKEN");
});

test("production requires a distinct edge ingestion credential", () => {
  expect(() => vercelBuildScripts({
    VERCEL_ENV: "production",
    CANTELOP_DATABASE_URL: "postgresql://control",
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
  })).toThrow("CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN");
  expect(() => vercelBuildScripts({
    VERCEL_ENV: "production",
    CANTELOP_DATABASE_URL: "postgresql://control",
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
  })).toThrow("distinct valid");
});

test("production requires a dedicated gateway product-control credential", () => {
  const base = {
    VERCEL_ENV: "production",
    CANTELOP_DATABASE_URL: "postgresql://control",
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: "b".repeat(64),
  };
  expect(() => vercelBuildScripts(base)).toThrow("CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN");
  expect(() => vercelBuildScripts({
    ...base,
    CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN: "b".repeat(64),
  })).toThrow("distinct valid");
  expect(() => vercelBuildScripts({
    ...base,
    CANTELOP_COMPUTE_CONTROL_TOKEN: "c".repeat(64),
    CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN: "c".repeat(64),
    CANTELOP_TURSO_ORGANIZATION: "cantelop",
    CANTELOP_TURSO_GROUP: "production",
    CANTELOP_TURSO_ENVIRONMENT: "production",
    CANTELOP_TURSO_PLATFORM_TOKEN: "test-platform-token",
    CANTELOP_CONFIG_ENCRYPTION_KEY: Buffer.alloc(32, 1).toString("base64url"),
    CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY: "e".repeat(64),
  })).toThrow("distinct valid");
});

test("previews migrate the shared database when configured or skip it", () => {
  expect(vercelBuildScripts({ VERCEL_ENV: "preview" })).toEqual(["build"]);
  expect(vercelBuildScripts({
    VERCEL_ENV: "preview",
    CANTELOP_DATABASE_URL: "postgresql://control-preview",
  })).toEqual(["db:migrate", "observability:db:migrate", "build"]);
  expect(() => vercelBuildScripts({
    VERCEL_ENV: "preview",
    CANTELOP_OBSERVABILITY_DATABASE_URL: "postgresql://observability-preview",
  })).toThrow("cannot be used without CANTELOP_DATABASE_URL");
  expect(vercelBuildScripts({
    VERCEL_ENV: "preview",
    CANTELOP_DATABASE_URL: "postgresql://control-preview",
    CANTELOP_OBSERVABILITY_DATABASE_URL: "postgresql://observability-preview",
  })).toEqual(["db:migrate", "observability:db:migrate", "build"]);
});
