import { expect, test } from "bun:test";

import { appDefinitionSummary } from "./app-definition";

const edge = {
  schema_version: 1,
  kind: "cantelop-protocol-edge",
  main_module: "worker.mjs",
  cli_build_protocol_version: 6,
  integration_protocol_version: 2,
  app_name: "support-agent",
  session_runtime_id: "rt_0123",
  dockerfile: "docker/Dockerfile",
  required_bindings: ["CANTELOP_INTEGRATION_TOKEN"],
  optional_bindings: ["CANTELOP_INTEGRATION_TOKEN_PREVIOUS", "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"],
  default_keep_alive_binding: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",
  webhooks: [{ path: "/webhooks/github", method: "POST" }],
  homepage: true,
  auth: true,
};
const runtime = {
  kind: "cantelop-session-runtime",
  capabilities: { steering: true, messageCancellation: false, durableView: true, authTerminal: true },
};

test("summarizes what an SDK 1 App definition compiled into", () => {
  expect(appDefinitionSummary(edge, runtime)).toEqual({
    app_name: "support-agent",
    session_runtime_id: "rt_0123",
    webhooks: [{ path: "/webhooks/github", method: "POST" }],
    homepage: true,
    native_auth: true,
    token_rotation: true,
    dockerfile: "docker/Dockerfile",
    capabilities: { steering: true, message_cancellation: false, durable_view: true },
  });
});

test("a release without rotation, auth or a runtime manifest reads as such", () => {
  const { auth: _auth, optional_bindings: _bindings, ...plain } = edge;
  expect(appDefinitionSummary({ ...plain, homepage: false, webhooks: [] }, null)).toMatchObject({
    native_auth: false, token_rotation: false, homepage: false, webhooks: [], capabilities: null,
  });
});

test("legacy API manifests have no App definition", () => {
  expect(appDefinitionSummary({ schema_version: 3, kind: "cantelop-edge-api", main_module: "worker.mjs", routes: [] }, null)).toBeUndefined();
  expect(appDefinitionSummary(null, runtime)).toBeUndefined();
});
