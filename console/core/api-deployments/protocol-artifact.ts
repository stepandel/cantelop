import {
  validateApplicationSchema,
  type ApplicationDatabaseSchema,
} from "../workspace-databases/schema";

export interface EnvironmentDeclaration {
  default?: string;
  secret?: boolean;
  required?: boolean;
}
export interface AppDeploymentConfiguration {
  app_name: string;
  session_runtime_id: string;
  environment?: Record<string, EnvironmentDeclaration>;
  dockerfile?: string;
}
export interface ProtocolEdgeManifest extends AppDeploymentConfiguration {
  schema_version: 1;
  kind: "cantelop-protocol-edge";
  main_module: "worker.mjs";
  cli_build_protocol_version: 6;
  integration_protocol_version: 2;
  required_bindings: ["CANTELOP_INTEGRATION_TOKEN"];
  /** Present when the generated Worker accepts a rotated-out token until its expiry. */
  optional_bindings?: typeof integrationRotationBindings;
  default_keep_alive_binding: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS";
  webhooks: { path: string; method: "GET" | "POST" }[];
  homepage: boolean;
  auth?: true;
}
export interface SessionRuntimeManifest extends AppDeploymentConfiguration {
  schema_version: 1;
  kind: "cantelop-session-runtime";
  main_module: "session-runtime.mjs";
  cli_build_protocol_version: 6;
  integration_protocol_version: 2;
  runtime_protocol_version: 2;
  capabilities: {
    steering: boolean;
    messageCancellation: boolean;
    durableView: boolean;
    authTerminal?: true;
  };
  database_schema?: ApplicationDatabaseSchema;
}
export const integrationRotationBindings = [
  "CANTELOP_INTEGRATION_TOKEN_PREVIOUS",
  "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT",
] as const;
const slug = /^(?!.*--)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const runtimeID = /^[a-z0-9][a-z0-9._:-]{0,127}$/;
const configurationKeys = [
  "app_name",
  "session_runtime_id",
  "environment",
  "dockerfile",
];
function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function fail(): never {
  throw new TypeError("invalid protocol artifact manifest");
}
function onlyKeys(value: Record<string, unknown>, keys: string[]) {
  if (Object.keys(value).some((key) => !keys.includes(key))) fail();
}
function configuration(
  value: Record<string, unknown>,
): AppDeploymentConfiguration {
  if (
    typeof value.app_name !== "string" ||
    !slug.test(value.app_name) ||
    typeof value.session_runtime_id !== "string" ||
    !runtimeID.test(value.session_runtime_id)
  )
    fail();
  if (
    value.dockerfile !== undefined &&
    (typeof value.dockerfile !== "string" ||
      !value.dockerfile ||
      value.dockerfile.startsWith("/") ||
      value.dockerfile.includes("\\") ||
      value.dockerfile.split("/").includes(".."))
  )
    fail();
  const result: AppDeploymentConfiguration = {
    app_name: value.app_name,
    session_runtime_id: value.session_runtime_id,
    ...(value.dockerfile === undefined
      ? {}
      : { dockerfile: value.dockerfile as string }),
  };
  if (value.environment !== undefined) {
    if (
      !record(value.environment) ||
      Object.keys(value.environment).length > 128
    )
      fail();
    const environment: Record<string, EnvironmentDeclaration> = {};
    for (const [name, declaration] of Object.entries(value.environment)) {
      if (
        !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name) ||
        name.startsWith("CANTELOP_") ||
        !record(declaration)
      )
        fail();
      onlyKeys(declaration, ["default", "secret", "required"]);
      if (
        (declaration.secret !== undefined &&
          typeof declaration.secret !== "boolean") ||
        (declaration.required !== undefined &&
          typeof declaration.required !== "boolean") ||
        (declaration.default !== undefined &&
          (typeof declaration.default !== "string" ||
            Buffer.byteLength(declaration.default) > 65536 ||
            /[\0\r\n]/.test(declaration.default))) ||
        (declaration.secret && declaration.default !== undefined)
      )
        fail();
      Object.defineProperty(environment, name, {
        value: { ...declaration },
        enumerable: true,
      });
    }
    result.environment = environment;
  }
  return result;
}
function base(value: unknown): Record<string, unknown> {
  if (
    !record(value) ||
    value.schema_version !== 1 ||
    value.cli_build_protocol_version !== 6 ||
    value.integration_protocol_version !== 2
  )
    fail();
  return value;
}
export function parseProtocolEdgeManifest(
  input: unknown,
): ProtocolEdgeManifest {
  const value = base(input);
  onlyKeys(value, [
    ...configurationKeys,
    "schema_version",
    "kind",
    "main_module",
    "cli_build_protocol_version",
    "integration_protocol_version",
    "required_bindings",
    "optional_bindings",
    "default_keep_alive_binding",
    "webhooks",
    "homepage",
    "auth",
  ]);
  if (
    value.kind !== "cantelop-protocol-edge" ||
    value.main_module !== "worker.mjs" ||
    !Array.isArray(value.required_bindings) ||
    value.required_bindings.length !== 1 ||
    value.required_bindings[0] !== "CANTELOP_INTEGRATION_TOKEN" ||
    (value.optional_bindings !== undefined &&
      canonical(value.optional_bindings) !==
        canonical(integrationRotationBindings)) ||
    value.default_keep_alive_binding !==
      "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS" ||
    typeof value.homepage !== "boolean" ||
    (value.auth !== undefined && value.auth !== true) ||
    !Array.isArray(value.webhooks) ||
    value.webhooks.length > 100
  )
    fail();
  const seen = new Set<string>();
  const webhooks = value.webhooks.map((route) => {
    if (!record(route)) fail();
    onlyKeys(route, ["path", "method"]);
    if (
      typeof route.path !== "string" ||
      !/^\/webhooks\/[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_-]+)*$/.test(route.path) ||
      route.path.endsWith("/") ||
      route.path.includes("//") ||
      route.path.length > 128 ||
      seen.has(route.path) ||
      (route.method !== "GET" && route.method !== "POST")
    )
      fail();
    seen.add(route.path);
    return { path: route.path, method: route.method as "GET" | "POST" };
  });
  return {
    ...configuration(value),
    schema_version: 1,
    kind: "cantelop-protocol-edge",
    main_module: "worker.mjs",
    cli_build_protocol_version: 6,
    integration_protocol_version: 2,
    required_bindings: ["CANTELOP_INTEGRATION_TOKEN"],
    ...(value.optional_bindings === undefined
      ? {}
      : { optional_bindings: integrationRotationBindings }),
    default_keep_alive_binding: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",
    webhooks,
    homepage: value.homepage,
    ...(value.auth === undefined ? {} : { auth: true }),
  };
}
export function parseSessionRuntimeManifest(
  input: unknown,
): SessionRuntimeManifest {
  const value = base(input);
  onlyKeys(value, [
    ...configurationKeys,
    "schema_version",
    "kind",
    "main_module",
    "cli_build_protocol_version",
    "integration_protocol_version",
    "runtime_protocol_version",
    "capabilities",
    "database_schema",
  ]);
  if (
    value.kind !== "cantelop-session-runtime" ||
    value.main_module !== "session-runtime.mjs" ||
    value.runtime_protocol_version !== 2 ||
    !record(value.capabilities)
  )
    fail();
  onlyKeys(value.capabilities, [
    "steering",
    "messageCancellation",
    "durableView",
    "authTerminal",
  ]);
  if (
    typeof value.capabilities.steering !== "boolean" ||
    typeof value.capabilities.messageCancellation !== "boolean" ||
    typeof value.capabilities.durableView !== "boolean" ||
    (value.capabilities.authTerminal !== undefined && value.capabilities.authTerminal !== true)
  )
    fail();
  return {
    ...configuration(value),
    schema_version: 1,
    kind: "cantelop-session-runtime",
    main_module: "session-runtime.mjs",
    cli_build_protocol_version: 6,
    integration_protocol_version: 2,
    runtime_protocol_version: 2,
    capabilities: {
      steering: value.capabilities.steering,
      messageCancellation: value.capabilities.messageCancellation,
      durableView: value.capabilities.durableView,
      ...(value.capabilities.authTerminal === undefined ? {} : { authTerminal: true }),
    },
    ...(value.database_schema === undefined
      ? {}
      : { database_schema: validateApplicationSchema(value.database_schema) }),
  };
}
export function validateArtifactPair(
  edge: ProtocolEdgeManifest,
  runtime: SessionRuntimeManifest,
): void {
  if (
    edge.app_name !== runtime.app_name ||
    edge.session_runtime_id !== runtime.session_runtime_id ||
    edge.dockerfile !== runtime.dockerfile ||
    (edge.auth === true) !== (runtime.capabilities.authTerminal === true) ||
    canonical(edge.environment ?? {}) !== canonical(runtime.environment ?? {})
  )
    throw new TypeError("incompatible Edge and Session runtime artifacts");
}
export function canonical(value: unknown): string {
  return JSON.stringify(value, (_key, item) =>
    record(item)
      ? Object.fromEntries(
          Object.entries(item).sort(([a], [b]) => a.localeCompare(b)),
        )
      : item,
  );
}
