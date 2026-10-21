import { expect, test } from "bun:test";

import { ControlError } from "../domain/errors";
import { OperationRepository } from "./operation-repository";

const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const operationID = "op_0123456789abcdef0123456789abcdef";
const at = new Date("2026-08-12T12:00:00Z");

function operationRow(overrides = {}) {
  return {
    id: operationID, owner_user_id: userID, app_id: appID, kind: "deploy_api", status: "queued",
    idempotency_key: "request-0123456789", input: { artifact_id: "artifact-1" }, output: null,
    error_code: null, attempts: 0, available_at: at, locked_by: null, locked_at: null,
    created_at: at, updated_at: at, ...overrides,
  };
}

function transactionDatabase(query) {
  return {
    query,
    async connect() {
      return {
        async query(sql, values) {
          if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql)) return { rowCount: null, rows: [] };
          return query(sql, values);
        },
        release() {},
      };
    },
  };
}

test("enqueues an owned App operation with replay-safe identity", async () => {
  let inserts = 0;
  const database = transactionDatabase(async (sql) => {
      if (sql.startsWith("SELECT u.status")) return { rowCount: 1, rows: [{ status: "active" }] };
      if (sql.startsWith("INSERT INTO")) {
        inserts++;
        return inserts === 1 ? { rowCount: 1, rows: [operationRow()] } : { rowCount: 0, rows: [] };
      }
      if (sql.startsWith("SELECT id")) return { rowCount: 1, rows: [operationRow()] };
      throw new Error(`unexpected query ${sql}`);
  });
  const repository = new OperationRepository(database, () => at);
  const first = await repository.enqueue(userID, appID, "deploy_api", "request-0123456789", { artifact_id: "artifact-1" });
  const replay = await repository.enqueue(userID, appID, "deploy_api", "request-0123456789", { artifact_id: "artifact-1" });
  expect(first).toEqual(replay);
  expect(first).toMatchObject({ id: operationID, appID, kind: "deploy_api", status: "queued", attempts: 0 });
});

test("rejects reuse of an idempotency key for a different operation", async () => {
  const database = transactionDatabase(async (sql) => {
      if (sql.startsWith("SELECT u.status")) return { rowCount: 1, rows: [{ status: "active" }] };
      return { rowCount: 0, rows: [] };
  });
  const repository = new OperationRepository(database, () => at);
  await expect(repository.enqueue(userID, appID, "archive_app", "request-0123456789", {}))
    .rejects.toEqual(new ControlError("conflict"));
});

test("claims work with skip-locked semantics and binds completion to its worker", async () => {
  const statements = [];
  const running = operationRow({ status: "running", attempts: 1, locked_by: "worker-1", locked_at: at });
  const succeeded = operationRow({
    status: "succeeded", attempts: 1, output: { release_id: "release-1" }, updated_at: at,
  });
  const database = {
    async query(sql) {
      statements.push(sql);
      if (sql.startsWith("WITH candidate")) return { rowCount: 1, rows: [running] };
      if (sql.startsWith('update "cantelop_control"."operations"')) return { rowCount: 1, rows: [succeeded] };
      throw new Error(`unexpected query ${sql}`);
    },
  };
  const repository = new OperationRepository(database, () => at);
  expect(await repository.claim("worker-1")).toMatchObject({ status: "running", lockedBy: "worker-1", attempts: 1 });
  expect(statements[0]).toContain("FOR UPDATE SKIP LOCKED");
  expect(await repository.succeed(operationID, "worker-1", { release_id: "release-1" }))
    .toMatchObject({ status: "succeeded", output: { release_id: "release-1" } });
});

test("claims only the operation kind assigned to a worker", async () => {
  const database = transactionDatabase(async (sql, values) => {
    if (sql.startsWith("WITH candidate")) {
      expect(values[2]).toBe("deploy_api");
      expect(sql).toContain("kind = $3");
      expect(sql).toContain("blocking_config.kind = 'sync_config'");
      expect(sql).toContain("blocking_config.status IN ('queued', 'running')");
      expect(sql).toContain("(blocking_config.created_at, blocking_config.id) < (candidate_operation.created_at, candidate_operation.id)");
      return { rowCount: 0, rows: [] };
    }
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new OperationRepository(database, () => at);
  expect(await repository.claim("api-worker-1", "deploy_api")).toBeUndefined();
});

test("recovers stale operations and release component state atomically", async () => {
  let statement;
  const database = transactionDatabase(async (sql) => {
    statement = sql;
    return { rowCount: 1, rows: [{ recovered_count: "2" }] };
  });
  const repository = new OperationRepository(database, () => at);
  expect(await repository.recoverStale(60_000)).toBe(2);
  expect(statement).toContain("WITH recovered AS");
  expect(statement).toContain("UPDATE cantelop_control.release_components");
  expect(statement).toContain("FROM recovered r");
  expect(statement).toContain("'archive_app'");
});

test("archive cleanup can retry past the ordinary operation attempt limit", async () => {
  let statement;
  let parameters;
  const database = transactionDatabase(async (sql, values) => {
    statement = sql;
    parameters = values;
    return { rowCount: 1, rows: [operationRow({ kind: "archive_app", status: "queued", attempts: 101 })] };
  });
  const repository = new OperationRepository(database, () => at);
  const next = new Date(at.getTime() + 30_000);
  expect(await repository.retry(operationID, "worker-1", next)).toMatchObject({ kind: "archive_app", attempts: 101 });
  expect(statement).toContain('"kind" = $12');
  expect(parameters).toContain("archive_app");
});

test("detects unfinished provider dependencies", async () => {
  const database = transactionDatabase(async (sql, values) => {
    expect(sql).toContain('"status" in ($4, $5)');
    expect(values).toEqual([userID, appID, "register_app", "queued", "running", 1]);
    return { rowCount: 1, rows: [{ id: operationID }] };
  });
  const repository = new OperationRepository(database);
  expect(await repository.hasUnfinished(userID, appID, "register_app")).toBe(true);
});

test("Edge configuration progress is durable and scoped to the owning running worker", async () => {
  const statements = [];
  const database = { async query(sql, values) {
    statements.push({ sql, values });
    return { rowCount: 1, rows: [operationRow({ kind: "sync_config", status: "failed", output: null, progress: "edge_config_applied" })] };
  } };
  const repository = new OperationRepository(database, () => at);
  await repository.recordEdgeConfiguration(operationID, "worker-1", 5);
  expect(statements[0].values).toEqual([operationID, "worker-1", 5, at]);
  expect(statements[0].sql).toContain("kind = 'sync_config' AND status = 'running' AND locked_by = $2");
  expect(statements[0].sql).toContain("progress = 'edge_config_applied'");
  expect(statements[0].sql).toContain("(input->>'revision')::bigint = $3");
  expect(statements[0].sql).not.toContain("SET output");
  await repository.fail(operationID, "worker-1", "provider_configuration_rejected");
  expect(statements[1].sql).not.toContain('"output" =');
  await expect(repository.recordEdgeConfiguration(operationID, "worker-1", 0)).rejects.toThrow(TypeError);
  await expect(new OperationRepository({ async query() { return { rowCount: 0, rows: [] }; } })
    .recordEdgeConfiguration(operationID, "worker-1", 5)).rejects.toMatchObject({ code: "conflict" });
});
