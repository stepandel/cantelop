import { expect, test } from "bun:test";

import { customerAppSpec, customerWorkspaceSpec } from "../lib/customer-app-input.ts";

test("maps the small customer form to an approved App shape", () => {
  expect(customerAppSpec({ app_slug: "alice-app" })).toEqual({
    slug: "alice-app",
    sessionRuntime: {
      internalPort: 57321,
    },
  });
});

test("rejects customer attempts to override template or resource limits", () => {
  const valid = { app_slug: "alice" };
  expect(() => customerAppSpec({ ...valid, vcpus: 16 })).toThrow();
  expect(() => customerAppSpec({ ...valid, app_slug: "Alice" })).toThrow();
  expect(() => customerAppSpec({ ...valid, workspace_slug: "staging" })).toThrow();
  expect(() => customerAppSpec({ ...valid, app_slug: "alice--app" })).toThrow();
  expect(() => customerAppSpec({ ...valid, app_slug: "a".repeat(64) })).toThrow();
  expect(() => customerAppSpec({ ...valid, internal_port: 8080 })).toThrow();
  expect(() => customerAppSpec({ ...valid, build_id: `sha256:${"a".repeat(64)}` })).toThrow();
});

test("maps and rejects Workspace creation input", () => {
  const appID = `app_${"0123456789abcdef".repeat(2)}`;
  expect(customerWorkspaceSpec({ app_id: appID, slug: "staging" })).toEqual({ appID, slug: "staging" });
  expect(() => customerWorkspaceSpec({ app_id: appID, slug: "Staging" })).toThrow();
  expect(() => customerWorkspaceSpec({ app_id: appID, slug: "bad--slug" })).toThrow();
  expect(() => customerWorkspaceSpec({ app_id: "app_short", slug: "staging" })).toThrow();
  expect(() => customerWorkspaceSpec({ app_id: appID, slug: "staging", workspace_id: "attach" })).toThrow();
});
