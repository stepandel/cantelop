import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";

const schemaURL = new URL("./drizzle/0000_complete_swarm.sql", import.meta.url);
const schema = (await readFile(schemaURL, "utf8")).replaceAll('"', "");
const upgradeURL = new URL("./drizzle/0001_preserve_workspaces.sql", import.meta.url);
const removeCapacityURL = new URL("./drizzle/0003_remove_session_capacity.sql", import.meta.url);
const removeCapacity = await readFile(removeCapacityURL, "utf8");
const cutoverURL = new URL("./drizzle/0004_session_runtime_cutover.sql", import.meta.url);
const cutover = (await readFile(cutoverURL, "utf8")).replaceAll('"', "");
const upgrade = (await readFile(upgradeURL, "utf8")).replaceAll('"', "");
const workspaceArchiveURL = new URL("./drizzle/0015_watery_kulan_gath.sql", import.meta.url);
const workspaceArchive = (await readFile(workspaceArchiveURL, "utf8")).replaceAll('"', "");
const workspacePurgeURL = new URL("./drizzle/0016_busy_zuras.sql", import.meta.url);
const workspacePurge = (await readFile(workspacePurgeURL, "utf8")).replaceAll('"', "");

test("product schema makes Postgres authoritative without provider internals", () => {
  for (const table of ["users", "user_quotas", "apps", "workspaces", "operations"]) {
    expect(schema).toContain(`CREATE TABLE cantelop_control.${table}`);
  }
  expect(cutover).toContain("RENAME TO app_session_runtimes");
  expect(schema).toContain("apps_owner_user_id_users_id_fk");
  expect(schema).toContain("CREATE UNIQUE INDEX apps_routable_slug");
  expect(schema).toContain("operations_owner_idempotency_unique");
  expect(schema).toContain("WHERE cantelop_control.operations.status = 'queued'");
  expect(schema).not.toContain("cloudflare");
  expect(schema).not.toContain("hetzner");
  expect(schema).not.toContain("machine_ip");
});

test("release API artifacts remain external and provider-neutral", () => {
  expect(schema).toContain("CREATE TABLE cantelop_control.api_artifacts");
  expect(schema).toContain("api_artifacts_object_key_unique");
  expect(schema).not.toContain("module bytea");
  expect(schema).not.toContain("CREATE TABLE cantelop_control.api_deployments");
  expect(schema).not.toContain("cloudflare");
});

test("product schema constrains lifecycle transitions structurally", () => {
  for (const constraint of ["apps_archived_at_check", "operations_running_check", "operations_succeeded_check", "operations_failed_check"]) {
    expect(schema).toContain(`CONSTRAINT ${constraint} CHECK`);
  }
});

test("unified release schema activates API and sessionRuntime as one product release", () => {
  expect(cutover).toContain("RENAME TO session_runtime_artifacts");
  expect(schema).toContain("CREATE TABLE cantelop_control.releases");
  expect(schema).toContain("api_artifact_id text NOT NULL");
  expect(cutover).toContain("RENAME COLUMN harness_artifact_id TO session_runtime_artifact_id");
  expect(schema).toContain("CREATE TABLE cantelop_control.release_components");
  expect(schema).toContain("release_components_kind_check");
  expect(schema).toContain("active_release_id text");
  expect(schema).toContain("CREATE UNIQUE INDEX releases_pending_app");
  expect(schema).toContain("CREATE UNIQUE INDEX releases_active_app");
  expect(schema).not.toContain("cloudflare");
  expect(schema).not.toContain("hetzner");
});

test("deployment upload schema binds direct uploads to an owned release request", () => {
  expect(schema).toContain("CREATE TABLE cantelop_control.deployment_uploads");
  expect(schema).toContain("owner_user_id text NOT NULL");
  expect(schema).toContain("api_digest text NOT NULL");
  expect(cutover).toContain("RENAME COLUMN harness_digest TO session_runtime_digest");
  expect(cutover).toContain("RENAME COLUMN harness_store_upload_id TO session_runtime_store_upload_id");
  expect(schema).toContain("deployment_uploads_release_id_unique");
  expect(schema).toContain("CREATE UNIQUE INDEX deployment_uploads_pending_app");
  expect(schema).not.toContain("cloudflare");
});

test("customer uploads use isolated staging keys before provider verification", () => {
  expect(schema).toContain("status IN ('preparing', 'uploading', 'finalized', 'expired')");
  expect(schema).toContain("^deployment-uploads/[0-9a-f]{32}/api/worker");
  expect(cutover).toContain("^deployment-uploads/[0-9a-f]{32}/session-runtime/image");
  expect(cutover).toContain("CREATE INDEX session_runtime_artifacts_digest");
});

test("sessionRuntime uploads describe the packaged Docker archive without claiming OCI layer semantics", () => {
  expect(cutover).toContain("application/vnd.cantelop.session-runtime.image.v1.tar+gzip");
  expect(schema).not.toContain("application/vnd.oci.image.layer.v1.tar+gzip");
});

test("configuration schema keeps secrets encrypted and product-owned", () => {
  expect(schema).toContain("CREATE TABLE cantelop_control.app_config_values");
  expect(schema).toContain("secret_nonce bytea");
  expect(schema).toContain("secret_ciphertext bytea");
  expect(schema).toContain("config_revision bigint");
  expect(schema).not.toContain("fire_fuse");
});

test("App collaborators add admins without duplicating ownership", async () => {
  const collaboration = (await readFile(new URL("./drizzle/0009_lush_invisible_woman.sql", import.meta.url), "utf8")).replaceAll('"', "");
  expect(collaboration).toContain("CREATE TABLE cantelop_control.app_collaborators");
  expect(collaboration).toContain("PRIMARY KEY(app_id,user_id)");
  expect(collaboration).toContain("app_collaborators_role_check");
  expect(collaboration).toContain("ON DELETE cascade");
});

test("identity migration consolidates duplicate-email users without dropping their provider logins", async () => {
  const identities = (await readFile(new URL("./drizzle/0010_link_user_identities.sql", import.meta.url), "utf8")).replaceAll('"', "");
  expect(identities).toContain("CREATE TABLE cantelop_control.user_identities");
  expect(identities).toContain("PARTITION BY CASE WHEN provider = 'google' AND email <> ''");
  expect(identities).toContain("@unverified.invalid");
  expect(identities).toContain("lower(btrim(cantelop_control.users.email) COLLATE C)");
  expect(identities).toContain("INSERT INTO cantelop_control.user_identities");
  expect(identities).toContain("UPDATE cantelop_control.apps resources SET owner_user_id = merge.user_id");
  expect(identities).toContain("DELETE FROM cantelop_control.users users");
  expect(identities).toContain("users_email_unique");
});

test("clean-slate migration preserves private schema permissions", () => {
  expect(schema).toContain("REVOKE ALL ON SCHEMA cantelop_control FROM PUBLIC");
  expect(schema).toContain("REVOKE ALL ON ALL TABLES IN SCHEMA cantelop_control FROM PUBLIC");
});

test("legacy migration preserves product Workspaces and queued operations", () => {
  expect(upgrade).toContain("to_regclass('cantelop_control.environments')");
  expect(upgrade).toContain("RENAME COLUMN max_runtime_instances TO max_sessions");
  expect(upgrade).toContain("kind = 'register_workspace'");
  expect(upgrade).toContain("workspace_slug");
  expect(upgrade).toContain("UPDATE cantelop_control.workspaces");
  expect(upgrade).toContain("DROP TABLE cantelop_control.environments");
  expect(upgrade).not.toContain("DROP SCHEMA");
});

test("session capacity migration removes the quota column", () => {
  expect(removeCapacity).toContain('ALTER TABLE "cantelop_control"."user_quotas" DROP COLUMN "max_sessions"');
});

test("Workspace archival frees its active slug and adds the archive operation", () => {
  expect(workspaceArchive).toContain("CREATE UNIQUE INDEX workspaces_routable_slug");
  expect(workspaceArchive).toContain("WHERE cantelop_control.workspaces.archived_at IS NULL");
  expect(workspaceArchive).toContain("archive_workspace");
});

test("Workspace purge has a dedicated operation kind", () => {
  expect(workspacePurge).toContain("purge_workspace");
});
