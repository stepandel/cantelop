import { expect, test } from "bun:test";

import { DeploymentService } from "./deployment-uploads/service";
import { ProductService } from "./product-service";

// The tenant API contract in docs/openapi-console.yaml is what the CLI and
// SDK clients code against. These tests run real response projections through
// the schemas the routes declare, in both directions: every required field is
// produced, and every produced field is documented.
const spec = Bun.YAML.parse(await Bun.file(new URL("../../docs/openapi-console.yaml", import.meta.url)).text());

const at = new Date("2026-08-12T12:00:00Z");
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const app = {
  id: appID, ownerUserID: userID, ownerEmail: "alice@example.com", adminCount: 1, accessRole: "owner",
  slug: "agent", status: "active", sessionRuntime: { internalPort: 3000 }, createdAt: at, updatedAt: at,
};
const sharedApp = { ...app, id: "app_fedcba9876543210fedcba9876543210", slug: "shared", accessRole: "admin",
  ownerEmail: undefined, adminCount: undefined };
const workspaces = [
  { id: "wsp_0123456789abcdef0123456789abcdef", appID, slug: "production", createdAt: at, updatedAt: at },
  { id: "wsp_fedcba9876543210fedcba9876543210", appID, slug: "staging", createdAt: at, updatedAt: at, archivedAt: at },
];
const component = (status, extra = {}) => ({ kind: "api", operationID: "op_0123456789abcdef0123456789abcdef", status, ...extra });
const releases = [{
  id: "rel_0123456789abcdef0123456789abcdef", appID, version: 1, status: "failed",
  apiArtifactDigest: `sha256:${"a".repeat(64)}`, sessionRuntimeArtifactDigest: `sha256:${"b".repeat(64)}`,
  components: {
    api: component("failed", { attempts: 3, errorCode: "provider_rejected", updatedAt: at }),
    session_runtime: component("queued", { attempts: 1, progress: "pulling", updatedAt: at, nextAttemptAt: at }),
  },
  errorCode: "provider_rejected", createdAt: at, updatedAt: at,
}, {
  id: "rel_fedcba9876543210fedcba9876543210", appID, version: 2, status: "active",
  apiArtifactDigest: `sha256:${"c".repeat(64)}`, sessionRuntimeArtifactDigest: `sha256:${"d".repeat(64)}`,
  components: { api: component("succeeded"), session_runtime: component("succeeded") },
  createdAt: at, updatedAt: at, activatedAt: at,
  definition: {
    app_name: "agent", session_runtime_id: "rt_1", webhooks: [{ path: "/webhooks/github", method: "POST" }],
    homepage: true, native_auth: false, token_rotation: true, dockerfile: "Dockerfile",
    capabilities: { steering: true, message_cancellation: true, durable_view: false },
  },
}];

function resolve(schema) {
  while (schema?.$ref) {
    schema = schema.$ref.replace(/^#\//, "").split("/").reduce((node, key) => node[key], spec);
  }
  return schema;
}

function responseSchema(path, method, status) {
  const operation = spec.paths[path]?.[method];
  expect(operation).toBeDefined();
  return resolve(operation.responses[status].content["application/json"].schema);
}

// A deliberately small validator for the keywords these schemas use. Objects
// are checked strictly: a produced field the schema does not declare fails.
function violations(value, rawSchema, at = "$") {
  const schema = resolve(rawSchema);
  const types = [schema.type ?? []].flat();
  if (value === null) return types.includes("null") ? [] : [`${at}: null is not allowed`];
  const errors = [];
  const kind = Array.isArray(value) ? "array" : Number.isInteger(value) ? "integer" : typeof value;
  if (types.length > 0 && !types.includes(kind) && !(kind === "integer" && types.includes("number"))) {
    return [`${at}: expected ${types.join(" | ")}, got ${kind}`];
  }
  if (schema.enum && !schema.enum.includes(value)) errors.push(`${at}: ${JSON.stringify(value)} is not in ${schema.enum}`);
  if (typeof value === "string") {
    if (schema.pattern && !new RegExp(schema.pattern, "u").test(value)) errors.push(`${at}: does not match ${schema.pattern}`);
    if (schema.format === "date-time" && (!/^\d{4}-\d\d-\d\dT/.test(value) || Number.isNaN(Date.parse(value)))) {
      errors.push(`${at}: is not a date-time`);
    }
  }
  if (typeof value === "number") {
    if (schema.minimum !== undefined && value < schema.minimum) errors.push(`${at}: below ${schema.minimum}`);
    if (schema.maximum !== undefined && value > schema.maximum) errors.push(`${at}: above ${schema.maximum}`);
  }
  if (Array.isArray(value) && schema.items) {
    value.forEach((item, index) => errors.push(...violations(item, schema.items, `${at}[${index}]`)));
  }
  if (kind === "object") {
    const properties = schema.properties ?? {};
    for (const name of schema.required ?? []) if (!(name in value)) errors.push(`${at}.${name}: required but missing`);
    for (const [name, field] of Object.entries(value)) {
      if (field === undefined) continue;
      if (!(name in properties)) errors.push(`${at}.${name}: produced but not documented`);
      else errors.push(...violations(field, properties[name], `${at}.${name}`));
    }
  }
  return errors;
}

test("the validator rejects drift in either direction", () => {
  const workspace = resolve({ $ref: "#/components/schemas/Workspace" });
  expect(violations({ id: workspaces[0].id, app_id: appID, slug: "production", created_at: at.toISOString() }, workspace))
    .toEqual(["$.updated_at: required but missing"]);
  expect(violations({ id: workspaces[0].id, app_id: appID, slug: "production", created_at: at.toISOString(),
    updated_at: at.toISOString(), session_runtime: {} }, workspace)).toEqual(["$.session_runtime: produced but not documented"]);
});

test("GET /v1/apps returns documented Apps", async () => {
  const service = new ProductService({ async listAppsForUser() { return [app, sharedApp]; } });
  const body = await service.listApps(userID);
  expect(body.items).toHaveLength(2);
  expect(violations(body, responseSchema("/v1/apps", "get", "200"))).toEqual([]);
  expect(violations(await new ProductService({ async getAppForUser() { return app; } }).getApp(userID, appID),
    responseSchema("/v1/apps/{id}", "get", "200"))).toEqual([]);
});

test("GET /v1/workspaces returns documented Workspaces", async () => {
  const service = new ProductService({
    async listAppsForUser() { return [app]; },
    async listWorkspacesForUser() { return workspaces; },
  });
  const body = await service.listWorkspaces(userID);
  expect(body.items).toHaveLength(2);
  expect(violations(body, responseSchema("/v1/workspaces", "get", "200"))).toEqual([]);
});

test("GET /v1/apps/{app_id}/deployments returns documented releases", async () => {
  const service = new DeploymentService(undefined, { async listForUser() { return releases; } }, undefined);
  const body = await service.list(userID, appID);
  expect(body.items).toHaveLength(2);
  expect(violations(body, responseSchema("/v1/apps/{app_id}/deployments", "get", "200"))).toEqual([]);
});
