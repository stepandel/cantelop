import { expect, test } from "bun:test";
import { DeploymentUploadRepository } from "./deployment-upload-repository";
import { edge, runtime } from "../api-deployments/protocol-artifact.test.mjs";

const owner = "usr_0123456789abcdef0123456789abcdef";
const app = "app_0123456789abcdef0123456789abcdef";
const id = "upl_0123456789abcdef0123456789abcdef";
const at = new Date("2026-08-12T12:00:00Z");
const expiresAt = new Date("2026-08-12T13:00:00Z");

function row(status = "uploading") {
  return {
    id, owner_user_id: owner, app_id: app, idempotency_key: "upload-request-012345", status,
    api_digest: `sha256:${"a".repeat(64)}`, api_object_key: `deployment-uploads/${id.slice(4)}/api/worker.mjs`,
    api_size_bytes: 27, api_manifest: edge,
    session_runtime_digest: `sha256:${"b".repeat(64)}`,
    session_runtime_object_key: `deployment-uploads/${id.slice(4)}/session-runtime/image.tar.gz`,
    session_runtime_size_bytes: 1024, session_runtime_media_type: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip",
    session_runtime_manifest: runtime,
    session_runtime_store_upload_id: status === "preparing" ? null : "store-1",
    session_runtime_part_size_bytes: status === "preparing" ? null : 64 << 20,
    session_runtime_part_count: status === "preparing" ? null : 1,
    release_id: status === "finalized" ? "rel_0123456789abcdef0123456789abcdef" : null,
    expires_at: expiresAt, created_at: at, updated_at: at,
  };
}

function pool(handler) {
  const statements = [];
  const client = {
    async query(sql, values = []) {
      statements.push({ sql, values });
      if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql)) return { rowCount: null, rows: [] };
      return handler(sql, values);
    },
    release() {},
  };
  return { statements, query: client.query, async connect() { return client; } };
}

for (const status of ["preparing", "uploading", "expired", "cancelled"]) {
  test(`cancels an owned ${status} upload under the App lock`, async () => {
    const db = pool((sql, values) => {
      if (sql.startsWith("SELECT id FROM cantelop_control.apps")) {
        expect(sql).toContain("FOR UPDATE");
        expect(values).toEqual([app, owner]);
        return { rowCount: 1, rows: [{ id: app }] };
      }
      if (sql.includes("FROM cantelop_control.deployment_uploads")) {
        expect(values).toEqual([id, owner, app]);
        expect(sql).toContain("FOR UPDATE");
        return { rowCount: 1, rows: [row(status)] };
      }
      if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) {
        expect(values).toEqual([owner, "upload-request-012345"]);
        return { rowCount: 0, rows: [] };
      }
      if (sql.startsWith("UPDATE cantelop_control.deployment_uploads")) {
        expect(values).toEqual([id, owner, app, at]);
        return { rowCount: 1, rows: [row("cancelled")] };
      }
      throw new Error(sql);
    });
    const repo = new DeploymentUploadRepository(db, () => at);
    expect(await repo.cancelForUser(owner, app, id)).toMatchObject({ id, status: "cancelled" });
    expect(db.statements.at(-1).sql).toBe("COMMIT");
    expect(db.statements.filter(({ sql }) => sql.startsWith("UPDATE"))).toHaveLength(status === "cancelled" ? 0 : 1);
  });
}

for (const scenario of ["foreign-app", "foreign-upload", "finalized", "legacy-release"]) {
  test(`cancellation rejects ${scenario} without mutating storage state`, async () => {
    const db = pool((sql) => {
      if (sql.startsWith("SELECT id FROM cantelop_control.apps")) return { rowCount: scenario === "foreign-app" ? 0 : 1, rows: [] };
      if (sql.includes("FROM cantelop_control.deployment_uploads")) return {
        rowCount: scenario === "foreign-upload" ? 0 : 1, rows: [row(scenario === "finalized" ? "finalized" : "uploading")],
      };
      if (sql.startsWith("SELECT 1 FROM cantelop_control.releases")) return { rowCount: 1, rows: [{}] };
      throw new Error(sql);
    });
    await expect(new DeploymentUploadRepository(db, () => at).cancelForUser(owner, app, id)).rejects.toMatchObject({
      code: scenario.startsWith("foreign") ? "not_found" : "conflict",
    });
    expect(db.statements.at(-1).sql).toBe("ROLLBACK");
    expect(db.statements.some(({ sql }) => sql.startsWith("UPDATE"))).toBe(false);
  });
}

test("pending upload discovery is owner scoped", async () => {
  const db = pool((sql, values) => {
    expect(sql).toContain("owner_user_id = $1 AND app_id = $2 AND status IN ('preparing', 'uploading')");
    expect(values).toEqual([owner, app]);
    return { rowCount: 1, rows: [row()] };
  });
  expect(await new DeploymentUploadRepository(db).listPendingForUser(owner, app)).toMatchObject([{ id }]);
});

test("replays the original upload despite fresh candidate storage paths", async () => {
  const original = row();
  const candidateID = "upl_ffffffffffffffffffffffffffffffff";
  const db = pool(() => ({ rowCount: 1, rows: [original] }));
  const repo = new DeploymentUploadRepository(db, () => at);
  const input = {
    api: { digest: original.api_digest, objectKey: `deployment-uploads/${candidateID.slice(4)}/api/worker.mjs`, sizeBytes: 27, manifest: original.api_manifest },
    sessionRuntime: { digest: original.session_runtime_digest, objectKey: `deployment-uploads/${candidateID.slice(4)}/session-runtime/image.tar.gz`, sizeBytes: 1024, mediaType: original.session_runtime_media_type, manifest: runtime },
  };
  expect(await repo.begin(candidateID, owner, app, input, original.idempotency_key, expiresAt)).toMatchObject({
    id, input: { api: { objectKey: original.api_object_key } },
  });
  input.sessionRuntime.digest = `sha256:${"c".repeat(64)}`;
  await expect(repo.begin(candidateID, owner, app, input, original.idempotency_key, expiresAt)).rejects.toMatchObject({ code: "conflict" });
});
