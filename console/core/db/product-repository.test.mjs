import { expect, test } from "bun:test";

import { ControlError } from "../domain/errors";
import { ProductRepository } from "./product-repository";

const userID = "usr_0123456789abcdef0123456789abcdef";
const collaboratorID = "usr_fedcba9876543210fedcba9876543210";

function fakePool(handler) {
  const statements = [];
  let released = false;
  const client = {
    async query(sql, values = []) {
      statements.push({ sql, values });
      if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql)) return { rows: [], rowCount: null };
      if (sql.startsWith("INSERT INTO cantelop_control.workspace_databases")) return { rows: [], rowCount: 1 };
      return handler(sql, values);
    },
    release() { released = true; },
  };
  return {
    statements,
    get released() { return released; },
    async connect() { return client; },
    query: client.query,
  };
}

// Registration key and payload are derived from the inserted row in SQL, so
// assert the statement ties every dependent write to inserted_workspace.
function expectWorkspaceAggregate(pool, { workspaceID, ownerUserID }) {
  const statement = pool.statements.find(({ sql }) => sql.startsWith("WITH inserted_workspace AS"));
  expect(statement.values[0]).toBe(workspaceID);
  expect(statement.values[5]).toBe(ownerUserID);
  expect(statement.sql).toContain("INSERT INTO cantelop_control.workspace_databases (workspace_id)\n  SELECT id FROM inserted_workspace");
  expect(statement.sql).toContain("'register-workspace:' || id");
  expect(statement.sql).toContain("jsonb_build_object('workspace_id', id, 'workspace_slug', slug)");
}

test("creates a user and default quota for the first verified identity", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT pg_advisory")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT users.id")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.users")) return {
      rowCount: 1,
      rows: [{ id: userID, email: "alice@example.com", status: "active", created_at: at, last_login_at: at }],
    };
    if (sql.startsWith("INSERT INTO cantelop_control.user_identities")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.user_quotas")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  expect(await repository.resolveIdentity({ provider: "github", providerSubject: "123", email: "alice@example.com" })).toEqual({
    id: userID, email: "alice@example.com", status: "active", createdAt: at, lastLoginAt: at,
  });
  expect(pool.statements.map(({ sql }) => sql)).toEqual(expect.arrayContaining(["BEGIN", "COMMIT"]));
  expect(pool.released).toBe(true);
});

test("links another provider with the same normalized email to the existing user", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT pg_advisory")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT users.id")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.users")) {
      expect(values[1]).toBe("alice@example.com");
      return { rowCount: 1, rows: [{ id: userID, email: "alice@example.com", status: "active", created_at: at, last_login_at: at }] };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.user_identities")) {
      expect(values.slice(0, 4)).toEqual([userID, "google", "google-123", "alice@example.com"]);
      return { rowCount: 1, rows: [] };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.user_quotas")) return { rowCount: 0, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  const user = await repository.resolveIdentity({ provider: "google", providerSubject: "google-123", email: "Alice@Example.com" });
  expect(user.id).toBe(userID);
});

test("keeps an established provider identity linked when its provider email changes", async () => {
  const createdAt = new Date("2026-08-01T12:00:00Z");
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT pg_advisory")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT users.id")) return {
      rowCount: 1, rows: [{ id: userID, email: "alice@example.com", status: "active", created_at: createdAt, last_login_at: createdAt }],
    };
    if (sql.startsWith("UPDATE cantelop_control.user_identities")) {
      expect(values[2]).toBe("new-address@example.com");
      return { rowCount: 1, rows: [] };
    }
    if (sql.startsWith("UPDATE cantelop_control.users")) {
      expect(values).toEqual([userID, at, "new-address@example.com"]);
      expect(sql).toContain("other.email = $3 AND other.id <> users.id");
      return {
        rowCount: 1, rows: [{ id: userID, email: "new-address@example.com", status: "active", created_at: createdAt, last_login_at: at }],
      };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.user_quotas")) return { rowCount: 0, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  expect(await repository.resolveIdentity({ provider: "github", providerSubject: "123", email: "new-address@example.com" }))
    .toMatchObject({ id: userID, email: "new-address@example.com", lastLoginAt: at });
});

test("creates the App aggregate and queues compute registration atomically", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT u.status")) return {
      rowCount: 1,
      rows: [{ status: "active", max_apps: 5 }],
    };
    if (sql.startsWith("SELECT count")) return {
      rowCount: 1, rows: [{ apps: "0" }],
    };
    if (sql.startsWith("INSERT INTO")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  const created = await repository.createAppForUser(userID, {
    slug: "agent",
    sessionRuntime: { internalPort: 3000 },
  });
  expect(created).toMatchObject({ ownerUserID: userID, slug: "agent", status: "provisioning" });
  expect(pool.statements.filter(({ sql }) => sql.startsWith("INSERT INTO cantelop_control"))).toHaveLength(3);
  const registration = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(registration.values[3]).toBe(`register-app:${created.id}`);
  expect(JSON.parse(registration.values[4])).toEqual({
    app_slug: "agent",
    internal_port: 3000,
  });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("rolls back aggregate creation when quota admission fails", async () => {
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT u.status")) return {
      rowCount: 1,
      rows: [{ status: "active", max_apps: 1 }],
    };
    if (sql.startsWith("SELECT count")) return {
      rowCount: 1, rows: [{ apps: "1" }],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool);
  await expect(repository.createAppForUser(userID, {
    slug: "agent",
    sessionRuntime: { internalPort: 3000 },
  })).rejects.toEqual(new ControlError("quota_exceeded"));
  expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
  expect(pool.released).toBe(true);
});

test("reads Apps through their owner boundary", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    expect(values).toEqual([userID, userID, appID, userID, "admin", "archived"]);
    if (sql.startsWith("select ")) return {
      rowCount: 1,
      rows: [{
        id: appID, owner_user_id: userID, slug: "agent", status: "active",
        internal_port: 3000,
        created_at: at, updated_at: at, actor_status: "active",
      }],
    };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool);
  expect(await repository.getAppForUser(userID, appID)).toMatchObject({ id: appID, ownerUserID: userID, slug: "agent", accessRole: "owner" });
  expect(pool.statements[0].sql).toContain('inner join "cantelop_control"."app_session_runtimes"');
  expect(pool.statements[0].sql).toContain('left join "cantelop_control"."app_collaborators"');
});

test("reads an App an admin collaborator was granted, as admin", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    expect(values).toEqual([collaboratorID, collaboratorID, appID, collaboratorID, "admin", "archived"]);
    return {
      rowCount: 1,
      rows: [{
        id: appID, owner_user_id: userID, slug: "agent", status: "active",
        internal_port: 3000, created_at: at, updated_at: at, actor_status: "active",
        owner_email: "alice@example.com", admin_count: 1,
      }],
    };
  });
  const repository = new ProductRepository(pool);
  expect(await repository.getAppForUser(collaboratorID, appID)).toMatchObject({
    ownerUserID: userID, accessRole: "admin", ownerEmail: "alice@example.com", adminCount: 1,
  });
  // The owner joins under its own alias — `users` is the actor — and the
  // admin count is a correlated subquery, so the access join stays one row.
  expect(pool.statements[0].sql).toContain('inner join "cantelop_control"."users" "owner"');
  expect(pool.statements[0].sql).toContain('(select count(*)::int from cantelop_control.app_collaborators admins where admins.app_id = "cantelop_control"."apps"."id"');
});

test("lists Apps with their owner and admin count", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool(() => ({
    rowCount: 1,
    rows: [{
      id: appID, owner_user_id: userID, slug: "agent", status: "active",
      internal_port: 3000, created_at: at, updated_at: at,
      owner_email: "alice@example.com", admin_count: 0, fleet_id: "flt_0123456789abcdef0123456789abcdef",
    }],
  }));
  const repository = new ProductRepository(pool);
  expect(await repository.listAppsForUser(userID)).toEqual([{
    id: appID, ownerUserID: userID, slug: "agent", status: "active",
    sessionRuntime: { internalPort: 3000 }, createdAt: at, updatedAt: at,
    ownerEmail: "alice@example.com", adminCount: 0, fleetID: "flt_0123456789abcdef0123456789abcdef", accessRole: "owner",
  }]);
  // An installation App names its Fleet through a correlated subquery.
  expect(pool.statements[0].sql).toContain('(select installs.fleet_id from cantelop_control.fleet_installations installs where installs.app_id = "cantelop_control"."apps"."id")');
  expect(pool.statements[0].sql).toContain('inner join "cantelop_control"."users" "owner"');
});

test("lists only unarchived Workspaces of accessible, unarchived Apps in one read", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const pool = fakePool(() => ({
    rowCount: 1,
    rows: [{ id: workspaceID, app_id: appID, slug: "production", created_at: at, updated_at: at, archived_at: null }],
  }));
  const repository = new ProductRepository(pool);
  expect(await repository.listWorkspacesForUser(userID, appID)).toEqual([{
    id: workspaceID, appID, slug: "production", createdAt: at, updatedAt: at,
  }]);
  expect(pool.statements).toHaveLength(1);
  const [{ sql, values }] = pool.statements;
  expect(sql).toContain('"cantelop_control"."apps"."status" <> $');
  expect(sql).toContain('"cantelop_control"."workspaces"."archived_at" is null');
  expect(values).toEqual(expect.arrayContaining([userID, "archived", appID]));
});

test("refuses App reads for a suspended actor", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool(() => ({
    rowCount: 1,
    rows: [{
      id: appID, owner_user_id: userID, slug: "agent", status: "active",
      internal_port: 3000, created_at: at, updated_at: at, actor_status: "suspended",
    }],
  }));
  const repository = new ProductRepository(pool);
  await expect(repository.getAppForUser(collaboratorID, appID)).rejects.toEqual(new ControlError("user_suspended"));
});

test("resolves an owned App directly by its indexed slug", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    expect(values).toEqual([userID, userID, "agent", userID, "admin", "archived"]);
    expect(sql).toContain('"cantelop_control"."apps"."slug" = $3');
    return {
      rowCount: 1,
      rows: [{
        id: appID, owner_user_id: userID, slug: "agent", status: "active",
        internal_port: 3000, created_at: at, updated_at: at, actor_status: "active",
      }],
    };
  });
  const repository = new ProductRepository(pool);
  expect(await repository.getAppBySlugForUser(userID, "agent")).toMatchObject({ id: appID, slug: "agent" });
  expect(pool.statements).toHaveLength(1);
});

test("queues an owned App archive without marking it archived before gate closure", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT pg_advisory_xact_lock")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT slug FROM cantelop_control.apps")) return { rowCount: 1, rows: [{ slug: "agent" }] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  await repository.archiveAppForUser(userID, appID);
  expect(pool.statements.filter(({ sql }) => sql.startsWith("UPDATE cantelop_control"))).toHaveLength(0);
  const archive = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(archive.values[3]).toBe(`archive-app:${appID}`);
  expect(JSON.parse(archive.values[4])).toEqual({ app_slug: "agent" });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("marks an App archived only when the worker completes closure", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT status FROM cantelop_control.apps")) return { rowCount: 1, rows: [{ status: "active" }] };
    if (sql.startsWith("UPDATE cantelop_control")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 2, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  await repository.completeArchive(userID, appID);
  const updates = pool.statements.filter(({ sql }) => sql.startsWith("UPDATE cantelop_control"));
  expect(updates.map(({ sql }) => sql.split("\n")[0])).toEqual([
    "UPDATE cantelop_control.operations", "UPDATE cantelop_control.apps", "UPDATE cantelop_control.workspaces",
  ]);
  expect(updates[1].sql).toContain("status = 'archived'");
  // Every archived Workspace of the App gets a purge, which deletes its database.
  const purge = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(pool.statements.indexOf(purge)).toBeGreaterThan(pool.statements.indexOf(updates[2]));
  expect(purge.sql).toContain("'purge_workspace'");
  expect(purge.sql).toContain("'purge-workspace:' || w.id");
  expect(purge.sql).toContain("w.app_id = $1 AND w.archived_at IS NOT NULL");
  expect(purge.sql).toContain("cantelop_control.operations.status = 'failed'");
  expect(purge.values).toEqual([appID, userID, at]);
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("operator purge archives any Workspace, including an archived App's, and queues cleanup", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT w.app_id")) {
      expect(sql).not.toContain("status <> 'archived'");
      expect(sql).not.toContain("owner_user_id = $2");
      expect(values).toEqual([workspaceID]);
      return { rowCount: 1, rows: [{ app_id: appID, owner_user_id: userID, archived_at: null }] };
    }
    if (sql.startsWith("UPDATE cantelop_control.workspaces") || sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  await repository.purgeWorkspaceAsOperator(workspaceID);
  expect(pool.statements.some(({ sql }) => sql.startsWith("UPDATE cantelop_control.workspaces"))).toBe(true);
  const purge = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(purge.values.slice(1, 5)).toEqual([userID, appID, `purge-workspace:${workspaceID}`, JSON.stringify({ workspace_id: workspaceID })]);
  expect(purge.sql).toContain("cantelop_control.operations.status = 'failed'");
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("operator purge of an unknown Workspace is not found", async () => {
  const pool = fakePool(() => ({ rowCount: 0, rows: [] }));
  await expect(new ProductRepository(pool).purgeWorkspaceAsOperator("wsp_0123456789abcdef0123456789abcdef")).rejects.toMatchObject({ code: "not_found" });
  expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
});

test("creates a Workspace instantly and queues provider projection atomically", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT a.owner_user_id")) return { rowCount: 1, rows: [{ owner_user_id: userID }] };
    if (sql.startsWith("SELECT\n  (SELECT count")) return { rowCount: 1, rows: [{ active: 0, pending: 0, purging: 0 }] };
    if (sql.startsWith("WITH inserted_workspace AS")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  const workspace = await repository.createWorkspaceForUser(userID, { appID, slug: "staging" });
  expectWorkspaceAggregate(pool, { workspaceID: workspace.id, ownerUserID: userID });
  expect(pool.statements.find(({ sql }) => sql.startsWith("WITH inserted_workspace AS")).sql).not.toContain("ON CONFLICT");
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("deletes an accessible Workspace and queues permanent provider cleanup atomically", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT w.app_id")) return { rowCount: 1, rows: [{ app_id: appID, owner_user_id: userID, archived_at: null }] };
    if (sql.startsWith("UPDATE cantelop_control.workspaces")) return { rowCount: 1, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  await repository.deleteWorkspaceForUser(userID, workspaceID);
  const purge = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(purge.sql).toContain("'purge_workspace'");
  expect(purge.values[3]).toBe(`purge-workspace:${workspaceID}`);
  expect(JSON.parse(purge.values[4])).toEqual({ workspace_id: workspaceID });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("App integration deletion locks and scopes the canonical Workspace and requeues archived purges", async () => {
  const appID = "app_0123456789abcdef0123456789abcdef", workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  for (const archived of [null, new Date("2026-10-19T00:00:00Z")]) {
    const pool = fakePool((sql, values) => {
      if (sql.startsWith("SELECT a.owner_user_id, w.archived_at")) {
        expect(sql).toContain("w.id = $1 AND w.app_id = $2 AND a.status <> 'archived'");
        expect(sql).toContain("FOR UPDATE OF w, a");
        expect(values).toEqual([workspaceID, appID]);
        return { rowCount: 1, rows: [{ owner_user_id: userID, archived_at: archived }] };
      }
      if (sql.startsWith("UPDATE cantelop_control.workspaces") || sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
      throw new Error(`unexpected query ${sql}`);
    });
    await new ProductRepository(pool).deleteWorkspaceForApp(appID, workspaceID);
    expect(pool.statements.some(({ sql }) => sql.startsWith("UPDATE cantelop_control.workspaces"))).toBe(archived === null);
    const purge = pool.statements.find(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
    expect(purge.values.slice(1, 4)).toEqual([userID, appID, `purge-workspace:${workspaceID}`]);
    expect(pool.statements.at(-1).sql).toBe("COMMIT");
  }
  const foreign = fakePool(() => ({ rowCount: 0, rows: [] }));
  await expect(new ProductRepository(foreign).deleteWorkspaceForApp(appID, workspaceID)).rejects.toMatchObject({ code: "not_found" });
  expect(foreign.statements.at(-1).sql).toBe("ROLLBACK");
});

test("records and reads compute host archival for archived Workspaces only", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  let hostArchived = false;
  const pool = fakePool((sql) => {
    if (sql.startsWith("UPDATE cantelop_control.workspaces")) {
      expect(sql).toContain("archived_at IS NOT NULL AND host_archived_at IS NULL");
      hostArchived = true;
      return { rowCount: 1, rows: [] };
    }
    if (sql.startsWith("SELECT host_archived_at IS NOT NULL")) return { rowCount: 1, rows: [{ host_archived: hostArchived }] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  expect(await repository.isWorkspaceHostArchived(workspaceID)).toBe(false);
  await repository.markWorkspaceHostArchived(workspaceID);
  expect(pool.statements.find(({ sql }) => sql.startsWith("UPDATE")).values).toEqual([workspaceID, at]);
  expect(await repository.isWorkspaceHostArchived(workspaceID)).toBe(true);
});

test("reports archived Workspace purge reconciliation status", async () => {
  const pool = fakePool((sql) => {
    expect(sql).toContain("workspace.archived_at IS NOT NULL");
    return { rowCount: 1, rows: [{ archived: 7, missing: 3, queued: 1, running: 0, succeeded: 2, failed: 1 }] };
  });
  const repository = new ProductRepository(pool);
  expect(await repository.workspacePurgeReconciliationStatus()).toEqual({
    archived: 7, missing: 3, queued: 1, running: 0, succeeded: 2, failed: 1,
  });
});

test("queues missing and failed archived Workspace purges in a bounded transaction", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const firstWorkspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const secondWorkspaceID = "wsp_fedcba9876543210fedcba9876543210";
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT\n  workspace.id")) {
      expect(values).toEqual([100]);
      expect(sql).toContain("operation.id IS NULL OR (operation.kind = 'purge_workspace' AND operation.status = 'failed')");
      expect(sql).toContain("FOR UPDATE OF workspace SKIP LOCKED");
      return { rowCount: 2, rows: [
        { id: firstWorkspaceID, app_id: appID, owner_user_id: userID },
        { id: secondWorkspaceID, app_id: appID, owner_user_id: userID },
      ] };
    }
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  expect(await repository.queueArchivedWorkspacePurges()).toBe(2);
  const inserts = pool.statements.filter(({ sql }) => sql.startsWith("INSERT INTO cantelop_control.operations"));
  expect(inserts.map(({ values }) => values[3])).toEqual([
    `purge-workspace:${firstWorkspaceID}`,
    `purge-workspace:${secondWorkspaceID}`,
  ]);
  expect(inserts.map(({ values }) => JSON.parse(values[4]))).toEqual([
    { workspace_id: firstWorkspaceID },
    { workspace_id: secondWorkspaceID },
  ]);
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("removes an admin only from an App the actor owns", async () => {
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    expect(sql).toContain("a.owner_user_id = $2");
    expect(sql).toContain("c.user_id = $3");
    expect(values).toEqual([appID, userID, collaboratorID]);
    return { rowCount: 1, rows: [] };
  });
  const repository = new ProductRepository(pool);
  await expect(repository.removeAppAdminForOwner(userID, appID, collaboratorID)).resolves.toBeUndefined();
  const denied = new ProductRepository(fakePool(() => ({ rowCount: 0, rows: [] })));
  await expect(denied.removeAppAdminForOwner(collaboratorID, appID, userID)).rejects.toEqual(new ControlError("not_found"));
});

test("lists an App's owner and admins in one read for any member", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    expect(values).toEqual([appID, collaboratorID]);
    return { rowCount: 1, rows: [{
      owner_id: userID, owner_email: "alice@example.com", actor_status: "active",
      user_id: collaboratorID, email: "bob@example.com", role: "admin", created_at: at,
    }] };
  });
  const repository = new ProductRepository(pool);
  expect(await repository.getAppAccessForUser(collaboratorID, appID)).toEqual({
    owner: { userID, email: "alice@example.com" },
    collaborators: [{ userID: collaboratorID, email: "bob@example.com", role: "admin", createdAt: at }],
  });
  expect(pool.statements).toHaveLength(1);

  const withoutAdmins = new ProductRepository(fakePool(() => ({ rowCount: 1, rows: [{
    owner_id: userID, owner_email: "alice@example.com", actor_status: "active",
    user_id: null, email: null, role: null, created_at: null,
  }] })));
  expect((await withoutAdmins.getAppAccessForUser(userID, appID)).collaborators).toEqual([]);

  const suspended = new ProductRepository(fakePool(() => ({ rowCount: 1, rows: [{
    owner_id: userID, owner_email: "alice@example.com", actor_status: "suspended",
    user_id: null, email: null, role: null, created_at: null,
  }] })));
  await expect(suspended.getAppAccessForUser(collaboratorID, appID)).rejects.toEqual(new ControlError("user_suspended"));

  const outsider = new ProductRepository(fakePool(() => ({ rowCount: 0, rows: [] })));
  await expect(outsider.getAppAccessForUser(collaboratorID, appID)).rejects.toEqual(new ControlError("not_found"));
});

test("creates a Workspace for a trusted App identity", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT owner_user_id")) {
      return { rowCount: 1, rows: [{ owner_user_id: userID }] };
    }
    if (sql.startsWith("SELECT\n  (SELECT count")) return { rowCount: 1, rows: [{ active: 0, pending: 0, purging: 0 }] };
    if (sql.startsWith("WITH inserted_workspace AS")) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  const workspace = await repository.createWorkspaceForApp({ appID, slug: "preview" });
  expectWorkspaceAggregate(pool, { workspaceID: workspace.id, ownerUserID: userID });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("opens a missing Workspace once and queues its provider projection", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT owner_user_id")) {
      return { rowCount: 1, rows: [{ owner_user_id: userID }] };
    }
    if (sql.startsWith("SELECT id, app_id")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT\n  (SELECT count")) return { rowCount: 1, rows: [{ active: 0, pending: 0, purging: 0 }] };
    if (sql.startsWith("WITH inserted_workspace AS")) {
      return { rowCount: 1, rows: [{
        id: values[0], app_id: appID, slug: "preview",
        created_at: at, updated_at: at, archived_at: null,
      }] };
    }
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  const workspace = await repository.openWorkspaceForApp({ appID, slug: "preview" });
  expect(workspace).toMatchObject({ appID, slug: "preview" });
  expectWorkspaceAggregate(pool, { workspaceID: workspace.id, ownerUserID: userID });
  expect(pool.statements).toHaveLength(6);
});

test("opening an existing Workspace does not queue another projection", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT owner_user_id")) return { rowCount: 1, rows: [{ owner_user_id: userID }] };
    if (sql.startsWith("SELECT id, app_id")) return { rowCount: 1, rows: [{
      id: workspaceID, app_id: appID, slug: "preview",
      created_at: at, updated_at: at, archived_at: null,
    }] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool, () => at);
  expect(await repository.openWorkspaceForApp({ appID, slug: "preview" })).toMatchObject({ id: workspaceID });
  expect(pool.statements.some(({ sql }) => sql.startsWith("WITH inserted_workspace AS"))).toBe(false);
});

test("Workspace creation rejects host capacity and reserves queue room for purges", async () => {
  const appID = "app_0123456789abcdef0123456789abcdef";
  for (const [active, pending, purging] of [[25, 0, 0], [0, 49, 0], [24, 25, 0], [24, 1, 1]]) {
    const pool = fakePool((sql) => {
      if (sql.startsWith("SELECT a.owner_user_id") || sql.startsWith("SELECT owner_user_id")) {
        return { rowCount: 1, rows: [{ owner_user_id: userID }] };
      }
      if (sql.startsWith("SELECT id, app_id")) return { rowCount: 0, rows: [] };
      if (sql.startsWith("SELECT\n  (SELECT count")) return { rowCount: 1, rows: [{ active, pending, purging }] };
      throw new Error(`unexpected query ${sql}`);
    });
    const repository = new ProductRepository(pool);
    for (const create of [
      () => repository.createWorkspaceForUser(userID, { appID, slug: "extra" }),
      () => repository.createWorkspaceForApp({ appID, slug: "extra" }),
      () => repository.openWorkspaceForApp({ appID, slug: "extra" }),
    ]) {
      await expect(create()).rejects.toEqual(new ControlError("quota_exceeded"));
    }
    expect(pool.statements.some(({ sql }) => sql.startsWith("INSERT INTO"))).toBe(false);
    expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
  }
});

test("opening an existing Workspace remains idempotent at capacity", async () => {
  const at = new Date("2026-08-12T12:00:00Z");
  const appID = "app_0123456789abcdef0123456789abcdef";
  const workspaceID = "wsp_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT owner_user_id")) return { rowCount: 1, rows: [{ owner_user_id: userID }] };
    if (sql.startsWith("SELECT id, app_id")) return { rowCount: 1, rows: [{
      id: workspaceID, app_id: appID, slug: "preview", created_at: at, updated_at: at, archived_at: null,
    }] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool);
  expect(await repository.openWorkspaceForApp({ appID, slug: "preview" })).toMatchObject({ id: workspaceID });
  expect(pool.statements.some(({ sql }) => sql.startsWith("SELECT\n  (SELECT count"))).toBe(false);
});

test("does not reveal an App that is not owned by the user", async () => {
  const appID = "app_0123456789abcdef0123456789abcdef";
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT pg_advisory_xact_lock")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT slug FROM cantelop_control.apps")) return { rowCount: 0, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ProductRepository(pool);
  await expect(repository.archiveAppForUser(userID, appID)).rejects.toEqual(new ControlError("not_found"));
  expect(pool.statements.at(-1).sql).toBe("ROLLBACK");
});

for (const maxApps of [1, 9]) {
  test(`reads the stored app quota (${maxApps}) instead of the default`, async () => {
    const pool = fakePool((sql, values) => {
      expect(values).toEqual([userID]);
      expect(sql).toContain("q.user_id = $1");
      expect(sql).toContain("u.status = 'active'");
      return { rowCount: 1, rows: [{ max_apps: maxApps, used_apps: 1 }] };
    });
    expect(await new ProductRepository(pool).getUserQuota(userID)).toEqual({ maxApps, usedApps: 1 });
  });
}

test("does not substitute a default for an unavailable account quota", async () => {
  const repository = new ProductRepository(fakePool(() => ({ rowCount: 0, rows: [] })));
  await expect(repository.getUserQuota(userID)).rejects.toEqual(new ControlError("not_found"));
});
