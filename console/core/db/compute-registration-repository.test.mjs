import { expect, test } from "bun:test";

import { ComputeRegistrationRepository } from "./compute-registration-repository";

test("completes the projection operation without mutating Workspace lifecycle", async () => {
  const statements = [];
  const at = new Date("2026-08-12T12:00:00Z");
  const client = {
    async query(sql, values = []) {
      statements.push({ sql, values });
      if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql)) return { rowCount: null, rows: [] };
      if (sql.startsWith("UPDATE cantelop_control.operations")) {
        return { rowCount: 1, rows: [{ id: "op_0123456789abcdef0123456789abcdef" }] };
      }
      throw new Error(`unexpected query ${sql}`);
    },
    release() {},
  };
  const repository = new ComputeRegistrationRepository({ async connect() { return client; }, query: client.query }, () => at);
  await repository.succeedWorkspace(
    "op_0123456789abcdef0123456789abcdef",
    "control-worker-1",
    "wsp_0123456789abcdef0123456789abcdef",
  );
  expect(statements.some(({ sql }) => sql.startsWith("UPDATE cantelop_control.workspaces"))).toBe(false);
  expect(statements.find(({ sql }) => sql.startsWith("UPDATE cantelop_control.operations")).sql)
    .toContain("provider_workspace_id");
  expect(statements).toHaveLength(1);
});

test("registration context is scoped to the canonical App and reads lifecycle and tenant together",async () => {
  let calls=0;
  const database={async query(sql,values) {
    calls++;expect(sql).toContain("w.id=$1 AND w.app_id=$2");
    return values[0].startsWith("wsp_0") ? {rows:[{archived:false,compute_tenant_id:"usr_0123456789abcdef0123456789abcdef" }]} : {rows:[]};
  }};
  const repository=new ComputeRegistrationRepository(database);
  expect(await repository.workspaceRegistrationContext("app_0123456789abcdef0123456789abcdef","wsp_0123456789abcdef0123456789abcdef")).toEqual({archived:false,computeTenantID:"usr_0123456789abcdef0123456789abcdef" });
  expect(await repository.workspaceRegistrationContext("app_0123456789abcdef0123456789abcdef","wsp_1123456789abcdef0123456789abcdef")).toBeNull();
  expect(calls).toBe(2);
  await expect(repository.workspaceRegistrationContext("../app","../wsp")).rejects.toThrow();
});

test("a stale worker cannot complete another worker's registration",async () => {
  const repository=new ComputeRegistrationRepository({async query(sql) {
    expect(sql).toContain("status = 'running' AND locked_by = $2");
    return {rowCount:0,rows:[]};
  }});
  await expect(repository.succeedWorkspace("op_0123456789abcdef0123456789abcdef","stale-worker","wsp_0123456789abcdef0123456789abcdef")).rejects.toMatchObject({code:"conflict"});
});
