import { test, expect } from "bun:test";
import {
  parseAPIArtifactManifest,
  apiArtifactRoutes,
  sameAPIArtifactManifest,
} from "./artifact";
import {
  parseSessionRuntimeManifest,
  validateArtifactPair,
} from "./protocol-artifact";
import { validateAppEnvironment } from "../configuration/declarations";
export const edge = {
  schema_version: 1,
  kind: "cantelop-protocol-edge",
  main_module: "worker.mjs",
  app_name: "agent",
  session_runtime_id: "runtime-v1",
  cli_build_protocol_version: 6,
  integration_protocol_version: 2,
  required_bindings: ["CANTELOP_INTEGRATION_TOKEN"],
  default_keep_alive_binding: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",
  webhooks: [{ path: "/webhooks/github", method: "POST" }],
  homepage: true,
  environment: {
    TOKEN: { secret: true, required: true },
    MODEL: { default: "small", required: true },
  },
};
export const runtime = {
  schema_version: 1,
  kind: "cantelop-session-runtime",
  main_module: "session-runtime.mjs",
  app_name: "agent",
  session_runtime_id: "runtime-v1",
  cli_build_protocol_version: 6,
  integration_protocol_version: 2,
  runtime_protocol_version: 2,
  capabilities: {
    steering: false,
    messageCancellation: false,
    durableView: false,
  },
  environment: edge.environment,
};
test("protocol manifests publish only generated commands and declared ingress", () => {
  expect(parseAPIArtifactManifest(edge)).toEqual(edge);
  expect(apiArtifactRoutes(edge)).toEqual([
    { method: "GET", path: "/" },
    { method: "HEAD", path: "/" },
    { method: "POST", path: "/commands/app.environment.set" },
    { method: "POST", path: "/commands/app.environment.status" },
    { method: "POST", path: "/commands/app.environment.unset" },
    { method: "POST", path: "/commands/cancel" },
    { method: "POST", path: "/commands/dispatch" },
    { method: "POST", path: "/commands/request" },
    { method: "POST", path: "/commands/status" },
    { method: "POST", path: "/commands/steer" },
    { method: "POST", path: "/commands/stop" },
    { method: "POST", path: "/commands/stream" },
    { method: "POST", path: "/commands/view" },
    { method: "POST", path: "/commands/workspace.database" },
    { method: "POST", path: "/commands/workspace.delete" },
    { method: "POST", path: "/commands/workspace.inspect" },
    { method: "POST", path: "/commands/workspace.resolve" },
    { method: "POST", path: "/webhooks/github" },
  ]);
  expect(parseSessionRuntimeManifest(runtime)).toEqual(runtime);
  validateArtifactPair(edge, runtime);
  expect(
    sameAPIArtifactManifest(edge, { ...edge, session_runtime_id: "changed" }),
  ).toBe(false);
});
test("artifact pairs reject changed identity or configuration and invalid capabilities", () => {
  for (const change of [
    { session_runtime_id: "other" },
    { app_name: "other" },
    { environment: {} },
    { dockerfile: "Dockerfile" },
  ])
    expect(() =>
      validateArtifactPair(edge, { ...runtime, ...change }),
    ).toThrow();
  for (const capability of ["steering", "messageCancellation", "durableView"])
    expect(() =>
      parseSessionRuntimeManifest({
        ...runtime,
        capabilities: { ...runtime.capabilities, [capability]: "true" },
      }),
    ).toThrow();
  expect(() => parseAPIArtifactManifest({ ...edge, routes: [] })).toThrow();
  expect(() =>
    parseAPIArtifactManifest({
      ...edge,
      webhooks: [{ path: "/commands", method: "POST" }],
    }),
  ).toThrow();
});
test("required environment validates secret kinds and applies declared variable defaults", () => {
  expect(() =>
    validateAppEnvironment(edge, [
      { name: "TOKEN", kind: "secret", value: "yes" },
    ]),
  ).not.toThrow();
  for (const values of [
    [],
    [{ name: "TOKEN", kind: "variable", value: "yes" }],
    [{ name: "TOKEN", kind: "secret", value: "" }],
  ])
    expect(() => validateAppEnvironment(edge, values)).toThrow(
      "app_environment_missing_or_invalid",
    );
});
test("Edge manifests may declare exactly the integration rotation bindings", () => {
  const rotation = {
    ...edge,
    optional_bindings: [
      "CANTELOP_INTEGRATION_TOKEN_PREVIOUS",
      "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT",
    ],
  };
  expect(parseAPIArtifactManifest(rotation)).toEqual(rotation);
  expect(sameAPIArtifactManifest(rotation, edge)).toBe(false);
  const fleet = { ...rotation, fleet_execution_protocol_version: 1,
    optional_bindings: [...rotation.optional_bindings, "CANTELOP_FLEET_EXECUTION_VERSION"] };
  expect(parseAPIArtifactManifest(fleet)).toEqual(fleet);
  expect(() => parseAPIArtifactManifest({ ...rotation, optional_bindings: fleet.optional_bindings })).toThrow();
  expect(() => parseAPIArtifactManifest({ ...fleet, fleet_execution_protocol_version: 2 })).toThrow();
  for (const optional_bindings of [
    [],
    ["CANTELOP_INTEGRATION_TOKEN_PREVIOUS"],
    [...rotation.optional_bindings].reverse(),
    [...rotation.optional_bindings, "OTHER"],
    "CANTELOP_INTEGRATION_TOKEN_PREVIOUS",
  ])
    expect(() =>
      parseAPIArtifactManifest({ ...edge, optional_bindings }),
    ).toThrow();
});


test("native auth declarations survive parsing and must match across artifacts", () => {
  const nativeEdge = { ...edge, auth: true };
  const nativeRuntime = { ...runtime, capabilities: { ...runtime.capabilities, authTerminal: true } };
  expect(parseAPIArtifactManifest(nativeEdge)).toEqual(nativeEdge);
  expect(parseSessionRuntimeManifest(nativeRuntime)).toEqual(nativeRuntime);
  validateArtifactPair(nativeEdge, nativeRuntime);
  expect(sameAPIArtifactManifest(nativeEdge, edge)).toBe(false);
  expect(() => validateArtifactPair(nativeEdge, runtime)).toThrow();
  expect(() => validateArtifactPair(edge, nativeRuntime)).toThrow();
  for (const invalid of [false, null, "true", 1]) {
    expect(() => parseAPIArtifactManifest({ ...edge, auth: invalid })).toThrow();
    expect(() => parseSessionRuntimeManifest({ ...runtime, capabilities: { ...runtime.capabilities, authTerminal: invalid } })).toThrow();
  }
});
