import { expect, mock, test } from "bun:test";

mock.module("server-only", () => ({}));

const userID = "usr_0123456789abcdef0123456789abcdef";
const owned = {
  id: "wsp_0123456789abcdef0123456789abcdef",
  app_id: "app_0123456789abcdef0123456789abcdef",
  slug: "production",
};
const foreign = {
  id: "wsp_ffffffffffffffffffffffffffffffff",
  app_id: "app_ffffffffffffffffffffffffffffffff",
};

function instance(id, workspace, overrides = {}) {
  return {
    id,
    workspace_id: workspace.id,
    app_id: workspace.app_id,
    build_id: `sha256:${"a".repeat(64)}`,
    keep_alive_seconds: 600,
    state: "running",
    created_at: "2026-08-13T00:00:00.000Z",
    updated_at: "2026-08-13T00:00:01.000Z",
    ...overrides,
  };
}

function sandbox(id, workspace, overrides = {}) {
  const { keep_alive_seconds: _, ...record } = instance(id, workspace, overrides);
  return { ...record, profile: "default", vcpus: 1, memory_mib: 1024 };
}

test("filters the host observer snapshot to PostgreSQL-owned Workspaces", async () => {
  const { getCustomerSnapshot } = await import("../lib/customer-telemetry.ts");
  const scopes = [];
  const result = await getCustomerSnapshot(userID, undefined, {
    workspaces: async (receivedUserID) => {
      expect(receivedUserID).toBe(userID);
      return { items: [owned] };
    },
    requestEvents: async () => { throw new Error("not used"); },
    // The observer is asked for the owned Workspace only, but its answer is
    // still filtered: this one ignores the scope and returns a foreign tenant.
    snapshot: async (workspaceIDs) => (scopes.push(workspaceIDs), {
      ready: true,
      observed_at: "2026-08-13T00:00:02.000Z",
      workspaces: [owned, foreign],
      sessions: [
        instance("ses_owned", owned, { app_id: "untrusted" }),
        instance("ses_foreign", foreign),
      ],
      sandboxes: [
        sandbox("sbx_owned", owned, { app_id: "untrusted" }),
        sandbox("sbx_foreign", foreign),
      ],
    }),
  });

  expect(scopes).toEqual([[owned.id]]);
  expect(result.workspaces).toEqual([owned]);
  expect(result.sessions).toEqual([
    instance("ses_owned", owned),
  ]);
  expect(result.sandboxes.map(({ id, app_id }) => ({ id, app_id }))).toEqual([
    { id: "sbx_owned", app_id: owned.app_id },
  ]);
  expect(result.observed_at).toBe("2026-08-13T00:00:02.000Z");
});

test("scopes the snapshot to one App when the observability tab asks for it", async () => {
  const { getCustomerSnapshot } = await import("../lib/customer-telemetry.ts");
  const otherApp = { ...owned, id: "wsp_22222222222222222222222222222222", app_id: "app_22222222222222222222222222222222" };
  const result = await getCustomerSnapshot(userID, { appID: owned.app_id }, {
    workspaces: async () => ({ items: [owned, otherApp] }),
    requestEvents: async () => { throw new Error("not used"); },
    snapshot: async () => ({
      ready: true,
      observed_at: "2026-08-13T00:00:02.000Z",
      workspaces: [owned, otherApp],
      sessions: [instance("ses_owned", owned), instance("ses_other", otherApp)],
      sandboxes: [sandbox("sbx_owned", owned), sandbox("sbx_other", otherApp)],
    }),
  });

  expect(result.workspaces).toEqual([owned]);
  expect(result.sessions.map(({ id }) => id)).toEqual(["ses_owned"]);
  expect(result.sandboxes.map(({ id }) => id)).toEqual(["sbx_owned"]);
});

test("asks the observer only for the scoped Workspaces", async () => {
  const { getCustomerSnapshot } = await import("../lib/customer-telemetry.ts");
  const second = { ...owned, id: "wsp_11111111111111111111111111111111", slug: "staging" };
  const otherApp = { ...owned, id: "wsp_22222222222222222222222222222222", app_id: "app_22222222222222222222222222222222" };
  const scopes = [];
  const dependencies = {
    workspaces: async () => ({ items: [owned, second, otherApp] }),
    requestEvents: async () => { throw new Error("not used"); },
    snapshot: async (workspaceIDs) => {
      scopes.push(workspaceIDs);
      return { ready: false, observed_at: "2026-08-13T00:00:02.000Z", workspaces: [], sessions: [], sandboxes: [] };
    },
  };

  await getCustomerSnapshot(userID, {}, dependencies);
  await getCustomerSnapshot(userID, { appID: owned.app_id }, dependencies);
  await getCustomerSnapshot(userID, { workspaceID: second.id }, dependencies);
  const empty = await getCustomerSnapshot(userID, { workspaceID: foreign.id }, dependencies);

  expect(scopes).toEqual([
    [owned.id, second.id, otherApp.id],
    [owned.id, second.id],
    [second.id],
    [],
  ]);
  expect(empty).toEqual({ ready: false, observed_at: "2026-08-13T00:00:02.000Z", workspaces: [], sessions: [], sandboxes: [] });
});

test("reads the whole host for a tenant too large for one scoped query", async () => {
  const { getCustomerSnapshot } = await import("../lib/customer-telemetry.ts");
  const { maxSnapshotWorkspaces } = await import("../lib/fire-fuse.ts");
  const many = Array.from({ length: maxSnapshotWorkspaces + 1 }, (_, index) => ({
    ...owned, id: `wsp_${index.toString(16).padStart(32, "0")}`,
  }));
  const scopes = [];
  const result = await getCustomerSnapshot(userID, {}, {
    workspaces: async () => ({ items: many }),
    requestEvents: async () => { throw new Error("not used"); },
    snapshot: async (workspaceIDs) => {
      scopes.push(workspaceIDs);
      return {
        ready: true,
        observed_at: "2026-08-13T00:00:02.000Z",
        workspaces: [],
        sessions: [instance("ses_owned", many[0]), instance("ses_foreign", foreign)],
        sandboxes: [sandbox("sbx_foreign", foreign)],
      };
    },
  });

  expect(scopes).toEqual([undefined]);
  expect(result.sessions.map(({ id }) => id)).toEqual(["ses_owned"]);
  expect(result.sandboxes).toEqual([]);
});

test("does not read the observer when the Workspace inventory is invalid", async () => {
  const { getCustomerSnapshot } = await import("../lib/customer-telemetry.ts");
  await expect(getCustomerSnapshot(userID, {}, {
    workspaces: async () => ({}),
    requestEvents: async () => { throw new Error("not used"); },
    snapshot: async () => { throw new Error("should not read the observer"); },
  })).rejects.toThrow("invalid customer Workspaces");
});

test("does not call the observer for a Workspace the user does not own", async () => {
  const { getCustomerRequestEvents } = await import("../lib/customer-telemetry.ts");
  let observerCalls = 0;
  const result = await getCustomerRequestEvents(userID, new URLSearchParams({
    workspace_id: foreign.id,
    limit: "50",
  }), {
    workspaces: async () => ({ items: [owned] }),
    requestEvents: async () => { observerCalls++; return { items: [] }; },
    snapshot: async () => { throw new Error("not used"); },
  });

  expect(result).toEqual({ items: [] });
  expect(observerCalls).toBe(0);
});

test("queries request history once for all owned Workspaces in an App", async () => {
  const { getCustomerRequestEvents } = await import("../lib/customer-telemetry.ts");
  const second = { ...owned, id: "wsp_11111111111111111111111111111111", slug: "staging" };
  const archived = { ...owned, id: "wsp_22222222222222222222222222222222", slug: "old" };
  const queries = [];
  const result = await getCustomerRequestEvents(userID, new URLSearchParams({
    app_id: owned.app_id,
    limit: "2",
  }), {
    workspaces: async () => ({ items: [owned, second, foreign] }),
    requestEvents: async (search) => {
      queries.push(search.toString());
      return { items: [
          { sequence: 7, event_id: "archived-event", workspace_id: archived.id, received_at: "2026-08-13T00:00:07Z", status_code: 200, duration_ms: 7 },
          { sequence: 6, event_id: "event-6", workspace_id: second.id, received_at: "2026-08-13T00:00:06Z", status_code: 202, duration_ms: 6 },
        ], next_before_sequence: 6 };
    },
    snapshot: async () => { throw new Error("not used"); },
  });

  expect(queries).toEqual([`limit=2&app_id=${owned.app_id}`]);
  expect(result.items.map(({ sequence }) => sequence)).toEqual([7, 6]);
  expect(result.next_before_sequence).toBe(6);
});

test("forwards a forward-reading cursor to the observer", async () => {
  const { getCustomerRequestEvents } = await import("../lib/customer-telemetry.ts");
  const queries = [];
  await getCustomerRequestEvents(userID, new URLSearchParams({
    app_id: owned.app_id,
    limit: "500",
    after_sequence: "41",
  }), {
    workspaces: async () => ({ items: [owned] }),
    requestEvents: async (search) => { queries.push(search.toString()); return { items: [] }; },
    snapshot: async () => { throw new Error("not used"); },
  });

  expect(queries).toEqual([`limit=500&app_id=${owned.app_id}&after_sequence=41`]);
});

test("rejects an unscoped history request before contacting the observer", async () => {
  const { getCustomerRequestEvents } = await import("../lib/customer-telemetry.ts");
  await expect(getCustomerRequestEvents(userID, new URLSearchParams({ limit: "50" }), {
    workspaces: async () => { throw new Error("should not list Workspaces"); },
    requestEvents: async () => { throw new Error("should not query observer"); },
    snapshot: async () => { throw new Error("not used"); },
  })).rejects.toThrow("requires an App or Workspace scope");
});
