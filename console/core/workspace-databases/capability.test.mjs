import { createHmac } from "node:crypto";
import { test, expect, spyOn } from "bun:test";
import { verifyDatabaseCapability, verifyActiveSandbox, SandboxVerificationError } from "./capability";
const key = "01".repeat(32);
const identity = { sandboxId: "sbx-0123456789abcdef0123456789abcdef", appId: "app_0123456789abcdef0123456789abcdef", workspaceId: "wsp_0123456789abcdef0123456789abcdef" };
function capability(value) {
  const body = Buffer.from(JSON.stringify(value)).toString("base64url");
  const signature = createHmac("sha256", Buffer.from(key,"hex")).update(`cantelop-workspace-database-v1\0${body}`).digest("base64url");
  return `Bearer ${body}.${signature}`;
}
test("capability binds immutable platform identity and rejects tampering", () => {
  const signed = capability(identity);
  expect(verifyDatabaseCapability(signed, key)).toEqual(identity);
  expect(() => verifyDatabaseCapability(signed, "02".repeat(32))).toThrow();
  expect(() => verifyDatabaseCapability(capability({ ...identity, workspaceId: "wsp_other" }), key)).toThrow();
});
test("active sandbox must match every signed identity field", async () => {
  const environment = { CANTELOP_COMPUTE_INGRESS_URL: "https://host.example.com", CANTELOP_COMPUTE_CONTROL_TOKEN: "ab".repeat(32) };
  await verifyActiveSandbox(identity, environment, async () => Response.json({ ...identity, active:true }));
  await expect(verifyActiveSandbox(identity, environment, async () => Response.json({ ...identity, workspaceId:"other", active:true }))).rejects.toThrow("inactive sandbox");
  await expect(verifyActiveSandbox(identity, environment, async () => Response.json({ error: { code:"not_found", message:"sandbox unavailable" } }, { status:404 }))).rejects.toThrow("inactive sandbox");
});

const observerEnvironment = {
  FIRE_FUSE_OBSERVER_URL: "https://status.apps.example.com",
  FIRE_FUSE_STATUS_TOKEN: "cd".repeat(32),
  CANTELOP_COMPUTE_INGRESS_URL: "http://10.77.0.1:8084",
  CANTELOP_COMPUTE_CONTROL_TOKEN: "ab".repeat(32),
};
const assignment = { ...identity, active: true };
test("Vercel verifies only one current assignment through the authenticated HTTPS observer", async () => {
  await verifyActiveSandbox(identity, observerEnvironment, async (url, options) => {
    expect(url).toBe(`https://status.apps.example.com/sandboxes/${identity.sandboxId}/database-access`);
    expect(options.headers.Authorization).toBe(`Bearer ${observerEnvironment.FIRE_FUSE_STATUS_TOKEN}`);
    expect(options.cache).toBe("no-store");
    expect(options.redirect).toBe("error");
    return Response.json(assignment);
  });
});
test("observer verification denies inactive, missing and mismatched assignments", async () => {
  const invalid = [
    {}, { ...assignment, active: false }, { ...assignment, active: "true" },
    ...["appId", "workspaceId", "sandboxId"].map(field => ({ ...assignment, [field]: "other" })),
  ];
  for (const value of invalid) {
    await expect(verifyActiveSandbox(identity, observerEnvironment, async () => Response.json(value))).rejects.toThrow("inactive sandbox");
  }
  await expect(verifyActiveSandbox(identity, observerEnvironment, async () => Response.json({ error: { code: "not_found" } }, { status: 404 }))).rejects.toMatchObject({ code: "inactive_sandbox", status: 404 });
  // nginx's catch-all and observers without the route return a bare 404.
  for (const body of [null, "404 page not found"]) {
    await expect(verifyActiveSandbox(identity, observerEnvironment, async () => new Response(body, { status: 404 }))).rejects.toMatchObject({ code: "verification_http_error", status: 404 });
  }
});
test("renewal rechecks assignment and never falls back to the dashboard or private ingress", async () => {
  let active = true, calls = 0;
  const request = async url => {
    expect(url).toBe(`https://status.apps.example.com/sandboxes/${identity.sandboxId}/database-access`);
    calls++;
    return Response.json({ ...assignment, workspaceId: active ? identity.workspaceId : "other" });
  };
  await verifyActiveSandbox(identity, observerEnvironment, request);
  active = false;
  await expect(verifyActiveSandbox(identity, observerEnvironment, request)).rejects.toMatchObject({ code: "inactive_sandbox" });
  expect(calls).toBe(2);
});
test("verification errors retain sanitized failure categories without infrastructure details", async () => {
  for (const status of [401, 403, 502, 503]) {
    await expect(verifyActiveSandbox(identity, observerEnvironment, async () => new Response("private-secret", { status }))).rejects.toMatchObject({ code: "verification_http_error", status });
  }
  for (const value of [null, [], "unexpected"]) {
    await expect(verifyActiveSandbox(identity, observerEnvironment, async () => Response.json(value))).rejects.toMatchObject({ code: "verification_response_invalid" });
  }
  await expect(verifyActiveSandbox(identity, observerEnvironment, async () => new Response("private-secret"))).rejects.toMatchObject({ code: "verification_response_invalid" });
  try {
    await verifyActiveSandbox(identity, observerEnvironment, async () => { throw new Error("private-secret"); });
    throw new Error("verification unexpectedly succeeded");
  } catch (error) {
    expect(error).toBeInstanceOf(SandboxVerificationError);
    expect(error.code).toBe("verification_unavailable");
    expect(JSON.stringify(error)).not.toContain("private-secret");
  }
});
test("verification timeout covers downloading the response body as well as connection setup", async () => {
  const timeout = spyOn(AbortSignal, "timeout").mockImplementation(milliseconds => {
    expect(milliseconds).toBe(5000);
    return AbortSignal.abort(new DOMException("private-secret", "TimeoutError"));
  });
  try {
    for (const body of [false, true]) {
      await expect(verifyActiveSandbox(identity, observerEnvironment, async (_url, { signal }) => {
        if (!body) signal.throwIfAborted();
        return { ok: true, json: async () => signal.throwIfAborted() };
      })).rejects.toMatchObject({ code: "verification_timeout" });
    }
  } finally { timeout.mockRestore(); }
});
test("a misconfigured observer fails closed without falling back to private ingress", async () => {
  let calls = 0;
  const request = async () => { calls++; return Response.json({ ...identity, active: true }); };
  await expect(verifyActiveSandbox(identity, { ...observerEnvironment, FIRE_FUSE_STATUS_TOKEN: undefined }, request)).rejects.toThrow("database credential service unavailable");
  for (const origin of ["invalid", "http://public.example.com", "https://status.example.com/path", "https://user:password@status.example.com", "https://status.example.com/?query=1"]) {
    await expect(verifyActiveSandbox(identity, { ...observerEnvironment, FIRE_FUSE_OBSERVER_URL: origin }, request)).rejects.toThrow("database credential service unavailable");
  }
  expect(calls).toBe(0);
});
