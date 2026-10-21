import { test, expect } from "bun:test";
import pg from "pg";
import { ProductRepository } from "../db/product-repository";
import { FleetRepository } from "./repository";
import { FleetBudget } from "./budget";
import { FleetPolicyWorker } from "./policy-worker";
import { FleetInstallationWorker } from "./worker";
import { ConfigRepository } from "../configuration/repository";
import { ConfigCipher } from "../configuration/cipher";
import { newResourceID } from "../domain/ids";
import { edge, runtime } from "../api-deployments/protocol-artifact.test.mjs";

const url = process.env.CANTELOP_TEST_DATABASE_URL;
(url ? test : test.skip)(
  "fleet credentials, packages, concurrent installs and reservations preserve isolation in PostgreSQL",
  async () => {
    const pool = new pg.Pool({ connectionString: url, max: 20 });
    const repo = new FleetRepository(pool),
      products = new ProductRepository(pool),
      budget = new FleetBudget(pool);
    try {
      const nonce = crypto.randomUUID().replaceAll("-", "");
      const user = await products.resolveIdentity({
        provider: "github",
        providerSubject: nonce,
        email: `${nonce}@example.com`,
      });
      await pool.query("UPDATE cantelop_control.user_quotas SET max_apps=3 WHERE user_id=$1", [user.id]);
      const fleet = await repo.create(user.id, `fleet-${nonce}`, null);
      expect(fleet.max_apps).toBeNull();
      const otherFleet = await repo.create(user.id, `other-${nonce}`, 20);
      const credential = await repo.principal(user.id, fleet.id, {
        label: "worker",
        scopes: ["packages:write", "installations:write", "installations:read", "policy:write"],
        expiresAt: new Date(Date.now() + 86400000).toISOString(),
      });
      const actor = await repo.authenticate(`Bearer ${credential.token}`, fleet.id);
      await expect(repo.authenticate(`Bearer ${credential.token}`, otherFleet.id)).rejects.toMatchObject({
        status: 401,
      });
      const source = await products.createAppForUser(user.id, {
        slug: `source-${nonce}`,
        sessionRuntime: { internalPort: 57321 },
      });
      expect(await products.getUserQuota(user.id)).toEqual({ maxApps: 3, usedApps: 3 });
      await expect(repo.create(user.id, `excess-${nonce}`, null)).rejects.toMatchObject({ code: "quota_exceeded" });
      const apiId = newResourceID("artifact"),
        runtimeId = newResourceID("sessionRuntimeArtifact"),
        releaseId = newResourceID("release");
      const api = {
        ...edge,
        app_name: "juiced",
        environment: { GH_TOKEN: { secret: true } },
        webhooks: [],
        homepage: false,
      };
      const rt = { ...runtime, app_name: "juiced", environment: api.environment };
      await pool.query(
        `INSERT INTO cantelop_control.api_artifacts(id,digest,object_key,size_bytes,manifest,created_at) VALUES($1,$2,$3,29,$4,now())`,
        [apiId, `sha256:${nonce.repeat(2)}`, `deployment-uploads/${nonce}/api/worker.mjs`, api],
      );
      await pool.query(
        `INSERT INTO cantelop_control.session_runtime_artifacts(id,digest,object_key,size_bytes,manifest,media_type,created_at)
      VALUES($1,$2,$3,1024,$4,'application/vnd.cantelop.session-runtime.image.v1.tar+gzip',now())`,
        [
          runtimeId,
          `sha256:${nonce.repeat(2)}`,
          `deployment-uploads/${nonce}/session-runtime/image.tar.gz`,
          rt,
        ],
      );
      await pool.query(
        `INSERT INTO cantelop_control.releases(id,owner_user_id,app_id,api_artifact_id,session_runtime_artifact_id,version,status,idempotency_key,created_at,updated_at,activated_at)
      VALUES($1,$2,$3,$4,$5,1,'active',$6,now(),now(),now())`,
        [releaseId, user.id, source.id, apiId, runtimeId, `source:${nonce}`],
      );
      const pkg = await repo.publish(actor, releaseId, "1.0.0");
      const installs = await Promise.all(
        Array.from({ length: 8 }, () => repo.install(actor, `account:${nonce}`, pkg.id)),
      );
      expect(new Set(installs.map((i) => i.app_id)).size).toBe(1);
      const a = installs[0],
        b = await repo.install(actor, `second:${nonce}`, pkg.id);
      expect(a.app_id).not.toBe(b.app_id);
      expect(await products.getUserQuota(user.id)).toEqual({ maxApps: 3, usedApps: 3 });
      const config = new ConfigRepository(pool, new ConfigCipher(Buffer.alloc(32, 7)));
      const worker = new FleetInstallationWorker(pool, config);
      await worker.runOnce();
      await worker.runOnce();
      const aKey = await config.integrationCredentialsForUser(user.id, a.app_id),
        bKey = await config.integrationCredentialsForUser(user.id, b.app_id);
      expect(aKey.token).not.toBe(bKey.token);
      expect(aKey.slug).not.toBe("juiced");
      await expect(config.prepareIntegrationDeployment(user.id, a.app_id, api)).rejects.toThrow(
        "does not match",
      );
      await expect(
        config.preparePackageDeployment(user.id, b.app_id, "pkg_" + "f".repeat(32)),
      ).rejects.toThrow("Unbound");
      const end = new Date(Date.now() + 86400000).toISOString();
      await budget.grant(a.app_id, "period-1", 100, end);
      await expect(budget.grant(a.app_id, "period-1", 200, end)).rejects.toMatchObject({
        code: "budget_grant_conflict",
      });
      const expiry = new Date(Date.now() + 50000).toISOString();
      const attempts = await Promise.allSettled(
        Array.from({ length: 20 }, (_, i) =>
          budget.reserve(a.app_id, "period-1", "host-1", `${nonce}:reservation:${i}`, 10, expiry),
        ),
      );
      expect(attempts.filter((r) => r.status === "fulfilled")).toHaveLength(10);
      expect((await budget.read(a.app_id, "period-1")).available).toBe("0");
      const reserved = attempts.find((r) => r.status === "fulfilled").value;
      await Promise.all([budget.settle(reserved.id, "host-1", 4), budget.settle(reserved.id, "host-1", 4)]);
      expect(await budget.read(a.app_id, "period-1")).toMatchObject({
        available: "6",
        settled: "4",
        reserved: "90",
      });
      await expect(budget.settle(reserved.id, "host-2", 4)).rejects.toMatchObject({ status: 404 });
      await expect(budget.settle(reserved.id, "host-1", 0)).rejects.toMatchObject({
        code: "settlement_conflict",
      });
      const computeEnd = new Date(Date.now() + 2 * 86400000).toISOString();
      await budget.grant(a.app_id, "compute-period", 10000000, computeEnd);
      await repo.updatePolicy(actor, a.id, {
        expectedRevision: 1,
        policy: {
          active: true,
          expiresAt: computeEnd,
          maxSandboxes: 2,
          maxVcpus: 8,
          maxMemoryMib: 9216,
          maxWorkspaces: 8,
          maxStorageBytes: 10 * 1024 ** 3,
        },
      });
      const hosts = new Map();
      let outage = false,
        reject = false;
      const host = async (url, init) => {
        if (outage) throw new Error("host unavailable");
        const app = new URL(url).pathname.split("/")[6];
        const state = hosts.get(app) ?? {
          state: { revision: 0, draining: false, grant: {} },
          usage: [],
          activity: 0,
          hostId: "host-compute-test",
        };
        hosts.set(app, state);
        if (reject && init.method === "PUT") return Response.json({ error: "policy_rejected" }, { status: 409 });
        if (init.method === "PUT") {
          const next = JSON.parse(init.body);
          if (state.state.grant.id && state.state.grant.id !== next.grant.id)
            state.usage.push({ id: state.state.grant.id, credits: 123, hostId: state.state.grant.hostId });
          state.state = { ...next, consumed: 0 };
        }
        if (init.method === "DELETE") state.usage = state.usage.filter((u) => !url.endsWith("/" + u.id));
        return Response.json(state);
      };
      const policies = new FleetPolicyWorker(pool, "https://compute.example", "1".repeat(64), host, fleet.id);
      await policies.runOnce();
      await policies.runOnce();
      expect((await repo.installation(actor, a.id)).enforced_revision).toBe(2);
      const state = hosts.get(a.app_id);
      expect(state.state.grant.epoch).toBe("compute-period");
      state.state.grant.expiresAt = new Date(Date.now() + 5000).toISOString();
      await pool.query("UPDATE cantelop_control.fleet_policies SET next_sync_at=now() WHERE app_id=$1", [
        a.app_id,
      ]);
      await policies.runOnce();
      expect((await budget.read(a.app_id, "compute-period")).settled).toBe("123");
      await repo.updatePolicy(actor, a.id, {
        expectedRevision: 2,
        policy: {
          active: false,
          expiresAt: computeEnd,
          maxSandboxes: 0,
          maxVcpus: 0,
          maxMemoryMib: 0,
          maxWorkspaces: 0,
          maxStorageBytes: 0,
        },
      });
      outage = true;
      await pool.query("UPDATE cantelop_control.fleet_policies SET next_sync_at=now() WHERE app_id=$1", [
        a.app_id,
      ]);
      await policies.runOnce();
      expect((await repo.installation(actor, a.id)).enforced_revision).toBe(2);
      const closedPolicy = {
        active: false,
        expiresAt: computeEnd,
        maxSandboxes: 0,
        maxVcpus: 0,
        maxMemoryMib: 0,
        maxWorkspaces: 0,
        maxStorageBytes: 0,
      };
      await repo.updatePolicy(actor, a.id, { expectedRevision: 3, policy: closedPolicy, sourceFence: "20" });
      await expect(
        repo.updatePolicy(actor, a.id, {
          expectedRevision: 4,
          policy: { ...closedPolicy, active: true },
          sourceFence: "19",
        }),
      ).rejects.toMatchObject({ code: "stale_policy_projection" });
      expect(
        (
          await repo.updatePolicy(actor, a.id, {
            expectedRevision: 1,
            policy: closedPolicy,
            sourceFence: "20",
          })
        ).revision,
      ).toBe(4);
      // A partially remaining allowance must fund a shorter window, including
      // the extra second reserved for projection/shutdown rounding.
      const rate = 8 * 1024 + 9216;
      await budget.grant(b.app_id, "tail-period", rate * 21, computeEnd);
      await repo.updatePolicy(actor, b.id, {
        expectedRevision: 1,
        policy: {
          active: true,
          expiresAt: computeEnd,
          maxSandboxes: 2,
          maxVcpus: 8,
          maxMemoryMib: 9216,
          maxWorkspaces: 8,
          maxStorageBytes: 10 * 1024 ** 3,
        },
      });
      outage = false;
      await pool.query(
        "UPDATE cantelop_control.fleet_policies SET next_sync_at=CASE WHEN app_id=$1 THEN now()-interval '1 minute' ELSE now()+interval '1 hour' END WHERE app_id IN ($1,$2)",
        [b.app_id, a.app_id],
      );
      await policies.runOnce();
      expect(hosts.get(b.app_id).state.grant.credits).toBe(rate * 21);
      expect(Date.parse(hosts.get(b.app_id).state.grant.expiresAt) - Date.now()).toBeGreaterThan(18000);
      // A failed release retries only after its backoff; a cancellation is final.
      const failed = (await repo.installation(actor, a.id)).release_id;
      await pool.query(
        "UPDATE cantelop_control.releases SET status='failed',error_code='release_failed',updated_at=now() WHERE id=$1",
        [failed],
      );
      expect(await worker.runOnce()).toBe(false);
      expect((await repo.installation(actor, a.id)).status).toBe("failed");
      await pool.query(
        "UPDATE cantelop_control.releases SET created_at=now()-interval '20 minutes',updated_at=now()-interval '10 minutes' WHERE id=$1",
        [failed],
      );
      expect(await worker.runOnce()).toBe(true);
      const retried = (await repo.installation(actor, a.id)).release_id;
      expect(retried).not.toBe(failed);
      await pool.query(
        "UPDATE cantelop_control.releases SET status='cancelled',created_at=now()-interval '20 minutes',updated_at=now()-interval '10 minutes' WHERE id=$1",
        [retried],
      );
      expect(await worker.runOnce()).toBe(false);
      // Funding uses the soonest-expiring epoch that can fund a window, and a
      // definite host rejection releases the reservation it never installed.
      const c = await repo.install(actor, `third:${nonce}`, pkg.id);
      await budget.grant(c.app_id, "late-empty", 0, computeEnd);
      await budget.grant(c.app_id, "early-funded", rate * 60, new Date(Date.now() + 86400000).toISOString());
      await repo.updatePolicy(actor, c.id, {
        expectedRevision: 1,
        policy: {
          active: true,
          expiresAt: computeEnd,
          maxSandboxes: 2,
          maxVcpus: 8,
          maxMemoryMib: 9216,
          maxWorkspaces: 8,
          maxStorageBytes: 10 * 1024 ** 3,
        },
      });
      const dueOnly = (app) =>
        pool.query(
          "UPDATE cantelop_control.fleet_policies SET next_sync_at=CASE WHEN app_id=$1 THEN now()-interval '1 minute' ELSE now()+interval '1 hour' END WHERE app_id IN ($2,$3,$4)",
          [app, a.app_id, b.app_id, c.app_id],
        );
      reject = true;
      await dueOnly(c.app_id);
      await policies.runOnce();
      expect(await budget.read(c.app_id, "early-funded")).toMatchObject({ reserved: "0", settled: "0" });
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM cantelop_control.fleet_reservations WHERE app_id=$1 AND consumed=0",
            [c.app_id],
          )
        ).rows[0].n,
      ).toBe(1);
      reject = false;
      await dueOnly(c.app_id);
      await policies.runOnce();
      expect(hosts.get(c.app_id).state.grant.epoch).toBe("early-funded");
      // The installation worker is global; leave nothing claimable behind.
      await repo.deleteByKey(actor, `third:${nonce}`);
      // Changing a Fleet's cap is owner-only. Finite capacity still
      // serializes concurrent installs and includes Apps awaiting purge.
      const foreign = await products.resolveIdentity({
        provider: "github", providerSubject: `foreign-${nonce}`, email: `foreign-${nonce}@example.com`,
      });
      await expect(repo.updateCapacity(foreign.id, fleet.id, 1)).rejects.toMatchObject({ code: "not_found" });
      expect((await repo.updateCapacity(user.id, fleet.id, 3)).max_apps).toBe(3);
      await expect(repo.install(actor, `blocked:${nonce}`, pkg.id)).rejects.toMatchObject({ code: "quota_exceeded" });
      expect((await repo.updateCapacity(user.id, fleet.id, null)).max_apps).toBeNull();
      const more = await Promise.all(Array.from({ length: 6 }, (_, n) => repo.install(actor, `unlimited:${n}:${nonce}`, pkg.id)));
      expect(new Set(more.map(i => i.app_id)).size).toBe(6);
      expect(await products.getUserQuota(user.id)).toEqual({ maxApps: 3, usedApps: 3 });
      for (const i of more) await repo.deleteByKey(actor, i.external_key);
      await repo.updateCapacity(user.id, fleet.id, 10);
      const lastSlot = await Promise.allSettled(Array.from({ length: 8 }, (_, n) =>
        repo.install(actor, `last-slot:${n}:${nonce}`, pkg.id)));
      expect(lastSlot.filter(r => r.status === "fulfilled")).toHaveLength(1);
      const refused = lastSlot.filter(r => r.status === "rejected");
      expect(refused).toHaveLength(7);
      for (const r of refused) expect(r.reason).toMatchObject({ code: "quota_exceeded", status: 429 });
      for (const r of lastSlot) if (r.status === "fulfilled") await repo.deleteByKey(actor, r.value.external_key);
      await expect(products.createAppForUser(user.id, {
        slug: `excess-${nonce}`, sessionRuntime: { internalPort: 3000 },
      })).rejects.toMatchObject({ code: "quota_exceeded" });
      const canceled = `canceled:${nonce}`;
      await repo.deleteByKey(actor, canceled);
      await expect(repo.install(actor, canceled, pkg.id)).rejects.toMatchObject({
        code: "installation_deleted",
      });
      await repo.revoke(user.id, fleet.id, credential.id);
      await expect(repo.authenticate(`Bearer ${credential.token}`, fleet.id)).rejects.toMatchObject({
        status: 401,
      });
    } finally {
      await pool.end();
    }
  },
  30000,
);

(url ? test : test.skip)("concurrent Fleet and standalone App creation share the last account slot", async () => {
  const pool = new pg.Pool({ connectionString: url, max: 10 });
  try {
    const products = new ProductRepository(pool), repo = new FleetRepository(pool);
    const nonce = crypto.randomUUID().replaceAll("-", "");
    const owner = await products.resolveIdentity({
      provider: "github", providerSubject: nonce, email: `${nonce}@example.com`,
    });
    await pool.query("UPDATE cantelop_control.user_quotas SET max_apps=1 WHERE user_id=$1", [owner.id]);
    const attempts = await Promise.allSettled(Array.from({ length: 10 }, (_, n) =>
      n % 2 === 0
        ? repo.create(owner.id, `race-${n}-${nonce}`, null)
        : products.createAppForUser(owner.id, {
          slug: `race-${n}-${nonce}`, sessionRuntime: { internalPort: 3000 },
        })));
    expect(attempts.filter(r => r.status === "fulfilled")).toHaveLength(1);
    for (const r of attempts) if (r.status === "rejected") expect(r.reason).toMatchObject({ code: "quota_exceeded" });
    expect(await products.getUserQuota(owner.id)).toEqual({ maxApps: 1, usedApps: 1 });

    // A second account's empty Fleet consumes its slot before any installation.
    const other = await products.resolveIdentity({
      provider: "github", providerSubject: `other-${nonce}`, email: `other-${nonce}@example.com`,
    });
    await pool.query("UPDATE cantelop_control.user_quotas SET max_apps=1 WHERE user_id=$1", [other.id]);
    const empty = await repo.create(other.id, `empty-${nonce}`, null);
    expect(await products.getUserQuota(other.id)).toEqual({ maxApps: 1, usedApps: 1 });
    await expect(repo.create(other.id, `extra-${nonce}`, null)).rejects.toMatchObject({ code: "quota_exceeded" });
    await expect(products.createAppForUser(other.id, {
      slug: `extra-${nonce}`, sessionRuntime: { internalPort: 3000 },
    })).rejects.toMatchObject({ code: "quota_exceeded" });
    for (const limit of [undefined, 0, -1, 1.5, 10001, "unlimited"]) {
      await expect(repo.updateCapacity(other.id, empty.id, limit)).rejects.toMatchObject({ code: "invalid_limit" });
    }
    // Unlimited capacity is an explicit `null`; an omitted cap is refused.
    await expect(repo.create(other.id, `omitted-${nonce}`)).rejects.toMatchObject({ code: "invalid_limit" });
    await expect(repo.transaction(tx => products.createAppInTransaction(tx, owner.id, {
      slug: `foreign-${nonce}`, sessionRuntime: { internalPort: 3000 },
    }, empty.id))).rejects.toMatchObject({ code: "not_found" });
    await pool.query("UPDATE cantelop_control.users SET status='suspended' WHERE id=$1", [other.id]);
    await expect(repo.updateCapacity(other.id, empty.id, 1)).rejects.toMatchObject({ code: "not_found" });
    await expect(repo.create(other.id, `suspended-${nonce}`, null)).rejects.toMatchObject({ code: "user_suspended" });
  } finally {
    await pool.end();
  }
}, 30000);
