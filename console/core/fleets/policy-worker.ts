import { randomUUID } from "node:crypto";
import type { SQLPool } from "../db/database";
import { FleetBudget } from "./budget";
import { ProductRepository } from "../db/product-repository";
import { FleetRepository } from "./repository";
import type { AppPolicy } from "./model";

// Concurrent reconciliations per pass; each holds no transaction across HTTP.
const batchSize = 8;
// Reconciliation deadline; the 120-second claim lease leaves ample margin.
const reconcileMilliseconds = 45000;

interface Grant {
  hostId: string;
  id: string;
  epoch: string;
  credits: number;
  expiresAt: string;
}
interface HostState {
  hostId: string;
  activity: number;
  state: { draining: boolean; revision: number; policy: AppPolicy; grant: Grant; consumed: number };
  usage: { id: string; credits: number; hostId: string }[];
}
/** One host placement today. The reservation is bound to this private ingress;
 * moving an App requires closing its old grant and provisioning a fresh one. */
export class FleetPolicyWorker {
  private origin: string;
  constructor(
    private db: SQLPool,
    origin: string,
    private token: string,
    private request: typeof fetch = fetch,
    private fleetId?: string,
  ) {
    const u = new URL(origin);
    if (
      u.username ||
      u.password ||
      u.pathname !== "/" ||
      u.search ||
      u.hash ||
      !/^https?:$/.test(u.protocol) ||
      !token
    )
      throw new Error("invalid managed compute configuration");
    this.origin = u.origin;
  }
  async call<T>(
    tenant: string,
    app: string,
    method: string,
    suffix = "",
    body?: unknown,
    deadline = Date.now() + 25000,
  ): Promise<T> {
    const remaining = Math.min(25000, deadline - Date.now());
    if (remaining <= 0) throw new Error("managed_reconcile_deadline");
    const r = await this.request(
      `${this.origin}/v1/internal/users/${tenant}/apps/${app}/managed-policy${suffix}`,
      {
        method,
        redirect: "error",
        headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json" },
        signal: AbortSignal.timeout(remaining),
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      },
    );
    if (!r.ok) throw Object.assign(new Error("managed_compute_unavailable"), { status: r.status });
    return r.json() as Promise<T>;
  }
  /** Claims a small batch briefly, then reconciles it outside any transaction. */
  async runOnce() {
    const rows = await new FleetRepository(this.db).transaction(async (tx) => {
      const claimed = (
        await tx.query(
          `SELECT p.*,i.id AS installation_id,i.desired_state,i.next_package_id,a.status AS app_status,coalesce(a.compute_tenant_id,a.owner_user_id) AS tenant,f.owner_user_id
        FROM cantelop_control.fleet_policies p JOIN cantelop_control.fleet_installations i ON i.app_id=p.app_id
        JOIN cantelop_control.apps a ON a.id=p.app_id JOIN cantelop_control.fleets f ON f.id=i.fleet_id
        WHERE p.next_sync_at<=now() AND a.status<>'archived' AND ($1::text IS NULL OR i.fleet_id=$1) ORDER BY p.next_sync_at FOR UPDATE OF p SKIP LOCKED LIMIT $2`,
          [this.fleetId ?? null, batchSize],
        )
      ).rows;
      if (claimed.length)
        await tx.query(
          `UPDATE cantelop_control.fleet_policies SET next_sync_at=now()+interval '120 seconds' WHERE app_id=ANY($1::text[])`,
          [claimed.map((row) => String(row.app_id))],
        );
      return claimed;
    });
    if (!rows.length) return false;
    await Promise.all(rows.map((row) => this.reconcile(row)));
    return true;
  }
  private async reconcile(row: Record<string, unknown>) {
    const app = String(row.app_id);
    // Every host call ends well inside the claim lease, so no other pass can
    // reconcile this App concurrently with a call still in flight.
    const deadline = Date.now() + reconcileMilliseconds;
    try {
      const tenant = String(row.tenant),
        budget = new FleetBudget(this.db),
        call = <T>(method: string, suffix = "", payload?: unknown) =>
          this.call<T>(tenant, app, method, suffix, payload, deadline),
        remote = await call<HostState>("GET");
      const host = remote.hostId;
      if (!/^[A-Za-z0-9_.:-]{1,128}$/.test(host)) throw new Error("host_identity_missing");
      const settle = async (items: HostState["usage"]) => {
        for (const u of items) {
          try {
            await budget.settle(u.id, u.hostId, u.credits);
          } catch {
            // Leave unsettleable evidence unacknowledged; its reservation stays
            // charged, and other usage and projection still proceed.
            continue;
          }
          await call("DELETE", `/usage/${encodeURIComponent(u.id)}`);
        }
      };
      await settle(remote.usage);
      const policy = row.policy as unknown as AppPolicy;
      let grant: Grant | Record<string, never> = {};
      const active =
        policy.active && Date.parse(policy.expiresAt) > Date.now() + 10000 && row.desired_state === "ready";
      // Desired suspension/deletion increments the policy once, independently
      // of retries. Expired/no-budget grants remain denied at the host.
      if (!active && policy.active && row.desired_state !== "ready") {
        policy.active = false;
        const previous = Number(row.revision);
        row.revision = previous + 1;
        const bumped = await this.db.query(
          `UPDATE cantelop_control.fleet_policies SET revision=$2,policy=$3 WHERE app_id=$1 AND revision=$4`,
          [app, row.revision, JSON.stringify(policy), previous],
        );
        if (!bumped.rowCount) throw new Error("policy_revision_changed");
      }
      const old = remote.state.grant;
      if (
        active &&
        remote.state.revision === row.revision &&
        old?.hostId === host &&
        old?.id &&
        Date.parse(old.expiresAt) > Date.now() + 15000 &&
        remote.state.consumed < old.credits
      )
        grant = old;
      else if (active) {
        // Reuse an ambiguously projected reservation, including after worker
        // death. Its identity/expiry/amount can never be changed by retries.
        const pending = (
          await this.db.query(
            `SELECT * FROM cantelop_control.fleet_reservations WHERE app_id=$1 AND host_id=$2 AND consumed IS NULL AND id<>$3 AND expires_at>now()+interval '10 seconds' ORDER BY expires_at LIMIT 1`,
            [app, host, old?.id ?? ""],
          )
        ).rows[0];
        if (pending)
          grant = {
            hostId: host,
            id: String(pending.id),
            epoch: String(pending.epoch),
            credits: Number(pending.credits),
            expiresAt: new Date(String(pending.expires_at)).toISOString(),
          };
        else {
          // Spend the soonest-expiring funded epoch that can fund a full window.
          const budgets = (
            await this.db.query(
              `SELECT *,included-settled-reserved AS available FROM cantelop_control.fleet_budgets WHERE app_id=$1 AND expires_at>now()+interval '10 seconds' AND included-settled-reserved>0 ORDER BY expires_at`,
              [app],
            )
          ).rows;
          const rate = policy.maxVcpus * 1024 + policy.maxMemoryMib;
          for (const b of rate > 0 ? budgets : []) {
            const seconds = Math.min(
              45,
              Math.floor(Number(b.available) / rate) - 1,
              Math.floor(
                (Math.min(new Date(String(b.expires_at)).getTime(), Date.parse(policy.expiresAt)) - Date.now()) /
                  1000,
              ),
            );
            if (seconds < 10) continue;
            const id = `lease_${randomUUID().replaceAll("-", "")}`,
              expiresAt = new Date(Date.now() + seconds * 1000).toISOString(),
              credits = rate * (seconds + 1);
            if (credits > Number(b.available)) continue;
            await budget.reserve(app, String(b.epoch), host, id, credits, expiresAt);
            grant = { hostId: host, id, epoch: String(b.epoch), credits, expiresAt };
            break;
          }
        }
      }
      // Project whenever the host differs. An empty grant stops active work and
      // finalizes the previous reservation before any refund is usable.
      const fresh = (grant as Grant).id ?? "";
      if (
        remote.state.draining !== Boolean(row.next_package_id) ||
        remote.state.revision !== row.revision ||
        (old?.id ?? "") !== fresh
      ) {
        try {
          await call("PUT", "", {
            revision: row.revision,
            policy,
            grant,
            draining: Boolean(row.next_package_id),
          });
        } catch (error) {
          // A definite rejection never installs the grant. Once the host confirms
          // it neither holds nor finalized it, release the reservation unused.
          if (fresh && fresh !== old?.id && (error as { status?: number }).status === 409) {
            const after = await call<HostState>("GET");
            if (after.state.grant?.id !== fresh && !after.usage.some((u) => u.id === fresh))
              await budget.settle(fresh, host, 0);
          }
          throw error;
        }
        await settle((await call<HostState>("GET")).usage);
      }
      await this.db.query(
        `UPDATE cantelop_control.fleet_policies SET enforced_revision=GREATEST(enforced_revision,$2) WHERE app_id=$1`,
        [app, row.revision],
      );
      if (row.desired_state === "deleted")
        await new ProductRepository(this.db).archiveAppForUser(String(row.owner_user_id), app);
    } catch {
      /* Lease expiry closes admission even when control/ledger is unavailable. */
    } finally {
      await this.db
        .query(`UPDATE cantelop_control.fleet_policies SET next_sync_at=now()+interval '5 seconds' WHERE app_id=$1`, [
          app,
        ])
        .catch(() => {});
    }
  }
}
