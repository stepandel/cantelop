import { isLegacyPlaceholder, reconcileLegacyUser } from "./reconcile-user";
import type { Pool } from "pg";
import { and, asc, eq, isNull, ne, sql } from "drizzle-orm";
import { alias } from "drizzle-orm/pg-core";

import { ControlError } from "../domain/errors";
import { assertResourceID, newResourceID } from "../domain/ids";
import {
  appSlugPattern,
  type CreateAppInput,
  type CreateWorkspaceInput,
  defaultUserQuota,
  type IdentityInput,
  type UserQuotaUsage,
  validateCreateApp,
  validateCreateWorkspace,
  validateIdentity,
} from "../domain/model";
import {
  controlQueryBuilder,
  executeDrizzle,
  withTransaction,
  type QueryResult,
  type SQLPool,
  type TransactionClient,
} from "./database";
import { appAccessJoin, appAccessRole, appAccessible, appAccessibleSQL, resolveAppOwnerForUser } from "./app-access";
import { appCollaborators, appSessionRuntimes, apps, workspaces, users } from "./schema";
import { allocateReleaseGeneration } from "./release-generation";
import { appQuotaUsageSQL, assertActiveAppOwner, assertAppQuotaAdmission } from "./app-quota";

export interface ProductUser {
  id: string;
  email: string;
  status: "active" | "suspended";
  createdAt: Date;
  lastLoginAt: Date;
}

export interface ProductApp {
  id: string;
  ownerUserID: string;
  accessRole?: "owner" | "admin";
  /** Who the App belongs to and how many admins they have added: read
   *  alongside the row on reads scoped to a user, where the console shows them. */
  ownerEmail?: string;
  adminCount?: number;
  slug: string;
  status: "provisioning" | "active" | "archived";
  sessionRuntime: {
    internalPort: number;
  };
  createdAt: Date;
  updatedAt: Date;
}

export interface ProductWorkspace {
  id: string;
  appID: string;
  slug: string;
  createdAt: Date;
  updatedAt: Date;
  archivedAt?: Date;
}

// Match the compute host's per-App registration limit and bound the amount of
// Workspace work one App can leave in the shared control queue.
export const maxWorkspacesPerApp = 25;
// Every active Workspace can still enqueue a purge, so reserve queue room for
// those deletes rather than blocking cleanup when registration is saturated.
const maxWorkspaceOperationsAndPurgeReservePerApp = 2 * maxWorkspacesPerApp;

export interface WorkspacePurgeReconciliationStatus {
  archived: number;
  missing: number;
  queued: number;
  running: number;
  succeeded: number;
  failed: number;
}

export interface AppCollaborator {
  userID: string;
  email: string;
  role: "admin";
  createdAt: Date;
}

export interface AppAccess {
  owner: { userID: string; email: string };
  collaborators: AppCollaborator[];
}

interface UserRow extends Record<string, unknown> {
  id: string;
  email: string;
  status: "active" | "suspended";
  created_at: Date;
  last_login_at: Date;
}

interface AppRow extends Record<string, unknown> {
  id: string;
  owner_user_id: string;
  slug: string;
  status: "provisioning" | "active" | "archived";
  internal_port: number;
  created_at: Date;
  updated_at: Date;
  owner_email?: string;
  admin_count?: number;
}

interface WorkspaceRow extends Record<string, unknown> {
  id: string;
  app_id: string;
  slug: string;
  created_at: Date;
  updated_at: Date;
  archived_at: Date | null;
}

const appSelection = {
  id: apps.id,
  owner_user_id: apps.ownerUserId,
  slug: apps.slug,
  status: apps.status,
  internal_port: appSessionRuntimes.internalPort,
  created_at: apps.createdAt,
  updated_at: apps.updatedAt,
};

// Reads scoped to a user also name the App's owner and count its admins:
// what the console puts on a card. `users` is taken by the actor row in the
// single-App reads, so the owner joins under its own alias, and the admin
// count is a correlated subquery so the LEFT JOIN that establishes the
// actor's access stays one row per App. The subquery names its table in
// the raw form the access predicates use: an aliased table inside a sql
// template renders as the bare alias.
const owners = alias(users, "owner");
const userAppSelection = {
  ...appSelection,
  owner_email: sql<string>`${owners.email}`.as("owner_email"),
  admin_count: sql<number>`(select count(*)::int from cantelop_control.app_collaborators admins where admins.app_id = ${apps.id} and admins.role = 'admin')`.as("admin_count"),
};

const accessibleAppSelection = { ...userAppSelection, actor_status: sql<"active" | "suspended">`${users.status}`.as("actor_status") };

const workspaceSelection = {
  id: workspaces.id,
  app_id: workspaces.appId,
  slug: workspaces.slug,
  created_at: workspaces.createdAt,
  updated_at: workspaces.updatedAt,
  archived_at: workspaces.archivedAt,
};

export class ProductRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  static fromPool(pool: Pool): ProductRepository {
    return new ProductRepository(pool as unknown as SQLPool);
  }

  async resolveIdentity(rawInput: IdentityInput): Promise<ProductUser> {
    const input = validateIdentity(rawInput);
    return withTransaction(this.database, async (client) => {
      const at = this.now();
      // Serialize first use of one provider subject. The users.email constraint
      // separately serializes two different providers arriving for the same
      // address, so both paths converge on one product user. An ordinary login
      // takes nothing wider: a shared lock here would queue every sign-in on
      // one connection apiece and drain the pool.
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [
        `${input.provider}:${input.providerSubject}`,
      ]);
      const existing = await client.query<UserRow>(`SELECT users.id, users.email, users.status, users.created_at, users.last_login_at
FROM cantelop_control.user_identities identities
JOIN cantelop_control.users users ON users.id = identities.user_id
WHERE identities.provider = $1 AND identities.provider_subject = $2
FOR UPDATE OF identities, users`, [input.provider, input.providerSubject]);

      let result: QueryResult<UserRow>;
      if (existing.rowCount === 1) {
        const current = existing.rows[0];
        if (current.status !== "active") throw new ControlError("user_suspended");
        // Only a legacy merge reaches across identities, so only it pays for
        // the wider lock. Always taken after the per-subject lock above, never
        // before, so the two orders cannot cycle.
        let resolvedID = current.id;
        if (isLegacyPlaceholder(current.email, input)) {
          await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [
            "cantelop:identity-reconciliation",
          ]);
          resolvedID = await reconcileLegacyUser(client, current.id, input.email, at);
        }
        await client.query(`UPDATE cantelop_control.user_identities
SET email = $3, last_login_at = $4
WHERE provider = $1 AND provider_subject = $2`, [input.provider, input.providerSubject, input.email, at]);
        // The account's address follows the newly verified one so a later
        // sign-in through the other provider still lands here; if another
        // account already holds that address the identity stays attached and
        // the address stays put.
        result = await client.query<UserRow>(`UPDATE cantelop_control.users
SET last_login_at = $2,
  email = CASE WHEN EXISTS (SELECT 1 FROM cantelop_control.users other WHERE other.email = $3 AND other.id <> users.id)
    THEN users.email ELSE $3 END
WHERE id = $1
RETURNING id, email, status, created_at, last_login_at`, [resolvedID, at, input.email]);
      } else {
        result = await client.query<UserRow>(`INSERT INTO cantelop_control.users
  (id, email, status, created_at, last_login_at)
VALUES ($1, $2, 'active', $3, $3)
ON CONFLICT (email) DO UPDATE
SET last_login_at = EXCLUDED.last_login_at
RETURNING id, email, status, created_at, last_login_at`, [newResourceID("user"), input.email, at]);
        const user = requiredRow(result, "resolve user by email");
        await client.query(`INSERT INTO cantelop_control.user_identities
  (user_id, provider, provider_subject, email, created_at, last_login_at)
VALUES ($1, $2, $3, $4, $5, $5)`, [user.id, input.provider, input.providerSubject, input.email, at]);
      }
      const user = requiredRow(result, "resolve user identity");
      await client.query(`INSERT INTO cantelop_control.user_quotas
  (user_id, max_apps)
VALUES ($1, $2)
ON CONFLICT (user_id) DO NOTHING`, [user.id, defaultUserQuota.maxApps]);
      if (user.status === "suspended") throw new ControlError("user_suspended");
      return mapUser(user);
    });
  }

  async createAppForUser(userID: string, rawInput: CreateAppInput): Promise<ProductApp> {
    return withTransaction(this.database, client => this.createAppInTransaction(client, userID, rawInput));
  }

  /** Internal aggregate creation; a Fleet caller must bind its installation in
   *  this same transaction. Only an existing, owned Fleet covers the App slot. */
  async createAppInTransaction(client: TransactionClient, userID: string, rawInput: CreateAppInput, fleetID?: string): Promise<ProductApp> {
    assertResourceID("user", userID);
    const input = validateCreateApp(rawInput);
    if (fleetID === undefined) {
      await assertAppQuotaAdmission(client, userID);
    } else {
      await assertActiveAppOwner(client, userID);
      const fleet = await client.query(`SELECT id FROM cantelop_control.fleets
WHERE id = $1 AND owner_user_id = $2 FOR KEY SHARE`, [fleetID, userID]);
      if (fleet.rowCount !== 1) throw new ControlError("not_found");
    }

    const appID = newResourceID("app");
    const at = this.now();
    try {
      await client.query(`INSERT INTO cantelop_control.apps
  (id, owner_user_id, slug, status, created_at, updated_at)
VALUES ($1, $2, $3, 'provisioning', $4, $4)`, [appID, userID, input.slug, at]);
      await client.query(`INSERT INTO cantelop_control.app_session_runtimes
  (app_id, internal_port, created_at, updated_at)
VALUES ($1, $2, $3, $3)`, [
        appID, input.sessionRuntime.internalPort, at,
      ]);
      await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'register_app', 'queued', $4, $5::jsonb, $6, $6, $6)`, [
        newResourceID("operation"), userID, appID, `register-app:${appID}`,
        JSON.stringify({
          app_slug: input.slug,
          internal_port: input.sessionRuntime.internalPort,
        }),
        at,
      ]);
    } catch (error) {
      if (isUniqueViolation(error)) throw new ControlError("conflict");
      throw error;
    }
    return {
      id: appID, ownerUserID: userID, slug: input.slug, status: "provisioning",
      sessionRuntime: input.sessionRuntime, createdAt: at, updatedAt: at,
    };
  }

  async getUserQuota(userID: string): Promise<UserQuotaUsage> {
    assertResourceID("user", userID);
    const result = await this.database.query<{ max_apps: number; used_apps: number }>(`SELECT q.max_apps,
(${appQuotaUsageSQL}) AS used_apps
FROM cantelop_control.user_quotas q
JOIN cantelop_control.users u ON u.id = q.user_id
WHERE q.user_id = $1 AND u.status = 'active'`, [userID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return { maxApps: result.rows[0].max_apps, usedApps: result.rows[0].used_apps };
  }

  async getActiveUser(userID: string): Promise<ProductUser> {
    assertResourceID("user", userID);
    // Resolve only a recorded merge, never an email supplied by a credential.
    // Live users take precedence; aliases are flattened inside the merge transaction.
    const result = await this.database.query<UserRow>(`SELECT id, email, status, created_at, last_login_at
FROM cantelop_control.users WHERE id = coalesce(
  (SELECT id FROM cantelop_control.users WHERE id = $1),
  (SELECT user_id FROM cantelop_control.user_aliases WHERE source_user_id = $1)
)`, [userID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    const user = mapUser(requiredRow(result, "read user"));
    if (user.status !== "active") throw new ControlError("user_suspended");
    return user;
  }

  async createWorkspaceForUser(userID: string, rawInput: CreateWorkspaceInput): Promise<ProductWorkspace> {
    assertResourceID("user", userID);
    const input = validateCreateWorkspace(rawInput);
    return withTransaction(this.database, async (client) => {
      const owner = await client.query<{ owner_user_id: string }>(`SELECT a.owner_user_id
FROM cantelop_control.apps a
WHERE a.id = $1 AND a.status <> 'archived'
  AND ${appAccessibleSQL()}
FOR UPDATE OF a`, [input.appID, userID]);
      if (owner.rowCount !== 1) throw new ControlError("not_found");
      const ownerUserID = requiredRow(owner, "read App owner").owner_user_id;
      await this.assertWorkspaceAdmission(client, input.appID);
      const workspaceID = newResourceID("workspace");
      const at = this.now();
      try {
        await insertWorkspaceAggregate(client, { workspaceID, appID: input.appID, slug: input.slug, ownerUserID, at }, "error");
      } catch (error) {
        if (isUniqueViolation(error)) throw new ControlError("conflict");
        throw error;
      }
      return {
        id: workspaceID, appID: input.appID, slug: input.slug, createdAt: at, updatedAt: at,
      };
    });
  }

  async createWorkspaceForApp(rawInput: CreateWorkspaceInput): Promise<ProductWorkspace> {
    const input = validateCreateWorkspace(rawInput);
    return withTransaction(this.database, async (client) => {
      const owner = await client.query<{ owner_user_id: string }>(`SELECT owner_user_id
FROM cantelop_control.apps
WHERE id = $1 AND status <> 'archived'
FOR UPDATE`, [input.appID]);
      if (owner.rowCount !== 1) throw new ControlError("not_found");
      const ownerUserID = requiredRow(owner, "read App owner").owner_user_id;
      await this.assertWorkspaceAdmission(client, input.appID);
      const workspaceID = newResourceID("workspace");
      const at = this.now();
      try {
        await insertWorkspaceAggregate(client, { workspaceID, appID: input.appID, slug: input.slug, ownerUserID, at }, "error");
      } catch (error) {
        if (isUniqueViolation(error)) throw new ControlError("conflict");
        throw error;
      }
      return {
        id: workspaceID, appID: input.appID, slug: input.slug, createdAt: at, updatedAt: at,
      };
    });
  }

  async deleteWorkspaceForUser(userID: string, workspaceID: string): Promise<void> {
    assertResourceID("user", userID);
    assertResourceID("workspace", workspaceID);
    await withTransaction(this.database, async (client) => {
      const at = this.now();
      const workspace = await client.query<{ app_id: string; owner_user_id: string; archived_at: Date | null }>(`SELECT w.app_id, a.owner_user_id, w.archived_at
FROM cantelop_control.workspaces w
JOIN cantelop_control.apps a ON a.id = w.app_id
WHERE w.id = $1 AND a.status <> 'archived'
  AND ${appAccessibleSQL()}
FOR UPDATE OF w, a`, [workspaceID, userID]);
      if (workspace.rowCount !== 1) throw new ControlError("not_found");
      const { app_id: appID, owner_user_id: ownerUserID, archived_at: archivedAt } = requiredRow(workspace, "read Workspace owner");
      // A repeated delete re-queues a purge that previously failed so durable
      // data is never silently retained after the user asked for deletion.
      await archiveAndQueueWorkspacePurge(client, { appID, ownerUserID, archivedAt }, workspaceID, at);
    });
  }

  /** Trusted App integration: scope the canonical ID in the same locked query
   *  that archives it. Slug reuse cannot redirect a retry to a replacement. */
  async deleteWorkspaceForApp(appID: string, workspaceID: string): Promise<void> {
    assertResourceID("app", appID);
    assertResourceID("workspace", workspaceID);
    await withTransaction(this.database, async (client) => {
      const at = this.now();
      const workspace = await client.query<{ owner_user_id: string; archived_at: Date | null }>(`SELECT a.owner_user_id, w.archived_at
FROM cantelop_control.workspaces w
JOIN cantelop_control.apps a ON a.id = w.app_id
WHERE w.id = $1 AND w.app_id = $2 AND a.status <> 'archived'
FOR UPDATE OF w, a`, [workspaceID, appID]);
      if (workspace.rowCount !== 1) throw new ControlError("not_found");
      const { owner_user_id: ownerUserID, archived_at: archivedAt } = requiredRow(workspace, "read App Workspace owner");
      await archiveAndQueueWorkspacePurge(client, { appID, ownerUserID, archivedAt }, workspaceID, at);
    });
  }

  /** Operator deletion: no ownership check, and it also covers Workspaces of
   *  archived Apps. Re-queues a failed purge; a succeeded purge stays done. */
  async purgeWorkspaceAsOperator(workspaceID: string): Promise<void> {
    assertResourceID("workspace", workspaceID);
    await withTransaction(this.database, async (client) => {
      const at = this.now();
      const workspace = await client.query<{ app_id: string; owner_user_id: string; archived_at: Date | null }>(`SELECT w.app_id, a.owner_user_id, w.archived_at
FROM cantelop_control.workspaces w
JOIN cantelop_control.apps a ON a.id = w.app_id
WHERE w.id = $1
FOR UPDATE OF w, a`, [workspaceID]);
      if (workspace.rowCount !== 1) throw new ControlError("not_found");
      const { app_id: appID, owner_user_id: ownerUserID, archived_at: archivedAt } = requiredRow(workspace, "read Workspace owner");
      await archiveAndQueueWorkspacePurge(client, { appID, ownerUserID, archivedAt }, workspaceID, at);
    });
  }

  /** Records that the compute host no longer resolves this archived Workspace. */
  async markWorkspaceHostArchived(workspaceID: string): Promise<void> {
    assertResourceID("workspace", workspaceID);
    await this.database.query(`UPDATE cantelop_control.workspaces
SET host_archived_at = $2
WHERE id = $1 AND archived_at IS NOT NULL AND host_archived_at IS NULL`, [workspaceID, this.now()]);
  }

  async isWorkspaceHostArchived(workspaceID: string): Promise<boolean> {
    assertResourceID("workspace", workspaceID);
    const result = await this.database.query<{ host_archived: boolean }>(`SELECT host_archived_at IS NOT NULL AS host_archived
FROM cantelop_control.workspaces WHERE id = $1`, [workspaceID]);
    return result.rows[0]?.host_archived === true;
  }

  async workspacePurgeReconciliationStatus(): Promise<WorkspacePurgeReconciliationStatus> {
    const result = await this.database.query<Record<keyof WorkspacePurgeReconciliationStatus, number>>(`SELECT
  count(*)::int AS archived,
  count(*) FILTER (WHERE operation.id IS NULL)::int AS missing,
  count(*) FILTER (WHERE operation.kind = 'purge_workspace' AND operation.status = 'queued')::int AS queued,
  count(*) FILTER (WHERE operation.kind = 'purge_workspace' AND operation.status = 'running')::int AS running,
  count(*) FILTER (WHERE operation.kind = 'purge_workspace' AND operation.status = 'succeeded')::int AS succeeded,
  count(*) FILTER (WHERE operation.kind = 'purge_workspace' AND operation.status = 'failed')::int AS failed
FROM cantelop_control.workspaces workspace
JOIN cantelop_control.apps app ON app.id = workspace.app_id
LEFT JOIN cantelop_control.operations operation
  ON operation.owner_user_id = app.owner_user_id
 AND operation.idempotency_key = 'purge-workspace:' || workspace.id
WHERE workspace.archived_at IS NOT NULL`);
    return requiredRow(result, "read Workspace purge reconciliation status");
  }

  async queueArchivedWorkspacePurges(limit = 100): Promise<number> {
    if (!Number.isInteger(limit) || limit < 1 || limit > 500) throw new TypeError("invalid Workspace purge batch limit");
    return withTransaction(this.database, async (client) => {
      const candidates = await client.query<{ id: string; app_id: string; owner_user_id: string }>(`SELECT
  workspace.id, workspace.app_id, app.owner_user_id
FROM cantelop_control.workspaces workspace
JOIN cantelop_control.apps app ON app.id = workspace.app_id
LEFT JOIN cantelop_control.operations operation
  ON operation.owner_user_id = app.owner_user_id
 AND operation.idempotency_key = 'purge-workspace:' || workspace.id
WHERE workspace.archived_at IS NOT NULL
  AND (operation.id IS NULL OR (operation.kind = 'purge_workspace' AND operation.status = 'failed'))
ORDER BY workspace.archived_at, workspace.id
LIMIT $1
FOR UPDATE OF workspace SKIP LOCKED`, [limit]);
      let queued = 0;
      const at = this.now();
      for (const workspace of candidates.rows) {
        queued += await queueWorkspacePurge(client, workspace.owner_user_id, workspace.app_id, workspace.id, at);
      }
      return queued;
    });
  }

  async openWorkspaceForApp(rawInput: CreateWorkspaceInput): Promise<ProductWorkspace> {
    const input = validateCreateWorkspace(rawInput);
    return withTransaction(this.database, async (client) => {
      const owner = await client.query<{ owner_user_id: string }>(`SELECT owner_user_id
FROM cantelop_control.apps
WHERE id = $1 AND status <> 'archived'
FOR UPDATE`, [input.appID]);
      if (owner.rowCount !== 1) throw new ControlError("not_found");
      const ownerUserID = requiredRow(owner, "read App owner").owner_user_id;
      const existing = await client.query<WorkspaceRow>(`SELECT id, app_id, slug, created_at, updated_at, archived_at
FROM cantelop_control.workspaces
WHERE app_id = $1 AND slug = $2 AND archived_at IS NULL`, [input.appID, input.slug]);
      if (existing.rowCount === 1) return mapWorkspace(requiredRow(existing, "read App Workspace"));
      await this.assertWorkspaceAdmission(client, input.appID);
      const workspaceID = newResourceID("workspace");
      const at = this.now();
      const inserted = await insertWorkspaceAggregate(client, {
        workspaceID, appID: input.appID, slug: input.slug, ownerUserID, at,
      }, "skip");
      if (inserted.rowCount === 1) {
        return mapWorkspace(requiredRow(inserted, "create App Workspace"));
      }
      const raced = await client.query<WorkspaceRow>(`SELECT id, app_id, slug, created_at, updated_at, archived_at
FROM cantelop_control.workspaces
WHERE app_id = $1 AND slug = $2 AND archived_at IS NULL`, [input.appID, input.slug]);
      if (raced.rowCount !== 1) throw new ControlError("conflict");
      return mapWorkspace(requiredRow(raced, "read App Workspace"));
    });
  }

  private async assertWorkspaceAdmission(client: TransactionClient, appID: string): Promise<void> {
    const result = await client.query<{ active: number; pending: number; purging: number;managed_limit?:number|null }>(`SELECT
  (SELECT count(*)::int FROM cantelop_control.workspaces
   WHERE app_id = $1 AND archived_at IS NULL) AS active,
  (SELECT count(*)::int FROM cantelop_control.operations
   WHERE app_id = $1 AND kind IN ('register_workspace', 'purge_workspace')
     AND status IN ('queued', 'running')) AS pending,
  (SELECT count(*)::int FROM cantelop_control.operations
   WHERE app_id = $1 AND kind = 'purge_workspace'
     AND status IN ('queued', 'running', 'failed')) AS purging,
  (SELECT CASE WHEN (policy->>'active')::boolean AND (policy->>'expiresAt')::timestamptz>now() THEN (policy->>'maxWorkspaces')::int ELSE 0 END FROM cantelop_control.fleet_policies WHERE app_id=$1) AS managed_limit`, [appID]);
    const { active, pending, purging } = requiredRow(result, "read Workspace admission");
    // A queued purge may still occupy a compute registration. Do not admit a
    // replacement until that cleanup has completed.
    if (active + purging >= Math.min(maxWorkspacesPerApp,result.rows[0]?.managed_limit??maxWorkspacesPerApp) ||
        pending + active + 2 > maxWorkspaceOperationsAndPurgeReservePerApp) {
      throw new ControlError("quota_exceeded");
    }
  }

  async listAppsForUser(userID: string): Promise<ProductApp[]> {
    assertResourceID("user", userID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(userAppSelection)
      .from(apps)
      .innerJoin(appSessionRuntimes, eq(appSessionRuntimes.appId, apps.id))
      .innerJoin(owners, eq(owners.id, apps.ownerUserId))
      .leftJoin(appCollaborators, appAccessJoin(userID))
      .where(and(appAccessible(userID), ne(apps.status, "archived")))
      .orderBy(asc(apps.createdAt), asc(apps.id)));
    return result.rows.map((row) => ({ ...mapApp(row), accessRole: appAccessRole(row.owner_user_id, userID) }));
  }

  async getAppForUser(userID: string, appID: string): Promise<ProductApp> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(accessibleAppSelection)
      .from(apps)
      .innerJoin(appSessionRuntimes, eq(appSessionRuntimes.appId, apps.id))
      .innerJoin(users, eq(users.id, userID))
      .innerJoin(owners, eq(owners.id, apps.ownerUserId))
      .leftJoin(appCollaborators, appAccessJoin(userID))
      .where(and(eq(apps.id, appID), appAccessible(userID), ne(apps.status, "archived"))));
    return accessibleApp(result, userID, "read App");
  }

  async getAppBySlugForUser(userID: string, slug: string): Promise<ProductApp> {
    assertResourceID("user", userID);
    if (!appSlugPattern.test(slug)) throw new TypeError("invalid App slug");
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(accessibleAppSelection)
      .from(apps)
      .innerJoin(appSessionRuntimes, eq(appSessionRuntimes.appId, apps.id))
      .innerJoin(users, eq(users.id, userID))
      .innerJoin(owners, eq(owners.id, apps.ownerUserId))
      .leftJoin(appCollaborators, appAccessJoin(userID))
      .where(and(eq(apps.slug, slug), appAccessible(userID), ne(apps.status, "archived"))));
    return accessibleApp(result, userID, "read App by slug");
  }

  async getAppByID(appID: string): Promise<ProductApp> {
    assertResourceID("app", appID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(appSelection)
      .from(apps)
      .innerJoin(appSessionRuntimes, eq(appSessionRuntimes.appId, apps.id))
      .where(and(eq(apps.id, appID), ne(apps.status, "archived"))));
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return mapApp(requiredRow(result, "read App"));
  }

  async archiveAppForUser(userID: string, appID: string): Promise<void> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    await withTransaction(this.database, async (client) => {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`deployment:${appID}`]);
      const at = this.now();
      const app = await client.query<{ slug: string }>(`SELECT slug FROM cantelop_control.apps
WHERE id = $1 AND owner_user_id = $2 AND status <> 'archived' FOR UPDATE`, [appID, userID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'archive_app', 'queued', $4, $5::jsonb, $6, $6, $6)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING`, [
        newResourceID("operation"), userID, appID, `archive-app:${appID}`,
        JSON.stringify({ app_slug: app.rows[0].slug }), at,
      ]);
    });
  }

  async nextArchiveGeneration(): Promise<string> {
    return allocateReleaseGeneration(this.database);
  }

  async releaseScriptIDs(appID: string): Promise<string[]> {
    assertResourceID("app", appID);
    const result = await this.database.query<{ id: string }>(`SELECT id FROM cantelop_control.releases r
WHERE app_id = $1 AND NOT EXISTS (SELECT 1 FROM cantelop_control.release_components c
  JOIN cantelop_control.operations o ON o.id=c.operation_id
  WHERE c.release_id=r.id AND c.kind='api' AND o.kind='attach_package') ORDER BY id`, [appID]);
    return result.rows.map((row) => {
      assertResourceID("release", row.id);
      return row.id;
    });
  }

  async completeArchive(ownerUserID: string, appID: string): Promise<void> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    await withTransaction(this.database, async (client) => {
      const at = this.now();
      const app = await client.query<{ status: string }>(`SELECT status FROM cantelop_control.apps
WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`, [appID, ownerUserID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      if (app.rows[0].status === "archived") return;
      await client.query(`UPDATE cantelop_control.operations
SET status = 'failed', error_code = 'app_archived', locked_by = NULL, locked_at = NULL, updated_at = $2
WHERE app_id = $1 AND kind = 'activate_release' AND status IN ('queued', 'running')`, [appID, at]);
      await client.query(`UPDATE cantelop_control.apps
SET status = 'archived', updated_at = $3, archived_at = $3
WHERE id = $1 AND owner_user_id = $2`, [appID, ownerUserID, at]);
      await client.query(`UPDATE cantelop_control.workspaces
SET updated_at = $2, archived_at = $2
WHERE app_id = $1 AND archived_at IS NULL`, [appID, at]);
      // An archived App never reopens, so its Workspaces' filesystem state and
      // databases have no further use. Purge every one, retrying failures.
      await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
SELECT 'op_' || replace(gen_random_uuid()::text, '-', ''), $2, $1, 'purge_workspace', 'queued',
  'purge-workspace:' || w.id, jsonb_build_object('workspace_id', w.id), $3, $3, $3
FROM cantelop_control.workspaces w
WHERE w.app_id = $1 AND w.archived_at IS NOT NULL
${requeueFailedPurgeSQL}`, [appID, ownerUserID, at]);
    });
  }

  async listWorkspacesForUser(userID: string, appID?: string): Promise<ProductWorkspace[]> {
    assertResourceID("user", userID);
    if (appID) assertResourceID("app", appID);
    const db = controlQueryBuilder(this.database);
    // The same App visibility as listAppsForUser, so every listed Workspace
    // belongs to an App the user can list.
    const ownership = and(
      appAccessible(userID),
      ne(apps.status, "archived"),
      isNull(workspaces.archivedAt),
      appID ? eq(workspaces.appId, appID) : undefined,
    );
    const result = await executeDrizzle(this.database, db.select(workspaceSelection)
      .from(workspaces)
      .innerJoin(apps, eq(apps.id, workspaces.appId))
      .leftJoin(appCollaborators, appAccessJoin(userID))
      .where(ownership)
      .orderBy(asc(workspaces.createdAt), asc(workspaces.id)));
    return result.rows.map(mapWorkspace);
  }

  resolveAppOwnerForUser(userID: string, appID: string): Promise<string> {
    return resolveAppOwnerForUser(this.database, userID, appID);
  }

  async getAppAccessForUser(userID: string, appID: string): Promise<AppAccess> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    // One statement so the owner check and the member list share a snapshot.
    const result = await this.database.query<{
      owner_id: string; owner_email: string; actor_status: "active" | "suspended";
      user_id: string | null; email: string | null; role: "admin" | null; created_at: Date | null;
    }>(`SELECT u.id AS owner_id, u.email AS owner_email, actor.status AS actor_status,
  c.user_id, cu.email, c.role, c.created_at
FROM cantelop_control.apps a
JOIN cantelop_control.users u ON u.id = a.owner_user_id
JOIN cantelop_control.users actor ON actor.id = $2
LEFT JOIN cantelop_control.app_collaborators c ON c.app_id = a.id
LEFT JOIN cantelop_control.users cu ON cu.id = c.user_id
WHERE a.id = $1 AND a.status <> 'archived'
  AND ${appAccessibleSQL()}
ORDER BY c.created_at, c.user_id`, [appID, userID]);
    const first = result.rows[0];
    if (!first) throw new ControlError("not_found");
    if (first.actor_status !== "active") throw new ControlError("user_suspended");
    return {
      owner: { userID: first.owner_id, email: first.owner_email },
      collaborators: result.rows.flatMap((row) => row.user_id === null ? [] : [{
        userID: row.user_id, email: row.email!, role: row.role!, createdAt: row.created_at!,
      }]),
    };
  }

  async removeAppAdminForOwner(ownerUserID: string, appID: string, collaboratorUserID: string): Promise<void> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("user", collaboratorUserID);
    const result = await this.database.query(`DELETE FROM cantelop_control.app_collaborators c
USING cantelop_control.apps a
WHERE c.app_id = a.id AND a.id = $1 AND a.owner_user_id = $2
  AND a.status <> 'archived' AND c.user_id = $3`, [appID, ownerUserID, collaboratorUserID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
  }


}

// Resets a failed purge; a queued, running or succeeded purge is left alone.
const requeueFailedPurgeSQL = `ON CONFLICT (owner_user_id, idempotency_key) DO UPDATE
SET status = 'queued', error_code = NULL, attempts = 0, available_at = EXCLUDED.available_at, updated_at = EXCLUDED.updated_at
WHERE cantelop_control.operations.kind = 'purge_workspace'
  AND cantelop_control.operations.status = 'failed'`;

// Inserts a Workspace with its database record and queued registration in one
// statement. The dependent inserts select FROM inserted_workspace rather than
// binding the new id, so when "skip" turns a slug conflict into zero inserted
// rows, no database record or registration is written for the existing row.
async function insertWorkspaceAggregate(client: TransactionClient, input: {
  workspaceID: string; appID: string; slug: string; ownerUserID: string; at: Date;
}, onConflict: "error" | "skip"): Promise<QueryResult<WorkspaceRow>> {
  const conflictSQL = onConflict === "skip" ? "\n  ON CONFLICT (app_id, slug) WHERE archived_at IS NULL DO NOTHING" : "";
  return client.query<WorkspaceRow>(`WITH inserted_workspace AS (
  INSERT INTO cantelop_control.workspaces
    (id, app_id, slug, created_at, updated_at)
  VALUES ($1, $2, $3, $4, $4)${conflictSQL}
  RETURNING id, app_id, slug, created_at, updated_at, archived_at
), inserted_database AS (
  INSERT INTO cantelop_control.workspace_databases (workspace_id)
  SELECT id FROM inserted_workspace
), queued_registration AS (
  INSERT INTO cantelop_control.operations
    (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
  SELECT $5, $6, app_id, 'register_workspace', 'queued', 'register-workspace:' || id,
    jsonb_build_object('workspace_id', id, 'workspace_slug', slug), $4, $4, $4
  FROM inserted_workspace
)
SELECT id, app_id, slug, created_at, updated_at, archived_at FROM inserted_workspace`, [
    input.workspaceID, input.appID, input.slug, input.at, newResourceID("operation"), input.ownerUserID,
  ]);
}

/** Archives a locked Workspace row if needed and (re)queues its durable purge. */
async function archiveAndQueueWorkspacePurge(
  client: TransactionClient,
  workspace: { appID: string; ownerUserID: string; archivedAt: Date | null },
  workspaceID: string,
  at: Date,
): Promise<void> {
  if (workspace.archivedAt === null) {
    await client.query(`UPDATE cantelop_control.workspaces
SET updated_at = $2, archived_at = $2
WHERE id = $1 AND archived_at IS NULL`, [workspaceID, at]);
  }
  await queueWorkspacePurge(client, workspace.ownerUserID, workspace.appID, workspaceID, at);
}

async function queueWorkspacePurge(client: TransactionClient, ownerUserID: string, appID: string, workspaceID: string, at: Date): Promise<number> {
  const result = await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'purge_workspace', 'queued', $4, $5::jsonb, $6, $6, $6)
${requeueFailedPurgeSQL}`, [
    newResourceID("operation"), ownerUserID, appID, `purge-workspace:${workspaceID}`,
    JSON.stringify({ workspace_id: workspaceID }), at,
  ]);
  return result.rowCount ?? 0;
}

function requiredRow<Row>(result: QueryResult<Row>, operation: string): Row {
  if (result.rowCount !== 1 || result.rows.length !== 1) throw new Error(`${operation} returned an invalid row count`);
  return result.rows[0];
}

function mapUser(row: UserRow): ProductUser {
  return { id: row.id, email: row.email, status: row.status, createdAt: row.created_at, lastLoginAt: row.last_login_at };
}

function accessibleApp(result: QueryResult<AppRow & { actor_status: "active" | "suspended" }>, userID: string, operation: string): ProductApp {
  if (result.rowCount !== 1) throw new ControlError("not_found");
  const row = requiredRow(result, operation);
  if (row.actor_status !== "active") throw new ControlError("user_suspended");
  return { ...mapApp(row), accessRole: appAccessRole(row.owner_user_id, userID) };
}

function mapApp(row: AppRow): ProductApp {
  return {
    id: row.id, ownerUserID: row.owner_user_id, slug: row.slug, status: row.status,
    sessionRuntime: {
      internalPort: row.internal_port,
    },
    createdAt: row.created_at, updatedAt: row.updated_at,
    ...(row.owner_email !== undefined ? { ownerEmail: row.owner_email } : {}),
    ...(row.admin_count !== undefined ? { adminCount: row.admin_count } : {}),
  };
}

function mapWorkspace(row: WorkspaceRow): ProductWorkspace {
  return {
    id: row.id, appID: row.app_id, slug: row.slug,
    createdAt: row.created_at, updatedAt: row.updated_at,
    ...(row.archived_at ? { archivedAt: row.archived_at } : {}),
  };
}

function isUniqueViolation(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "23505";
}
