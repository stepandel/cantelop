import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { withTransaction, type SQLPool, type TransactionClient } from "../db/database";
import { ProductRepository } from "../db/product-repository";
import { assertAppQuotaAdmission } from "../db/app-quota";
import { platformInternalPort } from "../domain/model";
import {
  parseProtocolEdgeManifest,
  parseSessionRuntimeManifest,
  validateArtifactPair,
} from "../api-deployments/protocol-artifact";
import {
  FleetError,
  identifier,
  integer,
  policy,
  requireScope,
  resource,
  scopes,
  type FleetActor,
  type FleetScope,
} from "./model";

export interface Installation extends Record<string, unknown> {
  id: string;
  external_key: string;
  app_id: string;
  package_id: string;
  runtime_id: string;
  status: string;
  desired_state: string;
  next_package_id: string | null;
}

export class FleetRepository {
  constructor(readonly db: SQLPool) {}
  transaction<T>(action: (tx: TransactionClient) => Promise<T>): Promise<T> {
    return withTransaction(this.db, action);
  }
  async create(owner: string, name: unknown, maxApps: unknown) {
    if (typeof name !== "string" || !/^[a-z][a-z0-9-]{0,62}$/.test(name))
      throw new FleetError("invalid_name");
    if (maxApps !== null) integer(maxApps, 1, 10000);
    return this.transaction(async (tx) => {
      await assertAppQuotaAdmission(tx, owner);
      const result = await tx.query(
        `INSERT INTO cantelop_control.fleets(id,owner_user_id,name,max_apps)
        VALUES($1,$2,$3,$4) RETURNING *`,
        [resource("flt"), owner, name, maxApps],
      );
      return result.rows[0];
    });
  }
  async updateCapacity(owner: string, fleet: string, maxApps: unknown) {
    identifier(fleet, "flt");
    if (maxApps !== null) integer(maxApps, 1, 10000);
    const result = await this.db.query(
      `UPDATE cantelop_control.fleets f SET max_apps=$3
      FROM cantelop_control.users u WHERE f.id=$1 AND f.owner_user_id=$2
        AND u.id=f.owner_user_id AND u.status='active' RETURNING f.*`,
      [fleet, owner, maxApps],
    );
    if (!result.rowCount) throw new FleetError("not_found", 404);
    return result.rows[0];
  }
  async principal(
    owner: string,
    fleet: string,
    input: { label: unknown; scopes: unknown; expiresAt: unknown },
  ) {
    identifier(fleet, "flt");
    if (
      typeof input.label !== "string" ||
      !input.label.trim() ||
      input.label.length > 128 ||
      !Array.isArray(input.scopes) ||
      !input.scopes.length ||
      new Set(input.scopes).size !== input.scopes.length ||
      input.scopes.some((s) => !scopes.includes(s)) ||
      typeof input.expiresAt !== "string" ||
      Date.parse(input.expiresAt) <= Date.now() ||
      Date.parse(input.expiresAt) > Date.now() + 366 * 86400_000 ||
      !Number.isFinite(Date.parse(input.expiresAt))
    )
      throw new FleetError("invalid_principal");
    const id = resource("fsp"),
      secret = randomBytes(32).toString("hex");
    const result = await this.db.query(
      `INSERT INTO cantelop_control.fleet_principals(id,fleet_id,label,secret_hash,scopes,expires_at)
      SELECT $1,id,$4,$5,$6::jsonb,$7 FROM cantelop_control.fleets WHERE id=$2 AND owner_user_id=$3 RETURNING id`,
      [id, fleet, owner, input.label, hash(secret), JSON.stringify(input.scopes), input.expiresAt],
    );
    if (!result.rowCount) throw new FleetError("not_found", 404);
    return { id, token: `${id}.${secret}`, expiresAt: input.expiresAt };
  }
  async revoke(owner: string, fleet: string, principal: string) {
    const result = await this.db.query(
      `UPDATE cantelop_control.fleet_principals p SET revoked_at=now()
      FROM cantelop_control.fleets f WHERE p.fleet_id=f.id AND f.id=$1 AND f.owner_user_id=$2 AND p.id=$3 RETURNING p.id`,
      [fleet, owner, principal],
    );
    if (!result.rowCount) throw new FleetError("not_found", 404);
  }
  async authenticate(authorization: string | null, fleet: string): Promise<FleetActor> {
    const match = /^Bearer (fsp_[a-f0-9]{32})\.([a-f0-9]{64})$/.exec(authorization ?? "");
    if (!match) throw new FleetError("unauthorized", 401);
    const result = await this.db.query<{
      id: string;
      fleet_id: string;
      owner_user_id: string;
      secret_hash: string;
      scopes: FleetScope[];
    }>(
      `SELECT p.id,p.fleet_id,f.owner_user_id,p.secret_hash,p.scopes
      FROM cantelop_control.fleet_principals p JOIN cantelop_control.fleets f ON f.id=p.fleet_id
      JOIN cantelop_control.users u ON u.id=f.owner_user_id
      WHERE p.id=$1 AND p.fleet_id=$2 AND p.revoked_at IS NULL AND p.expires_at>now() AND u.status='active'`,
      [match[1], fleet],
    );
    const row = result.rows[0];
    if (!row || !timingSafeEqual(Buffer.from(hash(match[2])), Buffer.from(row.secret_hash)))
      throw new FleetError("unauthorized", 401);
    return { id: row.id, fleetId: row.fleet_id, ownerUserId: row.owner_user_id, scopes: row.scopes };
  }
  async audit(tx: Pick<TransactionClient, "query">, actor: FleetActor, action: string, target: string) {
    await tx.query(
      `INSERT INTO cantelop_control.fleet_audit(fleet_id,actor_id,action,target) VALUES($1,$2,$3,$4)`,
      [actor.fleetId, actor.id, action, target],
    );
  }
  async publish(actor: FleetActor, releaseId: unknown, version: unknown) {
    requireScope(actor, "packages:write");
    identifier(releaseId, "rel");
    if (typeof version !== "string" || !/^\d+\.\d+\.\d+(?:-[a-z0-9.-]+)?$/.test(version))
      throw new FleetError("invalid_version");
    return this.transaction(async (tx) => {
      await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, [
        `${actor.fleetId}:publish:${version}`,
      ]);
      const source = await tx.query(
        `SELECT r.api_artifact_id,r.session_runtime_artifact_id,aa.manifest AS api,ra.manifest AS runtime,rc.output AS prepared
        FROM cantelop_control.releases r JOIN cantelop_control.api_artifacts aa ON aa.id=r.api_artifact_id
        JOIN cantelop_control.session_runtime_artifacts ra ON ra.id=r.session_runtime_artifact_id
        LEFT JOIN cantelop_control.release_components rc ON rc.release_id=r.id AND rc.kind='session_runtime' AND rc.status='succeeded'
        WHERE r.id=$1 AND r.owner_user_id=$2 AND r.status IN ('active','superseded')`,
        [releaseId, actor.ownerUserId],
      );
      const row = source.rows[0];
      if (!row) throw new FleetError("not_found", 404);
      const api = parseProtocolEdgeManifest(row.api),
        runtime = parseSessionRuntimeManifest(row.runtime);
      validateArtifactPair(api, runtime);
      const shared = api.fleet_execution_protocol_version === 1;
      const prepared = row.prepared as Record<string, unknown> | null;
      if (shared && (!prepared || typeof prepared.prepared_manifest_key !== "string" ||
          !/^prepared-images\/manifests\/[0-9a-f]{64}\.json$/.test(prepared.prepared_manifest_key) ||
          typeof prepared.prepared_manifest_digest !== "string" || !/^sha256:[0-9a-f]{64}$/.test(prepared.prepared_manifest_digest))) {
        throw new FleetError("package_runtime_not_prepared", 409);
      }
      const old = await tx.query(
        `SELECT * FROM cantelop_control.fleet_packages WHERE fleet_id=$1 AND name=$2 AND version=$3`,
        [actor.fleetId, api.app_name, version],
      );
      if (old.rows[0]) {
        if (
          old.rows[0].api_artifact_id !== row.api_artifact_id ||
          old.rows[0].runtime_artifact_id !== row.session_runtime_artifact_id
        )
          throw new FleetError("package_version_conflict", 409);
        return old.rows[0];
      }
      const result = await tx.query(
        `INSERT INTO cantelop_control.fleet_packages(id,fleet_id,name,version,runtime_id,api_artifact_id,runtime_artifact_id,prepared_manifest_key,prepared_manifest_digest)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *`,
        [
          resource("pkg"),
          actor.fleetId,
          api.app_name,
          version,
          api.session_runtime_id,
          row.api_artifact_id,
          row.session_runtime_artifact_id,
          shared ? prepared!.prepared_manifest_key : null,
          shared ? prepared!.prepared_manifest_digest : null,
        ],
      );
      await this.audit(tx, actor, "package.publish", String(result.rows[0]!.id));
      return result.rows[0];
    });
  }
  async package(actor: FleetActor, id: string, scope: FleetScope = "installations:read") {
    requireScope(actor, scope);
    identifier(id, "pkg");
    const result = await this.db.query(
      `SELECT id,name,version,runtime_id,disabled_at,
      CASE WHEN prepared_manifest_key IS NULL THEN 'legacy' WHEN edge_ready_at IS NULL THEN 'preparing' ELSE 'ready' END AS execution
      FROM cantelop_control.fleet_packages WHERE id=$1 AND fleet_id=$2`,
      [id, actor.fleetId],
    );
    if (!result.rows[0]) throw new FleetError("not_found", 404);
    return result.rows[0];
  }
  async install(actor: FleetActor, externalKey: string, packageId: unknown) {
    requireScope(actor, "installations:write");
    identifier(packageId, "pkg");
    if (!/^[A-Za-z0-9_.:-]{16,128}$/.test(externalKey)) throw new FleetError("invalid_installation_key");
    const id = await this.transaction(async (tx) => {
      await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, [
        `${actor.fleetId}:${externalKey}`,
      ]);
      if (
        (
          await tx.query(
            `SELECT 1 FROM cantelop_control.fleet_installation_tombstones WHERE fleet_id=$1 AND external_key=$2`,
            [actor.fleetId, externalKey],
          )
        ).rowCount
      )
        throw new FleetError("installation_deleted", 409);
      const existing = await tx.query(
        `SELECT id,package_id FROM cantelop_control.fleet_installations WHERE fleet_id=$1 AND external_key=$2`,
        [actor.fleetId, externalKey],
      );
      if (existing.rows[0]) {
        if (existing.rows[0].package_id !== packageId) throw new FleetError("installation_conflict", 409);
        return String(existing.rows[0].id);
      }
      // Only a finite cap needs installs serialized for its count; unlimited
      // Fleets take a key-share lock so concurrent installs proceed together.
      // Pick the mode before locking: upgrading a shared lock held by
      // concurrent installs deadlocks. Only a racing cap change upgrades.
      const lockFleet = (mode: string) =>
        tx.query(`SELECT max_apps FROM cantelop_control.fleets WHERE id=$1 FOR ${mode}`, [actor.fleetId]);
      const capped = (await tx.query(`SELECT max_apps FROM cantelop_control.fleets WHERE id=$1`, [actor.fleetId]))
        .rows[0]!.max_apps !== null;
      let fleet = await lockFleet(capped ? "UPDATE" : "KEY SHARE");
      if (!capped && fleet.rows[0]!.max_apps !== null) fleet = await lockFleet("UPDATE");
      if (fleet.rows[0]!.max_apps !== null) {
        const count = await tx.query(
          `SELECT count(*)::integer AS total FROM cantelop_control.fleet_installations i JOIN cantelop_control.apps a ON a.id=i.app_id WHERE i.fleet_id=$1 AND (a.status<>'archived' OR EXISTS(SELECT 1 FROM cantelop_control.operations o WHERE o.app_id=i.app_id AND o.kind='purge_workspace' AND o.status<>'succeeded'))`,
          [actor.fleetId],
        );
        if (Number(count.rows[0]!.total) >= Number(fleet.rows[0]!.max_apps))
          throw new FleetError("quota_exceeded", 429);
      }
      const pkg = await tx.query(
        `SELECT id FROM cantelop_control.fleet_packages WHERE id=$1 AND fleet_id=$2 AND disabled_at IS NULL`,
        [packageId, actor.fleetId],
      );
      if (!pkg.rowCount) throw new FleetError("not_found", 404);
      const installation = resource("ins");
      const app = await new ProductRepository(this.db).createAppInTransaction(tx, actor.ownerUserId, {
        slug: `j-${randomBytes(16).toString("hex")}`,
        sessionRuntime: { internalPort: platformInternalPort },
      }, actor.fleetId);
      await tx.query(
        `UPDATE cantelop_control.operations SET input=input||'{"managed":true}'::jsonb WHERE app_id=$1 AND kind='register_app'`,
        [app.id],
      );
      await tx.query(
        `INSERT INTO cantelop_control.fleet_installations(id,fleet_id,external_key,app_id,package_id) VALUES($1,$2,$3,$4,$5)`,
        [installation, actor.fleetId, externalKey, app.id, packageId],
      );
      await tx.query(`INSERT INTO cantelop_control.fleet_policies(app_id,policy) VALUES($1,$2::jsonb)`, [
        app.id,
        JSON.stringify({
          active: false,
          expiresAt: new Date().toISOString(),
          maxSandboxes: 0,
          maxVcpus: 0,
          maxMemoryMib: 0,
          maxWorkspaces: 0,
          maxStorageBytes: 0,
        }),
      ]);
      await this.audit(tx, actor, "installation.create", installation);
      return installation;
    });
    return this.installation(actor, id, "installations:write");
  }
  async deleteByKey(actor: FleetActor, key: string) {
    requireScope(actor, "installations:write");
    if (!/^[A-Za-z0-9_.:-]{16,128}$/.test(key)) throw new FleetError("invalid_installation_key");
    await this.transaction(async (tx) => {
      await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, [`${actor.fleetId}:${key}`]);
      await tx.query(
        `INSERT INTO cantelop_control.fleet_installation_tombstones(fleet_id,external_key) VALUES($1,$2) ON CONFLICT DO NOTHING`,
        [actor.fleetId, key],
      );
      await tx.query(
        `UPDATE cantelop_control.fleet_installations SET desired_state='deleted',updated_at=now() WHERE fleet_id=$1 AND external_key=$2`,
        [actor.fleetId, key],
      );
      await this.audit(tx, actor, "installation.delete-key", key);
    });
  }
  async installation(actor: FleetActor, id: string, scope: FleetScope = "installations:read") {
    requireScope(actor, scope);
    identifier(id, "ins");
    const result = await this.db.query<Installation>(
      `SELECT ${installationColumns} ${installationSource} WHERE i.id=$1 AND i.fleet_id=$2`,
      [id, actor.fleetId],
    );
    const row = result.rows[0];
    if (!row) throw new FleetError("not_found", 404);
    return { ...row, status: installationStatus(row) };
  }
  // Owner reads for the console. Each joins the owning user, so a foreign or
  // unknown Fleet reads as not_found, as updateCapacity does.
  async listForOwner(owner: string) {
    return (await this.db.query(`SELECT ${fleetSummary} FROM cantelop_control.fleets f WHERE f.owner_user_id=$1 ORDER BY f.created_at,f.id`, [owner])).rows;
  }
  async fleetForOwner(owner: string, fleet: string) {
    identifier(fleet, "flt");
    const row = (
      await this.db.query(`SELECT ${fleetSummary} FROM cantelop_control.fleets f WHERE f.id=$1 AND f.owner_user_id=$2`, [fleet, owner])
    ).rows[0];
    if (!row) throw new FleetError("not_found", 404);
    return row;
  }
  async principalsForOwner(owner: string, fleet: string) {
    await this.fleetForOwner(owner, fleet);
    return (
      await this.db.query(
        `SELECT id,label,scopes,expires_at,revoked_at,created_at FROM cantelop_control.fleet_principals WHERE fleet_id=$1 ORDER BY created_at DESC,id`,
        [fleet],
      )
    ).rows;
  }
  async packagesForOwner(owner: string, fleet: string) {
    await this.fleetForOwner(owner, fleet);
    return (
      await this.db.query(
        `SELECT id,name,version,runtime_id,disabled_at,created_at FROM cantelop_control.fleet_packages WHERE fleet_id=$1 ORDER BY created_at DESC,id`,
        [fleet],
      )
    ).rows;
  }
  /** Oldest first, keyed on the last installation id of the previous page.
   *  Installations whose App is archived and purged are left out. */
  async installationsForOwner(owner: string, fleet: string, page: { after?: string; limit?: number } = {}) {
    await this.fleetForOwner(owner, fleet);
    const limit = integer(page.limit ?? 50, 1, 200);
    if (page.after !== undefined) identifier(page.after, "ins");
    const rows = (
      await this.db.query<Installation>(
        `SELECT ${installationColumns},fp.policy,i.created_at,i.updated_at ${installationSource}
        WHERE i.fleet_id=$1 AND NOT (i.desired_state='deleted' AND a.status='archived' AND ${purgeSettled})
          AND ($2::text IS NULL OR (i.created_at,i.id)>(SELECT created_at,id FROM cantelop_control.fleet_installations WHERE id=$2 AND fleet_id=$1))
        ORDER BY i.created_at,i.id LIMIT $3`,
        [fleet, page.after ?? null, limit + 1],
      )
    ).rows;
    const items = rows.slice(0, limit).map((row) => ({ ...row, status: installationStatus(row) }));
    return { items, after: rows.length > limit ? items.at(-1)!.id : null };
  }
  async installationForOwner(owner: string, fleet: string, id: string) {
    await this.fleetForOwner(owner, fleet);
    identifier(id, "ins");
    const row = (
      await this.db.query<Installation>(
        `SELECT ${installationColumns},fp.policy,i.created_at,i.updated_at,np.version AS next_version ${installationSource}
        LEFT JOIN cantelop_control.fleet_packages np ON np.id=i.next_package_id WHERE i.id=$1 AND i.fleet_id=$2`,
        [id, fleet],
      )
    ).rows[0];
    if (!row) throw new FleetError("not_found", 404);
    return { ...row, status: installationStatus(row) };
  }
  async updatePolicy(
    actor: FleetActor,
    id: string,
    input: { expectedRevision: unknown; policy: unknown; sourceFence?: unknown },
  ) {
    requireScope(actor, "policy:write");
    const next = policy(input.policy);
    integer(input.expectedRevision, 1, 2147483646);
    return this.transaction(async (tx) => {
      const target = await this.installation(actor, id, "policy:write");
      if (target.desired_state !== "ready") throw new FleetError("installation_closed", 409);
      const fence = input.sourceFence;
      if (
        fence !== undefined &&
        (typeof fence !== "string" ||
          !/^\d{1,19}$/.test(fence) ||
          BigInt(fence) > BigInt("9223372036854775807"))
      )
        throw new FleetError("invalid_policy_fence");
      const current = (
        await tx.query(
          `SELECT revision,enforced_revision,source_fence::text,policy FROM cantelop_control.fleet_policies WHERE app_id=$1 FOR UPDATE`,
          [target.app_id],
        )
      ).rows[0]!;
      if (fence !== undefined && BigInt(fence as string) <= BigInt(String(current.source_fence))) {
        const same =
          JSON.stringify(Object.entries(next).sort()) ===
          JSON.stringify(Object.entries(current.policy as object).sort());
        if (BigInt(fence as string) === BigInt(String(current.source_fence)) && same)
          return { revision: current.revision, enforced_revision: current.enforced_revision };
        throw new FleetError("stale_policy_projection", 409);
      }
      const result = await tx.query(
        `UPDATE cantelop_control.fleet_policies SET revision=revision+1,policy=$3::jsonb,source_fence=coalesce($4::bigint,source_fence),updated_at=now()
        WHERE app_id=$1 AND revision=$2 RETURNING revision,enforced_revision`,
        [target.app_id, input.expectedRevision, JSON.stringify(next), fence ?? null],
      );
      if (!result.rowCount) throw new FleetError("policy_revision_conflict", 409);
      await this.audit(tx, actor, "policy.update", id);
      return result.rows[0];
    });
  }
}
const purgeSettled = `NOT EXISTS(SELECT 1 FROM cantelop_control.operations o WHERE o.app_id=i.app_id AND o.kind='purge_workspace' AND o.status<>'succeeded')`;
const installationColumns = `i.id,i.external_key,i.app_id,i.package_id,i.next_package_id,i.release_id,i.desired_state,i.error_code,
      p.runtime_id,p.version,a.status AS app_status,${purgeSettled} AS purged,a.active_release_id,r.status AS release_status,fp.revision AS policy_revision,fp.enforced_revision`;
const installationSource = `FROM cantelop_control.fleet_installations i JOIN cantelop_control.fleet_packages p ON p.id=i.package_id
      JOIN cantelop_control.apps a ON a.id=i.app_id LEFT JOIN cantelop_control.releases r ON r.id=i.release_id
      JOIN cantelop_control.fleet_policies fp ON fp.app_id=i.app_id`;
// used_apps counts what a finite cap counts in install(): live Apps and those
// still purging.
const fleetSummary = `f.id,f.name,f.max_apps,f.created_at,
  (SELECT count(*)::integer FROM cantelop_control.fleet_installations i JOIN cantelop_control.apps a ON a.id=i.app_id
    WHERE i.fleet_id=f.id AND (a.status<>'archived' OR NOT ${purgeSettled})) AS used_apps,
  (SELECT count(*)::integer FROM cantelop_control.fleet_principals p WHERE p.fleet_id=f.id AND p.revoked_at IS NULL AND p.expires_at>now()) AS active_principals`;

export type InstallationStatus = "deleted" | "deleting" | "suspended" | "upgrading" | "failed" | "ready" | "provisioning";
export function installationStatus(row: {
  desired_state: unknown;
  app_status?: unknown;
  purged?: unknown;
  next_package_id?: unknown;
  error_code?: unknown;
  release_status?: unknown;
  active_release_id?: unknown;
  release_id?: unknown;
}): InstallationStatus {
  return row.desired_state === "deleted"
    ? row.app_status === "archived" && row.purged
      ? "deleted"
      : "deleting"
    : row.desired_state === "suspended"
      ? "suspended"
      : row.next_package_id
        ? "upgrading"
        : row.error_code || row.release_status === "failed" || row.release_status === "cancelled"
          ? "failed"
          : row.release_status === "active" && row.active_release_id === row.release_id
            ? "ready"
            : "provisioning";
}
function hash(value: string) {
  return createHash("sha256").update(value).digest("hex");
}
