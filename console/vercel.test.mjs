import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";

import { runVercelBuild, vercelBuildScripts } from "./scripts/vercel-build";

const packageDocument = JSON.parse(
  await readFile(new URL("./package.json", import.meta.url), "utf8"),
);
const vercelDocument = JSON.parse(
  await readFile(new URL("./vercel.json", import.meta.url), "utf8"),
);
const publicPublication = {
  schema_version: 1,
  repository: "https://github.com/stepandel/cantelop",
  commit: "a".repeat(40),
  snapshot_sha256: "b".repeat(64),
};
const privatePublication = {
  ...publicPublication,
  repository: "https://github.com/stepandel/cantelop-internal",
  visibility: "private",
};
const productionEnvironment = {
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
};

test("an unpublished preview builds without publication verification", async () => {
  const scripts = [];
  await runVercelBuild(
    { VERCEL_ENV: "preview" },
    {
      publication: publicPublication,
      verifySource: () => { throw new Error("source changed since publication"); },
      runScript: async (script) => { scripts.push(script); },
    },
  );
  expect(scripts).toEqual(["build"]);
});

test("private production builds accept source changes without refreshing the snapshot", async () => {
  const scripts = [];
  await runVercelBuild(productionEnvironment, {
    publication: privatePublication,
    verifySource: () => { throw new Error("source changed since publication: console/bun.lock"); },
    runScript: async (script) => { scripts.push(script); },
  });
  expect(scripts).toEqual(["db:migrate", "observability:db:migrate", "build"]);
});

test("a public production source mismatch stops the build before any migration", async () => {
  const scripts = [];
  await expect(runVercelBuild(
    productionEnvironment,
    {
      publication: publicPublication,
      verifySource: () => { throw new Error("source changed since publication"); },
      runScript: async (script) => { scripts.push(script); },
    },
  )).rejects.toThrow("source changed since publication");
  expect(scripts).toEqual([]);
});

test("a public production snapshot is verified before migrations", async () => {
  const steps = [];
  await runVercelBuild(productionEnvironment, {
    publication: publicPublication,
    verifySource: () => { steps.push("verify"); },
    runScript: async (script) => { steps.push(script); },
  });
  expect(steps).toEqual(["verify", "db:migrate", "observability:db:migrate", "build"]);
});

test.each([
  [{ VERCEL_GIT_COMMIT_SHA: undefined }, "requires an exact source revision"],
  [{ VERCEL_GIT_COMMIT_SHA: "main" }, "full lowercase Git commit"],
  [{ CANTELOP_SOURCE_COMMIT: "f".repeat(40) }, "does not match the Vercel deployment"],
])("private production rejects an unavailable or incorrect deployment revision: %j", async (override, error) => {
  const scripts = [];
  await expect(runVercelBuild({ ...productionEnvironment, ...override }, {
    publication: privatePublication,
    verifySource: () => { throw new Error("private snapshot verification should be skipped"); },
    runScript: async (script) => { scripts.push(script); },
  })).rejects.toThrow(error);
  expect(scripts).toEqual([]);
});

test("Vercel migrates both PostgreSQL databases before building", () => {
  expect(vercelDocument.buildCommand).toBe("bun run vercel-build");
  expect(packageDocument.scripts["vercel-build"]).toBe("bun scripts/vercel-build.ts");
  expect(vercelBuildScripts(productionEnvironment, privatePublication)).toEqual(["db:migrate", "observability:db:migrate", "build"]);
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
