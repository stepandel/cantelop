import { afterEach, expect, mock, test } from "bun:test";

mock.module("server-only", () => ({}));

const originalFetch = globalThis.fetch;
const originalEnvironment = {
  observerURL: process.env.FIRE_FUSE_OBSERVER_URL,
  statusToken: process.env.FIRE_FUSE_STATUS_TOKEN,
  runtimeToken: process.env.FIRE_FUSE_RUNTIME_TOKEN,
};

afterEach(() => {
  globalThis.fetch = originalFetch;
  restore("FIRE_FUSE_OBSERVER_URL", originalEnvironment.observerURL);
  restore("FIRE_FUSE_STATUS_TOKEN", originalEnvironment.statusToken);
  restore("FIRE_FUSE_RUNTIME_TOKEN", originalEnvironment.runtimeToken);
});

test("uses one observer origin for reads and Session termination", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_STATUS_TOKEN = "s".repeat(32);
  process.env.FIRE_FUSE_RUNTIME_TOKEN = "r".repeat(32);
  const requests = [];
  globalThis.fetch = async (input, init) => {
    const url = new URL(input);
    requests.push({ url: url.toString(), method: init?.method ?? "GET", authorization: init?.headers?.Authorization });
    if (url.pathname === "/snapshot") {
      return Response.json({ ready: true, workspaces: [], sessions: [], sandboxes: [] });
    }
    if (url.pathname === "/requests") return Response.json({ items: [] });
    if (url.pathname === "/infrastructure") {
      return Response.json({ observed_at: "2026-08-31T12:00:00Z", range: "6h", charts: [] });
    }
    return Response.json({ id: "ses_test", workspace_id: "wsp_test" });
  };
  const { getInfrastructure, getRequestEvents, getSnapshot, terminateSession } = await import("../lib/fire-fuse.ts");

  await getSnapshot();
  await getRequestEvents(new URLSearchParams({ workspace_id: "wsp_test" }));
  await getInfrastructure("6h");
  await terminateSession("ses_test");

  expect(requests).toEqual([
    { url: "https://status.example.com/snapshot", method: "GET", authorization: `Bearer ${"s".repeat(32)}` },
    { url: "https://status.example.com/requests?workspace_id=wsp_test", method: "GET", authorization: `Bearer ${"s".repeat(32)}` },
    { url: "https://status.example.com/infrastructure?range=6h", method: "GET", authorization: `Bearer ${"s".repeat(32)}` },
    { url: "https://status.example.com/sessions/ses_test", method: "DELETE", authorization: `Bearer ${"r".repeat(32)}` },
  ]);
});

test("requires the observer URL to be an HTTPS origin", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com/snapshot";
  process.env.FIRE_FUSE_STATUS_TOKEN = "s".repeat(32);
  const { getSnapshot } = await import("../lib/fire-fuse.ts");

  await expect(getSnapshot()).rejects.toThrow("FIRE_FUSE_OBSERVER_URL must be an origin");
});

test("scopes the observer snapshot to named Workspaces", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_STATUS_TOKEN = "s".repeat(32);
  const urls = [];
  globalThis.fetch = async (input) => {
    urls.push(new URL(input).toString());
    return Response.json({ ready: true, workspaces: [], sessions: [], sandboxes: [] });
  };
  const { getSnapshot, maxSnapshotWorkspaces } = await import("../lib/fire-fuse.ts");
  const first = "wsp_0123456789abcdef0123456789abcdef";
  const second = "wsp_ffffffffffffffffffffffffffffffff";

  await getSnapshot([first, second]);
  await getSnapshot([]);
  await getSnapshot();
  expect(urls).toEqual([
    `https://status.example.com/snapshot?workspace_ids=${first}%2C${second}`,
    "https://status.example.com/snapshot?workspace_ids=",
    "https://status.example.com/snapshot",
  ]);

  await expect(getSnapshot(["wsp_x,wsp_y"])).rejects.toThrow("invalid snapshot Workspace scope");
  await expect(getSnapshot(["app_0123456789abcdef0123456789abcdef"])).rejects.toThrow("invalid snapshot Workspace scope");
  await expect(getSnapshot(Array(maxSnapshotWorkspaces + 1).fill(first))).rejects.toThrow("invalid snapshot Workspace scope");
  expect(urls).toHaveLength(3);
});

function restore(name, value) {
  if (value === undefined) delete process.env[name];
  else process.env[name] = value;
}

const sandboxID = "sbx-0123456789abcdef0123456789abcdef";
test("stops a Sandbox with the runtime credential and propagates runtime failures", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_RUNTIME_TOKEN = "r".repeat(32);
  const { terminateSandbox } = await import("../lib/fire-fuse.ts");
  let status = 200;
  let calls = 0;
  globalThis.fetch = async (input, init) => {
    calls++;
    expect(String(input)).toBe(`https://status.example.com/sandboxes/${sandboxID}`);
    expect(init.method).toBe("DELETE");
    expect(init.headers.Authorization).toBe(`Bearer ${"r".repeat(32)}`);
    return Response.json({ id: sandboxID, state: "terminated" }, { status });
  };
  expect(await terminateSandbox(sandboxID)).toEqual({ id: sandboxID, state: "terminated" });
  for (const [code, message] of [[404, "runtime_sandbox_not_found"], [409, "runtime_sandbox_invalid_state"], [503, "Fire Fuse runtime returned HTTP 503"]]) {
    status = code;
    await expect(terminateSandbox(sandboxID)).rejects.toThrow(message);
  }
  await expect(terminateSandbox("../sessions/ses_test")).rejects.toThrow("invalid Sandbox ID");
  expect(calls).toBe(4);
});

test("crashes a Sandbox with the runtime credential and POST action", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_RUNTIME_TOKEN = "r".repeat(32);
  const { crashSandbox } = await import("../lib/fire-fuse.ts");
  globalThis.fetch = async (input, init) => {
    expect(String(input)).toBe(`https://status.example.com/sandboxes/${sandboxID}/crash`);
    expect(init.method).toBe("POST");
    expect(init.headers.Authorization).toBe(`Bearer ${"r".repeat(32)}`);
    return Response.json({ id: sandboxID, state: "failed", failure: { reason: "runtime_exit" } });
  };
  expect(await crashSandbox(sandboxID)).toEqual({ id: sandboxID, state: "failed", failure: { reason: "runtime_exit" } });
  await expect(crashSandbox("../sessions/ses_test")).rejects.toThrow("invalid Sandbox ID");
});

test("accepts per-host infrastructure and rejects malformed hosts", async () => {
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_STATUS_TOKEN = "s".repeat(32);
  const { getInfrastructure } = await import("../lib/fire-fuse.ts");
  let hosts = [
    { id: "compute", title: "Compute", role: "", reachable: true, last_collected_at: "2026-09-28T12:00:00Z", charts: [] },
    { id: "builder", title: "Builder", role: "", reachable: false, charts: [] },
  ];
  globalThis.fetch = async () => Response.json({ observed_at: "2026-09-28T12:00:00Z", range: "1h", hosts, charts: [] });

  expect((await getInfrastructure("1h")).hosts).toHaveLength(2);
  hosts = [{ id: "storage", title: "Storage", reachable: "yes", charts: [] }];
  await expect(getInfrastructure("1h")).rejects.toThrow("invalid infrastructure metrics");
});
