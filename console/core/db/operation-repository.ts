import { and, eq, inArray, lt, or } from "drizzle-orm";

import { ControlError } from "../domain/errors";
import { assertResourceID, newResourceID } from "../domain/ids";
import { operationKinds, type OperationKind, type OperationStatus } from "../domain/model";
import { lockOwnedApp } from "./app-access";
import { controlQueryBuilder, executeDrizzle, withTransaction, type SQLPool } from "./database";
import { operations } from "./schema";

const knownOperationKinds = new Set<OperationKind>(operationKinds);
const idempotencyKeyPattern = /^[A-Za-z0-9_.:-]{16,128}$/;
const workerIDPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/;
const errorCodePattern = /^[a-z][a-z0-9_]{0,63}$/;
const maxOperationInputBytes = 128 << 10;

export interface ControlOperation {
  id: string;
  ownerUserID: string;
  appID: string;
  kind: OperationKind;
  status: OperationStatus;
  idempotencyKey: string;
  input: Record<string, unknown>;
  output?: Record<string, unknown>;
  errorCode?: string;
  attempts: number;
  availableAt: Date;
  lockedBy?: string;
  lockedAt?: Date;
  createdAt: Date;
  updatedAt: Date;
}

interface OperationRow extends Record<string, unknown> {
  id: string;
  owner_user_id: string;
  app_id: string;
  kind: OperationKind;
  status: OperationStatus;
  idempotency_key: string;
  input: Record<string, unknown>;
  output: Record<string, unknown> | null;
  error_code: string | null;
  attempts: number;
  available_at: Date;
  locked_by: string | null;
  locked_at: Date | null;
  created_at: Date;
  updated_at: Date;
}

const operationProjection = `id, owner_user_id, app_id, kind, status, idempotency_key,
       input, output, error_code, attempts, available_at, locked_by, locked_at,
       created_at, updated_at`;
const claimedOperationProjection = `o.id, o.owner_user_id, o.app_id, o.kind, o.status, o.idempotency_key,
       o.input, o.output, o.error_code, o.attempts, o.available_at, o.locked_by, o.locked_at,
       o.created_at, o.updated_at`;

const operationSelection = {
  id: operations.id,
  owner_user_id: operations.ownerUserId,
  app_id: operations.appId,
  kind: operations.kind,
  status: operations.status,
  idempotency_key: operations.idempotencyKey,
  input: operations.input,
  output: operations.output,
  error_code: operations.errorCode,
  attempts: operations.attempts,
  available_at: operations.availableAt,
  locked_by: operations.lockedBy,
  locked_at: operations.lockedAt,
  created_at: operations.createdAt,
  updated_at: operations.updatedAt,
};

export class OperationRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  async enqueue(
    ownerUserID: string,
    appID: string,
    kind: OperationKind,
    idempotencyKey: string,
    rawInput: Record<string, unknown>,
  ): Promise<ControlOperation> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    if (!knownOperationKinds.has(kind) || !idempotencyKeyPattern.test(idempotencyKey)) throw new TypeError("invalid operation");
    const input = validatedDocument(rawInput, "operation input");
    const inputJSON = JSON.stringify(input);
    if (Buffer.byteLength(inputJSON) > maxOperationInputBytes) throw new TypeError("operation input is too large");
    return withTransaction(this.database, async (client) => {
      await lockOwnedApp(client, appID, ownerUserID, "share_app_and_owner");
      const at = this.now();
      const result = await client.query<OperationRow>(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'queued', $5, $6::jsonb, $7, $7, $7)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING
RETURNING ${operationProjection}`, [
      newResourceID("operation"), ownerUserID, appID, kind, idempotencyKey, inputJSON, at,
    ]);
      if (result.rowCount === 1) return mapOperation(result.rows[0]);
      const replay = await client.query<OperationRow>(`SELECT ${operationProjection}
FROM cantelop_control.operations
WHERE owner_user_id = $1 AND idempotency_key = $2
  AND app_id = $3 AND kind = $4 AND input = $5::jsonb`, [ownerUserID, idempotencyKey, appID, kind, inputJSON]);
      if (replay.rowCount !== 1) throw new ControlError("conflict");
      return mapOperation(replay.rows[0]);
    });
  }

  async getForUser(ownerUserID: string, operationID: string): Promise<ControlOperation> {
    assertResourceID("user", ownerUserID);
    assertResourceID("operation", operationID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(operationSelection)
      .from(operations)
      .where(and(eq(operations.id, operationID), eq(operations.ownerUserId, ownerUserID))));
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return mapOperation(result.rows[0]);
  }

  async claim(workerID: string, kind?: OperationKind): Promise<ControlOperation | undefined> {
    if (!workerIDPattern.test(workerID) || (kind !== undefined && !knownOperationKinds.has(kind))) throw new TypeError("invalid operation worker ID");
    const at = this.now();
    const result = await this.database.query<OperationRow>(`WITH candidate AS (
  SELECT candidate_operation.id
  FROM cantelop_control.operations candidate_operation
  WHERE candidate_operation.status = 'queued' AND candidate_operation.available_at <= $1
    AND ($3::text IS NULL OR candidate_operation.kind = $3)
    AND (candidate_operation.kind <> 'sync_config' OR NOT EXISTS (
      SELECT 1 FROM cantelop_control.operations blocking_config
      WHERE blocking_config.app_id = candidate_operation.app_id
        AND blocking_config.kind = 'sync_config'
        AND blocking_config.status IN ('queued', 'running')
        AND (blocking_config.status = 'running'
          OR (blocking_config.created_at, blocking_config.id) < (candidate_operation.created_at, candidate_operation.id))
    ))
  ORDER BY candidate_operation.available_at, candidate_operation.created_at, candidate_operation.id
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE cantelop_control.operations o
SET status = 'running', attempts = attempts + 1, locked_by = $2, locked_at = $1, updated_at = $1
FROM candidate
WHERE o.id = candidate.id
RETURNING ${claimedOperationProjection}`, [at, workerID, kind ?? null]);
    if (result.rowCount === 0) return undefined;
    if (result.rowCount !== 1) throw new Error("operation claim returned an invalid row count");
    return mapOperation(result.rows[0]);
  }

  async recoverStale(maxAgeMilliseconds: number): Promise<number> {
    if (!Number.isInteger(maxAgeMilliseconds) || maxAgeMilliseconds < 60_000 || maxAgeMilliseconds > 24 * 60 * 60 * 1000) {
      throw new TypeError("invalid stale operation age");
    }
    const at = this.now();
    const cutoff = new Date(at.getTime() - maxAgeMilliseconds);
    const result = await this.database.query<{ recovered_count: string | number }>(`WITH recovered AS (
  UPDATE cantelop_control.operations
  SET status = 'queued', available_at = $1, locked_by = NULL, locked_at = NULL, updated_at = $1
  WHERE status = 'running' AND locked_at < $2 AND (attempts < 100 OR kind IN ('cancel_release', 'activate_release', 'archive_app'))
  RETURNING id
), reconciled_components AS (
  UPDATE cantelop_control.release_components c
  SET status = 'queued', updated_at = $1
  FROM recovered r
  WHERE c.operation_id = r.id AND c.status = 'running'
  RETURNING c.operation_id
)
SELECT count(*) AS recovered_count FROM recovered`, [at, cutoff]);
    if (result.rowCount !== 1) throw new Error("stale operation recovery returned an invalid row count");
    return Number(result.rows[0].recovered_count);
  }

  async recoverStaleCancellations(): Promise<void> {
    const at = this.now();
    await this.database.query(`UPDATE cantelop_control.operations
SET status = 'queued', available_at = $1, locked_by = NULL, locked_at = NULL, updated_at = $1
WHERE kind = 'cancel_release' AND status = 'running' AND locked_at < $2`, [at, new Date(at.getTime() - 5 * 60_000)]);
  }

  async recordProgress(operationID: string, workerID: string, progress: string): Promise<void> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID) || !errorCodePattern.test(progress)) throw new TypeError("invalid operation progress");
    const result = await this.database.query(`UPDATE cantelop_control.operations SET progress = $3, updated_at = $4
WHERE id = $1 AND status = 'running' AND locked_by = $2`, [operationID, workerID, progress, this.now()]);
    if (result.rowCount !== 1) throw new ControlError("conflict");
  }

  async hasUnfinished(ownerUserID: string, appID: string, kind: OperationKind): Promise<boolean> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    if (!knownOperationKinds.has(kind)) throw new TypeError("invalid operation kind");
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select({ id: operations.id })
      .from(operations)
      .where(and(
        eq(operations.ownerUserId, ownerUserID),
        eq(operations.appId, appID),
        eq(operations.kind, kind),
        inArray(operations.status, ["queued", "running"]),
      ))
      .limit(1));
    return result.rows.length !== 0;
  }

  async cancelQueuedWorkspaceRegistration(ownerUserID: string, appID: string, workspaceID: string): Promise<void> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("workspace", workspaceID);
    await this.database.query(`UPDATE cantelop_control.operations
SET status = 'failed', error_code = 'workspace_archived', updated_at = $4
WHERE owner_user_id = $1 AND app_id = $2 AND kind = 'register_workspace'
  AND status = 'queued' AND input->>'workspace_id' = $3`, [ownerUserID, appID, workspaceID, this.now()]);
  }

  async hasRunningWorkspaceRegistration(ownerUserID: string, appID: string, workspaceID: string): Promise<boolean> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    assertResourceID("workspace", workspaceID);
    const result = await this.database.query(`SELECT id FROM cantelop_control.operations
WHERE owner_user_id = $1 AND app_id = $2 AND kind = 'register_workspace'
  AND status = 'running' AND input->>'workspace_id' = $3
LIMIT 1`, [ownerUserID, appID, workspaceID]);
    return result.rowCount !== 0;
  }

  async succeed(operationID: string, workerID: string, rawOutput: Record<string, unknown>): Promise<ControlOperation> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID)) throw new TypeError("invalid operation worker ID");
    const output = validatedDocument(rawOutput, "operation output");
    if (Buffer.byteLength(JSON.stringify(output)) > maxOperationInputBytes) throw new TypeError("operation output is too large");
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.update(operations)
      .set({ status: "succeeded", output, lockedBy: null, lockedAt: null, updatedAt: this.now() })
      .where(and(eq(operations.id, operationID), eq(operations.status, "running"), eq(operations.lockedBy, workerID)))
      .returning(operationSelection));
    if (result.rowCount !== 1) throw new ControlError("conflict");
    return mapOperation(result.rows[0]);
  }

  /** Persist Edge propagation before compute sync, retaining it through retries and failures. */
  async recordEdgeConfiguration(operationID: string, workerID: string, revision: number): Promise<void> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID) || !Number.isSafeInteger(revision) || revision < 1)
      throw new TypeError("invalid Edge configuration progress");
    const result = await this.database.query(`UPDATE cantelop_control.operations
SET progress = 'edge_config_applied', updated_at = $4
WHERE id = $1 AND kind = 'sync_config' AND status = 'running' AND locked_by = $2
  AND (input->>'revision')::bigint = $3`,
      [operationID, workerID, revision, this.now()]);
    if (result.rowCount !== 1) throw new ControlError("conflict");
  }

  async fail(operationID: string, workerID: string, errorCode: string): Promise<ControlOperation> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID) || !errorCodePattern.test(errorCode)) throw new TypeError("invalid operation failure");
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.update(operations)
      .set({ status: "failed", errorCode, lockedBy: null, lockedAt: null, updatedAt: this.now() })
      .where(and(eq(operations.id, operationID), eq(operations.status, "running"), eq(operations.lockedBy, workerID)))
      .returning(operationSelection));
    if (result.rowCount !== 1) throw new ControlError("conflict");
    return mapOperation(result.rows[0]);
  }

  async retry(operationID: string, workerID: string, availableAt: Date): Promise<ControlOperation> {
    assertResourceID("operation", operationID);
    if (!workerIDPattern.test(workerID) || !Number.isFinite(availableAt.getTime()) || availableAt <= this.now()) {
      throw new TypeError("invalid operation retry");
    }
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.update(operations)
      .set({ status: "queued", availableAt, lockedBy: null, lockedAt: null, updatedAt: this.now() })
      .where(and(
        eq(operations.id, operationID),
        eq(operations.status, "running"),
        eq(operations.lockedBy, workerID),
        or(lt(operations.attempts, 100), eq(operations.kind, "cancel_release"), eq(operations.kind, "activate_release"), eq(operations.kind, "archive_app")),
      ))
      .returning(operationSelection));
    if (result.rowCount !== 1) throw new ControlError("conflict");
    return mapOperation(result.rows[0]);
  }
}

function validatedDocument(value: Record<string, unknown>, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new TypeError(`invalid ${label}`);
  }
  let json: string;
  try {
    json = JSON.stringify(value);
  } catch {
    throw new TypeError(`invalid ${label}`);
  }
  if (json === undefined) throw new TypeError(`invalid ${label}`);
  return JSON.parse(json) as Record<string, unknown>;
}

function mapOperation(row: OperationRow): ControlOperation {
  return {
    id: row.id, ownerUserID: row.owner_user_id, appID: row.app_id, kind: row.kind,
    status: row.status, idempotencyKey: row.idempotency_key, input: row.input,
    ...(row.output ? { output: row.output } : {}),
    ...(row.error_code ? { errorCode: row.error_code } : {}),
    attempts: row.attempts, availableAt: row.available_at,
    ...(row.locked_by ? { lockedBy: row.locked_by } : {}),
    ...(row.locked_at ? { lockedAt: row.locked_at } : {}),
    createdAt: row.created_at, updatedAt: row.updated_at,
  };
}
