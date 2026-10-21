import { expect, test } from "bun:test";
import { ConfigCipher } from "../configuration/cipher";
import { WorkspaceDatabaseService } from "./service";
import { withWorkspaceDatabases } from "./compute-provider";
const workspace = "wsp_0123456789abcdef0123456789abcdef";
const app = "app_0123456789abcdef0123456789abcdef";
const now = new Date("2026-10-05T00:00:00Z");
const token = `e30.${Buffer.from(JSON.stringify({ exp: now.getTime()/1000 + 900 })).toString("base64url")}.signature`;
function fixture(schema, clientFactory, newerSchemas = []) {
  let row = { workspace_id: workspace, app_id: app, archived_at: null, app_status: "active", status: "pending", provider_name: null, url: null };
  let tail = Promise.resolve();
  let connections = 0;
  const pool = {
    async query(sql) {
      if (sql.includes("r.version >")) return { rows: newerSchemas.map(digest => ({ manifest: { database_schema: { digest } } })), rowCount: newerSchemas.length };
      if (sql.startsWith("SELECT coalesce(runtime.manifest,artifact.manifest)")) return { rows: schema ? [{ manifest: { database_schema: schema } }] : [], rowCount: schema ? 1 : 0 };
      if (sql.startsWith("SELECT provider_name")) return { rows: [{ provider_name: row.provider_name }], rowCount: 1 };
      if (sql.startsWith("SELECT d.*")) return { rows: [{ ...row }], rowCount: 1 };
      throw new Error("unexpected pool query");
    },
    async connect() {
    connections++;
    const before = tail; let release; tail = new Promise(r => release = r); await before;
    let working = { ...row };
    return {
      async query(sql, values = []) {
        if (sql === "COMMIT") row = working;
        if (sql.startsWith("SELECT d.*")) return { rows: [working], rowCount: 1 };
        if (sql.includes("SET provider_name")) working.provider_name = values[1];
        if (sql.includes("SET provider_id")) Object.assign(working, { provider_id: values[1], url: values[2], status: "ready" });
        if (sql.includes("SET token_nonce")) Object.assign(working, { token_nonce: values[1], token_ciphertext: values[2], token_expires_at: values[3] });
        if (sql.includes("SET status = 'deleting'")) Object.assign(working, { status: "deleting", token_nonce: null, token_ciphertext: null, token_expires_at: null });
        if (sql.includes("SET status = 'deleted'")) Object.assign(working, { status: "deleted", token_nonce: null, token_ciphertext: null, token_expires_at: null });
        return { rows: [], rowCount: 1 };
      }, release,
    };
  } };
  const calls = [];
  const provider = {
    async ensure(name) { calls.push(["ensure", name]); return { id: "provider-id", url: "libsql://test-cantelop.turso.io" }; },
    async token(name) { calls.push(["token", name]); return token; },
    async probe() { calls.push(["probe"]); },
    async delete(name) { calls.push(["delete", name]); },
  };
  const service = new WorkspaceDatabaseService(pool, provider, "production", new ConfigCipher(Buffer.alloc(32, 1)), () => now, undefined, clientFactory);
  return { service, calls, provider, get connections() { return connections; }, get row() { return row; } };
}
test("concurrent provisioning produces one ready database and encrypted credential cache", async () => {
  const f = fixture();
  await Promise.all([f.service.ensure(workspace), f.service.ensure(workspace)]);
  expect(f.calls.filter(c => c[0] === "ensure")).toHaveLength(1);
  expect(f.row.status).toBe("ready");
  expect(f.row.token_ciphertext.toString()).not.toContain(token);
  const credentials = await f.service.credentials(app, workspace);
  expect(credentials.authToken).toBe(token);
  expect(f.calls.filter(c => c[0] === "token")).toHaveLength(1);
});
test("failed readiness probe preserves reserved identity but never exposes credentials", async () => {
  const f = fixture(); f.provider.probe = async () => { throw new Error("unavailable"); };
  await expect(f.service.ensure(workspace)).rejects.toThrow("unavailable");
  expect(f.row.provider_name).toContain(workspace.slice(4));
  expect(f.row.status).toBe("pending");
  await expect(f.service.credentials(app, workspace)).rejects.toThrow("unavailable");
});
test("denies other Apps and archived Workspaces before minting; purge is repeatable", async () => {
  const f = fixture(); await f.service.ensure(workspace);
  await expect(f.service.credentials("app_other", workspace)).rejects.toMatchObject({ code: "workspace_not_found" });
  f.row.archived_at = now;
  await expect(f.service.credentials(app, workspace)).rejects.toMatchObject({ code: "workspace_not_found" });
  await f.service.purge(workspace); await f.service.purge(workspace);
  expect(f.calls.filter(c => c[0] === "delete")).toHaveLength(1);
  expect(f.row.status).toBe("deleted");
});
test("purge commits deleting before the provider call so failures stay visible and retry", async () => {
  const f = fixture(); await f.service.ensure(workspace);
  f.row.archived_at = now;
  f.provider.delete = async () => { throw new Error("provider unavailable"); };
  await expect(f.service.purge(workspace)).rejects.toThrow("provider unavailable");
  expect(f.row.status).toBe("deleting");
  expect(f.row.token_ciphertext).toBeNull();
  await expect(f.service.ensure(workspace)).rejects.toMatchObject({ code: "workspace_not_found" });
  f.provider.delete = async (name) => { f.calls.push(["delete", name]); };
  await f.service.purge(workspace);
  expect(f.row.status).toBe("deleted");
  expect(f.calls.filter(c => c[0] === "delete")).toHaveLength(1);
});
test("purge settles compute before database deletion", async () => {
  const calls = [];
  const compute = { async registerWorkspace() { calls.push("filesystem"); }, async purgeWorkspace() { calls.push("stop-and-purge-filesystem"); } };
  const provider = withWorkspaceDatabases(compute, { async purge() { calls.push("delete-database"); } });
  await provider.registerWorkspace({ workspaceID: workspace }); await provider.purgeWorkspace({ workspaceID: workspace });
  expect(calls).toEqual(["filesystem", "stop-and-purge-filesystem", "delete-database"]);
});

test("valid credentials authorize without a locking transaction; renewal rechecks under lock", async () => {
  const f = fixture();
  await f.service.ensure(workspace);
  const before = f.connections;
  await f.service.credentials(app, workspace);
  expect(f.connections).toBe(before);
  await expect(f.service.credentials("app_other", workspace)).rejects.toMatchObject({ code: "workspace_not_found" });
  expect(f.connections).toBe(before);
  f.row.token_expires_at = new Date(now.getTime() + 60_000);
  await f.service.credentials(app, workspace);
  expect(f.connections).toBe(before + 1);
  expect(f.calls.filter(c => c[0] === "token")).toHaveLength(2);
});

test("application credential path migrates declared schema with ordinary credentials and no mailbox installation", async () => {
  const { mkdtemp, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { createClient } = await import("@libsql/client");
  const { sqliteTable, text } = await import("drizzle-orm/sqlite-core");
  const { createApplicationSchema } = await import("./schema");
  const directory = await mkdtemp(join(tmpdir(), "cantelop-platform-schema-"));
  const factory = () => createClient({ url: `file:${join(directory, "workspace.sqlite")}` });
  try {
    const schema = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey() }) });
    const f = fixture(schema, factory);
    await f.service.ensure(workspace);
    const credentials = await f.service.applicationCredentials(app, workspace);
    expect(credentials.authToken).toBe(token);
    expect(f.calls.filter(call => call[0] === "token")).toHaveLength(1);
    const db = factory();
    try {
      await db.execute("INSERT INTO tasks VALUES ('app')");
      await db.execute("CREATE TABLE user_owned (id TEXT)");
      expect((await db.execute("SELECT name FROM sqlite_schema WHERE name LIKE 'cantelop_mailbox_%' OR name='cantelop_system_migrations'")).rows).toHaveLength(0);
    } finally { db.close(); }
    const status = await f.service.migrationStatus(app, workspace);
    expect(status.application[0].statements[0]).toContain("CREATE TABLE");
    expect(status.system).toBeUndefined();
    await f.service.applicationCredentials(app, workspace);
    expect((await f.service.migrationStatus(app, workspace)).application).toHaveLength(1);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test("migration status identifies a newer schema intentionally retained after rollback", async () => {
  const { mkdtemp, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { createClient } = await import("@libsql/client");
  const { sqliteTable, text } = await import("drizzle-orm/sqlite-core");
  const { createApplicationSchema } = await import("./schema");
  const directory = await mkdtemp(join(tmpdir(), "cantelop-rollback-schema-"));
  const factory = () => createClient({ url: `file:${join(directory, "workspace.sqlite")}` });
  try {
    const oldSchema = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey() }) });
    const newerSchema = await createApplicationSchema({ tasks: sqliteTable("tasks", { id: text().primaryKey(), title: text() }) });
    const newerDigests = [newerSchema.digest];
    const f = fixture(oldSchema, factory, newerDigests);
    await f.service.ensure(workspace);
    await f.service.applicationCredentials(app, workspace);
    const db = factory();
    try { await db.execute({ sql: "UPDATE cantelop_application_migrations SET digest = ?", args: [newerSchema.digest] }); }
    finally { db.close(); }
    expect((await f.service.migrationStatus(app, workspace)).retained_newer_schema).toBe(true);
    newerDigests.pop();
    expect((await f.service.migrationStatus(app, workspace)).retained_newer_schema).toBe(false);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test("releases without a declared schema preserve runtime-owned database access", async () => {
  const f = fixture(undefined, () => { throw new Error("unexpected schema database access"); });
  await f.service.ensure(workspace);
  expect((await f.service.applicationCredentials(app, workspace)).authToken).toBe(token);
});


test("Workspace registration never provisions a database; first use does", async () => {
  const f=fixture();
  const calls=[];
  await withWorkspaceDatabases({async registerWorkspace(command){calls.push(command.workspaceID);}},f.service).registerWorkspace({appID:app,workspaceID:workspace});
  expect(calls).toEqual([workspace]);
  expect(f.calls.some(c=>c[0]==="ensure")).toBe(false);
});

test("first database access provisions once and concurrent callers share the credential cache",async () => {
  const f=fixture();
  const results=await Promise.all([f.service.credentials(app,workspace),f.service.credentials(app,workspace)]);
  expect(results[0]).toEqual(results[1]);
  expect(f.calls.filter(c=>c[0]==="ensure")).toHaveLength(1);
  expect(f.calls.filter(c=>c[0]==="probe")).toHaveLength(1);
});

test("first access cannot provision a foreign or archived Workspace",async () => {
  const f=fixture();
  await expect(f.service.credentials("app_other",workspace)).rejects.toMatchObject({code:"workspace_not_found"});
  f.row.archived_at=now;
  await expect(f.service.credentials(app,workspace)).rejects.toMatchObject({code:"workspace_not_found"});
  expect(f.calls).toHaveLength(0);
});
