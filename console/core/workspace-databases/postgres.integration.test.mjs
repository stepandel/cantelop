import { test, expect } from "bun:test";
import { randomUUID } from "node:crypto";
import pg from "pg";
import { controlDatabaseConfig } from "../db/database";
import { ProductRepository } from "../db/product-repository";
import { ConfigCipher } from "../configuration/cipher";
import { WorkspaceDatabaseService } from "./service";
const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const integrationTest = databaseURL ? test : test.skip;
integrationTest("PostgreSQL reservations, concurrent provisioning, renewal, isolation and purge", async () => {
 const pool = new pg.Pool({ ...controlDatabaseConfig({ CANTELOP_DATABASE_URL:databaseURL, CANTELOP_DATABASE_SSL:process.env.CANTELOP_DATABASE_SSL ?? "verify-full" }),max:5 });
 try {
  const products = ProductRepository.fromPool(pool);
  const suffix = randomUUID().replaceAll("-","").slice(0,12);
  const user = await products.resolveIdentity({provider:"github",providerSubject:`turso-${suffix}`,email:`turso-${suffix}@example.com`});
  const app = await products.createAppForUser(user.id,{slug:`turso-${suffix}`,sessionRuntime:{internalPort:3000}});
  const workspace = await products.createWorkspaceForUser(user.id,{appID:app.id,slug:"default"});
  const {ComputeRegistrationRepository}=await import("../db/compute-registration-repository");
  const registrations=new ComputeRegistrationRepository(pool);
  expect(await registrations.workspaceRegistrationContext(app.id,workspace.id)).toEqual({archived:false,computeTenantID:user.id});
  expect(await registrations.workspaceRegistrationContext("app_ffffffffffffffffffffffffffffffff",workspace.id)).toBeNull();
  const reserved = await pool.query("SELECT status FROM cantelop_control.workspace_databases WHERE workspace_id=$1",[workspace.id]);
  expect(reserved.rows).toEqual([{status:"pending"}]);
  let creates=0, tokens=0, deletes=0;
  const provider = {
   async ensure() { creates++; await new Promise(r=>setTimeout(r,20)); return {id:"provider-id",url:"libsql://test-cantelop.turso.io"}; },
   async token(){ tokens++; return `e30.${Buffer.from(JSON.stringify({exp:Math.floor(Date.now()/1000)+900,nonce:tokens})).toString("base64url")}.signature`; },
   async probe(){}, async delete(){ deletes++; },
  };
  const service = new WorkspaceDatabaseService(pool,provider,"test",new ConfigCipher(Buffer.alloc(32,1)));
  await Promise.all([service.credentials(app.id,workspace.id),service.credentials(app.id,workspace.id)]);
  expect(creates).toBe(1); expect(tokens).toBe(1);
  const first=await service.credentials(app.id,workspace.id);
  await expect(service.credentials("app_ffffffffffffffffffffffffffffffff",workspace.id)).rejects.toMatchObject({code:"workspace_not_found"});
  await pool.query("UPDATE cantelop_control.workspace_databases SET token_expires_at=now()+interval '30 seconds' WHERE workspace_id=$1",[workspace.id]);
  const renewed=await service.credentials(app.id,workspace.id);
  expect(renewed.authToken).not.toBe(first.authToken); expect(tokens).toBe(2);
  const stored=await pool.query("SELECT token_ciphertext FROM cantelop_control.workspace_databases WHERE workspace_id=$1",[workspace.id]);
  expect(stored.rows[0].token_ciphertext.toString()).not.toContain(renewed.authToken);
  await products.deleteWorkspaceForUser(user.id,workspace.id);
  await expect(service.credentials(app.id,workspace.id)).rejects.toMatchObject({code:"workspace_not_found"});
  await service.purge(workspace.id); await service.purge(workspace.id); expect(deletes).toBe(1);
 } finally { await pool.end(); }
});
integrationTest("PostgreSQL console states, operator purge, App archive purges and drift", async () => {
 const { listAdminWorkspaces } = await import("../db/admin-workspaces");
 const { listWorkspaceDatabaseStates } = await import("../db/workspace-database-states");
 const { checkWorkspaceDatabaseDrift } = await import("./drift");
 const pool = new pg.Pool({ ...controlDatabaseConfig({ CANTELOP_DATABASE_URL:databaseURL, CANTELOP_DATABASE_SSL:process.env.CANTELOP_DATABASE_SSL ?? "verify-full" }),max:5 });
 try {
  const products = ProductRepository.fromPool(pool);
  const suffix = randomUUID().replaceAll("-","").slice(0,12);
  const user = await products.resolveIdentity({provider:"github",providerSubject:`lifecycle-${suffix}`,email:`lifecycle-${suffix}@example.com`});
  const app = await products.createAppForUser(user.id,{slug:`life-${suffix}`,sessionRuntime:{internalPort:3000}});
  const ready = await products.createWorkspaceForUser(user.id,{appID:app.id,slug:"ready"});
  const stuck = await products.createWorkspaceForUser(user.id,{appID:app.id,slug:"stuck"});
  const deleted = await products.createWorkspaceForUser(user.id,{appID:app.id,slug:"deleted"});
  const names = new Map();
  const provider = {
   async ensure(name){ return {id:`id-${name}`,url:"libsql://test-cantelop.turso.io"}; },
   async token(){ return `e30.${Buffer.from(JSON.stringify({exp:Math.floor(Date.now()/1000)+900})).toString("base64url")}.signature`; },
   async probe(){}, async delete(name){ names.delete(name); },
  };
  const environment = `t${suffix.slice(0,8)}`;
  const service = new WorkspaceDatabaseService(pool,provider,environment,new ConfigCipher(Buffer.alloc(32,1)));
  await service.ensure(ready.id); await service.ensure(deleted.id);
  await pool.query(`UPDATE cantelop_control.operations SET status='failed', error_code='provider_registration_rejected' WHERE kind='register_workspace' AND input->>'workspace_id'=$1`,[stuck.id]);
  expect(await listWorkspaceDatabaseStates(user.id,app.id,pool)).toEqual({[ready.id]:"ready",[stuck.id]:"failed",[deleted.id]:"ready"});
  expect(await listWorkspaceDatabaseStates("usr_ffffffffffffffffffffffffffffffff",app.id,pool)).toEqual({});

  // Operator purge archives without an ownership check; the purge then deletes the database.
  await products.purgeWorkspaceAsOperator(deleted.id);
  await products.purgeWorkspaceAsOperator(deleted.id);
  const purges = await pool.query(`SELECT status FROM cantelop_control.operations WHERE idempotency_key=$1`,[`purge-workspace:${deleted.id}`]);
  expect(purges.rows).toEqual([{status:"queued"}]);
  let admin = (await listAdminWorkspaces(pool)).filter(w => w.app_id === app.id);
  expect(admin.find(w => w.id === deleted.id)).toMatchObject({status:"deleting",database:"ready"});
  expect(admin.find(w => w.id === stuck.id)).toMatchObject({status:"active",database:"failed",database_name:null});
  const readyName = admin.find(w => w.id === ready.id).database_name;
  const deletedName = admin.find(w => w.id === deleted.id).database_name;
  expect(readyName).toBe(`ct-${environment}-${ready.id.slice(4)}`);
  names.set(readyName,true); names.set(deletedName,true);
  const before = await checkWorkspaceDatabaseDrift(pool,{async list(){ return [...names.keys(), `ct-${environment}-stray`]; }},environment);
  expect(before.missing).toEqual([]);
  expect(before.orphaned.map(o => o.database_name)).toEqual([`ct-${environment}-stray`]);
  await service.purge(deleted.id);
  expect(names.has(deletedName)).toBe(false);

  // App archive completion queues a purge for every Workspace, keeping the existing one.
  await products.completeArchive(user.id,app.id);
  const queued = await pool.query(`SELECT input->>'workspace_id' AS workspace_id, status FROM cantelop_control.operations
WHERE app_id=$1 AND kind='purge_workspace' ORDER BY input->>'workspace_id'`,[app.id]);
  expect(queued.rows.map(r => r.workspace_id).sort()).toEqual([ready.id,stuck.id,deleted.id].sort());
  expect(queued.rows.every(r => r.status === "queued")).toBe(true);
  admin = (await listAdminWorkspaces(pool)).filter(w => w.app_id === app.id);
  expect(admin.every(w => w.status === "deleting")).toBe(true);
  expect(admin.find(w => w.id === deleted.id).database).toBe("deleted");

  // A database deleted out of band is reported missing, never recreated.
  names.delete(readyName);
  const after = await checkWorkspaceDatabaseDrift(pool,{async list(){ return [...names.keys()]; }},environment);
  expect(after.missing.map(m => m.workspace_id)).toEqual([ready.id]);
 } finally { await pool.end(); }
});
