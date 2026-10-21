import { sqliteTable, text } from "drizzle-orm/sqlite-core";
import { createApplicationSchema, applicationMigrationSQL } from "../core/workspace-databases/schema";
import { configuredAPIArtifactStore } from "../core/api-deployments/s3-artifact-store";
import { configuredDeploymentArtifactStore } from "../core/deployment-uploads/s3-artifact-store";
import { configuredPreparedArtifactStore } from "../core/session-runtime-materializations/prepared-artifact-store";

const initial = await createApplicationSchema({ probe: sqliteTable("release_probe", { id: text().primaryKey() }) });
const next = await createApplicationSchema({ probe: sqliteTable("release_probe", { id: text().primaryKey(), value: text() }) });
if (!(await applicationMigrationSQL(undefined, initial)).some(sql => sql.includes("CREATE TABLE")) ||
    !(await applicationMigrationSQL(initial, next)).some(sql => sql.includes("ADD"))) throw new Error("schema DDL generation failed");
const environment = {
  CANTELOP_ARTIFACT_BUCKET: "release-qualification", CANTELOP_ARTIFACT_ENDPOINT: "https://localhost",
  CANTELOP_ARTIFACT_REGION: "auto", CANTELOP_ARTIFACT_ACCESS_KEY_ID: "qualification-access",
  CANTELOP_ARTIFACT_SECRET_ACCESS_KEY: "qualification-secret",
};
configuredAPIArtifactStore(environment);
configuredPreparedArtifactStore(environment);
const target = await configuredDeploymentArtifactStore(environment).signAPIUpload(
  "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs", 1, `sha256:${"a".repeat(64)}`, 60,
);
if (new URL(target.url).hostname !== "localhost") throw new Error("artifact presigning failed");
console.log("Compiled Linux worker schema generation and artifact clients passed");
