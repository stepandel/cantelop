import { test, expect } from "bun:test";
import pg from "pg";
import { createHash } from "node:crypto";
import { FleetRepository } from "./repository";
import { FleetInstallationWorker } from "./worker";
import { FleetPackageWorker } from "./package-worker";
import { FleetAttachmentWorker } from "./attachment-worker";
import { ProductRepository } from "../db/product-repository";
import { ReleaseRepository } from "../db/release-repository";
import { OperationRepository } from "../db/operation-repository";
import { ConfigRepository } from "../configuration/repository";
import { ConfigCipher } from "../configuration/cipher";
import { newResourceID } from "../domain/ids";
import { edge, runtime } from "../api-deployments/protocol-artifact.test.mjs";

const url = process.env.CANTELOP_TEST_DATABASE_URL;
(url ? test : test.skip)("one package publication serves many isolated Apps without deploy or conversion operations", async () => {
  const pool = new pg.Pool({ connectionString: url });
  try {
    const products = new ProductRepository(pool);
    const nonce = crypto.randomUUID().replaceAll("-", "");
    const owner = await products.resolveIdentity({ provider: "github", providerSubject: nonce, email: `${nonce}@example.com` });
    const fleets = new FleetRepository(pool);
    const fleet = await fleets.create(owner.id, `shared-${nonce}`, null);
    const actor = { id: "test-publisher", fleetId: fleet.id, ownerUserId: owner.id,
      scopes: ["packages:write", "installations:write", "installations:read"] };
    const source = await products.createAppForUser(owner.id, { slug: `source-${nonce}`, sessionRuntime: { internalPort: 57321 } });
    const apiID = newResourceID("artifact"), runtimeID = newResourceID("sessionRuntimeArtifact"), sourceRelease = newResourceID("release");
    const module = Buffer.from("export default { fetch() { return new Response('shared'); } }");
    const digest = `sha256:${createHash("sha256").update(module).digest("hex")}`;
    const api = { ...edge, fleet_execution_protocol_version: 1, environment: {} };
    const rt = { ...runtime, environment: {} };
    const manifestKey = `prepared-images/manifests/${nonce.repeat(2)}.json`;
    const manifestDigest = `sha256:${nonce.repeat(2)}`;
    const buildID = `sha256:${"b".repeat(64)}`;
    await pool.query(`INSERT INTO cantelop_control.api_artifacts(id,digest,object_key,size_bytes,manifest,created_at)
      VALUES($1,$2,$3,$4,$5,now())`, [apiID, digest, `deployment-uploads/${nonce}/api/worker.mjs`, module.length, api]);
    await pool.query(`INSERT INTO cantelop_control.session_runtime_artifacts(id,digest,object_key,size_bytes,manifest,media_type,created_at)
      VALUES($1,$2,$3,1024,$4,'application/vnd.cantelop.session-runtime.image.v1.tar+gzip',now())`,
    [runtimeID, digest, `deployment-uploads/${nonce}/session-runtime/image.tar.gz`, rt]);
    await pool.query(`INSERT INTO cantelop_control.releases(id,owner_user_id,app_id,api_artifact_id,session_runtime_artifact_id,
      version,status,idempotency_key,created_at,updated_at,activated_at) VALUES($1,$2,$3,$4,$5,1,'active',$6,now(),now(),now())`,
    [sourceRelease, owner.id, source.id, apiID, runtimeID, `source:${nonce}`]);
    const preparedOperation = newResourceID("operation");
    const prepared = { prepared_manifest_key: manifestKey, prepared_manifest_digest: manifestDigest, build_id: buildID, build_version: 1 };
    await pool.query(`INSERT INTO cantelop_control.operations(id,owner_user_id,app_id,kind,status,idempotency_key,input,output,available_at,created_at,updated_at)
      VALUES($1,$2,$3,'materialize_build','succeeded',$4,'{}',$5,now(),now(),now())`,
    [preparedOperation, owner.id, source.id, `prepared:${nonce}`, prepared]);
    await pool.query(`INSERT INTO cantelop_control.release_components(release_id,kind,operation_id,status,output,created_at,updated_at)
      VALUES($1,'session_runtime',$2,'succeeded',$3,now(),now())`, [sourceRelease, preparedOperation, prepared]);
    const pkg = await fleets.publish(actor, sourceRelease, "1.0.0");
    expect(pkg.prepared_manifest_key).toBe(manifestKey);
    const apps = await Promise.all(Array.from({ length: 8 }, (_, index) => fleets.install(actor, `customer:${nonce}:${index}`, pkg.id)));
    expect(new Set(apps.map(app => app.app_id)).size).toBe(8);
    const placements = await pool.query("SELECT compute_pool FROM cantelop_control.apps WHERE id=ANY($1::text[])", [apps.map(app => app.app_id)]);
    expect(placements.rows.map(row => row.compute_pool)).toEqual(Array(8).fill("managed"));
    expect((await pool.query("SELECT compute_pool FROM cantelop_control.apps WHERE id=$1", [source.id])).rows[0].compute_pool).toBe("standard");
    const configuration = new ConfigRepository(pool, new ConfigCipher(Buffer.alloc(32, 7)));
    const installer = new FleetInstallationWorker(pool, configuration);
    expect(await installer.runOnce()).toBe(false);
    expect((await fleets.installation(actor, apps[0].id)).status).toBe("provisioning");
    const publications = [];
    const publisher = new FleetPackageWorker(pool, { async get() { return module; } }, {
      async publishPackage(id, code) { publications.push(id); expect(code.digest).toBe(digest); },
    });
    expect(await publisher.runOnce()).toBe(true);
    expect(await publisher.runOnce()).toBe(false);
    expect(publications).toEqual([pkg.id]);
    for (let i = 0; i < apps.length; i++) expect(await installer.runOnce()).toBe(true);
    expect(await installer.runOnce()).toBe(false);
    const operations = await pool.query(`SELECT kind,count(*)::integer AS count FROM cantelop_control.operations
      WHERE app_id=ANY($1::text[]) GROUP BY kind`, [apps.map(app => app.app_id)]);
    expect(operations.rows.find(row => row.kind === "attach_package").count).toBe(16);
    expect(operations.rows.some(row => ["deploy_api", "materialize_build"].includes(row.kind))).toBe(false);
    const attachments = [];
    const artifacts = {
      async readManifest(key, digest) { expect([key, digest]).toEqual([manifestKey, manifestDigest]);
        return { build_id: buildID, image_key: "shared-image" }; },
      async signImageDownload() { return "https://images.example/image?signed=1"; },
    };
    const attachment = new FleetAttachmentWorker("fleet-test", pool, new OperationRepository(pool), new ReleaseRepository(pool), artifacts, {
      async materialize(command) { attachments.push(command.appID); return { status: "succeeded", operationID: `bop_${command.commandID.slice(3)}`, buildID, buildVersion: 1 }; },
    });
    for (let i = 0; i < apps.length * 2; i++) expect(await attachment.runOnce()).toBe(true);
    expect(await attachment.runOnce()).toBe(false);
    expect(new Set(attachments).size).toBe(8);
    const releases = new ReleaseRepository(pool);
    for (const app of apps) {
      const [release] = await releases.listForUser(owner.id, app.app_id);
      expect(release.components.api.output).toEqual({ package_id: pkg.id, script_name: pkg.id });
      expect(release.components.session_runtime.output.prepared_manifest_key).toBe(manifestKey);
      expect(await products.releaseScriptIDs(app.app_id)).toEqual([]);
    }
    const credentials = await Promise.all(apps.map(app => configuration.integrationCredentialsForUser(owner.id, app.app_id)));
    expect(new Set(credentials.map(value => value.token)).size).toBe(8);
    // A committed release is still unavailable until activation has reopened
    // both gates and recorded its final acknowledgement.
    const activating = await fleets.installation(actor, apps[1].id);
    await pool.query("UPDATE cantelop_control.releases SET status='active',activated_at=now() WHERE id=$1", [activating.release_id]);
    await pool.query("UPDATE cantelop_control.apps SET active_release_id=$2 WHERE id=$1", [apps[1].app_id, activating.release_id]);
    await pool.query("UPDATE cantelop_control.fleet_policies SET enforced_revision=revision WHERE app_id=$1", [apps[1].app_id]);
    expect((await fleets.installation(actor, apps[1].id)).status).toBe("provisioning");
    await pool.query(`UPDATE cantelop_control.operations SET status='succeeded',output='{}',updated_at=now()
      WHERE app_id=$1 AND kind='activate_release' AND input->>'target_release_id'=$2`, [apps[1].app_id, activating.release_id]);
    expect((await fleets.installation(actor, apps[1].id)).status).toBe("ready");
    await fleets.deleteByKey(actor, apps[0].external_key);
    const deleted = await fleets.install(actor, `deleted:${nonce}`, pkg.id);
    await fleets.deleteByKey(actor, deleted.external_key);
    expect(await installer.runOnce()).toBe(false);
    expect((await fleets.installation(actor, deleted.id)).release_id).toBeNull();
    await products.completeArchive(owner.id, source.id);
    expect((await fleets.package(actor, pkg.id)).execution).toBe("ready");
    expect(await publisher.runOnce()).toBe(false);
  } finally { await pool.end(); }
}, 30_000);
