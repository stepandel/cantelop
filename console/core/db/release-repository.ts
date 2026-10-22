import {
  canonical,
  parseSessionRuntimeManifest,
  validateArtifactPair,
  type SessionRuntimeManifest,
} from "../api-deployments/protocol-artifact";
import { appDefinitionSummary, type AppDefinitionSummary } from "../api-deployments/app-definition";
import { and, asc, eq, sql } from "drizzle-orm";
import { alias } from "drizzle-orm/pg-core";

import {
  apiArtifactRoutes,
  isSDKOneEdgeManifest,
  parseAPIArtifactManifest,
  sameAPIArtifactManifest,
  validAPIArtifactManifest,
  type APIArtifactManifest,
  type APIRoute,
} from "../api-deployments/artifact";
import { ControlError } from "../domain/errors";
import { assertResourceID, newResourceID } from "../domain/ids";
import type { ReleaseComponentKind, ReleaseComponentStatus, ReleaseStatus } from "../domain/model";
import { appAccessibleSQL, lockOwnedApp } from "./app-access";
import { controlQueryBuilder, executeDrizzle, withTransaction, type SQLClient, type SQLPool, type TransactionClient } from "./database";
import { apiArtifacts, sessionRuntimeArtifacts, releaseComponents, releases, operations } from "./schema";
import { allocateReleaseGeneration } from "./release-generation";

const idempotencyKeyPattern = /^[A-Za-z0-9_.:-]{16,128}$/;
const workerIDPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/;
const errorCodePattern = /^[a-z][a-z0-9_]{0,63}$/;
const digestPattern = /^sha256:[0-9a-f]{64}$/;
const sessionRuntimeMediaType = "application/vnd.cantelop.session-runtime.image.v1.tar+gzip";
const maxComponentOutputBytes = 128 << 10;

export interface ReleaseAPIArtifact {
  digest: string;
  objectKey: string;
  sizeBytes: number;
  manifest: APIArtifactManifest;
}

export interface ReleaseSessionRuntimeArtifact {
  digest: string;
  objectKey: string;
  sizeBytes: number;
  mediaType: typeof sessionRuntimeMediaType;
  manifest?: SessionRuntimeManifest;
}

export interface ReleaseComponent {
  kind: ReleaseComponentKind;
  operationID: string;
  status: ReleaseComponentStatus;
  attempts?: number;
  progress?: string;
  updatedAt?: Date;
  nextAttemptAt?: Date;
  output?: Record<string, unknown>;
  errorCode?: string;
}

export interface ActiveReleaseSummary {
  appID: string;
  version: number;
  activatedAt?: Date;
}

/** The routes the App's active release declares; null routes when it declares none. */
export interface ActiveReleaseRoutes {
  releaseID: string;
  version: number;
  // Null when the live release predates SDK 1.0 and has no generated ingress.
  routes: APIRoute[] | null;
}

export interface ProductRelease {
  id: string;
  appID: string;
  version: number;
  status: ReleaseStatus;
  apiArtifactDigest: string;
  sessionRuntimeArtifactDigest: string;
  components: Readonly<Record<ReleaseComponentKind, ReleaseComponent>>;
  errorCode?: string;
  createdAt: Date;
  updatedAt: Date;
  activatedAt?: Date;
  /** Absent only for releases that predate SDK 1.0; they can no longer be activated. */
  definition?: AppDefinitionSummary;
}

interface ReleaseRow extends Record<string, unknown> {
  id: string;
  app_id: string;
  version: number;
  status: ReleaseStatus;
  api_artifact_digest: string;
  session_runtime_artifact_digest: string;
  api_operation_id: string;
  api_status: ReleaseComponentStatus;
  api_output: Record<string, unknown> | null;
  api_error_code: string | null;
  api_attempts: number;
  api_progress: string | null;
  api_updated_at: Date;
  api_next_attempt_at: Date | null;
  session_runtime_operation_id: string;
  session_runtime_status: ReleaseComponentStatus;
  session_runtime_output: Record<string, unknown> | null;
  session_runtime_error_code: string | null;
  session_runtime_attempts: number;
  session_runtime_progress: string | null;
  session_runtime_updated_at: Date;
  session_runtime_next_attempt_at: Date | null;
  error_code: string | null;
  created_at: Date;
  updated_at: Date;
  activated_at: Date | null;
  api_manifest?: unknown;
  session_runtime_manifest?: unknown;
  api_object_key?: string;
  api_size_bytes?: number;
}

const apiComponent = alias(releaseComponents, "api_component");
const sessionRuntimeComponent = alias(releaseComponents, "session_runtime_component");
const apiOperation = alias(operations, "api_operation");
const runtimeOperation = alias(operations, "runtime_operation");
const releaseSelection = {
  id: releases.id,
  app_id: releases.appId,
  version: releases.version,
  status: releases.status,
  api_artifact_digest: sql<string>`${apiArtifacts.digest}`.as("api_artifact_digest"),
  session_runtime_artifact_digest: sql<string>`${sessionRuntimeArtifacts.digest}`.as("session_runtime_artifact_digest"),
  api_operation_id: sql<string>`${apiComponent.operationId}`.as("api_operation_id"),
  api_status: sql<ReleaseComponentStatus>`${apiComponent.status}`.as("api_status"),
  api_output: sql<Record<string, unknown> | null>`${apiComponent.output}`.as("api_output"),
  api_progress: sql<string | null>`${apiOperation.progress}`.as("api_progress"),
  api_attempts: sql<number>`${apiOperation.attempts}`.as("api_attempts"),
  api_updated_at: sql<Date>`${apiOperation.updatedAt}`.as("api_updated_at"),
  api_next_attempt_at: sql<Date | null>`CASE WHEN ${apiOperation.status} = 'queued' THEN ${apiOperation.availableAt} END`.as("api_next_attempt_at"),
  api_error_code: sql<string | null>`${apiComponent.errorCode}`.as("api_error_code"),
  session_runtime_operation_id: sql<string>`${sessionRuntimeComponent.operationId}`.as("session_runtime_operation_id"),
  session_runtime_status: sql<ReleaseComponentStatus>`${sessionRuntimeComponent.status}`.as("session_runtime_status"),
  session_runtime_output: sql<Record<string, unknown> | null>`${sessionRuntimeComponent.output}`.as("session_runtime_output"),
  session_runtime_progress: sql<string | null>`${runtimeOperation.progress}`.as("session_runtime_progress"),
  session_runtime_attempts: sql<number>`${runtimeOperation.attempts}`.as("session_runtime_attempts"),
  session_runtime_updated_at: sql<Date>`${runtimeOperation.updatedAt}`.as("session_runtime_updated_at"),
  session_runtime_next_attempt_at: sql<Date | null>`CASE WHEN ${runtimeOperation.status} = 'queued' THEN ${runtimeOperation.availableAt} END`.as("session_runtime_next_attempt_at"),
  session_runtime_error_code: sql<string | null>`${sessionRuntimeComponent.errorCode}`.as("session_runtime_error_code"),
  error_code: releases.errorCode,
  created_at: releases.createdAt,
  updated_at: releases.updatedAt,
  activated_at: releases.activatedAt,
  api_manifest: sql<unknown>`${apiArtifacts.manifest}`.as("api_manifest"),
  session_runtime_manifest: sql<unknown>`${sessionRuntimeArtifacts.manifest}`.as("session_runtime_manifest"),
  // Idempotent replays compare the stored upload object with the request.
  api_object_key: sql<string>`${apiArtifacts.objectKey}`.as("api_object_key"),
  api_size_bytes: sql<number>`${apiArtifacts.sizeBytes}`.as("api_size_bytes"),
};

/** The one release read: transactional callers pass their client, others the pool. */
function releaseQuery(client: SQLClient) {
  return controlQueryBuilder(client).select(releaseSelection)
    .from(releases)
    .innerJoin(apiArtifacts, eq(apiArtifacts.id, releases.apiArtifactId))
    .innerJoin(sessionRuntimeArtifacts, eq(sessionRuntimeArtifacts.id, releases.sessionRuntimeArtifactId))
    .innerJoin(apiComponent, and(eq(apiComponent.releaseId, releases.id), eq(apiComponent.kind, "api")))
    .innerJoin(sessionRuntimeComponent, and(eq(sessionRuntimeComponent.releaseId, releases.id), eq(sessionRuntimeComponent.kind, "session_runtime")))
    .innerJoin(apiOperation, eq(apiOperation.id, apiComponent.operationId))
    .innerJoin(runtimeOperation, eq(runtimeOperation.id, sessionRuntimeComponent.operationId));
}

export class ReleaseRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  async activeReleaseCandidates(): Promise<Array<{ ownerUserID: string; appID: string; releaseID: string }>> {
    const result = await this.database.query<{ owner_user_id: string; id: string; active_release_id: string }>(`SELECT owner_user_id, id, active_release_id
FROM cantelop_control.apps WHERE status = 'active' AND active_release_id IS NOT NULL ORDER BY id`);
    return result.rows.map((row) => ({ ownerUserID: row.owner_user_id, appID: row.id, releaseID: row.active_release_id }));
  }

  async queueActiveReconciliation(ownerUserID: string, appID: string, releaseID: string): Promise<boolean> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    return withTransaction(this.database, async (client) => {
      const app = await client.query<{ active_release_id: string | null }>(`SELECT active_release_id
FROM cantelop_control.apps WHERE id = $1 AND owner_user_id = $2 AND status = 'active' FOR UPDATE`,
      [appID, ownerUserID]);
      if (app.rowCount !== 1 || app.rows[0].active_release_id !== releaseID) return false;
      if (await archivePending(client, appID)) return false;
      const pending = await client.query(`SELECT 1 FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'activate_release' AND status IN ('queued', 'running') LIMIT 1`, [appID]);
      if (pending.rowCount !== 0) return false;
      await enqueueActivation(client, ownerUserID, appID, releaseID, releaseID, this.now());
      return true;
    });
  }

  async begin(
    ownerUserID: string,
    appID: string,
    apiArtifact: ReleaseAPIArtifact,
    sessionRuntimeArtifact: ReleaseSessionRuntimeArtifact,
    idempotencyKey: string,
    uploadID?: string,
    packageID?: string,
  ): Promise<ProductRelease> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    validateAPIArtifact(apiArtifact);
    validateSessionRuntimeArtifact(sessionRuntimeArtifact);
    validateArtifactPair(apiArtifact.manifest, parseSessionRuntimeManifest(sessionRuntimeArtifact.manifest));
    if (!idempotencyKeyPattern.test(idempotencyKey)) throw new TypeError("invalid release idempotency key");
    if (uploadID) assertResourceID("deploymentUpload", uploadID);

    const finalizeUpload = async (client: TransactionClient, releaseID: string) => {
      if (!uploadID) return;
      // The upload transition and all release/operation rows commit together.
      // Cancellation winning first makes this fail and rolls back the release.
      const result = await client.query(`UPDATE cantelop_control.deployment_uploads
SET status = 'finalized', release_id = $4, updated_at = $5
WHERE id = $1 AND owner_user_id = $2 AND app_id = $3 AND idempotency_key = $6
  AND api_digest = $7 AND session_runtime_digest = $8
  AND ((status = 'uploading' AND expires_at > $5) OR (status = 'finalized' AND release_id = $4))`,
      [uploadID, ownerUserID, appID, releaseID, this.now(), idempotencyKey, apiArtifact.digest, sessionRuntimeArtifact.digest]);
      if (result.rowCount !== 1) throw new ControlError("conflict");
    };

    return withTransaction(this.database, async (client) => {
      const replay = await executeDrizzle(client, releaseQuery(client).where(and(
        eq(releases.ownerUserId, ownerUserID),
        eq(releases.idempotencyKey, idempotencyKey),
      )));
      if (replay.rowCount === 1) {
        const saved = replay.rows[0];
        const release = mapRelease(saved);
        if (release.appID !== appID || release.apiArtifactDigest !== apiArtifact.digest ||
            release.sessionRuntimeArtifactDigest !== sessionRuntimeArtifact.digest ||
            saved.api_object_key !== apiArtifact.objectKey || saved.api_size_bytes !== apiArtifact.sizeBytes ||
            !sameAPIArtifactManifest(saved.api_manifest, apiArtifact.manifest) ||
            canonical(saved.session_runtime_manifest ?? null) !== canonical(sessionRuntimeArtifact.manifest ?? null)) {
          throw new ControlError("conflict");
        }
        await finalizeUpload(client, release.id);
        return release;
      }
      if (replay.rowCount !== 0) throw new Error("release replay returned an invalid row count");

      await lockReleasableApp(client, ownerUserID, appID);
      if (packageID) {
        const bound = await client.query(`SELECT 1 FROM cantelop_control.fleet_installations i
JOIN cantelop_control.fleet_packages p ON p.id=i.package_id AND p.fleet_id=i.fleet_id
WHERE i.app_id=$1 AND i.package_id=$2 AND i.desired_state='ready' AND p.disabled_at IS NULL
  AND p.edge_ready_at IS NOT NULL AND p.prepared_manifest_key IS NOT NULL
  AND p.api_artifact_id IN (SELECT id FROM cantelop_control.api_artifacts WHERE object_key=$3 AND digest=$4)
  AND p.runtime_artifact_id IN (SELECT id FROM cantelop_control.session_runtime_artifacts WHERE object_key=$5 AND digest=$6)`,
        [appID, packageID, apiArtifact.objectKey, apiArtifact.digest, sessionRuntimeArtifact.objectKey, sessionRuntimeArtifact.digest]);
        if (bound.rowCount !== 1) throw new ControlError("conflict");
      }
      const pending = await client.query(`SELECT 1 FROM cantelop_control.releases
WHERE app_id = $1 AND status IN ('queued', 'provisioning', 'cancelling')`, [appID]);
      if (pending.rowCount !== 0) throw new ControlError("conflict");
      const activating = await client.query(`SELECT 1 FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'activate_release' AND status IN ('queued', 'running')`, [appID]);
      if (activating.rowCount !== 0) throw new ControlError("conflict");

      const at = this.now();
      const apiArtifactID = await storeAPIArtifact(client, apiArtifact, at);
      const sessionRuntimeArtifactID = await storeSessionRuntimeArtifact(client, sessionRuntimeArtifact, at);
      const versionResult = await client.query<{ version: number }>(
        `SELECT coalesce(max(version), 0) + 1 AS version FROM cantelop_control.releases WHERE app_id = $1`, [appID],
      );
      if (versionResult.rowCount !== 1) throw new Error("allocate release version returned an invalid row count");

      const releaseID = newResourceID("release");
      const apiOperationID = newResourceID("operation");
      const sessionRuntimeOperationID = newResourceID("operation");
      await client.query(`INSERT INTO cantelop_control.releases
  (id, owner_user_id, app_id, api_artifact_id, session_runtime_artifact_id, version, status,
   idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7, $8, $8)`, [
        releaseID, ownerUserID, appID, apiArtifactID, sessionRuntimeArtifactID,
        versionResult.rows[0].version, idempotencyKey, at,
      ]);
      await insertComponentOperation(client, {
        id: apiOperationID, ownerUserID, appID, kind: packageID ? "attach_package" : "deploy_api", releaseID,
        packageID, component: "api",
        idempotencyKey: `${releaseID}:api`, artifactID: apiArtifactID,
        objectKey: apiArtifact.objectKey, digest: apiArtifact.digest, at,
      });
      await insertComponentOperation(client, {
        id: sessionRuntimeOperationID, ownerUserID, appID, kind: packageID ? "attach_package" : "materialize_build", releaseID,
        packageID, component: "session_runtime",
        idempotencyKey: `${releaseID}:session-runtime`, artifactID: sessionRuntimeArtifactID,
        objectKey: sessionRuntimeArtifact.objectKey, digest: sessionRuntimeArtifact.digest,
        sizeBytes: sessionRuntimeArtifact.sizeBytes, at,
      });
      await client.query(`INSERT INTO cantelop_control.release_components
  (release_id, kind, operation_id, status, created_at, updated_at)
VALUES ($1, 'api', $2, 'queued', $4, $4),
       ($1, 'session_runtime', $3, 'queued', $4, $4)`, [releaseID, apiOperationID, sessionRuntimeOperationID, at]);
      await finalizeUpload(client, releaseID);
      return requiredRelease(client, releaseID);
    });
  }

  async getForUser(ownerUserID: string, appID: string, releaseID: string): Promise<ProductRelease> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    const result = await executeDrizzle(this.database, releaseQuery(this.database).where(and(
      eq(releases.id, releaseID),
      eq(releases.appId, appID),
      eq(releases.ownerUserId, ownerUserID),
    )));
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return mapRelease(result.rows[0]);
  }

  async listForUser(ownerUserID: string, appID: string): Promise<ProductRelease[]> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    const result = await executeDrizzle(this.database, releaseQuery(this.database)
      .where(and(eq(releases.appId, appID), eq(releases.ownerUserId, ownerUserID)))
      .orderBy(asc(releases.version)));
    return result.rows.map(mapRelease);
  }

  /**
   * The live release of every App the user may operate, in one statement and
   * without manifests: what a card needs, not the whole release history.
   */
  async activeReleasesForUser(userID: string): Promise<ActiveReleaseSummary[]> {
    assertResourceID("user", userID);
    const result = await this.database.query<{ app_id: string; version: number; activated_at: Date | null }>(`SELECT r.app_id, r.version, r.activated_at
FROM cantelop_control.apps a
JOIN cantelop_control.users actor ON actor.id = $1
JOIN cantelop_control.releases r ON r.app_id = a.id AND r.owner_user_id = a.owner_user_id AND r.status = 'active'
WHERE a.status <> 'archived' AND actor.status = 'active'
  AND ${appAccessibleSQL("$1")}`, [userID]);
    return result.rows.map((row) => ({
      appID: row.app_id,
      version: row.version,
      ...(row.activated_at ? { activatedAt: row.activated_at } : {}),
    }));
  }

  async activeRoutesForUser(ownerUserID: string, appID: string): Promise<ActiveReleaseRoutes | null> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    const result = await executeDrizzle(this.database, controlQueryBuilder(this.database)
      .select({ id: releases.id, version: releases.version, manifest: apiArtifacts.manifest })
      .from(releases)
      .innerJoin(apiArtifacts, eq(apiArtifacts.id, releases.apiArtifactId))
      .where(and(eq(releases.appId, appID), eq(releases.ownerUserId, ownerUserID), eq(releases.status, "active"))));
    const row = result.rows[0];
    if (row === undefined) return null;
    return {
      releaseID: row.id,
      version: row.version,
      routes: isSDKOneEdgeManifest(row.manifest) ? apiArtifactRoutes(parseAPIArtifactManifest(row.manifest)) : null,
    };
  }

  async activeScript(appID: string): Promise<string> {
    assertResourceID("app", appID);
    const result = await this.database.query<{ release_id: string | null; output: Record<string, unknown> | null; pending_package_id?: string }>(`SELECT a.active_release_id AS release_id, c.output,
CASE WHEN p.prepared_manifest_key IS NOT NULL THEN p.id END AS pending_package_id
FROM cantelop_control.apps a
LEFT JOIN cantelop_control.release_components c ON c.release_id = a.active_release_id AND c.kind = 'api'
LEFT JOIN cantelop_control.fleet_installations i ON i.app_id=a.id
LEFT JOIN cantelop_control.fleet_packages p ON p.id=i.package_id
WHERE a.id = $1 AND a.status <> 'archived'`, [appID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    const row = result.rows[0];
    if (row.release_id === null) return row.pending_package_id ?? appID;
    const scriptName = row.output?.script_name;
    if (scriptName === row.release_id) return scriptName;
    if (typeof scriptName === "string" && /^pkg_[0-9a-f]{32}$/.test(scriptName) && row.output?.package_id === scriptName) return scriptName;
    // Only pre-1.0 releases ran under the App's stable script name.
    throw new ControlError("release_unsupported");
  }

  /** The release's generated Edge manifest, or null for a release that predates SDK 1.0. */
  async deploymentManifest(
    appID: string,
    releaseID: string,
  ): Promise<APIArtifactManifest | null> {
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    const result = await this.database.query<{ manifest: APIArtifactManifest }>(
      `SELECT artifact.manifest FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts artifact ON artifact.id=r.api_artifact_id
WHERE r.app_id=$1 AND r.id=$2`,
      [appID, releaseID],
    );
    if (result.rowCount !== 1) throw new ControlError("not_found");
    const manifest: unknown = result.rows[0].manifest;
    return isSDKOneEdgeManifest(manifest) ? parseAPIArtifactManifest(manifest) : null;
  }

  /** The release a rollback would reactivate now, without locking or enqueueing anything. */
  async rollbackTargetForUser(ownerUserID: string, appID: string): Promise<ProductRelease | null> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    return withTransaction(this.database, async (client) => {
      const target = await client.query<{ id: string }>(rollbackTargetQuery, [appID, ownerUserID]);
      return target.rowCount === 1 ? requiredRelease(client, target.rows[0].id) : null;
    });
  }

  async rollbackForUser(ownerUserID: string, appID: string, expectedTargetID?: string): Promise<ProductRelease> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    if (expectedTargetID !== undefined) assertResourceID("release", expectedTargetID);
    return withTransaction(this.database, async (client) => {
      const app = await client.query<{ active_release_id: string | null }>(`SELECT active_release_id
FROM cantelop_control.apps
WHERE id = $1 AND owner_user_id = $2 AND status <> 'archived'
FOR UPDATE`, [appID, ownerUserID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      if (await archivePending(client, appID)) throw new ControlError("conflict");
      const currentReleaseID = app.rows[0].active_release_id;
      if (!currentReleaseID) throw new ControlError("conflict");

      const pending = await client.query(`SELECT 1 FROM cantelop_control.releases
WHERE app_id = $1 AND status IN ('queued', 'provisioning', 'cancelling')`, [appID]);
      if (pending.rowCount !== 0) throw new ControlError("conflict");
      const activating = await client.query(`SELECT 1 FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'activate_release' AND status IN ('queued', 'running')`, [appID]);
      if (activating.rowCount !== 0) throw new ControlError("conflict");
      const target = await client.query<{ id: string }>(`${rollbackTargetQuery}
FOR UPDATE OF r`, [appID, ownerUserID]);
      if (target.rowCount !== 1) throw new ControlError("conflict");
      // The console names a specific version. Do not silently activate a
      // different one if another deployment finished after its last poll.
      if (expectedTargetID !== undefined && target.rows[0].id !== expectedTargetID) throw new ControlError("conflict");

      await enqueueActivation(client, ownerUserID, appID, target.rows[0].id, currentReleaseID, this.now());
      return requiredRelease(client, target.rows[0].id);
    });
  }

  async startComponent(operationID: string, workerID: string): Promise<ProductRelease> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID)) throw new TypeError("invalid release worker ID");
    return withTransaction(this.database, async (client) => {
      const at = this.now();
      const releaseID = await lockReleaseForOperation(client, operationID);
      const component = await client.query<{ release_id: string }>(`UPDATE cantelop_control.release_components c
SET status = 'running', updated_at = $3
FROM cantelop_control.operations o
WHERE c.operation_id = $1 AND o.id = c.operation_id
  AND c.status = 'queued' AND o.status = 'running' AND o.locked_by = $2
RETURNING c.release_id`, [operationID, workerID, at]);
      if (requiredValue(component, "start release component").release_id !== releaseID) throw new ControlError("conflict");
      const release = await client.query(`UPDATE cantelop_control.releases
SET status = 'provisioning', updated_at = $2
WHERE id = $1 AND status IN ('queued', 'provisioning')`, [releaseID, at]);
      if (release.rowCount !== 1) throw new ControlError("conflict");
      return requiredRelease(client, releaseID);
    });
  }

  async succeedComponent(
    operationID: string,
    workerID: string,
    rawOutput: Record<string, unknown>,
  ): Promise<ProductRelease> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID)) throw new TypeError("invalid release worker ID");
    const output = validateOutput(rawOutput);
    return withTransaction(this.database, async (client) => {
      const at = this.now();
      const releaseID = await lockReleaseForOperation(client, operationID);
      const component = await client.query<{ release_id: string }>(`UPDATE cantelop_control.release_components c
SET status = 'succeeded', output = $3::jsonb, updated_at = $4
FROM cantelop_control.operations o
WHERE c.operation_id = $1 AND o.id = c.operation_id
  AND c.status = 'running' AND o.status = 'running' AND o.locked_by = $2
RETURNING c.release_id`, [operationID, workerID, output, at]);
      if (requiredValue(component, "succeed release component").release_id !== releaseID) throw new ControlError("conflict");
      const operation = await client.query(`UPDATE cantelop_control.operations
SET status = 'succeeded', output = $3::jsonb, locked_by = NULL, locked_at = NULL, updated_at = $4
WHERE id = $1 AND status = 'running' AND locked_by = $2`, [operationID, workerID, output, at]);
      if (operation.rowCount !== 1) throw new ControlError("conflict");

      const remaining = await client.query(`SELECT 1 FROM cantelop_control.release_components
WHERE release_id = $1 AND status <> 'succeeded'`, [releaseID]);
      if (remaining.rowCount === 0) {
        const app = await client.query<{ owner_user_id: string; id: string; active_release_id: string | null }>(`SELECT a.owner_user_id, a.id, a.active_release_id
FROM cantelop_control.apps a JOIN cantelop_control.releases r ON r.app_id = a.id
WHERE r.id = $1 AND r.status = 'provisioning' AND a.status <> 'archived'
FOR UPDATE OF a`, [releaseID]);
        if (app.rowCount !== 1) throw new ControlError("conflict");
        await enqueueActivation(client, app.rows[0].owner_user_id, app.rows[0].id,
          releaseID, app.rows[0].active_release_id, at);
      }
      return requiredRelease(client, releaseID);
    });
  }

  async failComponent(operationID: string, workerID: string, errorCode: string): Promise<ProductRelease> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID) || !errorCodePattern.test(errorCode)) throw new TypeError("invalid release failure");
    return withTransaction(this.database, async (client) => {
      const at = this.now();
      const releaseID = await lockReleaseForOperation(client, operationID);
      const component = await client.query<{ release_id: string }>(`UPDATE cantelop_control.release_components c
SET status = 'failed', error_code = $3, updated_at = $4
FROM cantelop_control.operations o
WHERE c.operation_id = $1 AND o.id = c.operation_id
  AND c.status = 'running' AND o.status = 'running' AND o.locked_by = $2
RETURNING c.release_id`, [operationID, workerID, errorCode, at]);
      if (requiredValue(component, "fail release component").release_id !== releaseID) throw new ControlError("conflict");
      const operation = await client.query(`UPDATE cantelop_control.operations
SET status = 'failed', error_code = $3, locked_by = NULL, locked_at = NULL, updated_at = $4
WHERE id = $1 AND status = 'running' AND locked_by = $2`, [operationID, workerID, errorCode, at]);
      if (operation.rowCount !== 1) throw new ControlError("conflict");
      await client.query(`UPDATE cantelop_control.release_components
SET status = 'failed', error_code = 'release_failed', updated_at = $2
WHERE release_id = $1 AND status IN ('queued', 'running')`, [releaseID, at]);
      await client.query(`UPDATE cantelop_control.operations o
SET status = 'failed', error_code = 'release_failed', locked_by = NULL, locked_at = NULL, updated_at = $2
FROM cantelop_control.release_components c
WHERE c.release_id = $1 AND c.operation_id = o.id
  AND o.status IN ('queued', 'running')`, [releaseID, at]);
      const release = await client.query(`UPDATE cantelop_control.releases
SET status = 'failed', error_code = $2, updated_at = $3
WHERE id = $1 AND status IN ('queued', 'provisioning')`, [releaseID, errorCode, at]);
      if (release.rowCount !== 1) throw new ControlError("conflict");
      return requiredRelease(client, releaseID);
    });
  }

  async retryComponent(
    operationID: string,
    workerID: string,
    availableAt: Date,
  ): Promise<ProductRelease> {
    assertResourceID("operation", operationID);
    const at = this.now();
    if (!workerIDPattern.test(workerID) || !Number.isFinite(availableAt.getTime()) || availableAt <= at) {
      throw new TypeError("invalid release component retry");
    }
    return withTransaction(this.database, async (client) => {
      const releaseID = await lockReleaseForOperation(client, operationID);
      const component = await client.query<{ release_id: string }>(`UPDATE cantelop_control.release_components c
SET status = 'queued', updated_at = $3
FROM cantelop_control.operations o
WHERE c.operation_id = $1 AND o.id = c.operation_id
  AND c.status = 'running' AND o.status = 'running' AND o.locked_by = $2 AND o.attempts < 100
RETURNING c.release_id`, [operationID, workerID, at]);
      if (requiredValue(component, "retry release component").release_id !== releaseID) throw new ControlError("conflict");
      const operation = await client.query(`UPDATE cantelop_control.operations
SET status = 'queued', available_at = $3, locked_by = NULL, locked_at = NULL, updated_at = $4
WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts < 100`, [
        operationID, workerID, availableAt, at,
      ]);
      if (operation.rowCount !== 1) throw new ControlError("conflict");
      return requiredRelease(client, releaseID);
    });
  }

  // Shared by API writes and recovery. Holding the lock through the provider
  // response prevents a cancelled API attempt from overwriting its replacement.
  async withDeploymentLock<T>(appID: string, action: () => Promise<T>): Promise<T> {
    assertResourceID("app", appID);
    return withTransaction(this.database, async (client) => {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`deployment:${appID}`]);
      return action();
    });
  }

  // Provider selection and gate readback happen before this transaction. A
  // retry after commit can observe the target already active and reopen the
  // same generation without changing release history a second time.
  async commitActivation(ownerUserID: string, appID: string, targetReleaseID: string,
    previousReleaseID: string | null): Promise<ProductRelease> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("release", targetReleaseID);
    if (previousReleaseID !== null) assertResourceID("release", previousReleaseID);
    return withTransaction(this.database, async (client) => {
      const app = await client.query<{ active_release_id: string | null }>(`SELECT active_release_id
FROM cantelop_control.apps WHERE id = $1 AND owner_user_id = $2 AND status <> 'archived'
FOR UPDATE`, [appID, ownerUserID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      const current = app.rows[0].active_release_id;
      if (current === targetReleaseID) return requiredRelease(client, targetReleaseID);
      if (current !== previousReleaseID) throw new ControlError("conflict");
      const target = await requiredRelease(client, targetReleaseID);
      if (target.appID !== appID || target.components.api.status !== "succeeded" ||
          target.components.session_runtime.status !== "succeeded" ||
          !["provisioning", "superseded"].includes(target.status)) throw new ControlError("conflict");
      const at = this.now();
      if (previousReleaseID) {
        const previous = await client.query(`UPDATE cantelop_control.releases
SET status = 'superseded', updated_at = $2
WHERE id = $1 AND app_id = $3 AND status = 'active'`, [previousReleaseID, at, appID]);
        if (previous.rowCount !== 1) throw new ControlError("conflict");
      }
      const selected = await client.query(`UPDATE cantelop_control.releases
SET status = 'active', activated_at = $2, updated_at = $2
WHERE id = $1 AND app_id = $3 AND status IN ('provisioning', 'superseded')`, [targetReleaseID, at, appID]);
      if (selected.rowCount !== 1) throw new ControlError("conflict");
      const updated = await client.query(`UPDATE cantelop_control.apps
SET active_release_id = $1, status = 'active', updated_at = $2
WHERE id = $3 AND owner_user_id = $4 AND active_release_id IS NOT DISTINCT FROM $5`,
      [targetReleaseID, at, appID, ownerUserID, previousReleaseID]);
      if (updated.rowCount !== 1) throw new ControlError("conflict");
      return requiredRelease(client, targetReleaseID);
    });
  }

  async requestRecovery(ownerUserID: string, appID: string, releaseID: string, restart: boolean): Promise<ProductRelease> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    return withTransaction(this.database, async (client) => {
      await lockReleasableApp(client, ownerUserID, appID);
      const owned = await client.query(`SELECT id FROM cantelop_control.releases
WHERE id = $1 AND app_id = $2 AND owner_user_id = $3 FOR UPDATE`, [releaseID, appID, ownerUserID]);
      if (owned.rowCount !== 1) throw new ControlError("not_found");
      if (restart) {
        const replay = await client.query<{ id: string }>(`SELECT id FROM cantelop_control.releases
WHERE app_id = $1 AND owner_user_id = $2 AND idempotency_key = $3`, [appID, ownerUserID, `restart:${releaseID}`]);
        if (replay.rowCount === 1) return requiredRelease(client, replay.rows[0].id);
      }
      const release = await requiredRelease(client, releaseID);
      if (release.status === "active" || release.status === "superseded") throw new ControlError("conflict");
      // A restart redeploys the same artifacts, which a pre-1.0 release cannot.
      if (restart && !release.definition) throw new ControlError("release_unsupported");
      if (release.status === "cancelled" && !restart) return release;
      const pending = await client.query(`SELECT id FROM cantelop_control.releases
WHERE app_id = $1 AND id <> $2 AND status IN ('queued', 'provisioning', 'cancelling')`, [appID, releaseID]);
      if (pending.rowCount !== 0) throw new ControlError("conflict");
      const activating = await client.query(`SELECT 1 FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'activate_release' AND status IN ('queued', 'running')`, [appID]);
      if (activating.rowCount !== 0) throw new ControlError("conflict");
      const at = this.now();
      await client.query(`UPDATE cantelop_control.releases SET status = 'cancelling', error_code = NULL, updated_at = $2 WHERE id = $1`, [releaseID, at]);
      await client.query(`UPDATE cantelop_control.operations o
SET status = 'failed', error_code = 'deployment_cancelled', locked_by = NULL, locked_at = NULL, updated_at = $2
FROM cantelop_control.release_components c WHERE c.release_id = $1 AND c.operation_id = o.id AND o.status IN ('queued', 'running')`, [releaseID, at]);
      await client.query(`UPDATE cantelop_control.release_components SET status = 'failed', error_code = 'deployment_cancelled', updated_at = $2
WHERE release_id = $1 AND status IN ('queued', 'running')`, [releaseID, at]);
      await client.query(`INSERT INTO cantelop_control.operations
(id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'cancel_release', 'queued', $4, $5::jsonb, $6, $6, $6)
ON CONFLICT (owner_user_id, idempotency_key) DO UPDATE
SET input = jsonb_set(operations.input, '{restart}', to_jsonb((operations.input->>'restart')::boolean OR $7)),
 status = CASE WHEN operations.status = 'succeeded' THEN 'queued' ELSE operations.status END,
 output = NULL, available_at = $6, updated_at = $6`, [newResourceID("operation"), ownerUserID, appID, `cancel:${releaseID}`,
        JSON.stringify({ release_id: releaseID, restart }), at, restart]);
      return requiredRelease(client, releaseID);
    });
  }

  async completeRecovery(operationID: string, workerID: string): Promise<void> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID)) throw new TypeError("invalid recovery worker");
    return withTransaction(this.database, async (client) => {
      // App -> release -> operation is also the request/creation lock order.
      const found = await client.query<{ app_id: string; owner_user_id: string; input: { release_id: string } }>(
        `SELECT app_id, owner_user_id, input FROM cantelop_control.operations WHERE id = $1 AND kind = 'cancel_release'`, [operationID]);
      const identity = requiredValue(found, "read recovery");
      await lockReleasableApp(client, identity.owner_user_id, identity.app_id);
      await client.query(`SELECT id FROM cantelop_control.releases WHERE id = $1 FOR UPDATE`, [identity.input.release_id]);
      const claimed = await client.query<{ input: { release_id: string; restart: boolean } }>(`SELECT input FROM cantelop_control.operations
WHERE id = $1 AND kind = 'cancel_release' AND status = 'running' AND locked_by = $2 FOR UPDATE`, [operationID, workerID]);
      const { input } = requiredValue(claimed, "complete recovery");
      const at = this.now();
      const cancelled = await client.query(`UPDATE cantelop_control.releases SET status = 'cancelled', error_code = NULL, updated_at = $2
WHERE id = $1 AND status = 'cancelling'`, [input.release_id, at]);
      if (cancelled.rowCount !== 1) throw new ControlError("conflict");
      if (input.restart) {
        const replacementID = newResourceID("release");
        await client.query(`INSERT INTO cantelop_control.releases
(id, owner_user_id, app_id, api_artifact_id, session_runtime_artifact_id, version, status, idempotency_key, created_at, updated_at)
SELECT $2, owner_user_id, app_id, api_artifact_id, session_runtime_artifact_id,
 (SELECT coalesce(max(version), 0) + 1 FROM cantelop_control.releases WHERE app_id = r.app_id), 'queued', $3, $4, $4
FROM cantelop_control.releases r WHERE id = $1`, [input.release_id, replacementID, `restart:${input.release_id}`, at]);
        for (const kind of ["api", "session_runtime"] as const) {
          const id = newResourceID("operation");
          await client.query(`INSERT INTO cantelop_control.operations
(id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
SELECT $2, o.owner_user_id, o.app_id, o.kind, 'queued', $3,
 o.input || jsonb_build_object('release_id', $4::text), $5, $5, $5
FROM cantelop_control.operations o JOIN cantelop_control.release_components c ON c.operation_id = o.id
WHERE c.release_id = $1 AND c.kind = $6`, [input.release_id, id, `${replacementID}:${kind}`, replacementID, at, kind]);
          await client.query(`INSERT INTO cantelop_control.release_components (release_id, kind, operation_id, status, created_at, updated_at)
VALUES ($1, $2, $3, 'queued', $4, $4)`, [replacementID, kind, id, at]);
        }
      }
      await client.query(`UPDATE cantelop_control.operations SET status = 'succeeded', output = '{}'::jsonb, locked_by = NULL, locked_at = NULL, updated_at = $2 WHERE id = $1`, [operationID, at]);
    });
  }
}

async function storeAPIArtifact(
  client: TransactionClient,
  artifact: ReleaseAPIArtifact,
  at: Date,
): Promise<string> {
  const candidateID = newResourceID("artifact");
  await client.query(`INSERT INTO cantelop_control.api_artifacts
  (id, digest, object_key, size_bytes, manifest, created_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)
ON CONFLICT (object_key) DO NOTHING`, [
    candidateID, artifact.digest, artifact.objectKey, artifact.sizeBytes, JSON.stringify(artifact.manifest), at,
  ]);
  const stored = await client.query<{
    id: string; digest: string; object_key: string; size_bytes: number; manifest: APIArtifactManifest;
  }>(`SELECT id, digest, object_key, size_bytes, manifest
FROM cantelop_control.api_artifacts WHERE object_key = $1`, [artifact.objectKey]);
  const row = requiredValue(stored, "store release API artifact");
  if (row.digest !== artifact.digest || row.size_bytes !== artifact.sizeBytes ||
      !sameAPIArtifactManifest(row.manifest, artifact.manifest)) {
    throw new ControlError("conflict");
  }
  return row.id;
}

async function storeSessionRuntimeArtifact(client: TransactionClient, artifact: ReleaseSessionRuntimeArtifact, at: Date): Promise<string> {
  const candidateID = newResourceID("sessionRuntimeArtifact");
  await client.query(`INSERT INTO cantelop_control.session_runtime_artifacts
  (id, digest, object_key, size_bytes, media_type, created_at, manifest)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
ON CONFLICT (object_key) DO NOTHING`, [
    candidateID, artifact.digest, artifact.objectKey, artifact.sizeBytes, artifact.mediaType, at, artifact.manifest ? JSON.stringify(artifact.manifest):null,
  ]);
  const stored = await client.query<{
    id: string; object_key: string; size_bytes: string | number; media_type: string; digest: string; manifest?:SessionRuntimeManifest|null;
  }>(
    `SELECT id, object_key, size_bytes, media_type, digest, manifest
FROM cantelop_control.session_runtime_artifacts WHERE object_key = $1`, [artifact.objectKey],
  );
  const row = requiredValue(stored, "store release sessionRuntime artifact");
  if (row.object_key !== artifact.objectKey || row.digest !== artifact.digest ||
      Number(row.size_bytes) !== artifact.sizeBytes || row.media_type !== artifact.mediaType || canonical(row.manifest ?? null) !== canonical(artifact.manifest ?? null)) {
    throw new ControlError("conflict");
  }
  return row.id;
}

interface ComponentOperationInput {
  id: string;
  ownerUserID: string;
  appID: string;
  kind: "deploy_api" | "materialize_build" | "attach_package";
  packageID?: string;
  component?: "api" | "session_runtime";
  releaseID: string;
  idempotencyKey: string;
  artifactID: string;
  objectKey: string;
  digest: string;
  sizeBytes?: number;
  at: Date;
}

async function insertComponentOperation(client: TransactionClient, operation: ComponentOperationInput): Promise<void> {
  const input = JSON.stringify({
    release_id: operation.releaseID,
    artifact_id: operation.artifactID,
    object_key: operation.objectKey,
    artifact_digest: operation.digest,
    ...(operation.sizeBytes ? { size_bytes: operation.sizeBytes } : {}),
    ...(operation.packageID ? { package_id: operation.packageID, component: operation.component } : {}),
  });
  await insertQueuedOperation(client, { ...operation, input }, "create release component operation");
}

async function insertQueuedOperation(client: TransactionClient, operation: {
  id: string; ownerUserID: string; appID: string; kind: "deploy_api" | "materialize_build" | "activate_release" | "attach_package";
  idempotencyKey: string; input: string; at: Date;
}, description: string): Promise<void> {
  const result = await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'queued', $5, $6::jsonb, $7, $7, $7)`, [
    operation.id, operation.ownerUserID, operation.appID, operation.kind,
    operation.idempotencyKey, operation.input, operation.at,
  ]);
  if (result.rowCount !== 1) throw new Error(`${description} returned an invalid row count`);
}

// Releases that predate SDK 1.0 stay in history but are never rollback targets.
const rollbackTargetQuery = `SELECT r.id
FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts artifact ON artifact.id = r.api_artifact_id
WHERE r.app_id = $1 AND r.owner_user_id = $2 AND r.status = 'superseded'
  AND artifact.manifest->>'kind' = 'cantelop-protocol-edge'
ORDER BY r.version DESC
LIMIT 1`;

async function requiredRelease(client: TransactionClient, releaseID: string): Promise<ProductRelease> {
  const result = await executeDrizzle(client, releaseQuery(client).where(eq(releases.id, releaseID)));
  return mapRelease(requiredValue(result, "read release"));
}

async function enqueueActivation(client: TransactionClient, ownerUserID: string, appID: string,
  targetReleaseID: string, previousReleaseID: string | null, at: Date): Promise<void> {
  const generation = await allocateReleaseGeneration(client);
  const operationID = newResourceID("operation");
  const input = JSON.stringify({ target_release_id: targetReleaseID, previous_release_id: previousReleaseID, generation });
  await insertQueuedOperation(client, {
    id: operationID, ownerUserID, appID, kind: "activate_release", idempotencyKey: `activate:${operationID}`, input, at,
  }, "enqueue release activation");
}

async function lockReleaseForOperation(client: TransactionClient, operationID: string): Promise<string> {
  const result = await client.query<{ release_id: string }>(`SELECT c.release_id
FROM cantelop_control.release_components c
JOIN cantelop_control.releases r ON r.id = c.release_id
WHERE c.operation_id = $1
FOR UPDATE OF r`, [operationID]);
  return requiredValue(result, "lock release component").release_id;
}

function validateAPIArtifact(artifact: ReleaseAPIArtifact): void {
  if (!digestPattern.test(artifact.digest) ||
      !/^deployment-uploads\/[0-9a-f]{32}\/api\/worker\.mjs$/.test(artifact.objectKey) ||
      !Number.isInteger(artifact.sizeBytes) || artifact.sizeBytes < 1 || artifact.sizeBytes > 1 << 20 ||
      !validAPIArtifactManifest(artifact.manifest)) {
    throw new TypeError("invalid release API artifact");
  }
}

function validateSessionRuntimeArtifact(artifact: ReleaseSessionRuntimeArtifact): void {
  if (!digestPattern.test(artifact.digest) ||
      !/^deployment-uploads\/[0-9a-f]{32}\/session-runtime\/image\.tar\.gz$/.test(artifact.objectKey) ||
      !Number.isSafeInteger(artifact.sizeBytes) || artifact.sizeBytes < 1 || artifact.sizeBytes > 20 * 1024 ** 3 ||
      artifact.mediaType !== sessionRuntimeMediaType) throw new TypeError("invalid release sessionRuntime artifact");
}

function validateOutput(output: Record<string, unknown>): string {
  if (typeof output !== "object" || output === null || Array.isArray(output) || Object.getPrototypeOf(output) !== Object.prototype) {
    throw new TypeError("invalid release component output");
  }
  let json: string;
  try {
    json = JSON.stringify(output);
  } catch {
    throw new TypeError("invalid release component output");
  }
  if (json === undefined || Buffer.byteLength(json) > maxComponentOutputBytes) {
    throw new TypeError("invalid release component output");
  }
  return json;
}

function requiredValue<Row>(result: { rows: Row[]; rowCount: number | null }, operation: string): Row {
  if (result.rowCount !== 1 || result.rows.length !== 1) throw new Error(`${operation} returned an invalid row count`);
  return result.rows[0];
}

function mapComponent(
  kind: ReleaseComponentKind,
  operationID: string,
  status: ReleaseComponentStatus,
  output: Record<string, unknown> | null,
  errorCode: string | null,
): ReleaseComponent {
  return {
    kind, operationID, status,
    ...(output ? { output } : {}),
    ...(errorCode ? { errorCode } : {}),
  };
}

function mapRelease(row: ReleaseRow): ProductRelease {
  return {
    id: row.id,
    appID: row.app_id,
    version: row.version,
    status: row.status,
    apiArtifactDigest: row.api_artifact_digest,
    sessionRuntimeArtifactDigest: row.session_runtime_artifact_digest,
    components: Object.freeze({
      api: {
        ...mapComponent("api", row.api_operation_id, row.api_status, row.api_output, row.api_error_code),
        attempts: row.api_attempts,
        progress: row.api_progress ?? undefined,
        updatedAt: row.api_updated_at,
        nextAttemptAt: row.api_next_attempt_at ?? undefined,
      },
      session_runtime: {
        ...mapComponent("session_runtime", row.session_runtime_operation_id, row.session_runtime_status, row.session_runtime_output, row.session_runtime_error_code),
        attempts: row.session_runtime_attempts,
        progress: row.session_runtime_progress ?? undefined,
        updatedAt: row.session_runtime_updated_at,
        nextAttemptAt: row.session_runtime_next_attempt_at ?? undefined,
      },
    }),
    ...(row.error_code ? { errorCode: row.error_code } : {}),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    ...(row.activated_at ? { activatedAt: row.activated_at } : {}),
    ...definition(row),
  };
}

function definition(row: ReleaseRow): { definition?: AppDefinitionSummary } {
  const summary = appDefinitionSummary(row.api_manifest, row.session_runtime_manifest);
  return summary ? { definition: summary } : {};
}

async function lockReleasableApp(client: TransactionClient, userID: string, appID: string): Promise<void> {
  await lockOwnedApp(client, appID, userID, "update_app");
  if (await archivePending(client, appID)) throw new ControlError("conflict");
}

async function archivePending(client: TransactionClient, appID: string): Promise<boolean> {
  const result = await client.query(`SELECT 1 FROM cantelop_control.operations
WHERE app_id = $1 AND kind = 'archive_app' AND status IN ('queued', 'running') LIMIT 1`, [appID]);
  return result.rowCount !== 0;
}
