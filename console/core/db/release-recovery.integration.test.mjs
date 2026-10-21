import { expect, test } from "bun:test";
import pg from "pg";
import { ProductRepository } from "./product-repository";
import { ReleaseRepository } from "./release-repository";
import { OperationRepository } from "./operation-repository";
import { DeploymentRecoveryWorker } from "../deployment-recovery/worker";
import { edge, runtime as runtimeManifest } from "../api-deployments/protocol-artifact.test.mjs";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
(databaseURL ? test : test.skip)("cancellation fences old attempts and restart is atomic and replay safe", async () => {
 const pool = new pg.Pool({connectionString: databaseURL});
 try {
  await pool.query("TRUNCATE cantelop_control.users CASCADE");
  const products = ProductRepository.fromPool(pool);
  const user = await products.resolveIdentity({provider:"github", providerSubject:crypto.randomUUID(), email:"recovery@example.com"});
  const app = await products.createAppForUser(user.id, {slug:`recovery-${Date.now()}`, sessionRuntime:{internalPort:3000}});
  let at = new Date(Date.now()+3600_000);
  const releases = new ReleaseRepository(pool,()=>at);
  const operations = new OperationRepository(pool,()=>at);
  let entered, finish;
  const acquired = new Promise(resolve => { entered = resolve; });
  const held = new Promise(resolve => { finish = resolve; });
  const firstLock = releases.withDeploymentLock(app.id, async () => { entered(); await held; });
  await acquired;
  let secondEntered = false;
  const secondLock = releases.withDeploymentLock(app.id, async () => { secondEntered = true; });
  try {
    await new Promise(resolve => setTimeout(resolve, 20));
    expect(secondEntered).toBe(false);
  } finally { finish(); }
  await Promise.all([firstLock, secondLock]);
  expect(secondEntered).toBe(true);

  const nonce=crypto.randomUUID().replaceAll("-", "");
  const api={digest:`sha256:${nonce.repeat(2)}`,objectKey:`deployment-uploads/${nonce}/api/worker.mjs`,sizeBytes:29,manifest:edge};
  const runtime={digest:`sha256:${"b".repeat(64)}`,objectKey:`deployment-uploads/${nonce}/session-runtime/image.tar.gz`,sizeBytes:1024,mediaType:"application/vnd.cantelop.session-runtime.image.v1.tar+gzip",manifest:runtimeManifest};
  const original=await releases.begin(user.id,app.id,api,runtime,`initial:${crypto.randomUUID()}`);
  const oldAPI=await operations.claim("old-api","deploy_api");
  const oldRuntime=await operations.claim("old-runtime","materialize_build");
  await releases.startComponent(oldAPI.id,"old-api");
  await releases.startComponent(oldRuntime.id,"old-runtime");
  await expect(releases.requestRecovery("usr_"+"f".repeat(32),app.id,original.id,true)).rejects.toMatchObject({code:"not_found"});
  const results=await Promise.all([releases.requestRecovery(user.id,app.id,original.id,true),releases.requestRecovery(user.id,app.id,original.id,true)]);
  expect(results.every(r=>r.status==="cancelling")).toBe(true);
  await expect(releases.begin(user.id,app.id,api,runtime,`blocked:${crypto.randomUUID()}`)).rejects.toMatchObject({code:"conflict"});
  await expect(releases.succeedComponent(oldAPI.id,"old-api",{provider_release_id:"late"})).rejects.toThrow();
  await expect(releases.retryComponent(oldRuntime.id,"old-runtime",new Date(at.getTime()+30_000))).rejects.toThrow();
  let unavailable=true;
  const cancelled=[];
  const worker=new DeploymentRecoveryWorker("recovery",operations,releases,{async cancel(...args){cancelled.push(args);if(unavailable)throw Error("offline");}},{async prepare(){return "ready";}},()=>at);
  await worker.runOnce();
  expect((await releases.getForUser(user.id,app.id,original.id)).status).toBe("cancelling");
  expect(await releases.listForUser(user.id,app.id)).toHaveLength(1);
  at=new Date(at.getTime()+31_000);unavailable=false;
  await worker.runOnce();
  const history=await releases.listForUser(user.id,app.id);
  expect(history).toHaveLength(2);
  const replacement=history.find(r=>r.id!==original.id);
  expect(replacement).toMatchObject({version:2,status:"queued",apiArtifactDigest:api.digest,sessionRuntimeArtifactDigest:runtime.digest});
  expect(replacement.components.api.operationID).not.toBe(oldAPI.id);
  expect(replacement.components.session_runtime.operationID).not.toBe(oldRuntime.id);
  expect(cancelled[0]).toEqual([user.id,app.id,oldRuntime.id]);
  expect((await releases.requestRecovery(user.id,app.id,original.id,true)).id).toBe(replacement.id);
  expect((await releases.requestRecovery(user.id,app.id,original.id,false)).status).toBe("cancelled");
  for(const [kind,component] of [["deploy_api","api"],["materialize_build","session_runtime"]]){
   const op=await operations.claim("new-worker",kind);
   expect(op.id).toBe(replacement.components[component].operationID);
   await releases.startComponent(op.id,"new-worker");
   await releases.succeedComponent(op.id,"new-worker",component==="api"?{provider_release_id:"new-api"}:{build_id:runtime.digest,build_version:1});
  }
  // Completed components queue activation; commit it as the activation worker does.
  expect((await releases.getForUser(user.id,app.id,replacement.id)).status).toBe("provisioning");
  const activation=await operations.claim("new-worker","activate_release");
  expect(activation.input).toMatchObject({target_release_id:replacement.id,previous_release_id:null});
  await releases.commitActivation(user.id,app.id,replacement.id,null);
  await operations.succeed(activation.id,"new-worker",{target_release_id:replacement.id});
  expect((await releases.getForUser(user.id,app.id,replacement.id)).status).toBe("active");
  await expect(releases.requestRecovery(user.id,app.id,replacement.id,false)).rejects.toMatchObject({code:"conflict"});
  // Cancel-only releases can later be restarted, and duplicate requests remain safe.
  const next=await releases.begin(user.id,app.id,api,runtime,`next:${crypto.randomUUID()}`);
  await releases.requestRecovery(user.id,app.id,next.id,false);
  await worker.runOnce();
  expect((await releases.getForUser(user.id,app.id,next.id)).status).toBe("cancelled");
  await releases.requestRecovery(user.id,app.id,next.id,true);
  await worker.runOnce();
  const restarted=await releases.requestRecovery(user.id,app.id,next.id,true);
  expect(restarted.version).toBe(4);
  expect(restarted.status).toBe("queued");
  await releases.requestRecovery(user.id,app.id,restarted.id,false);
  const abandoned = await operations.claim("crashed-worker", "cancel_release");
  expect(abandoned).toBeDefined();
  at = new Date(at.getTime() + 6 * 60_000);
  await worker.runOnce();
  expect((await releases.getForUser(user.id,app.id,restarted.id)).status).toBe("cancelled");
  const active=(await pool.query("SELECT active_release_id FROM cantelop_control.apps WHERE id=$1",[app.id])).rows[0].active_release_id;
  expect(active).toBe(replacement.id);
 } finally {await pool.query("TRUNCATE cantelop_control.users CASCADE");await pool.end();}
},30_000);
