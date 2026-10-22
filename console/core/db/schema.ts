import { sql, type SQL } from "drizzle-orm";
import {
  bigint,
  check,
  customType,
  index,
  integer,
  jsonb,
  pgSchema,
  primaryKey,
  text,
  timestamp,
  unique,
  uniqueIndex,
  type AnyPgColumn,
} from "drizzle-orm/pg-core";

import type { SessionRuntimeManifest } from "../api-deployments/protocol-artifact";
import type { APIArtifactManifest } from "../api-deployments/artifact";
import {
  appStatuses,
  operationKinds,
  operationStatuses,
  releaseComponentKinds,
  releaseComponentStatuses,
  releaseStatuses,
  userStatuses,
} from "../domain/model";

const control = pgSchema("cantelop_control");
const bytea = customType<{ data: Buffer }>({ dataType: () => "bytea" });
const timestampTZ = (name: string) => timestamp(name, { withTimezone: true, mode: "date" });

export const fleets = control.table("fleets", {
  id: text().primaryKey(),
  ownerUserId: text("owner_user_id").notNull().references(() => users.id),
  name: text().notNull(),
  maxApps: integer("max_apps"),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
}, t => [check("fleets_capacity", sql`${t.maxApps} IS NULL OR ${t.maxApps} BETWEEN 1 AND 10000`)]);

export const fleetPrincipals = control.table("fleet_principals", {
  id: text().primaryKey(),
  fleetId: text("fleet_id").notNull().references(() => fleets.id),
  label: text().notNull(),
  secretHash: text("secret_hash").notNull(),
  scopes: jsonb().$type<string[]>().notNull(),
  expiresAt: timestampTZ("expires_at").notNull(),
  revokedAt: timestampTZ("revoked_at"),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
}, t => [index("fleet_principals_fleet").on(t.fleetId)]);

export const fleetPackages = control.table("fleet_packages", {
  id: text().primaryKey(),
  fleetId: text("fleet_id").notNull().references(() => fleets.id),
  name: text().notNull(),
  version: text().notNull(),
  runtimeId: text("runtime_id").notNull(),
  apiArtifactId: text("api_artifact_id").notNull().references(() => apiArtifacts.id),
  runtimeArtifactId: text("runtime_artifact_id").notNull().references(() => sessionRuntimeArtifacts.id),
  preparedManifestKey: text("prepared_manifest_key"),
  preparedManifestDigest: text("prepared_manifest_digest"),
  edgeReadyAt: timestampTZ("edge_ready_at"),
  edgeRetryAt: timestampTZ("edge_retry_at").notNull().defaultNow(),
  edgeCompatibilityDate: text("edge_compatibility_date").notNull().default("2026-08-11"),
  disabledAt: timestampTZ("disabled_at"),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
}, t => [unique("fleet_package_version").on(t.fleetId, t.name, t.version)]);

export const fleetInstallations = control.table("fleet_installations", {
  id: text().primaryKey(),
  fleetId: text("fleet_id").notNull().references(() => fleets.id),
  externalKey: text("external_key").notNull(),
  appId: text("app_id").notNull().unique().references(() => apps.id),
  packageId: text("package_id").notNull().references(() => fleetPackages.id),
 nextPackageId: text("next_package_id").references(()=>fleetPackages.id),
  releaseId: text("release_id").references(() => releases.id),
  desiredState: text("desired_state").notNull().default("ready"),
  errorCode: text("error_code"),
  leaseUntil: timestampTZ("lease_until"),
  updatedAt: timestampTZ("updated_at").notNull().defaultNow(),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
}, t => [unique("fleet_installation_external_key").on(t.fleetId, t.externalKey),
  check("fleet_installation_desired", sql`${t.desiredState} IN ('ready','suspended','deleted')`)]);

export const fleetInstallationTombstones=control.table("fleet_installation_tombstones",{
 fleetId:text("fleet_id").notNull().references(()=>fleets.id),externalKey:text("external_key").notNull(),createdAt:timestampTZ("created_at").notNull().defaultNow(),
},t=>[primaryKey({columns:[t.fleetId,t.externalKey]})]);

export const fleetPolicies = control.table("fleet_policies", {
  appId: text("app_id").primaryKey().references(() => apps.id),
  revision: integer().notNull().default(1),
  policy: jsonb().$type<Record<string, unknown>>().notNull(),
  enforcedRevision: integer("enforced_revision").notNull().default(0),
 sourceFence: bigint("source_fence",{mode:"bigint"}).notNull().default(sql`0`),
 nextSyncAt: timestampTZ("next_sync_at").notNull().defaultNow(),
  updatedAt: timestampTZ("updated_at").notNull().defaultNow(),
});

export const fleetBudgets = control.table("fleet_budgets", {
  appId: text("app_id").notNull().references(() => apps.id),
  epoch: text().notNull(),
  included: bigint({ mode: "bigint" }).notNull(),
  settled: bigint({ mode: "bigint" }).notNull().default(sql`0`),
  reserved: bigint({ mode: "bigint" }).notNull().default(sql`0`),
  expiresAt: timestampTZ("expires_at").notNull(),
}, t => [primaryKey({ columns: [t.appId, t.epoch] }),
  check("fleet_budget_conservation", sql`${t.included} >= 0 AND ${t.settled} >= 0 AND ${t.reserved} >= 0 AND ${t.settled}+${t.reserved} <= ${t.included}`)]);

export const fleetReservations = control.table("fleet_reservations", {
  id: text().primaryKey(),
  appId: text("app_id").notNull().references(() => apps.id),
  epoch: text().notNull(),
  hostId: text("host_id").notNull(),
  credits: bigint({ mode: "bigint" }).notNull(),
  consumed: bigint({ mode: "bigint" }),
  expiresAt: timestampTZ("expires_at").notNull(),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
}, t => [check("fleet_reservation_credit", sql`${t.credits} > 0 AND (${t.consumed} IS NULL OR ${t.consumed} BETWEEN 0 AND ${t.credits})`),
  index("fleet_reservations_app").on(t.appId, t.epoch)]);

export const fleetUsage = control.table("fleet_usage", {
  sequence: bigint({ mode: "number" }).primaryKey().generatedAlwaysAsIdentity(),
  id: text().notNull().unique(),
  fleetId: text("fleet_id").notNull().references(() => fleets.id),
  appId: text("app_id").notNull().references(() => apps.id),
  reservationId: text("reservation_id").notNull().unique().references(() => fleetReservations.id),
  credits: bigint({ mode: "bigint" }).notNull(),
  observedAt: timestampTZ("observed_at").notNull().defaultNow(),
}, t => [index("fleet_usage_cursor").on(t.fleetId, t.sequence)]);

export const fleetAudit = control.table("fleet_audit", {
  sequence: bigint({ mode: "number" }).primaryKey().generatedAlwaysAsIdentity(),
  fleetId: text("fleet_id").notNull().references(() => fleets.id),
  actorId: text("actor_id").notNull(),
  action: text().notNull(),
  target: text().notNull(),
  createdAt: timestampTZ("created_at").notNull().defaultNow(),
});
// `column IN ('a', 'b')` for a CHECK, rendered exactly as the literal SQL the
// migrations were generated from. Values are fixed identifiers, never input.
function oneOf(column: AnyPgColumn, values: readonly string[]): SQL {
  if (values.some((value) => !/^[a-z][a-z0-9_]*$/.test(value))) throw new TypeError("invalid enumerated value");
  return sql`${column} IN (${sql.raw(values.map((value) => `'${value}'`).join(", "))})`;
}

export const users = control.table("users", {
  id: text().primaryKey(),
  email: text().notNull(),
  status: text().$type<typeof userStatuses[number]>().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  lastLoginAt: timestampTZ("last_login_at").notNull(),
}, (table) => [
  check("users_id_check", sql`${table.id} ~ '^usr_[0-9a-f]{32}$'`),
  check("users_email_check", sql`length(${table.email}) <= 320 AND ${table.email} = lower(btrim(${table.email}) COLLATE "C")`),
  check("users_status_check", oneOf(table.status, userStatuses)),
  check("users_last_login_check", sql`${table.lastLoginAt} >= ${table.createdAt}`),
  unique("users_email_unique").on(table.email),
]);

export const userIdentities = control.table("user_identities", {
  userId: text("user_id").notNull().references(() => users.id, { onDelete: "cascade" }),
  provider: text().notNull(),
  providerSubject: text("provider_subject").notNull(),
  email: text().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  lastLoginAt: timestampTZ("last_login_at").notNull(),
}, (table) => [
  primaryKey({ columns: [table.provider, table.providerSubject] }),
  check("user_identities_provider_check", sql`${table.provider} ~ '^[a-z][a-z0-9_-]{0,31}$'`),
  check("user_identities_provider_subject_check", sql`${table.providerSubject} ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'`),
  check("user_identities_email_check", sql`length(${table.email}) <= 320 AND ${table.email} = lower(btrim(${table.email}) COLLATE "C")`),
  check("user_identities_last_login_check", sql`${table.lastLoginAt} >= ${table.createdAt}`),
  index("user_identities_user").on(table.userId),
]);

// Only verified account reconciliation writes these aliases. The source is a
// deleted user ID; the target always points directly to a live canonical user.
export const userAliases = control.table("user_aliases", {
  sourceUserId: text("source_user_id").primaryKey(),
  userId: text("user_id").notNull().references(() => users.id, { onDelete: "cascade" }),
  createdAt: timestampTZ("created_at").notNull(),
}, (table) => [
  check("user_aliases_source_check", sql`${table.sourceUserId} ~ '^usr_[0-9a-f]{32}$'`),
  check("user_aliases_distinct_check", sql`${table.sourceUserId} <> ${table.userId}`),
  index("user_aliases_user").on(table.userId),
]);

export const userQuotas = control.table("user_quotas", {
  userId: text("user_id").primaryKey().references(() => users.id, { onDelete: "cascade" }),
  maxApps: integer("max_apps").notNull(),
  // Unused; drop in the next release after this one is deployed. Nothing
  // enforces per-user vCPU or memory quotas since the fixed Sandbox shape. The
  // defaults let new code omit them while old code that still writes them
  // during a rolling deploy keeps working.
  maxVcpus: integer("max_vcpus").notNull().default(16),
  maxMemoryMib: bigint("max_memory_mib", { mode: "number" }).notNull().default(16384),
}, (table) => [
  check("user_quotas_max_apps_check", sql`${table.maxApps} BETWEEN 1 AND 10000`),
  check("user_quotas_max_vcpus_check", sql`${table.maxVcpus} BETWEEN 1 AND 1000000`),
  check("user_quotas_max_memory_mib_check", sql`${table.maxMemoryMib} BETWEEN 1 AND 1073741824`),
]);

export const apps = control.table("apps", {
  id: text().primaryKey(),
  ownerUserId: text("owner_user_id").notNull().references(() => users.id),
  // Opaque provider ownership survives product-account reconciliation.
  // Null uses ownerUserId until an account merge pins the original tenant.
  computeTenantId: text("compute_tenant_id"),
  computePool: text("compute_pool").$type<"standard" | "managed">().notNull().default("standard"),
  slug: text().notNull(),
  status: text().$type<typeof appStatuses[number]>().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
  archivedAt: timestampTZ("archived_at"),
  activeReleaseId: text("active_release_id").references((): AnyPgColumn => releases.id),
  configRevision: bigint("config_revision", { mode: "number" }).notNull().default(0),
}, (table) => [
  check("apps_compute_tenant_check", sql`${table.computeTenantId} IS NULL OR ${table.computeTenantId} ~ '^usr_[0-9a-f]{32}$'`),
  check("apps_compute_pool_check", sql`${table.computePool} IN ('standard', 'managed')`),
  check("apps_id_check", sql`${table.id} ~ '^app_[0-9a-f]{32}$'`),
  check("apps_slug_check", sql`${table.slug} ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND position('--' IN ${table.slug}) = 0`),
  check("apps_status_check", oneOf(table.status, appStatuses)),
  check("apps_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  check("apps_archived_at_check", sql`(${table.status} = 'archived') = (${table.archivedAt} IS NOT NULL)`),
  check("apps_config_revision_check", sql`${table.configRevision} >= 0`),
  uniqueIndex("apps_routable_slug").on(table.slug).where(sql`${table.status} <> 'archived'`),
  index("apps_owner").on(table.ownerUserId, table.createdAt, table.id),
]);

export const appCollaborators = control.table("app_collaborators", {
  appId: text("app_id").notNull().references(() => apps.id, { onDelete: "cascade" }),
  userId: text("user_id").notNull().references(() => users.id, { onDelete: "cascade" }),
  role: text().$type<"admin">().notNull(),
  addedByUserId: text("added_by_user_id").notNull().references(() => users.id),
  createdAt: timestampTZ("created_at").notNull(),
}, (table) => [
  primaryKey({ columns: [table.appId, table.userId] }),
  check("app_collaborators_role_check", sql`${table.role} = 'admin'`),
  index("app_collaborators_user").on(table.userId, table.createdAt, table.appId),
]);

export const appInvitations = control.table("app_invitations", {
  id: text().primaryKey(),
  appId: text("app_id").notNull().references(() => apps.id, { onDelete: "cascade" }),
  email: text().notNull(),
  invitedByUserId: text("invited_by_user_id").notNull().references(() => users.id),
  tokenHash: text("token_hash").notNull(),
  state: text().$type<"pending" | "accepted" | "revoked">().notNull(),
  delivery: text().$type<"sending" | "sent" | "failed" | "delivered" | "delayed" | "bounced" | "complained" | "suppressed">().notNull(),
  providerMessageId: text("provider_message_id"),
  createdAt: timestampTZ("created_at").notNull(),
  expiresAt: timestampTZ("expires_at").notNull(),
  acceptedByUserId: text("accepted_by_user_id").references(() => users.id),
}, (table) => [
  check("app_invitations_state_check", sql`${table.state} IN ('pending', 'accepted', 'revoked')`),
  check("app_invitations_delivery_check", sql`${table.delivery} IN ('sending', 'sent', 'failed', 'delivered', 'delayed', 'bounced', 'complained', 'suppressed')`),
  check("app_invitations_email_check", sql`length(${table.email}) BETWEEN 3 AND 320 AND ${table.email} = lower(btrim(${table.email}) COLLATE "C")`),
  check("app_invitations_token_check", sql`${table.tokenHash} ~ '^[0-9a-f]{64}$'`),
  check("app_invitations_expiry_check", sql`${table.expiresAt} > ${table.createdAt}`),
  check("app_invitations_acceptance_check", sql`(${table.state} = 'accepted') = (${table.acceptedByUserId} IS NOT NULL)`),
  uniqueIndex("app_invitations_token").on(table.tokenHash),
  uniqueIndex("app_invitations_pending_email").on(table.appId, table.email).where(sql`${table.state} = 'pending'`),
  uniqueIndex("app_invitations_provider_message").on(table.providerMessageId),
  index("app_invitations_recipient_time").on(table.email, table.createdAt),
  index("app_invitations_sender_time").on(table.invitedByUserId, table.createdAt),
]);

export const invitationSuppressions = control.table("invitation_suppressions", {
  recipientHash: text("recipient_hash").primaryKey(),
  reason: text().$type<"opt_out" | "bounced" | "complained" | "suppressed">().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
}, (table) => [
  check("invitation_suppressions_recipient_hash_check", sql`${table.recipientHash} ~ '^[a-f0-9]{64}$'`),
  check("invitation_suppressions_reason_check", sql`${table.reason} IN ('opt_out', 'bounced', 'complained', 'suppressed')`),
]);

export const invitationDeliveryEvents = control.table("invitation_delivery_events", {
  id: text().primaryKey(),
  invitationId: text("invitation_id").notNull().references(() => appInvitations.id, { onDelete: "cascade" }),
  type: text().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
});

export const demoVisitorRequestBudgets = control.table("demo_visitor_request_budgets", {
  visitorId: text("visitor_id").primaryKey(),
  dayStartedAt: timestampTZ("day_started_at").notNull(),
  dayCount: integer("day_count").notNull(),
  minuteStartedAt: timestampTZ("minute_started_at").notNull(),
  minuteCount: integer("minute_count").notNull(),
}, (table) => [
  check("demo_visitor_request_budgets_day_count_check", sql`${table.dayCount} BETWEEN 1 AND 50`),
  check("demo_visitor_request_budgets_minute_count_check", sql`${table.minuteCount} BETWEEN 1 AND 10`),
]);

export const demoRequestBudgets = control.table("demo_request_budgets", {
  userId: text("user_id").primaryKey().references(() => users.id, { onDelete: "cascade" }),
  dayStartedAt: timestampTZ("day_started_at").notNull(),
  dayCount: integer("day_count").notNull(),
  minuteStartedAt: timestampTZ("minute_started_at").notNull(),
  minuteCount: integer("minute_count").notNull(),
}, (table) => [
  check("demo_request_budgets_day_count_check", sql`${table.dayCount} BETWEEN 1 AND 50`),
  check("demo_request_budgets_minute_count_check", sql`${table.minuteCount} BETWEEN 1 AND 10`),
]);

export const appSessionRuntimes = control.table("app_session_runtimes", {
  appId: text("app_id").primaryKey().references(() => apps.id, { onDelete: "cascade" }),
  internalPort: integer("internal_port").notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
}, (table) => [
  check("app_session_runtimes_internal_port_check", sql`${table.internalPort} BETWEEN 1 AND 65535`),
  check("app_session_runtimes_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
]);

export const workspaces = control.table("workspaces", {
  id: text().primaryKey(),
  appId: text("app_id").notNull().references(() => apps.id),
  slug: text().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
  archivedAt: timestampTZ("archived_at"),
  // Set once the compute host has also archived the Workspace. The gateway
  // opens host-registered Workspaces by slug without asking product control.
  hostArchivedAt: timestampTZ("host_archived_at"),
}, (table) => [
  check("workspaces_id_check", sql`${table.id} ~ '^wsp_[0-9a-f]{32}$'`),
  check("workspaces_slug_check", sql`${table.slug} ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND position('--' IN ${table.slug}) = 0`),
  check("workspaces_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  uniqueIndex("workspaces_routable_slug").on(table.appId, table.slug).where(sql`${table.archivedAt} IS NULL`),
  index("workspaces_app").on(table.appId, table.createdAt, table.id),
]);

export const workspaceDatabases = control.table("workspace_databases", {
  workspaceId: text("workspace_id").primaryKey().references(() => workspaces.id, { onDelete: "cascade" }),
  providerName: text("provider_name"),
  providerId: text("provider_id"),
  url: text(),
  status: text().$type<"pending" | "ready" | "deleting" | "deleted">().notNull().default("pending"),
  tokenNonce: bytea("token_nonce"),
  tokenCiphertext: bytea("token_ciphertext"),
  tokenExpiresAt: timestampTZ("token_expires_at"),
  updatedAt: timestampTZ("updated_at").notNull().defaultNow(),
}, (table) => [
  uniqueIndex("workspace_databases_provider_name").on(table.providerName),
  check("workspace_databases_status_check", sql`${table.status} IN ('pending', 'ready', 'deleting', 'deleted')`),
  check("workspace_databases_ready_check", sql`${table.status} <> 'ready' OR (${table.providerName} IS NOT NULL AND ${table.providerId} IS NOT NULL AND ${table.url} IS NOT NULL)`),
]);

export const operations = control.table("operations", {
  id: text().primaryKey(),
  ownerUserId: text("owner_user_id").notNull().references(() => users.id),
  appId: text("app_id").notNull().references(() => apps.id),
  kind: text().$type<typeof operationKinds[number]>().notNull(),
  progress: text(),
  status: text().$type<typeof operationStatuses[number]>().notNull(),
  idempotencyKey: text("idempotency_key").notNull(),
  input: jsonb().$type<Record<string, unknown>>().notNull(),
  output: jsonb().$type<Record<string, unknown>>(),
  errorCode: text("error_code"),
  attempts: integer().notNull().default(0),
  availableAt: timestampTZ("available_at").notNull(),
  lockedBy: text("locked_by"),
  lockedAt: timestampTZ("locked_at"),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
}, (table) => [
  check("operations_id_check", sql`${table.id} ~ '^op_[0-9a-f]{32}$'`),
  check("operations_kind_check", oneOf(table.kind, operationKinds)),
  check("operations_status_check", oneOf(table.status, operationStatuses)),
  check("operations_idempotency_key_check", sql`length(${table.idempotencyKey}) BETWEEN 16 AND 128 AND ${table.idempotencyKey} ~ '^[A-Za-z0-9_.:-]+$'`),
  check("operations_input_check", sql`jsonb_typeof(${table.input}) = 'object'`),
  check("operations_output_check", sql`${table.output} IS NULL OR jsonb_typeof(${table.output}) = 'object'`),
  check("operations_error_code_check", sql`${table.errorCode} IS NULL OR ${table.errorCode} ~ '^[a-z][a-z0-9_]{0,63}$'`),
  check("operations_attempts_check", sql`${table.attempts} >= 0 AND (${table.kind} IN ('cancel_release', 'activate_release', 'archive_app') OR ${table.attempts} <= 100)`),
  check("operations_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  check("operations_running_check", sql`(${table.status} = 'running') = (${table.lockedBy} IS NOT NULL AND ${table.lockedAt} IS NOT NULL)`),
  check("operations_succeeded_check", sql`(${table.status} = 'succeeded') = (${table.output} IS NOT NULL)`),
  check("operations_failed_check", sql`(${table.status} = 'failed') = (${table.errorCode} IS NOT NULL)`),
  unique("operations_owner_idempotency_unique").on(table.ownerUserId, table.idempotencyKey),
  index("operations_ready").on(table.availableAt, table.createdAt, table.id).where(sql`${table.status} = 'queued'`),
  index("operations_app").on(table.appId, table.createdAt, table.id),
]);

export const apiArtifacts = control.table("api_artifacts", {
  id: text().primaryKey(),
  digest: text().notNull(),
  objectKey: text("object_key").notNull().unique(),
  sizeBytes: integer("size_bytes").notNull(),
  manifest: jsonb().$type<APIArtifactManifest>().notNull(),
  createdAt: timestampTZ("created_at").notNull(),
}, (table) => [
  check("api_artifacts_id_check", sql`${table.id} ~ '^art_[0-9a-f]{32}$'`),
  check("api_artifacts_digest_check", sql`${table.digest} ~ '^sha256:[0-9a-f]{64}$'`),
  check("api_artifacts_object_key_check", sql`${table.objectKey} ~ '^deployment-uploads/[0-9a-f]{32}/api/worker\\.mjs$'`),
  check("api_artifacts_size_bytes_check", sql`${table.sizeBytes} BETWEEN 1 AND 1048576`),
  check("api_artifacts_manifest_check", sql`jsonb_typeof(${table.manifest}) = 'object'`),
  index("api_artifacts_digest").on(table.digest),
]);

export const sessionRuntimeArtifacts = control.table("session_runtime_artifacts", {
  id: text().primaryKey(),
  digest: text().notNull(),
  objectKey: text("object_key").notNull().unique(),
  sizeBytes: bigint("size_bytes", { mode: "number" }).notNull(),
  mediaType: text("media_type").notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  manifest: jsonb().$type<SessionRuntimeManifest>(),
}, (table) => [
  check("session_runtime_artifacts_manifest_check", sql`${table.manifest} IS NULL OR jsonb_typeof(${table.manifest}) = 'object'`),
  check("session_runtime_artifacts_id_check", sql`${table.id} ~ '^sra_[0-9a-f]{32}$'`),
  check("session_runtime_artifacts_digest_check", sql`${table.digest} ~ '^sha256:[0-9a-f]{64}$'`),
  check("session_runtime_artifacts_object_key_check", sql`${table.objectKey} ~ '^deployment-uploads/[0-9a-f]{32}/session-runtime/image\\.tar\\.gz$'`),
  check("session_runtime_artifacts_size_bytes_check", sql`${table.sizeBytes} BETWEEN 1 AND 21474836480`),
  check("session_runtime_artifacts_media_type_check", sql`${table.mediaType} = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip'`),
  index("session_runtime_artifacts_digest").on(table.digest),
]);

export const releases = control.table("releases", {
  id: text().primaryKey(),
  ownerUserId: text("owner_user_id").notNull().references(() => users.id),
  appId: text("app_id").notNull().references(() => apps.id),
  apiArtifactId: text("api_artifact_id").notNull().references(() => apiArtifacts.id),
  sessionRuntimeArtifactId: text("session_runtime_artifact_id").notNull().references(() => sessionRuntimeArtifacts.id),
  version: integer().notNull(),
  status: text().$type<typeof releaseStatuses[number]>().notNull(),
  idempotencyKey: text("idempotency_key").notNull(),
  errorCode: text("error_code"),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
  activatedAt: timestampTZ("activated_at"),
}, (table) => [
  check("releases_id_check", sql`${table.id} ~ '^rel_[0-9a-f]{32}$'`),
  check("releases_version_check", sql`${table.version} > 0`),
  check("releases_status_check", oneOf(table.status, releaseStatuses)),
  check("releases_idempotency_key_check", sql`length(${table.idempotencyKey}) BETWEEN 16 AND 128 AND ${table.idempotencyKey} ~ '^[A-Za-z0-9_.:-]+$'`),
  check("releases_error_code_check", sql`${table.errorCode} IS NULL OR ${table.errorCode} ~ '^[a-z][a-z0-9_]{0,63}$'`),
  check("releases_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  check("releases_failed_check", sql`(${table.status} = 'failed') = (${table.errorCode} IS NOT NULL)`),
  check("releases_activated_check", sql`(${table.status} IN ('active', 'superseded')) = (${table.activatedAt} IS NOT NULL)`),
  unique("releases_app_version_unique").on(table.appId, table.version),
  unique("releases_owner_idempotency_unique").on(table.ownerUserId, table.idempotencyKey),
  uniqueIndex("releases_pending_app").on(table.appId).where(sql`${table.status} IN ('queued', 'provisioning', 'cancelling')`),
  uniqueIndex("releases_active_app").on(table.appId).where(sql`${table.status} = 'active'`),
  index("releases_app").on(table.appId, table.version),
]);

export const releaseComponents = control.table("release_components", {
  releaseId: text("release_id").notNull().references(() => releases.id, { onDelete: "cascade" }),
  kind: text().$type<typeof releaseComponentKinds[number]>().notNull(),
  operationId: text("operation_id").notNull().unique().references(() => operations.id),
  status: text().$type<typeof releaseComponentStatuses[number]>().notNull(),
  output: jsonb().$type<Record<string, unknown>>(),
  errorCode: text("error_code"),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
}, (table) => [
  primaryKey({ columns: [table.releaseId, table.kind] }),
  check("release_components_kind_check", oneOf(table.kind, releaseComponentKinds)),
  check("release_components_status_check", oneOf(table.status, releaseComponentStatuses)),
  check("release_components_output_check", sql`${table.output} IS NULL OR jsonb_typeof(${table.output}) = 'object'`),
  check("release_components_error_code_check", sql`${table.errorCode} IS NULL OR ${table.errorCode} ~ '^[a-z][a-z0-9_]{0,63}$'`),
  check("release_components_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  check("release_components_succeeded_check", sql`(${table.status} = 'succeeded') = (${table.output} IS NOT NULL)`),
  check("release_components_failed_check", sql`(${table.status} = 'failed') = (${table.errorCode} IS NOT NULL)`),
  index("release_components_status").on(table.status, table.updatedAt, table.releaseId),
]);

export const deploymentUploads = control.table("deployment_uploads", {
  id: text().primaryKey(),
  ownerUserId: text("owner_user_id").notNull().references(() => users.id),
  appId: text("app_id").notNull().references(() => apps.id),
  idempotencyKey: text("idempotency_key").notNull(),
  status: text().$type<"preparing" | "uploading" | "finalized" | "expired" | "cancelled">().notNull(),
  apiDigest: text("api_digest").notNull(),
  apiObjectKey: text("api_object_key").notNull(),
  apiSizeBytes: integer("api_size_bytes").notNull(),
  apiManifest: jsonb("api_manifest").$type<APIArtifactManifest>().notNull(),
  sessionRuntimeDigest: text("session_runtime_digest").notNull(),
  sessionRuntimeObjectKey: text("session_runtime_object_key").notNull(),
  sessionRuntimeSizeBytes: bigint("session_runtime_size_bytes", { mode: "number" }).notNull(),
  sessionRuntimeManifest: jsonb("session_runtime_manifest").$type<SessionRuntimeManifest>(),
  sessionRuntimeMediaType: text("session_runtime_media_type")
    .$type<"application/vnd.cantelop.session-runtime.image.v1.tar+gzip">()
    .notNull(),
  sessionRuntimeStoreUploadId: text("session_runtime_store_upload_id"),
  sessionRuntimePartSizeBytes: integer("session_runtime_part_size_bytes"),
  sessionRuntimePartCount: integer("session_runtime_part_count"),
  releaseId: text("release_id").unique().references(() => releases.id),
  expiresAt: timestampTZ("expires_at").notNull(),
  createdAt: timestampTZ("created_at").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
}, (table) => [
  check("deployment_uploads_runtime_manifest_check",sql`${table.sessionRuntimeManifest} IS NULL OR jsonb_typeof(${table.sessionRuntimeManifest}) = 'object'`),
  check("deployment_uploads_id_check", sql`${table.id} ~ '^upl_[0-9a-f]{32}$'`),
  check("deployment_uploads_idempotency_key_check", sql`length(${table.idempotencyKey}) BETWEEN 16 AND 128 AND ${table.idempotencyKey} ~ '^[A-Za-z0-9_.:-]+$'`),
  check("deployment_uploads_status_check", sql`${table.status} IN ('preparing', 'uploading', 'finalized', 'expired', 'cancelled')`),
  check("deployment_uploads_api_digest_check", sql`${table.apiDigest} ~ '^sha256:[0-9a-f]{64}$'`),
  check("deployment_uploads_api_object_key_check", sql`${table.apiObjectKey} ~ '^deployment-uploads/[0-9a-f]{32}/api/worker\\.mjs$'`),
  check("deployment_uploads_api_size_bytes_check", sql`${table.apiSizeBytes} BETWEEN 1 AND 1048576`),
  check("deployment_uploads_api_manifest_check", sql`jsonb_typeof(${table.apiManifest}) = 'object'`),
  check("deployment_uploads_session_runtime_digest_check", sql`${table.sessionRuntimeDigest} ~ '^sha256:[0-9a-f]{64}$'`),
  check("deployment_uploads_session_runtime_object_key_check", sql`${table.sessionRuntimeObjectKey} ~ '^deployment-uploads/[0-9a-f]{32}/session-runtime/image\\.tar\\.gz$'`),
  check("deployment_uploads_session_runtime_size_bytes_check", sql`${table.sessionRuntimeSizeBytes} BETWEEN 1 AND 21474836480`),
  check("deployment_uploads_session_runtime_media_type_check", sql`${table.sessionRuntimeMediaType} = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip'`),
  check("deployment_uploads_session_runtime_store_upload_id_check", sql`${table.sessionRuntimeStoreUploadId} IS NULL OR (length(${table.sessionRuntimeStoreUploadId}) BETWEEN 1 AND 1024 AND ${table.sessionRuntimeStoreUploadId} !~ '[\\x00\\r\\n]')`),
  check("deployment_uploads_session_runtime_part_size_check", sql`${table.sessionRuntimePartSizeBytes} IS NULL OR ${table.sessionRuntimePartSizeBytes} BETWEEN 5242880 AND 536870912`),
  check("deployment_uploads_session_runtime_part_count_check", sql`${table.sessionRuntimePartCount} IS NULL OR ${table.sessionRuntimePartCount} BETWEEN 1 AND 10000`),
  check("deployment_uploads_preparing_check", sql`${table.status} <> 'preparing' OR (${table.sessionRuntimeStoreUploadId} IS NULL AND ${table.sessionRuntimePartSizeBytes} IS NULL AND ${table.sessionRuntimePartCount} IS NULL)`),
  check("deployment_uploads_uploaded_check", sql`${table.status} NOT IN ('uploading', 'finalized') OR (${table.sessionRuntimeStoreUploadId} IS NOT NULL AND ${table.sessionRuntimePartSizeBytes} IS NOT NULL AND ${table.sessionRuntimePartCount} IS NOT NULL)`),
  check("deployment_uploads_updated_at_check", sql`${table.updatedAt} >= ${table.createdAt}`),
  check("deployment_uploads_finalized_check", sql`(${table.status} = 'finalized') = (${table.releaseId} IS NOT NULL)`),
  check("deployment_uploads_expiry_check", sql`${table.expiresAt} > ${table.createdAt}`),
  unique("deployment_uploads_owner_idempotency_unique").on(table.ownerUserId, table.idempotencyKey),
  index("deployment_uploads_expiry").on(table.expiresAt, table.id).where(sql`${table.status} = 'uploading'`),
  uniqueIndex("deployment_uploads_pending_app").on(table.appId).where(sql`${table.status} IN ('preparing', 'uploading')`),
]);

export const appConfigValues = control.table("app_config_values", {
  appId: text("app_id").notNull().references(() => apps.id, { onDelete: "cascade" }),
  name: text().notNull(),
  kind: text().$type<"variable" | "secret">().notNull(),
  valueText: text("value_text"),
  secretNonce: bytea("secret_nonce"),
  secretCiphertext: bytea("secret_ciphertext"),
  valueBytes: integer("value_bytes").notNull(),
  updatedAt: timestampTZ("updated_at").notNull(),
}, (table) => [
  primaryKey({ columns: [table.appId, table.name] }),
  check("app_config_values_name_check", sql`${table.name} ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$' AND (${table.name} !~ '^CANTELOP_' OR (${table.name} = 'CANTELOP_INTEGRATION_TOKEN' AND ${table.kind} = 'secret'))`),
  check("app_config_values_kind_check", sql`${table.kind} IN ('variable', 'secret')`),
  check("app_config_values_value_bytes_check", sql`${table.valueBytes} BETWEEN 0 AND 65536`),
  check("app_config_values_shape_check", sql`(${table.kind} = 'variable' AND ${table.valueText} IS NOT NULL AND ${table.secretNonce} IS NULL AND ${table.secretCiphertext} IS NULL) OR (${table.kind} = 'secret' AND ${table.valueText} IS NULL AND octet_length(${table.secretNonce}) = 12 AND octet_length(${table.secretCiphertext}) >= 16)`),
  index("app_config_values_app").on(table.appId, table.name),
]);

// The current token stays in app_config_values. A rotation keeps the rotated-out
// token only while its grace period lasts and records the configuration revision
// whose projection makes the new token live.
export const appIntegrationRotations = control.table("app_integration_rotations", {
  appId: text("app_id").primaryKey().references(() => apps.id, { onDelete: "cascade" }),
  previousNonce: bytea("previous_nonce"),
  previousCiphertext: bytea("previous_ciphertext"),
  previousExpiresAt: timestampTZ("previous_expires_at"),
  configRevision: bigint("config_revision", { mode: "number" }).notNull(),
  rotatedAt: timestampTZ("rotated_at").notNull(),
}, (table) => [
  check("app_integration_rotations_previous_check", sql`(${table.previousNonce} IS NULL AND ${table.previousCiphertext} IS NULL AND ${table.previousExpiresAt} IS NULL) OR (octet_length(${table.previousNonce}) = 12 AND octet_length(${table.previousCiphertext}) >= 16 AND ${table.previousExpiresAt} > ${table.rotatedAt})`),
  check("app_integration_rotations_revision_check", sql`${table.configRevision} >= 1`),
]);

export const controlSchema = {
  users,
  userIdentities,
  userQuotas,
  apps,
  appCollaborators,
  appSessionRuntimes,
  workspaces,
  operations,
  apiArtifacts,
  sessionRuntimeArtifacts,
  releases,
  releaseComponents,
  deploymentUploads,
  appConfigValues,
  appIntegrationRotations,
};

export const deploymentLogs = control.table("deployment_logs", {
  id: bigint({ mode: "bigint" }).primaryKey().generatedAlwaysAsIdentity(),
  releaseId: text("release_id").notNull().references(() => releases.id, { onDelete: "cascade" }),
  createdAt: timestampTZ("created_at").notNull().default(sql`clock_timestamp()`),
  source: text().notNull(),
  level: text().notNull(),
  message: text().notNull(),
}, (table) => [
  check("deployment_logs_source_check", sql`${table.source} IN ('deployment', 'api', 'session_runtime')`),
  check("deployment_logs_level_check", sql`${table.level} IN ('info', 'warning', 'error')`),
  index("deployment_logs_release").on(table.releaseId, table.id),
]);
