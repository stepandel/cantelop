import { expect, test } from "bun:test";

import { publicCreateAppInput, publicCreateWorkspaceInput } from "./input";

test("translates the stable CLI App contract into the product domain", () => {
  expect(publicCreateAppInput({
    slug: "agent",
    session_runtime: {
      runtime: { profile: "default" },
    },
  })).toEqual({
    slug: "agent",
    sessionRuntime: { internalPort: 57321 },
  });
});

test("rejects attempts to select the platform-owned internal port", () => {
  expect(() => publicCreateAppInput({
    slug: "agent",
    session_runtime: {
      runtime: { profile: "default", internal_port: 8080 },
    },
  })).toThrow("invalid input");
});

test("rejects the removed runtime policy", () => {
  expect(() => publicCreateAppInput({
    slug: "agent",
    session_runtime: {
      runtime: { profile: "default" },
      policy: { min_instances_per_workspace: 0 },
    },
  })).toThrow("invalid input");
});

test("rejects unknown App fields", () => {
  expect(() => publicCreateAppInput({ slug: "agent", extra: true })).toThrow("invalid input");
});

test("translates the stable Workspace contract", () => {
  expect(publicCreateWorkspaceInput({
    app_id: "app_0123456789abcdef0123456789abcdef",
    slug: "staging",
  })).toEqual({ appID: "app_0123456789abcdef0123456789abcdef", slug: "staging" });
});
